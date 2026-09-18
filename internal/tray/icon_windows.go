//go:build windows

package tray

import _ "embed"

// trayIcon is the tray image encoded as ICO; the Windows backend writes it to a
// temp file and hands it to Shell_NotifyIcon, which expects an icon resource.
//
//go:embed assets/plexo.ico
var trayIcon []byte
