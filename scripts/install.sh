#!/usr/bin/env bash
# Installs the latest NusaLLM release for the current user:
#
#   curl -fsSL https://raw.githubusercontent.com/jahrulnr/NusaLLM/master/scripts/install.sh | bash
#
# The release binary is checked against SHA256SUMS, then run with
# `serve install`, which copies it to ~/.local/bin/nusallm and registers
# the WebUI service. Arguments are passed on to `serve install`:
#
#   ... | bash -s -- --no-service      # CLI only
#
# Remove it again with `nusallm uninstall`.
set -eo pipefail

RELEASE_URL="https://github.com/jahrulnr/NusaLLM/releases/latest/download"

die() {
  echo "nusallm install: $*" >&2
  exit 1
}

[ "$(uname -s)" = Linux ] || die "release binaries are published for Linux only"
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required"

asset="nusallm-linux-$arch"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "nusallm install: downloading $asset"
curl -fsSL -o "$tmp/$asset" "$RELEASE_URL/$asset" || die "download failed: $RELEASE_URL/$asset"
curl -fsSL -o "$tmp/SHA256SUMS" "$RELEASE_URL/SHA256SUMS" || die "download failed: $RELEASE_URL/SHA256SUMS"

expected=$(awk -v name="$asset" '$2 == name { print $1 }' "$tmp/SHA256SUMS")
actual=$(sha256sum "$tmp/$asset" | awk '{ print $1 }')
[ -n "$expected" ] || die "$asset is not listed in SHA256SUMS"
[ "$expected" = "$actual" ] || die "checksum mismatch for $asset"

chmod +x "$tmp/$asset"
# stdin belongs to the piped script; keep the binary away from it.
"$tmp/$asset" serve install "$@" </dev/null
