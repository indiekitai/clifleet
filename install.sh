#!/bin/sh
# Install or upgrade clifleet from a GitHub release build:
#
#   curl -fsSL https://raw.githubusercontent.com/indiekitai/clifleet/main/install.sh | sh
#
# CLIFLEET_VERSION picks a tag (default: the latest release).
# CLIFLEET_INSTALL_DIR picks the directory (default: ~/.local/bin).
# The archive must match the release's SHA256SUMS, and the new binary must
# run here before it replaces anything.
set -eu

releases=https://github.com/indiekitai/clifleet/releases
dir=${CLIFLEET_INSTALL_DIR:-$HOME/.local/bin}

fail() {
  printf 'clifleet install: %s\n' "$*" >&2
  exit 1
}

case $(uname -s) in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "no release build for $(uname -s); build from source with go install" ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "no release build for $(uname -m); build from source with go install" ;;
esac
command -v curl >/dev/null 2>&1 || fail "curl is required"

tag=${CLIFLEET_VERSION:-}
if [ -z "$tag" ]; then
  url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$releases/latest") || fail "cannot reach $releases"
  tag=${url##*/}
fi
case $tag in
  v*) ;;
  *) tag=v$tag ;;
esac
case $tag in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) fail "unexpected release tag: $tag" ;;
esac

name=clifleet_${os}_${arch}
work=$(mktemp -d 2>/dev/null || mktemp -d -t clifleet)
trap 'rm -rf "$work"' EXIT

curl -fsSL -o "$work/$name.tar.gz" "$releases/download/$tag/$name.tar.gz" || fail "cannot download $name.tar.gz from $tag"
curl -fsSL -o "$work/SHA256SUMS" "$releases/download/$tag/SHA256SUMS" || fail "$tag has no SHA256SUMS"
want=$(awk -v f="$name.tar.gz" '$2 == f || $2 == "*" f { print $1 }' "$work/SHA256SUMS")
[ -n "$want" ] || fail "$name.tar.gz is not listed in SHA256SUMS"
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$work/$name.tar.gz" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  got=$(shasum -a 256 "$work/$name.tar.gz" | awk '{ print $1 }')
else
  fail "sha256sum or shasum is required"
fi
[ "$got" = "$want" ] || fail "$name.tar.gz does not match SHA256SUMS of $tag; nothing was installed"

tar -xzf "$work/$name.tar.gz" -C "$work" "$name" || fail "$name is not in $name.tar.gz"
mkdir -p "$dir"
new=$dir/.clifleet-install.$$
cp "$work/$name" "$new"
chmod 755 "$new"
if [ "$("$new" version 2>/dev/null || true)" != "clifleet $tag" ]; then
  rm -f "$new"
  fail "the $tag build does not run here; nothing was installed"
fi
mv -f "$new" "$dir/clifleet"
printf 'installed clifleet %s to %s\n' "$tag" "$dir/clifleet"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) printf 'add %s to PATH to run it as clifleet\n' "$dir" ;;
esac
