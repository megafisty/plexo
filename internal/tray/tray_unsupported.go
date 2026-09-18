//go:build (darwin && !cgo) || (!windows && !linux && !darwin)

package tray

// Available is false where no tray backend is compiled in, so the caller uses
// the console instead.
func Available() bool { return false }

// Run is a no-op; callers must check Available first.
func Run(string) {}

// Quit is a no-op; no event loop is running.
func Quit() {}
