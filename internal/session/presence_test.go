package session

import (
	"testing"
	"time"

	"plexo/internal/broker"
	"plexo/internal/model"
)

// waitPresence fails unless the subscription delivers a presence event for the
// named character within the deadline.
func waitPresence(t *testing.T, sub *broker.Subscription, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed before presence event")
			}
			for _, ev := range batch.Events {
				sp, ok := stateFor(ev, model.CharacterKey(want))
				if !ok {
					continue
				}
				if p, ok := sp.Value.(model.PresencePayload); ok && p.Character == want {
					return
				}
			}
		case <-deadline:
			t.Fatalf("did not observe presence for %q", want)
		}
	}
}

// TestMembershipAddStreamsPresence: a channel member introduced by ICH must
// stream its presence to a full-interest subscriber. LIS hydration is quiet and
// the member list carries names only, so without this the member renders
// unknown until its next status change.
func TestMembershipAddStreamsPresence(t *testing.T) {
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b})
	// As if LIS had hydrated the roster before the channel was joined.
	s.setPresenceQuiet("Alice", "Female", "looking", "hi")

	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()

	// Join first (the server always sends JCH for the joiner before ICH), so the
	// channel is joined and its member presence is eligible for delivery.
	if err := s.handle(jsonFrame("JCH", `{"channel":"Frontpage","title":"Frontpage","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("JCH self: %v", err)
	}
	if err := s.handle(jsonFrame("ICH", `{"channel":"Frontpage","users":["Alice","Vix"]}`)); err != nil {
		t.Fatalf("ICH: %v", err)
	}
	waitPresence(t, sub, "Alice")
}

// TestMembershipAddStreamsPresenceOnJoin: a member who joins after the initial
// list arrives via JCH and must also stream presence.
func TestMembershipAddStreamsPresenceOnJoin(t *testing.T) {
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b})
	s.setPresenceQuiet("Bob", "Male", "away", "")

	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()

	// Join first (see TestMembershipAddStreamsPresence).
	if err := s.handle(jsonFrame("JCH", `{"channel":"Frontpage","title":"Frontpage","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("JCH self: %v", err)
	}
	if err := s.handle(jsonFrame("JCH", `{"channel":"Frontpage","title":"Frontpage","character":{"identity":"Bob"}}`)); err != nil {
		t.Fatalf("JCH: %v", err)
	}
	waitPresence(t, sub, "Bob")
}
