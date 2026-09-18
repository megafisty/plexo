package console

import (
	"sync"
	"time"
)

// noticeTTL bounds how long a notice stays on screen after it is set, so a
// stale error does not linger forever.
const noticeTTL = 30 * time.Second

// Notice holds the most recent short-lived status message, typically an error.
// The core sets it from its own goroutines while the screen reads it on every
// refresh, so all access is guarded. A notice older than noticeTTL reads as
// empty.
type Notice struct {
	mu   sync.Mutex
	text string
	at   time.Time
}

// Set records text as the current notice. An empty text clears it.
func (n *Notice) Set(text string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.text = text
	n.at = time.Now()
	n.mu.Unlock()
}

// Text returns the current notice, or "" when none was set or it has expired.
func (n *Notice) Text() string {
	if n == nil {
		return ""
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.text == "" || time.Since(n.at) > noticeTTL {
		return ""
	}
	return n.text
}
