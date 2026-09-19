package core_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"plexo/internal/broker"
	"plexo/internal/config"
	"plexo/internal/core"
	"plexo/internal/fchat"
	"plexo/internal/model"
	"plexo/internal/render"
	"plexo/internal/session"
	"plexo/internal/store"
	"plexo/test/fakeserver"
	"plexo/test/fakeui"
	"plexo/test/memstore"
)

const (
	acct = "acct"
	char = "Vix"
	conv = "official:Frontpage"
)

func testTickets(_ context.Context, account string) (fchat.Ticket, error) {
	return fchat.Ticket{Value: "tkt-" + account, MintedAt: time.Now()}, nil
}

type harness struct {
	mgr   *core.Manager
	store store.Store
	fac   *fakeserver.Factory
	sub   *broker.Subscription
	ui    *fakeui.UI

	joinedFrontpage bool
}

func newHarness(t *testing.T, defaultInterest model.Interest) *harness {
	t.Helper()
	fac := fakeserver.NewWSFactory(fakeserver.Options{
		Character: char,
		Channels:  []fchat.OfficialChannel{{Name: "Frontpage", Characters: 12}},
		Rooms:     []fchat.PublicRoom{{Name: "adh-test0001", Title: "Test Room", Characters: 3}},
	})
	t.Cleanup(fac.Close)
	return newHarnessWithDial(t, defaultInterest, func(string) session.Dialer {
		return session.Dialer(fac.Dial)
	}, fac)
}

// newHarnessWithDial builds a harness whose sessions dial through the given
// factory; tests that need to fail dials use it directly.
func newHarnessWithDial(t *testing.T, defaultInterest model.Interest, dial func(string) session.Dialer, fac *fakeserver.Factory) *harness {
	t.Helper()
	ctx := context.Background()
	st := memstore.New()
	renderer, err := render.New()
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	mgr := core.NewManager(ctx, core.Config{
		Store:    st,
		Tickets:  fchat.TicketFunc(testTickets),
		Dial:     dial,
		Renderer: renderer,
		Settings: config.NewProvider(st),
	})
	opts := broker.DefaultSubOpts()
	opts.DefaultInterest = defaultInterest
	opts.FlushEvery = 5 * time.Millisecond
	sub := mgr.Broker().Subscribe(opts)
	ui := fakeui.New(sub)
	go ui.Run(ctx)

	t.Cleanup(func() {
		sub.Close()
		mgr.Logout(char)
	})
	return &harness{mgr: mgr, store: st, fac: fac, sub: sub, ui: ui}
}

func (h *harness) waitLive(t *testing.T) {
	t.Helper()
	ok := h.ui.WaitForCount(2*time.Second, 1, func(ev model.Event) bool {
		p, ok := stateValue[model.SessionStatePayload](ev, model.SessionKey(char))
		return ok && p.State == "live"
	})
	if !ok {
		t.Fatalf("session never became live; events: %s", dump(h.ui))
	}
}

// sendChannel injects a channel message from "Other" through the fake server.
// It joins Frontpage first: frames for a channel the session is not in are
// ignored (docs/fchat.md).
func (h *harness) sendChannel(t *testing.T, body string) {
	t.Helper()
	h.joinFrontpage(t)
	if err := h.fac.First().Send("MSG", fchat.MSGEvent{Character: "Other", Channel: "Frontpage", Message: body}); err != nil {
		t.Fatal(err)
	}
}

// joinFrontpage joins Frontpage once per harness and waits for the joined
// event, so a subsequent injected channel message is accepted.
func (h *harness) joinFrontpage(t *testing.T) {
	t.Helper()
	if h.joinedFrontpage {
		return
	}
	res := h.mgr.Dispatch(model.Command{
		CID: "join-frontpage", Session: char, Op: model.OpJoin,
		Conv: model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"},
	})
	if !res.Accepted {
		t.Fatalf("join Frontpage rejected: %+v", res)
	}
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		_, ok := stateValue[model.ConvStatePayload](ev, model.ConvKey(char, officialConv("Frontpage")))
		return ok
	}); !ok {
		t.Fatalf("Frontpage never joined; events: %s", dump(h.ui))
	}
	h.joinedFrontpage = true
}

// TestE2ERoomCreateRole: creating a room force-joins it, and the conversation
// record the core emits carries the creator's owner role, so the client can
// offer management affordances without a separate fetch.
func TestE2ERoomCreateRole(t *testing.T) {
	h := newHarness(t, model.InterestFull)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	res := h.mgr.Dispatch(model.Command{
		CID: "create-room", Session: char, Op: model.OpRoomAdmin,
		Room: &model.RoomAdminRequest{Action: "create", Title: "New Room"},
	})
	if !res.Accepted {
		t.Fatalf("create rejected: %+v", res)
	}

	ev, ok := h.ui.WaitFor(3*time.Second, func(ev model.Event) bool {
		if ev.Kind != model.EvState {
			return false
		}
		sp, ok := ev.Payload.(model.StatePayload)
		if !ok || model.KeyNamespace(sp.Key) != model.StateConv {
			return false
		}
		p, ok := sp.Value.(model.ConvStatePayload)
		return ok && p.Conv.Kind == model.ConvRoom && p.Title == "New Room"
	})
	if !ok {
		t.Fatalf("room conversation never emitted; events: %s", dump(h.ui))
	}
	sp, _ := ev.Payload.(model.StatePayload)
	p, _ := sp.Value.(model.ConvStatePayload)
	if p.Role != model.RoomRoleOwner {
		t.Fatalf("role = %q, want owner", p.Role)
	}
}

