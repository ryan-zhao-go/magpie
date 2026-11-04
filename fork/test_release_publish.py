import importlib.util
import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


source = Path(__file__).with_name("release_publish.py")


class FakeGitHub:
    def __init__(self, release=None):
        self.release = release
        self.created = 0
        self.uploads = []
        self.published = 0

    def read_release(self, tag):
        return self.release

    def create(self, tag, name, notes):
        self.created += 1
        self.release = {"name": name, "body": notes, "draft": True, "assets": []}

    def upload(self, tag, path):
        self.uploads.append(path.name)
        self.release["assets"].append({"name": path.name, "state": "uploaded", "size": path.stat().st_size})

    def download(self, tag, name, directory):
        if name == "magpie-cli-linux-amd64":
            (directory / name).write_bytes(b"published binary")
        elif name == "magpie-cli-linux-amd64.sha256":
            (directory / name).write_text(hashlib.sha256(b"published binary").hexdigest() + "  magpie-cli-linux-amd64\n")
        else:
            (directory / name).write_bytes(b"fixture")

    def publish(self, tag):
        self.published += 1
        self.release["draft"] = False


class ReleasePublishTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        specification = importlib.util.spec_from_file_location("release_publish", source)
        cls.module = importlib.util.module_from_spec(specification)
        specification.loader.exec_module(cls.module)

    def test_partial_release_uploads_only_missing_assets(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in self.module.ASSETS:
                (root / name).write_bytes(b"fixture")
            remote = FakeGitHub({"name": "magpie 1.2.3-fork.1", "body": "sources", "draft": True,
                                 "assets": [{"name": "magpie-cli-linux-amd64", "state": "uploaded", "size": 16}]})
            version = self.module.reconcile(remote, "fork-tag", "magpie new", "sources", root)
            self.assertEqual(version, "1.2.3-fork.1")
            self.assertEqual(remote.created, 0)
            self.assertNotIn("magpie-cli-linux-amd64", remote.uploads)
            self.assertEqual(len(remote.uploads), 4)
            self.assertEqual(remote.published, 1)

    def test_complete_release_still_prepares_original_oci_binary(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            remote = FakeGitHub({"name": "magpie original", "body": "sources", "draft": False,
                                 "assets": [{"name": name, "state": "uploaded", "size": 1} for name in self.module.ASSETS]})
            version = self.module.reconcile(remote, "fork-tag", "magpie new", "sources", root)
            self.assertEqual(version, "original")
            self.assertEqual((root / "magpie-cli-linux-amd64").read_bytes(), b"published binary")
            self.assertEqual(remote.uploads, [])
            self.assertEqual(remote.published, 0)
            self.assertEqual(remote.created, 0)

    def test_new_release_is_draft_until_all_assets_exist(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in self.module.ASSETS:
                (root / name).write_bytes(b"fixture")
            remote = FakeGitHub()
            self.module.reconcile(remote, "fork-tag", "magpie original", "sources", root)
            self.assertEqual(remote.created, 1)
            self.assertEqual(len(remote.uploads), 5)
            self.assertEqual(remote.published, 1)

    def test_source_mismatch_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            remote = FakeGitHub({"name": "magpie original", "body": "different sources", "draft": False, "assets": []})
            with self.assertRaises(RuntimeError):
                self.module.reconcile(remote, "fork-tag", "magpie new", "sources", Path(directory))
            self.assertEqual(remote.created, 0)
            self.assertEqual(remote.uploads, [])

    def test_draft_is_found_when_tag_endpoint_returns_404(self):
        import subprocess
        responses = [
            subprocess.CompletedProcess([], 1, "", "gh: Not Found (HTTP 404)"),
            subprocess.CompletedProcess([], 0, '[{"tag_name":"fork-tag","draft":true,"name":"magpie original","assets":[]}]', ""),
        ]
        with patch.object(self.module.subprocess, "run", side_effect=responses):
            release = self.module.GitHub("owner/repo").read_release("fork-tag")
        self.assertTrue(release["draft"])

    def test_missing_created_release_remains_retryable(self):
        remote = FakeGitHub()
        remote.create = lambda *arguments: None
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(RuntimeError):
                self.module.reconcile(remote, "fork-tag", "magpie original", "sources", Path(directory))


if __name__ == "__main__":
    unittest.main()
