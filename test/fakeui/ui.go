// Package fakeui is a test subscriber that stands in for the browser. It
// consumes broker batches, records events, and can wait for a predicate.
package fakeui

import (
	"context"
	"sync"
	"time"

	"plexo/internal/broker"
	"plexo/internal/model"
)

// UI is a fake client connected to one subscription.
type UI struct {
	Sub *broker.Subscription

	mu      sync.Mutex
	events  []model.Event
	batches []broker.Batch
}

// New creates a fake UI around a subscription.
func New(sub *broker.Subscription) *UI { return &UI{Sub: sub} }

// Run consumes batches until the context is cancelled or the subscription
// closes.
func (u *UI) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case batch, ok := <-u.Sub.Events():
			if !ok {
				return
			}
			u.mu.Lock()
			u.batches = append(u.batches, batch)
			u.events = append(u.events, batch.Events...)
			u.mu.Unlock()
		}
	}
}

// Events returns a snapshot of the events seen so far.
func (u *UI) Events() []model.Event {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]model.Event, len(u.events))
	copy(out, u.events)
	return out
}

// WaitFor polls until pred matches an event, or the timeout elapses. It
// returns the first match.
func (u *UI) WaitFor(timeout time.Duration, pred func(model.Event) bool) (model.Event, bool) {
	deadline := time.Now().Add(timeout)
	for {
		for _, ev := range u.Events() {
			if pred(ev) {
				return ev, true
			}
		}
		if time.Now().After(deadline) {
			return model.Event{}, false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// WaitForCount polls until at least n events satisfy pred, or the timeout
// elapses.
func (u *UI) WaitForCount(timeout time.Duration, n int, pred func(model.Event) bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		count := 0
		for _, ev := range u.Events() {
			if pred(ev) {
				count++
			}
		}
		if count >= n {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}