// createRoomForTest creates a room and returns its conversation ref once the
// self JCH has materialized it.
func (h *harness) createRoomForTest(t *testing.T, title string) model.ConvRef {
	t.Helper()
	res := h.mgr.Dispatch(model.Command{
		CID: "create-room", Session: char, Op: model.OpRoomAdmin,
		Room: &model.RoomAdminRequest{Action: "create", Title: title},
	})
	if !res.Accepted {
		t.Fatalf("create rejected: %+v", res)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		for _, s := range h.mgr.Snapshot().Sessions {
			for _, c := range s.Conversations {
				if c.Kind == model.ConvRoom && c.Title == title {
					return c.Conv
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("room %q never appeared", title)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestE2ERoomPublishUpdatesCatalog: RST has no server broadcast, so the
// session updates the core-wide catalog optimistically; publishing adds the
// room and unpublishing removes it.
func TestE2ERoomPublishUpdatesCatalog(t *testing.T) {
	h := newHarness(t, model.InterestFull)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)
	ref := h.createRoomForTest(t, "Public Room")

	res := h.mgr.Dispatch(model.Command{
		CID: "publish", Session: char, Op: model.OpRoomAdmin, Conv: ref,
		Room: &model.RoomAdminRequest{Action: "visibility", Visibility: "public"},
	})
	if !res.Accepted {
		t.Fatalf("publish rejected: %+v", res)
	}
	waitRoomInCatalog(t, h, ref.ID, true)

	res = h.mgr.Dispatch(model.Command{
		CID: "unpublish", Session: char, Op: model.OpRoomAdmin, Conv: ref,
		Room: &model.RoomAdminRequest{Action: "visibility", Visibility: "private"},
	})
	if !res.Accepted {
		t.Fatalf("unpublish rejected: %+v", res)
	}
	waitRoomInCatalog(t, h, ref.ID, false)
}

func waitRoomInCatalog(t *testing.T, h *harness, id string, want bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		found := false
		for _, r := range h.mgr.Snapshot().Catalog.Rooms {
			if r.Name == id {
				found = true
			}
		}
		if found == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("room %s in catalog = %v, want %v", id, found, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestE2EInviteRoundTrip: inviting a character surfaces a pending invitation
// (the CIU reaches the target) and dismiss_invite removes it. The target is
// self so the per-connection fake can deliver the CIU frame.
func TestE2EInviteRoundTrip(t *testing.T) {
	h := newHarness(t, model.InterestFull)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)
	ref := h.createRoomForTest(t, "Invite Room")

	res := h.mgr.Dispatch(model.Command{
		CID: "invite", Session: char, Op: model.OpRoomAdmin, Conv: ref,
		Room: &model.RoomAdminRequest{Action: "invite", Character: char},
	})
	if !res.Accepted {
		t.Fatalf("invite rejected: %+v", res)
	}
	waitInviteCount(t, h, 1)

	res = h.mgr.Dispatch(model.Command{CID: "dismiss", Session: char, Op: model.OpDismissInvite, Conv: ref})
	if !res.Accepted {
		t.Fatalf("dismiss rejected: %+v", res)
	}
	waitInviteCount(t, h, 0)
}

func waitInviteCount(t *testing.T, h *harness, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := 0
		for _, s := range h.mgr.Snapshot().Sessions {
			got += len(s.Invites)
		}
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("invite count = %d, want %d", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitHighlight waits for a channel message and reports whether it arrived
// highlighted.
func (h *harness) waitHighlight(t *testing.T, body string) bool {
	t.Helper()
	ev, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, isM := ev.Payload.(model.MessagePayload)
		return ev.Kind == model.EvMessage && isM && p.Entry.Body == body
	})
	if !ok {
		t.Fatalf("no message event for %q; events: %s", body, dump(h.ui))
	}
	return ev.Payload.(model.MessagePayload).Highlight
}

// putHighlights stores a character's highlight list in the harness store.
func (h *harness) putHighlights(t *testing.T, character string, highlights []string) {
	t.Helper()
	err := config.NewProvider(h.store).SaveCharacter(context.Background(), character, config.Character{Highlights: highlights})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLoginHydrationAndIncomingMessage(t *testing.T) {
	h := newHarness(t, model.InterestFull)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := stateValue[model.PresencePayload](ev, model.CharacterKey(char))
		return ok && p.Online
	}); !ok {
		t.Fatalf("no self presence; events: %s", dump(h.ui))
	}

	// Join a channel and observe the conversation event.
	res := h.mgr.Dispatch(model.Command{
		CID: "c1", Session: char, Op: model.OpJoin,
		Conv: model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"},
	})
	if !res.Accepted {
		t.Fatalf("join rejected: %+v", res)
	}
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		_, ok := stateValue[model.ConvStatePayload](ev, model.ConvKey(char, officialConv("Frontpage")))
		return ok
	}); !ok {
		t.Fatalf("no conversation joined; events: %s", dump(h.ui))
	}

	// Another character speaks.
	if err := h.fac.First().Send("MSG", fchat.MSGEvent{Character: "Other", Channel: "Frontpage", Message: "hello there"}); err != nil {
		t.Fatal(err)
	}
	ev, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, isM := ev.Payload.(model.MessagePayload)
		return ev.Kind == model.EvMessage && isM && p.Entry.Body == "hello there" && p.Entry.ConvSeq == 1
	})
	if !ok {
		t.Fatalf("no message event; events: %s", dump(h.ui))
	}
	if ev.Session != char {
		t.Fatalf("message session = %q", ev.Session)
	}

	// And it must be persisted with a conv_seq.
	entries, err := h.store.History(context.Background(), store.HistoryQuery{
		Session: char, Conv: model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Body != "hello there" || entries[0].ConvSeq != 1 {
		t.Fatalf("persisted entries = %+v", entries)
	}
}

// TestOutboundMessageIsRecordedAndPersisted: the server never delivers a
// channel message back to its sender, so the session must record the sender's
// own copy with a canonical sequence and timestamp.
func TestOutboundMessageIsRecordedAndPersisted(t *testing.T) {
	h := newHarness(t, model.InterestFull)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	res := h.mgr.Dispatch(model.Command{
		CID: "send-1", Session: char, Op: model.OpSendMessage,
		Conv: model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"},
		Body: "my line",
	})
	if !res.Accepted {
		t.Fatalf("send rejected: %+v", res)
	}

	srv := h.fac.First()
	if !waitServer(srv, 2*time.Second, "MSG") {
		t.Fatal("server never received MSG")
	}

	// The session records the sender's own copy: the server does not echo it.
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, isM := ev.Payload.(model.MessagePayload)
		return ev.Kind == model.EvMessage && isM && p.Entry.Body == "my line" && p.Self
	}); !ok {
		t.Fatalf("no self message event; events: %s", dump(h.ui))
	}
	entries, _ := h.store.History(context.Background(), store.HistoryQuery{
		Session: char, Conv: model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"},
	})
	if len(entries) != 1 || entries[0].Speaker != char || entries[0].ConvSeq == 0 {
		t.Fatalf("persisted entries = %+v", entries)
	}
}

