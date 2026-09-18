package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"plexo/internal/broker"
	"plexo/internal/model"
	"plexo/internal/render"
	"plexo/internal/store"
	"plexo/test/memstore"
)

func newRLLSession(t *testing.T, self string) (*Session, *broker.Subscription, *memstore.MemStore) {
	t.Helper()
	renderer, err := render.New()
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	b := broker.New()
	st := memstore.New()
	s := New(Config{Character: self, Broker: b, Store: st, Renderer: renderer})
	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	t.Cleanup(sub.Close)
	return s, sub, st
}

// waitMessage drains subscription batches until one EvMessage arrives.
func waitMessage(t *testing.T, sub *broker.Subscription) model.MessagePayload {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed before a message event")
			}
			for _, ev := range batch.Events {
				if ev.Kind != model.EvMessage {
					continue
				}
				if p, ok := ev.Payload.(model.MessagePayload); ok {
					return p
				}
			}
		case <-deadline:
			t.Fatal("did not observe a message event")
		}
	}
}

// TestRLLChannelRendersStructured verifies a channel roll persists as kind
// "rll" with the whole payload and renders from the template, not the raw text.
func TestRLLChannelRendersStructured(t *testing.T) {
	s, sub, st := newRLLSession(t, "Vix")
	frame := jsonFrame("RLL", `{"character":"Kira","channel":"Frontpage","type":"dice","rolls":["2d6","3"],"results":[9,3],"endresult":12,"message":"[user]Kira[/user] rolls 2d6+3: 9 + 3 = [b]12[/b]"}`)
	if err := s.handle(frame); err != nil {
		t.Fatal(err)
	}

	p := waitMessage(t, sub)
	if p.Conv.Kind != model.ConvOfficial || p.Conv.ID != "Frontpage" {
		t.Fatalf("conv = %+v", p.Conv)
	}
	if p.Entry.Kind != "rll" {
		t.Fatalf("kind = %q, want rll", p.Entry.Kind)
	}
	if !strings.Contains(p.Entry.HTML, "roll-dice") || !strings.Contains(p.Entry.HTML, "🎲") || !strings.Contains(p.Entry.HTML, `roll-expr">2d6 &#43; 3<`) || !strings.Contains(p.Entry.HTML, `roll-total">12<`) {
		t.Fatalf("rendered HTML = %q", p.Entry.HTML)
	}
	if strings.Contains(p.Entry.HTML, "[user]") || strings.Contains(p.Entry.HTML, "rolls 2d6") {
		t.Fatalf("HTML used raw message: %q", p.Entry.HTML)
	}

	hist, err := st.History(context.Background(), store.HistoryQuery{Session: "Vix", Conv: p.Conv})
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 || hist[0].Kind != "rll" {
		t.Fatalf("history = %+v", hist)
	}
	if !strings.Contains(string(hist[0].Data), `"type":"dice"`) {
		t.Fatalf("stored data = %q, want whole payload", hist[0].Data)
	}
}

// TestRLLDMRouting verifies both directions of a DM roll land in the same
// conversation with the right speaker and are both persisted.
func TestRLLDMRouting(t *testing.T) {
	s, _, st := newRLLSession(t, "Vix")
	// Opponent's roll: on our copy the recipient is us, so the partner is the
	// speaker.
	if err := s.handle(jsonFrame("RLL", `{"character":"Kira","recipient":"Vix","type":"dice","rolls":["1d20"],"results":[20],"endresult":20,"message":"opponent"}`)); err != nil {
		t.Fatal(err)
	}
	// Our roll: the recipient is the opponent, so the partner is the recipient.
	if err := s.handle(jsonFrame("RLL", `{"character":"Vix","recipient":"Kira","type":"dice","rolls":["1d20"],"results":[1],"endresult":1,"message":"ours"}`)); err != nil {
		t.Fatal(err)
	}

	dm := model.ConvRef{Kind: model.ConvDM, ID: "Kira"}
	hist, err := st.History(context.Background(), store.HistoryQuery{Session: "Vix", Conv: dm})
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("dm history = %+v, want 2 entries", hist)
	}
	if hist[0].Speaker != "Kira" || hist[1].Speaker != "Vix" {
		t.Fatalf("speakers = %q, %q; want Kira, Vix", hist[0].Speaker, hist[1].Speaker)
	}
	for _, e := range hist {
		if e.Kind != "rll" {
			t.Fatalf("kind = %q, want rll", e.Kind)
		}
	}
	// The bug's phantom conversation must not exist.
	bad, err := st.History(context.Background(), store.HistoryQuery{Session: "Vix", Conv: model.ConvRef{Kind: model.ConvOfficial}})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("phantom official:\"\" entries = %+v", bad)
	}
}
