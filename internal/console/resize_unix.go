//go:build !windows

package console

import (
	"os"
	"os/signal"
	"syscall"
)

// resizeSignals returns a channel that fires on terminal resize.
func resizeSignals() <-chan os.Signal {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	return ch
}