// TestOutboundDMIsRecordedAndPersisted: private messages are not echoed either
// (event.PRI targets only the recipient), so the sender's copy is recorded on
// send. An incoming PRI still lands in the partner's conversation as non-self.
func TestOutboundDMIsRecordedAndPersisted(t *testing.T) {
	h := newHarness(t, model.InterestFull)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	dm := model.ConvRef{Kind: model.ConvDM, ID: "Other"}
	res := h.mgr.Dispatch(model.Command{
		CID: "dm-1", Session: char, Op: model.OpSendMessage,
		Conv: dm, Body: "psst",
	})
	if !res.Accepted {
		t.Fatalf("dm send rejected: %+v", res)
	}
	if !waitServer(h.fac.First(), 2*time.Second, "PRI") {
		t.Fatal("server never received PRI")
	}
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, isM := ev.Payload.(model.MessagePayload)
		return ev.Kind == model.EvMessage && isM && p.Conv == dm && p.Entry.Body == "psst" && p.Self
	}); !ok {
		t.Fatalf("no self DM event; events: %s", dump(h.ui))
	}

	// An incoming private message from the partner is recorded as non-self.
	if err := h.fac.First().Send("PRI", fchat.PRIEvent{Character: "Other", Recipient: char, Message: "hi back"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, isM := ev.Payload.(model.MessagePayload)
		return ev.Kind == model.EvMessage && isM && p.Conv == dm && p.Entry.Body == "hi back" && !p.Self
	}); !ok {
		t.Fatalf("no incoming DM event; events: %s", dump(h.ui))
	}

	entries, err := h.store.History(context.Background(), store.HistoryQuery{Session: char, Conv: dm})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Speaker != char || entries[0].ConvSeq == 0 || entries[1].Speaker != "Other" {
		t.Fatalf("persisted DMs = %+v", entries)
	}
}

func TestInterestGatingAndMaterialization(t *testing.T) {
	// Default interest is summary: bodies must not be delivered.
	h := newHarness(t, model.InterestSummary)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	h.sendChannel(t, "secret")
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := nsValue[model.SummaryPayload](ev, model.StateSummary)
		return ok && p.Conv.ID == "Frontpage"
	}); !ok {
		t.Fatalf("no summary; events: %s", dump(h.ui))
	}
	for _, ev := range h.ui.Events() {
		if ev.Kind == model.EvMessage {
			t.Fatalf("message body delivered at summary interest: %+v", ev)
		}
	}

	// Enabling full interest materializes the window from the store.
	h.sub.SetInterest(char, model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}, model.InterestFull, 0)
	ev, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		return ev.Kind == model.EvConvView
	})
	if !ok {
		t.Fatalf("no conversation view; events: %s", dump(h.ui))
	}
	view := ev.Payload.(model.ConvView)
	if len(view.Window) != 1 || view.Window[0].Body != "secret" {
		t.Fatalf("materialized window = %+v", view.Window)
	}
	if view.Cursor.AsOfSeq != 1 {
		t.Fatalf("cursor = %+v, want AsOfSeq 1", view.Cursor)
	}
	if view.Cursor.HasOlder {
		t.Fatalf("unexpected HasOlder: %+v", view.Cursor)
	}
}

