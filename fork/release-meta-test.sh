#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd -P)
script="$root/fork/release-meta.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() {
  printf 'release-meta test failed: %s\n' "$*" >&2
  exit 1
}

out="$tmp/output"
GITHUB_OUTPUT="$out" "$script" \
  v0.1.2732 \
  f3551387c6de0502dbd913d079c09c2f61938b11 \
  c35874e05d64b83b178b0f19bf7f07c797c304f0 \
  20261102153040

grep -qx 'version=0.1.2732-fork.20261102153040' "$out" || fail "wrong version"
grep -qx 'release_tag=fork-v0.1.2732-f3551387c6de-c35874e05d64' "$out" || fail "wrong release tag"
grep -qx 'release_name=magpie 0.1.2732-fork.20261102153040' "$out" || fail "wrong release name"

bad() {
  local name=$1
  shift
  if GITHUB_OUTPUT="$tmp/$name" "$script" "$@" >/dev/null 2>&1; then
    fail "$name was accepted"
  fi
}

bad version v0.1/2732 f3551387c6de0502dbd913d079c09c2f61938b11 c35874e05d64b83b178b0f19bf7f07c797c304f0 20261102153040
bad upstream v0.1.2732 'f355 bad' c35874e05d64b83b178b0f19bf7f07c797c304f0 20261102153040
bad patch v0.1.2732 f3551387c6de0502dbd913d079c09c2f61938b11 c35874e05d64b83b178b0f19bf7f07c797c304g0 20261102153040
bad timestamp v0.1.2732 f3551387c6de0502dbd913d079c09c2f61938b11 c35874e05d64b83b178b0f19bf7f07c797c304f0 '2026-11-02 15:30'
bad missing v0.1.2732 f3551387c6de0502dbd913d079c09c2f61938b11 c35874e05d64b83b178b0f19bf7f07c797c304f0

printf 'release-meta tests passed\n'
