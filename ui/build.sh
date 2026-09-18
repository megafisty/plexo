#!/bin/sh
# Builds the Plexo web client. TypeScript is compiled with tsc and CSS with
# sass (Dart Sass); there is no bundler beyond those. The emitted JavaScript in
# ui/app/ and the two compiled stylesheets are committed, so `go build` works on
# a fresh checkout without node or sass.
#
# CSS compiles to flat sheets — no @layer and no @import — because the target
# runtime (QtWebKit) predates CSS cascade layers. index.html links base.css then
# themes.css, which is the whole cascade order.
set -eu
cd "$(dirname "$0")"

tsc -p tsconfig.json
sass --no-source-map --style=compressed base.scss base.css
sass --no-source-map --style=compressed themes.scss themes.css