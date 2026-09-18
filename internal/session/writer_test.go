package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"plexo/internal/fchat"
)

// succeedConn accepts writes and blocks reads until the context is done.
type succeedConn struct{}

func (succeedConn) Read(ctx context.Context) (fchat.Frame, error) {
	<-ctx.Done()
	return fchat.Frame{}, ctx.Err()
}

func (succeedConn) Write(context.Context, fchat.Frame) error { return nil }
func (succeedConn) Close() error                             { return nil }

// failConn rejects every write.
type failConn struct{}

func (failConn) Read(ctx context.Context) (fchat.Frame, error) {
	<-ctx.Done()
	return fchat.Frame{}, ctx.Err()
}

func (failConn) Write(context.Context, fchat.Frame) error { return errors.New("write failed") }
func (failConn) Close() error                             { return nil }

func liveCtx(t *testing.T, s *Session) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	return ctx
}

// TestWriteLoopReportsWriteFailure: a dead writer must wake the actor with an
// input error instead of leaving it queueing into a channel nobody drains, and
// must not run the frame's onSent side effect.
func TestWriteLoopReportsWriteFailure(t *testing.T) {
	s := New(Config{Character: "Vix"})
	ctx := liveCtx(t, s)

	out := make(chan outbound, 1)
	ran := false
	out <- outbound{wire: fchat.Frame{Code: "MSG"}, onSent: func() { ran = true }}
	go s.writeLoop(ctx, failConn{}, out, 7)

	select {
	case in := <-s.inbox:
		ni, ok := in.(netInput)
		if !ok || ni.gen != 7 || ni.err == nil {
			t.Fatalf("expected a generation-7 netInput error, got %#v", in)
		}
	case <-time.After(time.Second):
		t.Fatal("write failure was not reported to the actor")
	}
	if ran {
		t.Fatal("onSent ran despite a failed write")
	}
}

// TestWriteLoopRunsOnSentAfterWrite: onSent is delivered to the actor only
// after the frame is written.
func TestWriteLoopRunsOnSentAfterWrite(t *testing.T) {
	s := New(Config{Character: "Vix"})
	ctx := liveCtx(t, s)

	out := make(chan outbound, 1)
	ran := false
	out <- outbound{wire: fchat.Frame{Code: "MSG"}, onSent: func() { ran = true }}
	go s.writeLoop(ctx, succeedConn{}, out, 3)

	select {
	case in := <-s.inbox:
		si, ok := in.(sentInput)
		if !ok || si.gen != 3 || si.onSent == nil {
			t.Fatalf("expected a generation-3 sentInput, got %#v", in)
		}
		si.onSent()
	case <-time.After(time.Second):
		t.Fatal("onSent was not delivered after a successful write")
	}
	if !ran {
		t.Fatal("onSent did not run")
	}
}
