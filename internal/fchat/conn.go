package fchat

import "context"

// ErrClosed is returned by a connection after it is closed.
type ErrClosed struct{}

func (ErrClosed) Error() string { return "fchat: connection closed" }

// Conn is the transport abstraction. Implementations deliver whole F-Chat
// frames (a code plus optional JSON payload), so the session layer is
// transport agnostic. A real implementation wraps a WebSocket.
//
// Read must be called serially. Write is safe for concurrent callers.
type Conn interface {
	Read(ctx context.Context) (Frame, error)
	Write(ctx context.Context, cmd Frame) error
	Close() error
}
