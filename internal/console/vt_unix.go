//go:build !windows

package console

// enableVirtualTerminal is a no-op outside Windows, where terminals process
// ANSI escape sequences natively.
func enableVirtualTerminal(uintptr) {}
