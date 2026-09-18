package session

import (
	"testing"
	"time"

	"plexo/internal/broker"
	"plexo/internal/model"
)

// waitPresenceAdmin fails unless the subscription delivers a presence event for
// the named character with the given admin flag within the deadline.
func waitPresenceAdmin(t *testing.T, sub *broker.Subscription, want string, admin bool) {
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
				p, ok := sp.Value.(model.PresencePayload)
				if ok && p.Admin == admin {
					return
				}
			}
		case <-deadline:
			t.Fatalf("did not observe presence for %q with admin=%v", want, admin)
		}
	}
}

// TestADLReplacesAdminSet: ADL is the full global-moderator list, so a later
// ADL replaces the set. A moderator dropped from the list must not stay
// flagged (the old merge behavior left them as admin forever).
func TestADLReplacesAdminSet(t *testing.T) {
	s := New(Config{Character: "Vix"})
	if err := s.handle(jsonFrame("ADL", `{"ops":["Kira","Sam"]}`)); err != nil {
		t.Fatalf("ADL: %v", err)
	}
	if !s.st.admins[nameKey("Kira")] || !s.st.admins[nameKey("Sam")] {
		t.Fatalf("ADL did not seed both admins: %v", s.st.admins)
	}
	if err := s.handle(jsonFrame("ADL", `{"ops":["Kira"]}`)); err != nil {
		t.Fatalf("ADL: %v", err)
	}
	if !s.st.admins[nameKey("Kira")] {
		t.Fatalf("Kira lost admin on the second ADL: %v", s.st.admins)
	}
	if s.st.admins[nameKey("Sam")] {
		t.Fatalf("Sam stayed admin after being dropped: %v", s.st.admins)
	}
}

// TestAOPDOPUpdateAdmins: the documented incremental updates to the global
// moderator list. DOP removes, AOP adds, durably.
func TestAOPDOPUpdateAdmins(t *testing.T) {
	s := New(Config{Character: "Vix"})
	if err := s.handle(jsonFrame("ADL", `{"ops":["Kira"]}`)); err != nil {
		t.Fatalf("ADL: %v", err)
	}
	if err := s.handle(jsonFrame("DOP", `{"character":"Kira"}`)); err != nil {
		t.Fatalf("DOP: %v", err)
	}
	if s.st.admins[nameKey("Kira")] {
		t.Fatalf("DOP did not clear Kira: %v", s.st.admins)
	}
	if err := s.handle(jsonFrame("AOP", `{"character":"Sam"}`)); err != nil {
		t.Fatalf("AOP: %v", err)
	}
	if !s.st.admins[nameKey("Sam")] {
		t.Fatalf("AOP did not add Sam: %v", s.st.admins)
	}
}

// TestDOPReEmitsPresence: a DOP for a watched character streams a corrected
// presence event, so the client drops the crown immediately instead of keeping
// it until the next unrelated presence change.
func TestDOPReEmitsPresence(t *testing.T) {
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b})
	s.setPresenceQuiet("Alice", "Female", "looking", "")

	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()

	// Join first so Alice is a member of a watched conversation.
	if err := s.handle(jsonFrame("JCH", `{"channel":"Frontpage","title":"Frontpage","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("JCH self: %v", err)
	}
	if err := s.handle(jsonFrame("ICH", `{"channel":"Frontpage","users":["Alice","Vix"]}`)); err != nil {
		t.Fatalf("ICH: %v", err)
	}
	if err := s.handle(jsonFrame("ADL", `{"ops":["Alice"]}`)); err != nil {
		t.Fatalf("ADL: %v", err)
	}
	if err := s.handle(jsonFrame("DOP", `{"character":"Alice"}`)); err != nil {
		t.Fatalf("DOP: %v", err)
	}
	waitPresenceAdmin(t, sub, "Alice", false)
}

// TestSnapshotSelfCarriesAdmin: the snapshot's self entry resolves the admin
// flag like a streamed presence event, so a reload keeps the crown.
func TestSnapshotSelfCarriesAdmin(t *testing.T) {
	s := New(Config{Character: "Vix"})
	if err := s.handle(jsonFrame("ADL", `{"ops":["Vix"]}`)); err != nil {
		t.Fatalf("ADL: %v", err)
	}
	if !s.selfPresence().Admin {
		t.Fatal("selfPresence did not resolve the admin flag")
	}
	if !s.snapshotLocked().Self.Admin {
		t.Fatal("snapshot self lost the admin flag")
	}
}
