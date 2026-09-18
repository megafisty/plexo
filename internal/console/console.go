// Package console renders a fixed, non-scrolling status screen for the core
// while it runs in a terminal: the UI address, the live character sessions,
// and the connected browser clients. It never writes log output.
//
// When stdin or stdout is not a terminal (a daemon or a service manager runs
// it headless) the screen is skipped and the process simply waits for a
// reload or shutdown signal, so daemon use is unaffected.
package console

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"plexo/internal/browser"
)

// Options configures the console screen. The callbacks are optional; a nil
// callback reports no items.
type Options struct {
	// URL is the UI address shown to the user.
	URL string
	// Sessions reports the currently live character names.
	Sessions func() []string
	// Clients reports the remote addresses of connected browser clients.
	Clients func() []string
	// Notice holds the most recent error, shown above the key legend. Nil
	// disables the notice line.
	Notice *Notice
}

// refreshInterval is how often the screen is re-read from the callbacks. The
// screen is only repainted when its contents change.
const refreshInterval = 500 * time.Millisecond

// State is the data shown on the screen.
type State struct {
	URL      string
	Sessions []string
	Clients  []string
	// Notice is the most recent error, or "".
	Notice string
}

// Run displays the status screen until the user quits, then restores the
// terminal. It returns when stdin closes, on 'q' or Ctrl-C, or on SIGINT or
// SIGTERM. When stdin or stdout is not a terminal, Run instead waits for one
// of those signals without drawing anything.
func Run(opts Options) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return waitForExit()
	}

	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, old)
	enableVirtualTerminal(os.Stdout.Fd())

	out := bufio.NewWriter(os.Stdout)
	// Enter the alternate screen so the UI never scrolls the user's
	// scrollback, and hide the cursor.
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")
	_ = out.Flush()
	defer func() {
		fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")
		_ = out.Flush()
	}()

	keys := make(chan byte)
	go readKeys(keys)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	resize := resizeSignals()

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	last := ""
	draw := func() {
		w, h, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || w <= 0 || h <= 0 {
			return
		}
		lines := Layout(w, h, State{
			URL:      opts.URL,
			Sessions: call(opts.Sessions),
			Clients:  call(opts.Clients),
			Notice:   opts.Notice.Text(),
		})
		frame := strings.Join(lines, "\n")
		if frame == last {
			return
		}
		last = frame
		paint(out, lines)
	}
	draw()

	for {
		select {
		case b, ok := <-keys:
			if !ok {
				return nil
			}
			switch b {
			case 'q', 3: // 'q' or Ctrl-C (delivered as a byte in raw mode)
				return nil
			case 'b':
				browser.Open(opts.URL)
			}
		case <-ticker.C:
			draw()
		case <-resize:
			draw()
		case <-interrupt:
			return nil
		}
	}
}

// call invokes an optional source, tolerating a nil callback.
func call(fn func() []string) []string {
	if fn == nil {
		return nil
	}
	return fn()
}

// paint redraws every line in place. Each line is cleared first, so a shorter
// line cannot leave stale text, and no trailing newline is written after the
// last line so the bottom row never scrolls.
func paint(out *bufio.Writer, lines []string) {
	out.WriteString("\x1b[H")
	for i, line := range lines {
		out.WriteString("\x1b[2K")
		out.WriteString(line)
		if i < len(lines)-1 {
			out.WriteString("\r\n")
		}
	}
	_ = out.Flush()
}

// readKeys forwards single keypress bytes from stdin until it errors or
// closes.
func readKeys(keys chan<- byte) {
	defer close(keys)
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if n == 1 {
			keys <- buf[0]
		}
		if err != nil {
			return
		}
	}
}

// waitForExit blocks until the process is asked to stop.
func waitForExit() error {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
	return nil
}