func TestNetworkDropAutoRetriesOnce(t *testing.T) {
	h := newHarness(t, model.InterestSummary)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	// Drop the connection; the session should reconnect once via a new dial.
	_ = h.fac.First().Close()

	if !h.ui.WaitForCount(3*time.Second, 2, func(ev model.Event) bool {
		p, ok := stateValue[model.SessionStatePayload](ev, model.SessionKey(char))
		return ok && p.State == "live"
	}) {
		t.Fatalf("session did not reconnect; events: %s", dump(h.ui))
	}
	deadline := time.Now().Add(time.Second)
	for h.fac.Count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.fac.Count() != 2 {
		t.Fatalf("dial count = %d, want 2", h.fac.Count())
	}
}

func TestNonRetryableErrorSurfaces(t *testing.T) {
	h := newHarness(t, model.InterestSummary)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	if err := h.fac.First().Send("ERR", fchat.EREvent{Code: 31, Message: "taken over"}); err != nil {
		t.Fatal(err)
	}
	ev, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := stateValue[model.SessionStatePayload](ev, model.SessionKey(char))
		return ok && p.State == "disconnected"
	})
	if !ok {
		t.Fatalf("no disconnected event; events: %s", dump(h.ui))
	}
	p, _ := stateValue[model.SessionStatePayload](ev, model.SessionKey(char))
	if p.Reason != "taken_over" || p.AutoRetry {
		t.Fatalf("disconnected payload = %+v", p)
	}
	time.Sleep(100 * time.Millisecond)
	if h.fac.Count() != 1 {
		t.Fatalf("session reconnected after a non-retryable error (dials=%d)", h.fac.Count())
	}
}

func TestMalformedFrameIsNonRetryable(t *testing.T) {
	h := newHarness(t, model.InterestSummary)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	if err := h.fac.First().SendRawText("bad frame, not a command"); err != nil {
		t.Fatal(err)
	}
	ev, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := stateValue[model.SessionStatePayload](ev, model.SessionKey(char))
		return ok && p.State == "disconnected"
	})
	if !ok {
		t.Fatalf("no disconnected event; events: %s", dump(h.ui))
	}
	p, _ := stateValue[model.SessionStatePayload](ev, model.SessionKey(char))
	if p.Reason != "protocol_mismatch" || p.AutoRetry {
		t.Fatalf("disconnected payload = %+v", p)
	}
	time.Sleep(100 * time.Millisecond)
	if h.fac.Count() != 1 {
		t.Fatalf("session reconnected after a malformed frame (dials=%d)", h.fac.Count())
	}
}

// countingTickets implements fchat.TicketManager and fchat.TicketInvalidator so
// tests can observe minting and invalidation.
type countingTickets struct {
	mints, invalidates atomic.Int32
}

func (t *countingTickets) Ticket(ctx context.Context, account string) (fchat.Ticket, error) {
	t.mints.Add(1)
	return fchat.Ticket{Value: "tkt-" + account, MintedAt: time.Now()}, nil
}

func (t *countingTickets) Invalidate(account string) { t.invalidates.Add(1) }

// TestBenignErrorStaysConnected: a per-command ERR must surface as an error
// event without ending the session.
func TestBenignErrorStaysConnected(t *testing.T) {
	h := newHarness(t, model.InterestSummary)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	if err := h.fac.First().Send("ERR", fchat.EREvent{Code: 28, Message: "already joined"}); err != nil {
		t.Fatal(err)
	}
	ev, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, isErr := ev.Payload.(model.ErrorPayload)
		return ev.Kind == model.EvError && isErr && p.Code == 28
	})
	if !ok {
		t.Fatalf("no error event; events: %s", dump(h.ui))
	}
	if p := ev.Payload.(model.ErrorPayload); p.Message != "already joined" {
		t.Fatalf("error payload = %+v", p)
	}
	// The session must still be live: no disconnected event, no new dial.
	for _, ev := range h.ui.Events() {
		if p, ok := stateValue[model.SessionStatePayload](ev, model.SessionKey(char)); ok && p.State == "disconnected" {
			t.Fatalf("session disconnected on a benign ERR; events: %s", dump(h.ui))
		}
	}
	if h.fac.Count() != 1 {
		t.Fatalf("dial count = %d, want 1", h.fac.Count())
	}
}

