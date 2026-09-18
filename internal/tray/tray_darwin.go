//go:build darwin && cgo

package tray

// Available reports whether the system tray is usable.
func Available() bool { return true }

// Run blocks on the tray event loop. It must be called from the main
// goroutine.
func Run(url string) { runSystray(url) }
