package session

import (
	"context"
	"testing"
	"time"

	"plexo/internal/broker"
	"plexo/internal/fchat"
	"plexo/internal/model"
)

// waitTyping drains the subscription until it sees a typing event for the
// character matching the requested on/off state, skipping everything else.
func waitTyping(t *testing.T, sub *broker.Subscription, character string, wantOn bool) model.TypingPayload {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed before typing event")
			}
			for _, ev := range batch.Events {
				if ev.Kind != model.EvState {
					continue
				}
				sp, ok := ev.Payload.(model.StatePayload)
				if !ok {
					continue
				}
				p, ok := sp.Value.(model.TypingPayload)
				if ok && p.Character == character && p.On == wantOn {
					return p
				}
			}
		case <-deadline:
			t.Fatalf("did not observe typing on=%v for %s", wantOn, character)
		}
	}
}

// TestTPNIsDmScoped: TPN is a private-message signal with no channel field, so
// it must land on the character's DM conversation and never on a shared
// channel, even when the typist is a member there. It also guards the
// typing/paused/clear status mapping.
func TestTPNIsDmScoped(t *testing.T) {
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b})

	// Kira is a member of a joined channel; her TPN must not touch it.
	ch := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	cs := s.ensureConv(ch)
	cs.membership = memJoined
	cs.members[nameKey("Kira")] = true

	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()

	dm := model.ConvRef{Kind: model.ConvDM, ID: "Kira"}

	if err := s.handle(jsonFrame("TPN", `{"character":"Kira","status":"typing"}`)); err != nil {
		t.Fatalf("TPN typing: %v", err)
	}
	if ev := waitTyping(t, sub, "Kira", true); ev.Conv != dm || ev.Paused {
		t.Fatalf("typing event = %+v, want active typing on DM %+v", ev, dm)
	}
	if _, ok := s.st.typing[convKey(ch)]; ok {
		t.Fatal("TPN created channel typing state; it is DM-only")
	}

	if err := s.handle(jsonFrame("TPN", `{"character":"Kira","status":"paused"}`)); err != nil {
		t.Fatalf("TPN paused: %v", err)
	}
	if ev := waitTyping(t, sub, "Kira", true); !ev.Paused {
		t.Fatalf("paused event = %+v, want paused", ev)
	}

	if err := s.handle(jsonFrame("TPN", `{"character":"Kira","status":"clear"}`)); err != nil {
		t.Fatalf("TPN clear: %v", err)
	}
	waitTyping(t, sub, "Kira", false)

	// A sent DM retires the sender's typing state without a clear TPN.
	if err := s.handle(jsonFrame("TPN", `{"character":"Kira","status":"typing"}`)); err != nil {
		t.Fatalf("TPN typing: %v", err)
	}
	waitTyping(t, sub, "Kira", true)
	if err := s.handle(jsonFrame("PRI", `{"character":"Kira","recipient":"Vix","message":"hi"}`)); err != nil {
		t.Fatalf("PRI: %v", err)
	}
	waitTyping(t, sub, "Kira", false)
}

// TestFLNClearsTyping: a character going offline cannot be typing, and no clear
// TPN will follow, so FLN must retire the indicator (case-insensitively).
func TestFLNClearsTyping(t *testing.T) {
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b})
	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()

	if err := s.handle(jsonFrame("TPN", `{"character":"Kira","status":"typing"}`)); err != nil {
		t.Fatalf("TPN: %v", err)
	}
	waitTyping(t, sub, "Kira", true)

	if err := s.handle(jsonFrame("FLN", `{"character":"kira"}`)); err != nil {
		t.Fatalf("FLN: %v", err)
	}
	if ev := waitTyping(t, sub, "Kira", false); ev.Conv.Kind != model.ConvDM {
		t.Fatalf("FLN off landed on %+v, want a DM", ev.Conv)
	}
	if len(s.st.typing) != 0 {
		t.Fatalf("typing state not cleared: %+v", s.st.typing)
	}
}

// TestSendTypingCommandQueuesTPN: the send_typing op maps to an outbound TPN
// addressed to the DM partner, and rejects channel targets and bad statuses.
func TestSendTypingCommandQueuesTPN(t *testing.T) {
	s := New(Config{Character: "Vix"})
	s.out = make(chan outbound, 4)
	// A live session always has a context; without one, done() returns an
	// already-closed channel and queue() may race the send against it.
	s.mu.Lock()
	s.ctx = context.Background()
	s.mu.Unlock()

	dm := model.ConvRef{Kind: model.ConvDM, ID: "Kira"}
	res := s.handleCommand(model.Command{Op: model.OpSendTyping, Conv: dm, Status: "typing"})
	if !res.Accepted {
		t.Fatalf("send_typing rejected: %+v", res)
	}
	select {
	case ob := <-s.out:
		f := ob.wire
		if f.Code != "TPN" {
			t.Fatalf("expected TPN, got %s", f.Code)
		}
		p, err := fchat.Decode[fchat.TypingNotification](f)
		if err != nil {
			t.Fatalf("decode TPN: %v", err)
		}
		if p.Character != "Kira" || p.Status != "typing" {
			t.Fatalf("unexpected TPN payload: %+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("no TPN frame queued")
	}

	// TPN is DM-only; a channel target is a client bug.
	ch := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	if res := s.handleCommand(model.Command{Op: model.OpSendTyping, Conv: ch, Status: "typing"}); res.Accepted {
		t.Fatalf("channel typing must be rejected: %+v", res)
	}
	// Unknown statuses are rejected.
	if res := s.handleCommand(model.Command{Op: model.OpSendTyping, Conv: dm, Status: "bogus"}); res.Accepted {
		t.Fatalf("bad status must be rejected: %+v", res)
	}
}
