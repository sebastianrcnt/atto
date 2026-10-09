#!/bin/sh
# Install or update atto from its GitHub releases.
#
#   curl -fsSL https://raw.githubusercontent.com/sebastianrcnt/atto/main/install.sh | sh
#
# ATTO_VARIANT=slim|full picks the binary variant (default: full).
# ATTO_CHANNEL=stable|edge picks the channel (default: stable, the latest
# release; edge is the unstable build of main);
# ATTO_VERSION=v0.1.0 pins an exact release and wins over ATTO_CHANNEL;
# ATTO_INSTALL_DIR picks the directory (default: ~/.local/bin).
# Running it again installs the newest build of the channel over the old one.
set -eu

repo=sebastianrcnt/atto
dir=${ATTO_INSTALL_DIR:-$HOME/.local/bin}
channel=${ATTO_CHANNEL:-stable}
version=${ATTO_VERSION:-}
variant=${ATTO_VARIANT:-full}

fail() { echo "atto install: $*" >&2; exit 1; }

case $(uname -s) in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "unsupported OS $(uname -s); on Windows use install.ps1" ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "unsupported CPU $(uname -m)" ;;
esac
case $variant in
  full) prefix=atto ;;
  slim) prefix=atto-slim ;;
  *) fail "unknown ATTO_VARIANT '$variant'; use slim or full" ;;
esac
asset=${prefix}_${os}_${arch}

# ATTO_VERSION=edge and ATTO_VERSION=latest are older spellings of
# ATTO_CHANNEL=edge and ATTO_CHANNEL=stable; they are still accepted.
case $version in
  edge) channel=edge version= ;;
  latest) version= ;;
esac
case $channel in
  stable | edge) ;;
  *) fail "unknown ATTO_CHANNEL '$channel'; use stable or edge" ;;
esac

if [ -n "${ATTO_DOWNLOAD_BASE:-}" ]; then # a mirror, or a local test server
  base=$ATTO_DOWNLOAD_BASE
elif [ -n "$version" ]; then
  base=https://github.com/$repo/releases/download/$version
elif [ "$channel" = edge ]; then
  base=https://github.com/$repo/releases/download/edge
else
  base=https://github.com/$repo/releases/latest/download
fi

if command -v curl >/dev/null 2>&1; then
  get() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  get() { wget -qO "$2" "$1"; }
else
  fail "needs curl or wget"
fi
if command -v sha256sum >/dev/null 2>&1; then
  sha() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  fail "needs sha256sum or shasum to verify the download"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $asset (${version:-$channel})..."
get "$base/$asset" "$tmp/atto" || fail "download failed: $base/$asset"
get "$base/checksums.txt" "$tmp/checksums.txt" || fail "download failed: $base/checksums.txt"

want=$(awk -v a="$asset" '$2 == a || $2 == "*" a { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "checksums.txt has no $asset"
[ "$(sha "$tmp/atto")" = "$want" ] || fail "checksum mismatch for $asset; not installing"

mkdir -p "$dir"
chmod 755 "$tmp/atto"
mv -f "$tmp/atto" "$dir/atto.new"
mv -f "$dir/atto.new" "$dir/atto"
echo "Installed $("$dir/atto" -version) to $dir/atto"

case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "Add $dir to your PATH, e.g.: echo 'export PATH=\"$dir:\$PATH\"' >> ~/.profile" ;;
esac
