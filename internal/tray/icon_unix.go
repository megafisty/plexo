//go:build !windows

package tray

import _ "embed"

// trayIcon is the tray image encoded as PNG. The Unix backend decodes it with
// image.Decode and only registers a PNG decoder, so it must be PNG.
//
//go:embed assets/plexo.png
var trayIcon []byte
