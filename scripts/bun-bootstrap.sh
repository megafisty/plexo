#!/bin/sh
# Installs the pinned Bun release into the project-local .bun/ directory.
#
# Downloads the official release archive, verifies it against the committed
# SHA256 in scripts/bun-checksums.txt, and extracts the binary to .bun/bin/bun.
# Nothing touches a global install and no remote script is executed. Re-running
# is a no-op once the pinned version is in place.
#
# Why project-local at all: a globally installed Bun is a popular entrypoint for
# npm supply-chain malware, so Bun stays pinned, checksum-verified and off the
# global PATH.
#
# The version is read from .bun-version; bump that and scripts/bun-checksums.txt
# together. Windows users install Bun manually (see docs/ui-css.md).
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
version=$(tr -d '[:space:]' <"$root/.bun-version")
[ -n "$version" ] || { echo "bun-bootstrap: .bun-version is empty" >&2; exit 1; }

dest="$root/.bun"
bun="$dest/bin/bun"

if [ -x "$bun" ] && [ "$("$bun" --version 2>/dev/null || true)" = "$version" ]; then
	echo "bun-bootstrap: bun $version already installed at .bun/bin/bun"
	exit 0
fi

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*)
	echo "bun-bootstrap: unsupported OS '$(uname -s)'. Windows/MSYS users install Bun manually:" >&2
	echo "  https://github.com/oven-sh/bun/releases/download/bun-v$version/bun-windows-x64.zip" >&2
	echo "  place bun.exe on PATH, or set BUN to its path, and re-run ./ui/build.sh." >&2
	exit 1
	;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=x64 ;;
aarch64 | arm64) arch=aarch64 ;;
*)
	echo "bun-bootstrap: unsupported architecture '$(uname -m)'" >&2
	exit 1
	;;
esac

asset="bun-$os-$arch.zip"
notes="$root/scripts/bun-checksums.txt"
expected=$(awk -v a="$asset" '$2 == a { print $1 }' "$notes")
[ -n "$expected" ] || { echo "bun-bootstrap: no checksum for $asset in scripts/bun-checksums.txt" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || { echo "bun-bootstrap: curl not found" >&2; exit 1; }
command -v unzip >/dev/null 2>&1 || { echo "bun-bootstrap: unzip not found" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
	sum() { sha256sum "$1" | awk '{ print $1 }'; }
elif command -v shasum >/dev/null 2>&1; then
	sum() { shasum -a 256 "$1" | awk '{ print $1 }'; }
else
	echo "bun-bootstrap: need sha256sum or shasum to verify the download" >&2
	exit 1
fi

url="https://github.com/oven-sh/bun/releases/download/bun-v$version/$asset"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/plexo-bun.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "bun-bootstrap: downloading $asset ($version)"
curl -fsSL --retry 3 -o "$tmp/$asset" "$url"

actual=$(sum "$tmp/$asset")
if [ "$actual" != "$expected" ]; then
	echo "bun-bootstrap: checksum mismatch for $asset" >&2
	echo "  expected $expected" >&2
	echo "  actual   $actual" >&2
	exit 1
fi

unzip -q "$tmp/$asset" -d "$tmp/extract"
mkdir -p "$dest/bin"
install -m 0755 "$tmp/extract/bun-$os-$arch/bun" "$bun"

installed=$("$bun" --version)
[ "$installed" = "$version" ] || { echo "bun-bootstrap: installed bun reports '$installed', expected '$version'" >&2; exit 1; }
echo "bun-bootstrap: installed bun $version at .bun/bin/bun"