package session

import (
	"context"
	"testing"
	"time"

	"plexo/internal/broker"
	"plexo/internal/model"
	"plexo/internal/render"
)

// TestConversationDescriptionIsSparse: the description rides in conversation
// state only when it changes. A roster or mode update omits it so the client
// keeps its copy; clearing it is an explicit empty value, never an omission.
func TestConversationDescriptionIsSparse(t *testing.T) {
	b := broker.New()
	renderer, err := render.New()
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	s := New(Config{Character: "Vix", Broker: b, Renderer: renderer})
	s.out = make(chan outbound, 16)
	s.mu.Lock()
	s.ctx = context.Background()
	s.mu.Unlock()

	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()

	conv := roomConv("ADH-abc")
	key := model.ConvKey("Vix", conv)

	// next returns the description from the next conversation state event. It is
	// called only after the triggering frame has been handled, so the broker's
	// coalescing cannot merge two emissions into one.
	next := func() *string {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case batch, ok := <-sub.Events():
				if !ok {
					t.Fatal("subscription closed")
				}
				for _, ev := range batch.Events {
					sp, ok := stateFor(ev, key)
					if !ok {
						continue
					}
					p, ok := sp.Value.(model.ConvStatePayload)
					if !ok {
						continue
					}
					return p.Description
				}
			case <-deadline:
				t.Fatal("no conversation state event")
			}
		}
	}

	// A join is a state change with no description: the field is omitted.
	joinRoomTest(t, s, "ADH-abc", "Secret")
	if got := next(); got != nil {
		t.Fatalf("join description = %q, want nil", *got)
	}

	// A CDS change carries the rendered description.
	if err := s.handle(jsonFrame("CDS", `{"channel":"ADH-abc","description":"[b]hi[/b]"}`)); err != nil {
		t.Fatalf("CDS: %v", err)
	}
	if got := next(); got == nil || *got != "<b>hi</b>" {
		t.Fatalf("changed description = %v, want rendered BBCode", got)
	}

	// An unrelated roster update must omit the unchanged description.
	if err := s.handle(jsonFrame("COL", `{"channel":"ADH-abc","oplist":["Vix"]}`)); err != nil {
		t.Fatalf("COL: %v", err)
	}
	if got := next(); got != nil {
		t.Fatalf("unchanged description = %q, want omitted", *got)
	}

	// Clearing is an explicit empty value, not an omission.
	if err := s.handle(jsonFrame("CDS", `{"channel":"ADH-abc","description":""}`)); err != nil {
		t.Fatalf("CDS clear: %v", err)
	}
	if got := next(); got == nil || *got != "" {
		t.Fatalf("cleared description = %v, want a pointer to empty", got)
	}
}
