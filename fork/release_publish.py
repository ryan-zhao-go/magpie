import gzip
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time


ASSETS = (
    "magpie-cli-linux-amd64",
    "magpie-cli-linux-amd64.sha256",
    "magpie-fork-update",
    "magpie-fork-update.service",
    "magpie-fork-update.timer",
)


def complete(release):
    if not release or release.get("draft"):
        return False
    uploaded = {asset["name"] for asset in release.get("assets", [])
                if asset.get("state") == "uploaded" and asset.get("size", 0) > 0}
    return set(ASSETS).issubset(uploaded)


def reconcile(remote, tag, name, notes, directory):
    release = remote.read_release(tag)
    if release is None:
        remote.create(tag, name, notes)
        release = remote.read_release(tag)
    expected = [line for line in notes.splitlines() if line.startswith(("- Upstream:", "- Patch branch:"))]
    if not expected:
        expected = [notes]
    if any(line not in release.get("body", "") for line in expected):
        raise RuntimeError("Existing release names different sources; refusing overwrite")
    if not release["name"].startswith("magpie "):
        raise RuntimeError("Existing release has no valid binary version")
    assets = {asset["name"]: asset for asset in release.get("assets", [])}
    existing_binary = assets.get(ASSETS[0])
    if existing_binary and existing_binary.get("state") == "uploaded":
        remote.download(tag, ASSETS[0], directory)
        digest = hashlib.sha256((directory / ASSETS[0]).read_bytes()).hexdigest()
        checksum = directory / ASSETS[1]
        if ASSETS[1] in assets:
            remote.download(tag, ASSETS[1], directory)
            if checksum.read_text().split()[0] != digest:
                raise RuntimeError("Existing binary and checksum disagree")
        else:
            checksum.write_text(f"{digest}  {ASSETS[0]}\n")
    for asset_name in ASSETS:
        asset = assets.get(asset_name)
        if asset:
            if asset.get("state") != "uploaded" or asset.get("size", 0) <= 0:
                raise RuntimeError(f"Incomplete remote asset requires repair: {asset_name}")
            continue
        if not release.get("draft"):
            raise RuntimeError("A published immutable release is missing assets")
        remote.upload(tag, directory / asset_name)
    release = remote.read_release(tag)
    available = {asset["name"] for asset in release.get("assets", [])
                 if asset.get("state") == "uploaded" and asset.get("size", 0) > 0}
    if not set(ASSETS).issubset(available):
        raise RuntimeError("Release assets are incomplete; refusing publish")
    if release.get("draft"):
        remote.publish(tag)
    return release["name"].removeprefix("magpie ")


class GitHub:
    def __init__(self, repository):
        self.repository = repository

    def read_release(self, tag):
        result = subprocess.run(["gh", "api", f"repos/{self.repository}/releases/tags/{tag}"],
                                text=True, capture_output=True)
        if result.returncode:
            if "HTTP 404" in result.stderr:
                return None
            raise RuntimeError(f"Cannot read release: {result.stderr.strip()}")
        return json.loads(result.stdout)

    def create(self, tag, name, notes):
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8") as file:
            file.write(notes)
            file.flush()
            result = subprocess.run(["gh", "release", "create", tag, "--repo", self.repository,
                                     "--verify-tag", "--draft", "--title", name, "--notes-file", file.name],
                                    text=True, capture_output=True)
        if result.returncode and self.read_release(tag) is None:
            raise RuntimeError(result.stderr)

    def upload(self, tag, path):
        result = subprocess.run(["gh", "release", "upload", tag, str(path), "--repo", self.repository],
                                text=True, capture_output=True)
        if result.returncode:
            release = self.read_release(tag)
            if not any(asset["name"] == path.name and asset.get("state") == "uploaded"
                       for asset in release.get("assets", [])):
                raise RuntimeError(result.stderr)

    def download(self, tag, name, directory):
        subprocess.run(["gh", "release", "download", tag, "--repo", self.repository,
                        "--pattern", name, "--dir", str(directory), "--clobber"], check=True)

    def publish(self, tag):
        result = subprocess.run(["gh", "release", "edit", tag, "--repo", self.repository,
                                 "--draft=false", "--latest"], text=True, capture_output=True)
        if result.returncode and not complete(self.read_release(tag)):
            raise RuntimeError(result.stderr)


def main():
    import sys
    remote = GitHub(os.environ["GITHUB_REPOSITORY"])
    tag = os.environ["RELEASE_TAG"]
    output = os.environ["GITHUB_OUTPUT"]
    if sys.argv[1] == "inspect":
        release = remote.read_release(tag)
        skip = complete(release) and os.environ.get("VERIFY_ONLY") != "true"
        with open(output, "a", encoding="utf-8") as file:
            file.write(f"skip={str(skip).lower()}\n")
            if release:
                if not release["name"].startswith("magpie "):
                    raise RuntimeError("Existing release has an invalid version")
                file.write(f"version={release['name'].removeprefix('magpie ')}\n")
        return
    directory = Path("dist")
    directory.mkdir(exist_ok=True)
    notes = Path("release-notes.md").read_text()
    for attempt in range(3):
        try:
            version = reconcile(remote, tag, os.environ["RELEASE_NAME"], notes, directory)
            break
        except (RuntimeError, subprocess.CalledProcessError):
            if attempt == 2:
                raise
            time.sleep(5)
    if not complete(remote.read_release(tag)):
        raise RuntimeError("Release did not complete")
    binary = directory / ASSETS[0]
    if not binary.exists():
        remote.download(tag, ASSETS[0], directory)
    remote.download(tag, ASSETS[1], directory)
    digest = hashlib.sha256(binary.read_bytes()).hexdigest()
    if digest != (directory / ASSETS[1]).read_text().split()[0]:
        raise RuntimeError("Published binary checksum mismatch")
    compressed = directory / f"{ASSETS[0]}.gz"
    with binary.open("rb") as file, compressed.open("wb") as destination:
        with gzip.GzipFile(fileobj=destination, mode="wb", mtime=0, filename="") as archive:
            import shutil
            shutil.copyfileobj(file, archive)
    with open(output, "a", encoding="utf-8") as file:
        file.write(f"version={version}\n")


if __name__ == "__main__":
    main()
