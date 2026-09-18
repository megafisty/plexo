#!/bin/sh
# Headless unit tests for the compiled client logic (store reducers, timeline
# window, reconnect interest). Compiles TypeScript first because the tests
# import ui/app/*.js, then runs node's test runner with the browser stubs
# preloaded. No bundler and no network; unlike test/uibrowse this is not the UI
# harness and needs no approval to run.
set -eu
cd "$(dirname "$0")"

tsc -p tsconfig.json
node --import ./test/setup.mjs --test test/