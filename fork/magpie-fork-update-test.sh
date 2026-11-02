#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd -P)
updater="$root/fork/magpie-fork-update"
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

fail() {
  printf 'magpie-fork-update test failed: %s\n' "$*" >&2
  exit 1
}

fixture_checksum() {
  local file=$1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file"
  else
    shasum -a 256 "$file"
  fi
}

refresh_fixture_manifest() {
  local binary=$1 manifest=$2 digest size
  digest=$(fixture_checksum "$binary" | awk '{print $1}')
  size=$(wc -c <"$binary" | tr -d ' ')
  printf '{"schemaVersion":2,"layers":[{"mediaType":"application/vnd.magpie.binary","digest":"sha256:%s","size":%s}]}\n' \
    "$digest" "$size" >"$manifest"
}

make_binary() {
  local path=$1 marker=$2 healthy=$3
  cat >"$path" <<EOF
#!/bin/sh
case "\${1:-}" in
  update) exit 0 ;;
  healthcheck) exit $((healthy ? 0 : 1)) ;;
  version) printf '%s\\n' '$marker' ;;
esac
exit 0
EOF
  chmod +x "$path"
}

setup_case() {
  case_dir=$scratch/$1
  mkdir -p "$case_dir/home/.local/bin" "$case_dir/fixtures" "$case_dir/mock-bin"
  bin=$case_dir/home/.local/bin/magpie
  make_binary "$bin" old 1
  make_binary "$case_dir/fixtures/magpie-cli-linux-amd64" new 1
  (
    cd "$case_dir/fixtures"
    fixture_checksum magpie-cli-linux-amd64 >magpie-cli-linux-amd64.sha256
  )
  printf '{"token":"fixture-token"}\n' >"$case_dir/fixtures/token.json"
  refresh_fixture_manifest "$case_dir/fixtures/magpie-cli-linux-amd64" "$case_dir/fixtures/manifest.json"
  cat >"$case_dir/fixtures/release.json" <<'EOF'
{"tag_name":"fork-v0.1.2732-good","assets":[
  {"name":"magpie-cli-linux-amd64","browser_download_url":"https://github.com/ryan-zhao-go/magpie/releases/download/fork-v0.1.2732-good/magpie-cli-linux-amd64"},
  {"name":"magpie-cli-linux-amd64.sha256","browser_download_url":"https://github.com/ryan-zhao-go/magpie/releases/download/fork-v0.1.2732-good/magpie-cli-linux-amd64.sha256"}
]}
EOF
  cat >"$case_dir/mock-bin/curl" <<'EOF'
#!/bin/sh
set -eu
out=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output|-o) out=$2; shift 2 ;;
    --header|-H) shift 2 ;;
    --resolve) shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
[ -n "$out" ] && [ -n "$url" ]
case "$url" in
  'https://ghcr.io/token?scope=repository:ryan-zhao-go/magpie-fork:pull&service=ghcr.io')
    cp "$FIXTURE_DIR/token.json" "$out" ;;
  https://ghcr.io/v2/ryan-zhao-go/magpie-fork/manifests/latest)
    cp "$FIXTURE_DIR/manifest.json" "$out" ;;
  https://ghcr.io/v2/ryan-zhao-go/magpie-fork/blobs/sha256:*)
    cp "$FIXTURE_DIR/magpie-cli-linux-amd64" "$out" ;;
  https://github.com/*|https://raw.githubusercontent.com/*|https://api.github.com/*)
    exit 28 ;;
  *) exit 22 ;;
esac
EOF
  cat >"$case_dir/mock-bin/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$SYSTEMCTL_LOG"
if [ "${STAGED_OFFICIAL:-}" ] && [ "$*" = "--user stop magpie.service" ] && [ ! -e "$SYSTEMCTL_LOG.staged" ]; then
  cp "$STAGED_OFFICIAL" "$MAGPIE_BIN"
  : >"$SYSTEMCTL_LOG.staged"
fi
exit 0
EOF
  chmod +x "$case_dir/mock-bin/curl" "$case_dir/mock-bin/systemctl"
  export HOME=$case_dir/home
  export MAGPIE_BIN=$bin
  export MAGPIE_HEALTH_TRIES=1
  export MAGPIE_HEALTH_DELAY=0
  export FIXTURE_DIR=$case_dir/fixtures
  export SYSTEMCTL_LOG=$case_dir/systemctl.log
  export PATH=$case_dir/mock-bin:/usr/bin:/bin
  unset STAGED_OFFICIAL
}

setup_case success
"$updater"
[ "$("$bin" version)" = new ] || fail "the verified release was not installed"
[ "$("$bin.previous" version)" = old ] || fail "the last-known-good binary was not retained"
[ "$(cat "$SYSTEMCTL_LOG")" = $'--user stop magpie.service\n--user start magpie.service' ] || fail "service was not stopped and started exactly once"

before=$(wc -l <"$SYSTEMCTL_LOG")
"$updater"
[ "$(wc -l <"$SYSTEMCTL_LOG")" -eq "$before" ] || fail "an unchanged release restarted the service"

setup_case staged-official
make_binary "$case_dir/fixtures/official" official 1
export STAGED_OFFICIAL=$case_dir/fixtures/official
"$updater"
[ "$("$bin" version)" = new ] || fail "the fork did not replace an official update staged on stop"
[ "$("$bin.previous" version)" = old ] || fail "the staged official binary replaced the last-known-good fork"

setup_case bad-oci-digest
sed -i.bak "s/sha256:[0-9a-f]\\{64\\}/sha256:$(printf '%064d' 0)/" "$FIXTURE_DIR/manifest.json"
if "$updater" >/dev/null 2>&1; then fail "a bad OCI digest was accepted"; fi
[ "$("$bin" version)" = old ] || fail "a bad OCI digest changed the binary"

setup_case incompatible-binary
printf 'not a Linux executable\n' >"$FIXTURE_DIR/magpie-cli-linux-amd64"
chmod +x "$FIXTURE_DIR/magpie-cli-linux-amd64"
refresh_fixture_manifest "$FIXTURE_DIR/magpie-cli-linux-amd64" "$FIXTURE_DIR/manifest.json"
if "$updater" >/dev/null 2>&1; then fail "an incompatible binary was accepted"; fi
[ ! -e "$SYSTEMCTL_LOG" ] || fail "an incompatible binary stopped or started the service"
[ "$("$bin" version)" = old ] || fail "an incompatible binary changed the installed binary"

setup_case rollback
make_binary "$FIXTURE_DIR/magpie-cli-linux-amd64" broken 0
(
  cd "$FIXTURE_DIR"
  fixture_checksum magpie-cli-linux-amd64 >magpie-cli-linux-amd64.sha256
)
refresh_fixture_manifest "$FIXTURE_DIR/magpie-cli-linux-amd64" "$FIXTURE_DIR/manifest.json"
if "$updater" >/dev/null 2>&1; then fail "an unhealthy release was accepted"; fi
[ "$("$bin" version)" = old ] || fail "health failure did not restore the old binary"
[ "$(cat "$SYSTEMCTL_LOG")" = $'--user stop magpie.service\n--user start magpie.service\n--user stop magpie.service\n--user start magpie.service' ] || fail "rollback did not stop and start the service"

printf 'magpie-fork-update tests passed\n'
