#!/bin/sh
# Builds zipped release binaries for macOS, Windows and Linux into /tmp.
#
# Usage:
#   scripts/deploy.sh
#   DEPLOY_OUT=/tmp/release  scripts/deploy.sh
#   DEPLOY_TARGETS="linux/arm64 darwin/arm64" scripts/deploy.sh
#   DEPLOY_SKIP_UI=1 scripts/deploy.sh      # reuse existing compiled UI output
#   DEPLOY_STRIP=0   scripts/deploy.sh      # keep symbols in the binaries
#
# Env:
#   DEPLOY_OUT          output directory for the zips (default /tmp)
#   DEPLOY_TARGETS      space-separated os/arch pairs, e.g. "linux/amd64"
#   DEPLOY_SKIP_UI      1 skips ./ui/build.sh (requires ui/app, ui/base.css,
#                       ui/themes.css to already exist — they are embedded)
#   DEPLOY_STRIP        0 drops -s -w from the linker flags (default 1)
#   DEPLOY_DARWIN_CGO   auto (default) | 1 | 0 — see the macOS note below
#   DEPLOY_DARWIN_CC    cc wrapper for the darwin target (osxcross), per-arch
#                       default: o64-clang for amd64, oa64-clang for arm64
#
# The UI is embedded into the binary, so the compiled TypeScript/CSS is built
# first. Output names are plexo-<version>-<os>-<arch>.zip plus a SHA256SUMS
# file, where <version> comes from `git describe --tags --always --dirty`.
#
# macOS: the system tray needs cgo. Native builds on macOS always use it; from
# Linux that needs an osxcross-style cross toolchain (see DEPLOY_DARWIN_CC).
# Without one the darwin binary is built with CGO_ENABLED=0, which compiles the
# tray out and falls back to the terminal console (--no-systray behavior).
#
# Requires: go, zip. tsc and sass for the UI step.
set -eu

cd "$(dirname "$0")/.."

OUT="${DEPLOY_OUT:-/tmp}"
TARGETS="${DEPLOY_TARGETS:-linux/amd64 windows/amd64 darwin/amd64}"
SKIP_UI="${DEPLOY_SKIP_UI:-0}"
STRIP="${DEPLOY_STRIP:-1}"

command -v go >/dev/null 2>&1 || { echo "deploy: go not found in PATH" >&2; exit 1; }
command -v zip >/dev/null 2>&1 || { echo "deploy: zip not found in PATH" >&2; exit 1; }

if [ "$SKIP_UI" != 1 ]; then
	./ui/build.sh
else
	for f in ui/app ui/base.css ui/themes.css; do
		[ -e "$f" ] || { echo "deploy: $f missing; drop DEPLOY_SKIP_UI=1" >&2; exit 1; }
	done
fi

VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
VERSION="$(printf '%s' "$VERSION" | tr '/ ' '--')"

LDFLAGS=""
if [ "$STRIP" != 0 ]; then
	LDFLAGS="-s -w"
fi

# Prints the cgo cross compiler for a darwin arch, if one is available.
darwin_cc() {
	arch="$1"
	if [ -n "${DEPLOY_DARWIN_CC:-}" ]; then
		command -v "$DEPLOY_DARWIN_CC" >/dev/null 2>&1 || return 1
		printf '%s\n' "$DEPLOY_DARWIN_CC"
		return 0
	fi
	case "$arch" in
	amd64) candidates="o64-clang x86_64-apple-darwin-clang" ;;
	arm64) candidates="oa64-clang arm64-apple-darwin-clang" ;;
	*) candidates="" ;;
	esac
	for c in $candidates; do
		if command -v "$c" >/dev/null 2>&1; then
			printf '%s\n' "$c"
			return 0
		fi
	done
	return 1
}

mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"

STAGE="$(mktemp -d "${TMPDIR:-/tmp}/plexo-deploy.XXXXXX")"
trap 'rm -rf "$STAGE"' EXIT INT TERM

SUMS="$OUT/plexo-$VERSION-SHA256SUMS"
: >"$SUMS"

echo "deploy: version $VERSION -> $OUT"
for target in $TARGETS; do
	case "$target" in
	*/*) ;;
	*)
		echo "deploy: bad target '$target' (want os/arch)" >&2
		exit 1
		;;
	esac
	os="${target%%/*}"
	arch="${target##*/}"
	bin="plexo"
	[ "$os" = windows ] && bin="plexo.exe"

	cgo=0
	cc=""
	if [ "$os" = darwin ]; then
		case "${DEPLOY_DARWIN_CGO:-auto}" in
		0) cgo=0 ;;
		1)
			cc="$(darwin_cc "$arch")" || {
				echo "deploy: DEPLOY_DARWIN_CGO=1 but no cross compiler for darwin/$arch" >&2
				echo "deploy: set DEPLOY_DARWIN_CC to an osxcross clang wrapper" >&2
				exit 1
			}
			cgo=1
			;;
		auto)
			if [ "$(uname -s)" = Darwin ]; then
				cgo=1
			elif cc="$(darwin_cc "$arch")"; then
				cgo=1
			else
				echo "deploy: warning: no darwin cross compiler; building darwin/$arch without cgo (no system tray)" >&2
			fi
			;;
		*)
			echo "deploy: DEPLOY_DARWIN_CGO must be auto, 1 or 0" >&2
			exit 1
			;;
		esac
	fi

	echo "deploy: building $os/$arch (cgo=$cgo${cc:+, cc=$cc})"
	rm -f "$STAGE/$bin"
	if [ -n "$cc" ]; then
		env GOOS="$os" GOARCH="$arch" CGO_ENABLED="$cgo" CC="$cc" \
			go build -trimpath -ldflags "$LDFLAGS" -o "$STAGE/$bin" ./cmd/plexo
	else
		env GOOS="$os" GOARCH="$arch" CGO_ENABLED="$cgo" \
			go build -trimpath -ldflags "$LDFLAGS" -o "$STAGE/$bin" ./cmd/plexo
	fi

	zipname="plexo-$VERSION-$os-$arch.zip"
	rm -f "$OUT/$zipname"
	zip -q -9 -j "$OUT/$zipname" "$STAGE/$bin"

	if command -v sha256sum >/dev/null 2>&1; then
		(cd "$OUT" && sha256sum "$zipname" >>"$SUMS")
	elif command -v shasum >/dev/null 2>&1; then
		(cd "$OUT" && shasum -a 256 "$zipname" >>"$SUMS")
	fi
	printf 'deploy: %s (%s)\n' "$OUT/$zipname" "$(wc -c <"$OUT/$zipname" | tr -d ' ') bytes"
done

echo "deploy: checksums in $SUMS"