// TestIdentFailedInvalidatesTicket: after an IDENT_FAILED the cached ticket is
// dropped, so a later connect mints a fresh one instead of replaying the
// rejected ticket.
func TestIdentFailedInvalidatesTicket(t *testing.T) {
	ctx := context.Background()
	fac := fakeserver.NewWSFactory(fakeserver.Options{Character: char})
	defer fac.Close()
	tickets := &countingTickets{}
	mgr := core.NewManager(ctx, core.Config{
		Store:   memstore.New(),
		Tickets: tickets,
		Dial: func(string) session.Dialer {
			return session.Dialer(fac.Dial)
		},
	})
	opts := broker.DefaultSubOpts()
	opts.FlushEvery = 5 * time.Millisecond
	sub := mgr.Broker().Subscribe(opts)
	ui := fakeui.New(sub)
	go ui.Run(ctx)
	t.Cleanup(func() { sub.Close(); mgr.Logout(char) })

	if err := mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	if !ui.WaitForCount(2*time.Second, 1, func(ev model.Event) bool {
		p, ok := stateValue[model.SessionStatePayload](ev, model.SessionKey(char))
		return ok && p.State == "live"
	}) {
		t.Fatalf("session never became live; events: %s", dump(ui))
	}
	if tickets.mints.Load() != 1 {
		t.Fatalf("mints = %d, want 1", tickets.mints.Load())
	}

	if err := fac.First().Send("ERR", fchat.EREvent{Message: "IDENT_FAILED: invalid ticket"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := stateValue[model.SessionStatePayload](ev, model.SessionKey(char))
		return ok && p.State == "disconnected" && p.Reason == "ident_failed"
	}); !ok {
		t.Fatalf("no ident_failed disconnect; events: %s", dump(ui))
	}
	if tickets.invalidates.Load() != 1 {
		t.Fatalf("invalidations = %d, want 1", tickets.invalidates.Load())
	}
}

// TestRetryAfterLiveDoesNotLoopOnDialFailure: one auto retry after a live
// connection, then stop — a failed reconnect dial must not inherit the previous
// connection's liveness and retry forever.
func TestRetryAfterLiveDoesNotLoopOnDialFailure(t *testing.T) {
	fac := fakeserver.NewWSFactory(fakeserver.Options{Character: char})
	defer fac.Close()

	var failDial atomic.Bool
	var dials atomic.Int32
	h := newHarnessWithDial(t, model.InterestSummary, func(string) session.Dialer {
		return session.Dialer(func(ctx context.Context) (fchat.Conn, error) {
			dials.Add(1)
			if failDial.Load() {
				return nil, errors.New("dial: connection refused")
			}
			return fac.Dial(ctx)
		})
	}, fac)

	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	failDial.Store(true)
	_ = fac.First().Close()

	if _, ok := h.ui.WaitFor(3*time.Second, func(ev model.Event) bool {
		p, ok := stateValue[model.SessionStatePayload](ev, model.SessionKey(char))
		return ok && p.State == "disconnected"
	}); !ok {
		t.Fatalf("no disconnected event; events: %s", dump(h.ui))
	}
	// Give any (incorrect) extra retry loop time to fire: the default retry
	// delay is 250ms, so 3x covers several erroneous cycles.
	time.Sleep(750 * time.Millisecond)
	if n := dials.Load(); n != 2 {
		t.Fatalf("dial count = %d, want 2 (live dial + one failed retry)", n)
	}
}

func TestFriendsListSyncsOnceAndStreamsPresence(t *testing.T) {
	fac := fakeserver.NewWSFactory(fakeserver.Options{
		Character: char,
		Friends:   []string{"BestFriend", "Neko"},
		Roster:    [][]string{{"Neko", "Female", "online", ""}},
	})
	defer fac.Close()
	h := newHarnessWithDial(t, model.InterestSummary, func(string) session.Dialer {
		return session.Dialer(fac.Dial)
	}, fac)

	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	// The friends/bookmarks union arrives with inline presence from the
	// roster: Neko online, BestFriend outside the roster (offline).
	ev, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		_, ok := stateValue[model.FriendsPayload](ev, model.AccountKey("friends"))
		return ok
	})
	if !ok {
		t.Fatalf("no friends event; events: %s", dump(h.ui))
	}
	p, _ := stateValue[model.FriendsPayload](ev, model.AccountKey("friends"))
	if len(p.Friends) != 2 {
		t.Fatalf("friends payload = %+v, want 2 entries", ev.Payload)
	}
	byName := map[string]model.MemberInfo{}
	for _, f := range p.Friends {
		byName[f.Name] = f
	}
	if byName["Neko"].Online != true {
		t.Fatalf("Neko presence = %+v, want online", byName["Neko"])
	}
	if byName["BestFriend"].Online != false {
		t.Fatalf("BestFriend presence = %+v, want offline", byName["BestFriend"])
	}

	// Friends presence streams even though they are in no conversation: the
	// subscription watches them.
	if err := fac.First().Send("FLN", fchat.FLNEvent{Character: "Neko"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := stateValue[model.PresencePayload](ev, model.CharacterKey("Neko"))
		return ok && !p.Online
	}); !ok {
		t.Fatalf("no offline presence for watched friend; events: %s", dump(h.ui))
	}

	// The snapshot carries the same list once, account-wide.
	snaps := h.mgr.Snapshot()
	if len(snaps.Sessions) != 1 || len(snaps.Friends) != 2 {
		t.Fatalf("snapshot friends = %+v", snaps)
	}
}

