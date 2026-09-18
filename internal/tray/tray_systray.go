//go:build windows || linux || (darwin && cgo)

package tray

import (
	"fyne.io/systray"

	"plexo/internal/browser"
)

// runSystray runs the tray event loop on the calling goroutine, which must be
// the main goroutine, and blocks until the user quits. The menu shows the UI
// address (click to open a browser) and a Shut Down item.
func runSystray(url string) {
	systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTitle("Plexo")
		systray.SetTooltip("Plexo")
		open := systray.AddMenuItem("Plexo UI at "+url+" (open in browser)", "Open the Plexo UI")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Shut Down", "Stop the Plexo core")
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					browser.Open(url)
				case <-quit.ClickedCh:
					systray.Quit()
					return
				}
			}
		}()
	}, func() {})
}

// Quit stops the tray event loop, unblocking Run.
func Quit() { systray.Quit() }
