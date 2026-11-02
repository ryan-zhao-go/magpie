#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 4 ]]; then
  printf 'usage: release-meta.sh <upstream-version> <upstream-sha> <patch-sha> <utc-timestamp>\n' >&2
  exit 2
fi

base=${1#v}
upstream=$2
patch=$3
stamp=$4

if [[ ! $base =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  printf 'invalid upstream version: %s\n' "$1" >&2
  exit 2
fi
if [[ ! $upstream =~ ^[0-9a-f]{40}$ ]]; then
  printf 'invalid upstream commit: %s\n' "$upstream" >&2
  exit 2
fi
if [[ ! $patch =~ ^[0-9a-f]{40}$ ]]; then
  printf 'invalid patch commit: %s\n' "$patch" >&2
  exit 2
fi
if [[ ! $stamp =~ ^[0-9]{14}$ ]]; then
  printf 'invalid UTC timestamp: %s\n' "$stamp" >&2
  exit 2
fi
if [[ -z ${GITHUB_OUTPUT:-} ]]; then
  printf 'GITHUB_OUTPUT is not set\n' >&2
  exit 2
fi

version="$base-fork.$stamp"
release_tag="fork-v$base-${upstream:0:12}-${patch:0:12}"

{
  printf 'version=%s\n' "$version"
  printf 'release_tag=%s\n' "$release_tag"
  printf 'release_name=magpie %s\n' "$version"
} >>"$GITHUB_OUTPUT"