// TestChannelCatalogFetchesOnceAndRefreshesRooms: the core-wide catalog is
// requested through the first live session (post-NLN), official channels are
// fetched once per core lifetime, and stale rooms are re-requested on later
// PINs.
func TestChannelCatalogFetchesOnceAndRefreshesRooms(t *testing.T) {
	oldTTL := core.RoomsTTL
	core.RoomsTTL = 120 * time.Millisecond
	t.Cleanup(func() { core.RoomsTTL = oldTTL })

	fac := fakeserver.NewWSFactory(fakeserver.Options{
		Character: char,
		PinEvery:  60 * time.Millisecond,
		Channels:  []fchat.OfficialChannel{{Name: "Frontpage", Characters: 12}},
		Rooms:     []fchat.PublicRoom{{Name: "adh-test0001", Title: "Test Room", Characters: 3}},
	})
	defer fac.Close()
	h := newHarnessWithDial(t, model.InterestSummary, func(string) session.Dialer {
		return session.Dialer(fac.Dial)
	}, fac)

	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	// The whole catalog arrives as one set-to event; the two replies may
	// land separately, so wait until both halves are populated.
	ev, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := stateValue[model.ChannelCatalogPayload](ev, model.AccountKey("catalog"))
		return ok && len(p.Official) > 0 && len(p.Rooms) > 0
	})
	if !ok {
		t.Fatalf("no channels event; events: %s", dump(h.ui))
	}
	p, _ := stateValue[model.ChannelCatalogPayload](ev, model.AccountKey("catalog"))
	if len(p.Official) != 1 || len(p.Rooms) != 1 {
		t.Fatalf("channels payload = %+v, want 1 official + 1 room", ev.Payload)
	}
	if p.Official[0].Name != "Frontpage" || p.Official[0].Characters != 12 {
		t.Fatalf("official channels = %+v", p.Official)
	}
	if p.Rooms[0].Name != "adh-test0001" || p.Rooms[0].Title != "Test Room" {
		t.Fatalf("rooms = %+v", p.Rooms)
	}

	// The snapshot carries the same catalog.
	snaps := h.mgr.Snapshot()
	if len(snaps.Catalog.Official) != 1 || len(snaps.Catalog.Rooms) != 1 {
		t.Fatalf("snapshot catalog = %+v", snaps.Catalog)
	}

	// Across several PIN-driven refreshes: exactly one CHA, ORS every TTL.
	srv := fac.First()
	deadline := time.Now().Add(500 * time.Millisecond)
	cha, ors := 0, 0
	for time.Now().Before(deadline) {
		select {
		case cmd := <-srv.Received():
			switch cmd.Code {
			case "CHA":
				cha++
			case "ORS":
				ors++
			}
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	if cha != 1 {
		t.Fatalf("CHA requests = %d, want 1", cha)
	}
	if ors < 2 {
		t.Fatalf("ORS requests = %d, want at least one TTL refresh", ors)
	}
}

func TestPingKeepsConnectionAlive(t *testing.T) {
	h := newHarness(t, model.InterestSummary)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	if err := h.fac.First().Send("PIN", nil); err != nil {
		t.Fatal(err)
	}
	if !waitServer(h.fac.First(), 2*time.Second, "PIN") {
		t.Fatal("session did not answer the server's PIN")
	}
}

// TestSeqSeededFromStoreAfterRelogin: read state is gone, but conv_seq must
// still be seeded from the store so a relogin continues the sequence instead
// of reusing persisted numbers and corrupting history cursors.
func TestSeqSeededFromStoreAfterRelogin(t *testing.T) {
	h := newHarness(t, model.InterestSummary)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	// First session persists the first message as seq 1.
	h.sendChannel(t, "while you were away")
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := nsValue[model.SummaryPayload](ev, model.StateSummary)
		return ok && p.Conv.ID == "Frontpage"
	}); !ok {
		t.Fatalf("no summary; events: %s", dump(h.ui))
	}

	h.mgr.Logout(char)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	if !h.ui.WaitForCount(2*time.Second, 2, func(ev model.Event) bool {
		p, ok := stateValue[model.SessionStatePayload](ev, model.SessionKey(char))
		return ok && p.State == "live"
	}) {
		t.Fatalf("second session never became live; events: %s", dump(h.ui))
	}

	// Bring the conversation into the new session and record a message. The
	// sequence must resume at 2, not restart at 1.
	res := h.mgr.Dispatch(model.Command{
		CID: "j2", Session: char, Op: model.OpJoin,
		Conv: model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"},
	})
	if !res.Accepted {
		t.Fatalf("join rejected: %+v", res)
	}
	send := h.mgr.Dispatch(model.Command{
		CID: "s2", Session: char, Op: model.OpSendMessage,
		Conv: model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"},
		Body: "after relogin",
	})
	if !send.Accepted {
		t.Fatalf("send rejected: %+v", send)
	}
	// Recording now happens only after the frame is written, so wait for the
	// self summary before inspecting the store.
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := nsValue[model.SummaryPayload](ev, model.StateSummary)
		return ok && p.Conv.ID == "Frontpage" && p.Self
	}); !ok {
		t.Fatalf("no self summary; events: %s", dump(h.ui))
	}

	entries, err := h.store.History(context.Background(), store.HistoryQuery{
		Session: char, Conv: model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ConvSeq != 1 || entries[1].ConvSeq != 2 {
		t.Fatalf("persisted seqs = %+v, want [1 2]", entries)
	}
}

// TestFriendPresenceFromRoster: a friend/bookmark that is online in the LIS
// roster but in no channel must be delivered online in the friends event, and
// later status changes must stream even at the default summary interest. This
// is the account-wide path that does not depend on conversation membership.
func TestFriendPresenceFromRoster(t *testing.T) {
	fac := fakeserver.NewWSFactory(fakeserver.Options{
		Character: char,
		Roster:    [][]string{{"BestFriend", "Female", "online", "hi"}},
		Friends:   []string{"BestFriend"},
	})
	t.Cleanup(fac.Close)
	h := newHarnessWithDial(t, model.InterestSummary, func(string) session.Dialer {
		return session.Dialer(fac.Dial)
	}, fac)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := stateValue[model.FriendsPayload](ev, model.AccountKey("friends"))
		if !ok {
			return false
		}
		for _, f := range p.Friends {
			if f.Name == "BestFriend" && f.Online {
				return true
			}
		}
		return false
	}); !ok {
		t.Fatalf("friend not delivered online; events: %s", dump(h.ui))
	}

	if err := fac.First().Send("STA", fchat.STAEvent{Character: "BestFriend", Status: "looking", StatusMsg: "rp?"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := stateValue[model.PresencePayload](ev, model.CharacterKey("BestFriend"))
		return ok && p.Status == "looking"
	}); !ok {
		t.Fatalf("friend status did not stream; events: %s", dump(h.ui))
	}
}

