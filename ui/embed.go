// Package ui holds the embedded web client assets. The files here are always
// embedded into the binary; at runtime the on-disk ./ui directory takes
// precedence when it exists (see package web), so editing these files in a dev
// checkout is enough.
//
// The TypeScript sources live in ui/src/ and are compiled to ui/app/ by
// ui/build.sh. The stylesheets are compiled by ui/build.sh (sass) from
// base.scss and themes.scss into base.css and themes.css. All output is
// committed so that `go build` needs no JavaScript or CSS toolchain.
package ui

import "embed"

// FS holds the embedded UI assets: the page, the two compiled stylesheets, the
// compiled app, the vendored Mithril bundle, and the notification sound.
//
//go:embed index.html base.css themes.css app vendor sound
var FS embed.FS
