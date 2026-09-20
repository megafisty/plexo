package session

import (
	"context"
	"time"

	"plexo/internal/model"
)

// Persistence batching bounds. A session never writes the store on its actor
// goroutine: recordEntry hands an entry to persist, and runPersist coalesces a
// burst into one transaction. This keeps protocol handling off the disk and
// turns N messages into one commit instead of N.
const (
	// persistQueue bounds the items buffered between the actor and its writer.
	// It is much larger than a batch so a burst never blocks the actor on a
	// single slow commit; a sustained overrun still applies backpressure rather
	// than dropping history.
	persistQueue = 1024
	// persistBatch forces an immediate flush once this many entries are queued.
	persistBatch = 64
	// persistFlush bounds how long the oldest queued entry waits before it is
	// written.
	persistFlush = 50 * time.Millisecond
)

// persistItem is one message to the persistence goroutine: an entry to store, a
// flush barrier, or a stop request. A barrier is enqueued through the same FIFO
// as entries, so the writer has already appended every earlier entry to its
// batch when it reaches the barrier; flushing then makes them all durable.
type persistItem struct {
	entry model.Entry
	sync  chan struct{} // non-nil: flush everything queued so far, then close
	stop  bool          // flush and exit
}

// Sync blocks until every entry enqueued before the call is written to the
// store. A conversation materialization calls it first, because the live event
// and the store row are no longer produced in lockstep and a full view replaces
// the client's window. A session that was never started persisted
// synchronously, so Sync is a no-op.
func (s *Session) Sync(ctx context.Context) error {
	s.mu.Lock()
	ch := s.persist
	s.mu.Unlock()
	if ch == nil {
		return nil
	}
	done := make(chan struct{})
	select {
	case ch <- persistItem{sync: done}:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done():
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done():
		return nil
	}
}

// runPersist drains a session's queue into the store in batches. The actor only
// ever enqueues; this goroutine owns the store writes. It exits on the stop
// sentinel the actor sends as it shuts down, flushing the tail first, so a
// graceful Stop never loses a recorded entry.
func (s *Session) runPersist(ch <-chan persistItem) {
	if s.cfg.Store == nil {
		// No persistence configured (tests, ephemeral sessions): keep draining
		// so the actor never blocks, and discard.
		for item := range ch {
			if item.sync != nil {
				close(item.sync)
			}
			if item.stop {
				return
			}
		}
		return
	}
	ticker := time.NewTicker(persistFlush)
	defer ticker.Stop()

	batch := make([]model.Entry, 0, persistBatch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.cfg.Store.Append(context.Background(), batch); err != nil {
			s.log().Warn("store append failed",
				"character", s.cfg.Character, "entries", len(batch), "err", err)
		}
		batch = batch[:0]
	}
	for {
		select {
		case item := <-ch:
			switch {
			case item.stop:
				flush()
				return
			case item.sync != nil:
				flush()
				close(item.sync)
			default:
				batch = append(batch, item.entry)
				if len(batch) >= persistBatch {
					flush()
				}
			}
		case <-ticker.C:
			flush()
		}
	}
}
