#!/bin/sh
# Builds the Plexo web client. TypeScript is typechecked by tsc (from
# node_modules) and bundled into a single ui/app/main.js by Bun; CSS is compiled
# by Dart Sass. Both tools are project-local: Bun and its package cache live in
# the gitignored .bun/, the rest in ui/node_modules/.
#
# ./scripts/bun resolves the project-local Bun and bootstraps (download +
# checksum verify) on a fresh checkout, so nothing needs a global install.
#
# CSS stays on Sass: Bun's CSS bundler injects --buncss-* custom properties and
# its minifier rewrites colors/keyframes (e.g. 4-digit hex), which the QtWebKit
# target may not support. See docs/ui-css.md.
#
# Output (ui/app/main.js, ui/base.css, ui/themes.css) is gitignored, so run this
# before `go build` on a fresh checkout.
set -eu
cd "$(dirname "$0")"
exec ../scripts/bun run build