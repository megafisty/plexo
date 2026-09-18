// Package fchatpipe provides an in-memory fchat.Conn pair for tests and the
// fake server, so the whole chat pipeline can run without a network.
package fchatpipe

import (
	"context"
	"sync"

	"plexo/internal/fchat"
)

// pipe is the shared state for a pair of in-memory connections.
type pipe struct {
	aToB chan fchat.Frame
	bToA chan fchat.Frame
	done chan struct{}
	once sync.Once
}

// Pipe returns two connected Conns. Writing on one is readable on the other.
func Pipe() (fchat.Conn, fchat.Conn) {
	p := &pipe{
		aToB: make(chan fchat.Frame, 256),
		bToA: make(chan fchat.Frame, 256),
		done: make(chan struct{}),
	}
	a := &memConn{in: p.bToA, out: p.aToB, p: p}
	b := &memConn{in: p.aToB, out: p.bToA, p: p}
	return a, b
}

type memConn struct {
	in  <-chan fchat.Frame
	out chan<- fchat.Frame
	p   *pipe
}

func (c *memConn) Read(ctx context.Context) (fchat.Frame, error) {
	select {
	case cmd := <-c.in:
		return cmd, nil
	case <-c.p.done:
		return fchat.Frame{}, fchat.ErrClosed{}
	case <-ctx.Done():
		return fchat.Frame{}, ctx.Err()
	}
}

func (c *memConn) Write(ctx context.Context, cmd fchat.Frame) error {
	select {
	case c.out <- cmd:
		return nil
	case <-c.p.done:
		return fchat.ErrClosed{}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *memConn) Close() error {
	c.p.once.Do(func() { close(c.p.done) })
	return nil
}
