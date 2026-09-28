package core_test

import (
	"testing"
	"time"

	"plexo/internal/fchat"
	"plexo/internal/model"
	"plexo/internal/session"
	"plexo/test/fakeserver"
	"plexo/test/fakeui"
)

// TestChannelMemberPresenceSurvivesDeltaReactivation reproduces the UI bug
// where a channel member who joins while the client is looking at another
// conversation renders as a white "unknown status" placeholder, and only
// reconciles on a later full re-materialization.
//
// While the conversation is at summary interest the broker still delivers its
// conv_state (the member list, names only), but drops the joining member's
// presence event, which is gated to full interest (policy.go, StateCharacter).
// Re-activating the conversation with a retained window asks the core for a
// delta view, and the delta view omits Members (manager.ConvView), so the
// presence is never backfilled. The client keeps the placeholder until a full
// re-materialization happens to replace the window.
//
// The requirement this test pins down: re-activating a conversation must leave
// the client able to resolve presence for every current member — either the
// delta view carries the member list or the core replays the members'
// presence on the interest upgrade.
func TestChannelMemberPresenceSurvivesDeltaReactivation(t *testing.T) {
	fac := fakeserver.NewWSFactory(fakeserver.Options{
		Character: char,
		Channels:  []fchat.OfficialChannel{{Name: "Frontpage", Characters: 12}},
		// Bob is online at login, so the session roster knows his gender and
		// status; the client never sees him until he is watched.
		Roster: [][]string{{"Bob", "Male", "online", ""}},
	})
	t.Cleanup(fac.Close)
	h := newHarnessWithDial(t, model.InterestSummary, func(string) session.Dialer {
		return session.Dialer(fac.Dial)
	}, fac)

	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)
	h.joinFrontpage(t)

	ref := officialConv("Frontpage")

	// The client opens the channel: a full materialization establishes its
	// retained window and the current member list.
	h.sub.SetInterest(char, ref, model.InterestFull, 0)
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		return ev.Kind == model.EvConvView
	}); !ok {
		t.Fatalf("channel never materialized; events: %s", dump(h.ui))
	}

	// The client looks elsewhere: release the conversation to summary while
	// keeping its window. Interest changes are queued on the broker's run loop,
	// so give that a moment before injecting the join.
	h.sub.SetInterest(char, ref, model.InterestSummary, 0)
	time.Sleep(50 * time.Millisecond)

	// Bob joins the channel while it is not at full interest. The conv_state
	// (names only) is delivered; his presence event is gated away.
	if err := fac.First().Send("JCH", fchat.JCHEvent{
		Channel:   "Frontpage",
		Character: fchat.NameOrIdentity{Name: "Bob"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := stateValue[model.ConvStatePayload](ev, model.ConvKey(char, ref))
		if !ok {
			return false
		}
		for _, m := range p.Members {
			if m == "Bob" {
				return true
			}
		}
		return false
	}); !ok {
		t.Fatalf("Bob never appeared in the member list; events: %s", dump(h.ui))
	}
	if hasCharacterPresence(h.ui, "Bob") {
		t.Fatal("Bob's presence arrived while at summary interest; the reproduction is invalid")
	}

	// Returning to the channel supplies the retained cursor, so the core
	// resumes with a delta rather than a full view. Wait for the resumed view so
	// the requirement is checked against the actual re-activation delivery.
	h.sub.SetInterest(char, ref, model.InterestFull, 1)
	ev, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		if ev.Kind != model.EvConvView {
			return false
		}
		v, ok := ev.Payload.(model.ConvView)
		return ok && v.Delta
	})
	if !ok {
		t.Fatalf("no view on re-activation; events: %s", dump(h.ui))
	}
	view, _ := ev.Payload.(model.ConvView)

	// The client must be able to resolve Bob's presence after re-activation:
	// either the resumed view lists him or the core replays his presence.
	if hasCharacterPresence(h.ui, "Bob") || viewListsMember(h.ui, "Bob") {
		return
	}
	t.Fatalf("Bob's presence was never made available on re-activation; delta=%v viewMembers=%d; events: %s",
		view.Delta, len(view.Members), dump(h.ui))
}

// hasCharacterPresence reports whether the subscription has observed a
// presence state record for name.
func hasCharacterPresence(ui *fakeui.UI, name string) bool {
	for _, ev := range ui.Events() {
		p, ok := stateValue[model.PresencePayload](ev, model.CharacterKey(name))
		if ok && p.Character == name {
			return true
		}
	}
	return false
}

// viewListsMember reports whether any delivered conv_view carries name in its
// member list.
func viewListsMember(ui *fakeui.UI, name string) bool {
	for _, ev := range ui.Events() {
		if ev.Kind != model.EvConvView {
			continue
		}
		v, ok := ev.Payload.(model.ConvView)
		if !ok {
			continue
		}
		for _, m := range v.Members {
			if m.Name == name {
				return true
			}
		}
	}
	return false
}
