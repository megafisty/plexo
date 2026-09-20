#!/bin/sh
# Headless unit tests for the client logic (store reducers, timeline window,
# reconnect interest). Runs the TypeScript sources directly under `bun test`,
# which transpiles on the fly — there is no separate compile step and the
# importable ui/app/*.js intermediate is gone. test/setup.mjs is preloaded via
# ui/bunfig.toml to stub the browser globals the modules touch at import time.
# No bundler and no network; unlike test/uibrowse this is not the UI harness and
# needs no approval to run.
set -eu
cd "$(dirname "$0")"
exec ../scripts/bun run test