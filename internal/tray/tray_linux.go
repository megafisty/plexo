//go:build linux

package tray

import "github.com/godbus/dbus/v5"

// Available reports whether a StatusNotifier host is present on the session
// bus. The tray registers through the org.kde.StatusNotifierWatcher name;
// without an owner there is nothing to show the icon (and fyne's backend logs
// a registration error and panics on shutdown), so the caller falls back to
// the console instead.
func Available() bool {
	// A private connection avoids touching the shared one fyne will use, and
	// NoAutoStartup avoids launching a session bus just to probe for one.
	conn, err := dbus.SessionBusPrivateNoAutoStartup()
	if err != nil {
		return false
	}
	defer conn.Close()
	if err := conn.Auth(nil); err != nil {
		return false
	}
	if err := conn.Hello(); err != nil {
		return false
	}
	var hasOwner bool
	err = conn.BusObject().
		Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").
		Store(&hasOwner)
	if err != nil {
		return false
	}
	return hasOwner
}

// Run blocks on the tray event loop. It must be called from the main
// goroutine.
func Run(url string) { runSystray(url) }