// TestHighlightLiveReload: a config change in the store must reach an
// already-running session via ReloadConfig, without a reconnect.
func TestHighlightLiveReload(t *testing.T) {
	h := newHarness(t, model.InterestFull)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	h.sendChannel(t, "no match here")
	if h.waitHighlight(t, "no match here") {
		t.Fatal("message highlighted before any config was stored")
	}

	h.putHighlights(t, char, []string{"secret"})
	if err := h.mgr.ReloadConfig(context.Background()); err != nil {
		t.Fatal(err)
	}

	h.sendChannel(t, "the secret word")
	if !h.waitHighlight(t, "the secret word") {
		t.Fatal("live-reloaded highlight did not take effect")
	}
}

// TestHighlightAppliedAtLogin: a character config stored before login applies to
// the new session.
func TestHighlightAppliedAtLogin(t *testing.T) {
	h := newHarness(t, model.InterestFull)
	h.putHighlights(t, char, []string{"chartterm"})
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	h.sendChannel(t, "a chartterm here")
	if !h.waitHighlight(t, "a chartterm here") {
		t.Fatal("per-character highlight not applied at login")
	}
	h.sendChannel(t, "nothing matching here")
	if h.waitHighlight(t, "nothing matching here") {
		t.Fatal("unexpected highlight")
	}
}

