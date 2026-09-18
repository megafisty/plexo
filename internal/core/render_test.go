package core_test

import (
	"context"
	"testing"
	"time"

	"plexo/internal/fchat"
	"plexo/internal/model"
	"plexo/internal/session"
	"plexo/test/fakeserver"
)

// TestRenderedStatusAndDescription: BBCode in status messages and conversation
// descriptions is rendered to HTML by the core, in both live events and
// materialized views. The client only ever receives HTML, never the source.
func TestRenderedStatusAndDescription(t *testing.T) {
	fac := fakeserver.NewWSFactory(fakeserver.Options{
		Character: char,
		Roster:    [][]string{{"Other", "Male", "online", "[b]status[/b]"}},
		Channels:  []fchat.OfficialChannel{{Name: "Frontpage", Characters: 1}},
	})
	t.Cleanup(fac.Close)
	h := newHarnessWithDial(t, model.InterestFull, func(string) session.Dialer {
		return session.Dialer(fac.Dial)
	}, fac)

	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	res := h.mgr.Dispatch(model.Command{
		CID: "j1", Session: char, Op: model.OpJoin,
		Conv: model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"},
	})
	if !res.Accepted {
		t.Fatalf("join rejected: %+v", res)
	}

	// Wait for the fake server's join echo (including its default description)
	// so our update is not overwritten by it.
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := nsValue[model.ConvStatePayload](ev, model.StateConv)
		return ok && p.Description == "Fake channel"
	}); !ok {
		t.Fatalf("no default description; events: %s", dump(h.ui))
	}

	// A second character joins (so its roster row becomes a member), then the
	// channel description arrives as BBCode.
	if err := fac.First().Send("JCH", fchat.JCHEvent{
		Channel: "Frontpage", Character: fchat.NameOrIdentity{Name: "Other"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fac.First().Send("CDS", fchat.CDSEvent{
		Channel: "Frontpage", Description: "[b]topic[/b]",
	}); err != nil {
		t.Fatal(err)
	}

	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := nsValue[model.ConvStatePayload](ev, model.StateConv)
		return ok && p.Description == "<b>topic</b>"
	}); !ok {
		t.Fatalf("description not rendered in conv event; events: %s", dump(h.ui))
	}

	// The client's own status is always delivered, regardless of interest.
	if err := fac.First().Send("STA", fchat.STAEvent{
		Character: char, Status: "online", StatusMsg: "[eicon]bloop[/eicon]",
	}); err != nil {
		t.Fatal(err)
	}
	const eiconHTML = `<img class="bc-eicon" src="https://static.f-list.net/images/eicon/bloop.png" alt="bloop">`
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := stateValue[model.PresencePayload](ev, model.CharacterKey(char))
		return ok && p.StatusMsg == eiconHTML
	}); !ok {
		t.Fatalf("status message not rendered in presence event; events: %s", dump(h.ui))
	}

	// Materialized view: the description and each member's status message.
	view, err := h.mgr.ConvView(context.Background(), char,
		model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}, 0, 0)
	if err != nil {
		t.Fatalf("ConvView: %v", err)
	}
	if view.Description != "<b>topic</b>" {
		t.Fatalf("view description = %q, want rendered BBCode", view.Description)
	}
	var other *model.MemberInfo
	for i := range view.Members {
		if view.Members[i].Name == "Other" {
			other = &view.Members[i]
		}
	}
	if other == nil {
		t.Fatalf("Other missing from members: %+v", view.Members)
	}
	if other.StatusMsg != "<b>status</b>" {
		t.Fatalf("member status = %q, want rendered BBCode", other.StatusMsg)
	}
}
