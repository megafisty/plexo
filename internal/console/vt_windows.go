//go:build windows

package console

import "golang.org/x/sys/windows"

// enableVirtualTerminal asks the Windows console to interpret ANSI escape
// sequences on the given output handle. x/term turns on VT input but not VT
// output, and conhost leaves output processing off by default, so the escape
// sequences the screen relies on would otherwise be printed literally.
// Terminals that already process them succeed here too; errors are ignored.
func enableVirtualTerminal(fd uintptr) {
	h := windows.Handle(fd)
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return
	}
	_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
}