// TestAutoJoinOnLogin: the character's configured channels and rooms are joined
// with JCH once the session is ready. Joining is best-effort, so the test only
// asserts the requests were sent.
func TestAutoJoinOnLogin(t *testing.T) {
	h := newHarness(t, model.InterestSummary)
	err := config.NewProvider(h.store).SaveCharacter(context.Background(), char, config.Character{
		AutoJoin: []config.JoinTarget{
			{Kind: model.ConvOfficial, ID: "Frontpage", Name: "Frontpage"},
			{Kind: model.ConvRoom, ID: "adh-test0001", Name: "Test Room"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	got := map[string]bool{}
	srv := h.fac.First()
	deadline := time.Now().Add(2 * time.Second)
	for len(got) < 2 && time.Now().Before(deadline) {
		select {
		case cmd := <-srv.Received():
			if cmd.Code != "JCH" {
				continue
			}
			p, err := fchat.Decode[fchat.ChannelRef](cmd)
			if err != nil {
				t.Fatalf("decode JCH: %v", err)
			}
			got[p.Channel] = true
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	if !got["Frontpage"] || !got["adh-test0001"] {
		t.Fatalf("auto-join channels = %v, want Frontpage and adh-test0001", got)
	}
}

// TestAutoStatusOnLogin: a configured automatic status is sent as STA once the
// session is ready, the session's own presence reflects it without a manual
// set_status, and the raw message is remembered for the editor.
func TestAutoStatusOnLogin(t *testing.T) {
	h := newHarness(t, model.InterestSummary)
	err := config.NewProvider(h.store).SaveCharacter(context.Background(), char, config.Character{
		AutoStatus: &config.AutoStatus{Status: "away", Message: "brb [b]soon[/b]"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	// The STA carries the configured status and the raw BBCode message.
	srv := h.fac.First()
	var got fchat.StatusUpdate
	deadline := time.Now().Add(2 * time.Second)
	found := false
	for !found && time.Now().Before(deadline) {
		select {
		case cmd := <-srv.Received():
			if cmd.Code != "STA" {
				continue
			}
			p, err := fchat.Decode[fchat.StatusUpdate](cmd)
			if err != nil {
				t.Fatalf("decode STA: %v", err)
			}
			got = p
			found = true
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	if !found {
		t.Fatal("no STA sent for the automatic status")
	}
	if got.Status != "away" || got.StatusMsg != "brb [b]soon[/b]" {
		t.Fatalf("auto status STA = %+v", got)
	}

	// The session's own presence is updated optimistically.
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := stateValue[model.PresencePayload](ev, model.CharacterKey(char))
		return ok && p.Status == "away"
	}); !ok {
		t.Fatalf("no self presence after auto status; events: %s", dump(h.ui))
	}
	snaps := h.mgr.Snapshot()
	if len(snaps.Sessions) != 1 || snaps.Sessions[0].SelfStatusText != "brb [b]soon[/b]" {
		t.Fatalf("self status text = %+v", snaps.Sessions)
	}
}

// TestSetStatusUpdatesSelfWithoutReemit: setting a status updates the session's
// own presence optimistically (rendered for delivery), remembers the raw text
// for the editor, and does not send an STA merely because a session logs in.
func TestSetStatusUpdatesSelfWithoutReemit(t *testing.T) {
	h := newHarness(t, model.InterestSummary)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	// Login must not replay a stored status: no STA may be sent before the user
	// asks for one.
	srv := h.fac.First()
	drain := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(drain) {
		select {
		case cmd := <-srv.Received():
			if cmd.Code == "STA" {
				t.Fatalf("login re-emitted STA: %s", cmd)
			}
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}

	res := h.mgr.Dispatch(model.Command{
		CID: "s1", Session: char, Op: model.OpSetStatus,
		Status: "away", StatusMsg: "brb [b]soon[/b]",
	})
	if !res.Accepted {
		t.Fatalf("set_status rejected: %+v", res)
	}

	ev, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := stateValue[model.PresencePayload](ev, model.CharacterKey(char))
		return ok && p.Status == "away"
	})
	if !ok {
		t.Fatalf("no self presence after set_status; events: %s", dump(h.ui))
	}
	if p, _ := stateValue[model.PresencePayload](ev, model.CharacterKey(char)); p.StatusMsg != "brb <b>soon</b>" {
		t.Fatalf("rendered status msg = %q", p.StatusMsg)
	}

	// The STA reaches the server, and the raw text is available for the editor.
	select {
	case cmd := <-srv.Received():
		if cmd.Code != "STA" {
			t.Fatalf("expected STA, got %s", cmd)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no STA sent to the server")
	}
	snaps := h.mgr.Snapshot()
	if len(snaps.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(snaps.Sessions))
	}
	self := snaps.Sessions[0]
	if self.Self.Status != "away" {
		t.Fatalf("snapshot self status = %q", self.Self.Status)
	}
	if self.SelfStatusText != "brb [b]soon[/b]" {
		t.Fatalf("snapshot self status text = %q", self.SelfStatusText)
	}

	// Crown is moderator-granted and displayed when the server sends it, but
	// the character can never emit it.
	crown := h.mgr.Dispatch(model.Command{
		CID: "s2", Session: char, Op: model.OpSetStatus,
		Status: "crown", StatusMsg: "nope",
	})
	if crown.Accepted {
		t.Fatal("set_status accepted the reserved crown status")
	}

	// A crown sent by the server (a moderator granted it out of band) is
	// accepted and displayed like any other presence.
	if err := srv.Send("STA", fchat.STAEvent{Character: char, Status: "crown", StatusMsg: "granted"}); err != nil {
		t.Fatal(err)
	}
	ev, ok = h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, ok := stateValue[model.PresencePayload](ev, model.CharacterKey(char))
		return ok && p.Status == "crown"
	})
	if !ok {
		t.Fatalf("no crown presence from the server; events: %s", dump(h.ui))
	}
}

// --- helpers ---

func dump(ui *fakeui.UI) string {
	var b []byte
	for _, ev := range ui.Events() {
		b = append(b, []byte(ev.Kind+"; ")...)
	}
	return string(b)
}

func waitServer(srv *fakeserver.Server, timeout time.Duration, code string) bool {
	deadline := time.Now().Add(timeout)
	for {
		select {
		case cmd := <-srv.Received():
			if cmd.Code == code {
				return true
			}
		default:
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestConvViewDelta: a repeat visit that supplies the client's cursor
// materializes only the entries after it, so the client merges them into its
// retained window instead of rebuilding a full one. The delta omits members,
// which stream separately at summary interest.
func TestConvViewDelta(t *testing.T) {
	h := newHarness(t, model.InterestFull)
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	conv := officialConv("Frontpage")
	for _, body := range []string{"one", "two", "three"} {
		h.sendChannel(t, body)
		if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
			p, isM := ev.Payload.(model.MessagePayload)
			return ev.Kind == model.EvMessage && isM && p.Entry.Body == body
		}); !ok {
			t.Fatalf("message %q not seen; events: %s", body, dump(h.ui))
		}
	}

	full, err := h.mgr.ConvView(context.Background(), char, conv, 120, 0)
	if err != nil {
		t.Fatalf("ConvView full: %v", err)
	}
	if full.Delta {
		t.Fatal("a since=0 view must be a full materialization")
	}
	if len(full.Window) != 3 {
		t.Fatalf("full window = %d entries, want 3", len(full.Window))
	}
	mid := full.Window[1].ConvSeq // "two"

	delta, err := h.mgr.ConvView(context.Background(), char, conv, 120, mid)
	if err != nil {
		t.Fatalf("ConvView delta: %v", err)
	}
	if !delta.Delta {
		t.Fatal("a since>0 view must be a delta")
	}
	if len(delta.Window) != 1 || delta.Window[0].ConvSeq != full.Cursor.AsOfSeq {
		t.Fatalf("delta window = %+v, want only the entry after %d", delta.Window, mid)
	}
	if len(delta.Members) != 0 {
		t.Fatalf("delta view shipped %d members; metadata streams separately", len(delta.Members))
	}
}
