package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"plexo/internal/broker"
	"plexo/internal/config"
	"plexo/internal/fchat"
	"plexo/internal/model"
	"plexo/internal/render"
	"plexo/test/memstore"
)

// TestClearTypingResetsAndEmitsOff guards stale typing state: ending a
// connection must drop the buffered typists and tell subscribers they are off.
func TestClearTypingResetsAndEmitsOff(t *testing.T) {
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b})
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	s.ensureConv(conv)
	s.applyTyping(conv, "Other", true, false)

	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()

	s.clearTyping()
	if len(s.st.typing) != 0 {
		t.Fatalf("typing map not cleared: %+v", s.st.typing)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed before typing-off arrived")
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
				if !ok || p.On || typistFromKey(sp.Key) != "Other" {
					continue
				}
				gotConv, _ := model.ConvRefFromKey(sp.Key)
				if gotConv == conv {
					return
				}
			}
		case <-deadline:
			t.Fatal("did not observe a typing-off event")
		}
	}
}

// TestSearchCacheLifecycle guards the session's FKS cache: publishing stores
// the enriched rows and bumps the revision, and clearing on disconnect empties
// it and bumps again so a stale client fetch is rejected. Clearing an already
// empty cache is a no-op.
func TestSearchCacheLifecycle(t *testing.T) {
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b})

	if s.st.search == nil || len(s.st.search) != 0 || s.st.searchRev != 0 {
		t.Fatalf("initial cache = %#v rev %d, want empty rev 0", s.st.search, s.st.searchRev)
	}

	s.publishSearch([]model.MemberInfo{{Name: "Some Guy", Online: true}})
	if len(s.st.search) != 1 || s.st.searchRev != 1 {
		t.Fatalf("after publish: len %d rev %d, want 1/1", len(s.st.search), s.st.searchRev)
	}

	s.clearSearch()
	if s.st.search == nil || len(s.st.search) != 0 || s.st.searchRev != 2 {
		t.Fatalf("after clear: %#v rev %d, want empty rev 2", s.st.search, s.st.searchRev)
	}

	s.clearSearch()
	if s.st.searchRev != 2 {
		t.Fatalf("clearing an empty cache bumped revision to %d", s.st.searchRev)
	}
}

// TestRecordEntryRendersHTML: live message events carry rendered HTML while the
// stored body stays raw BBCode.
func TestRecordEntryRendersHTML(t *testing.T) {
	renderer, err := render.New()
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b, Store: memstore.New(), Renderer: renderer})
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}

	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()

	s.recordEntry(conv, "msg", "Other", "[b]hi[/b]", nil)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed before message event")
			}
			for _, ev := range batch.Events {
				if ev.Kind != model.EvMessage {
					continue
				}
				p, ok := ev.Payload.(model.MessagePayload)
				if !ok {
					continue
				}
				if p.Entry.HTML != ": <b>hi</b>" {
					t.Fatalf("HTML = %q, want %q", p.Entry.HTML, ": <b>hi</b>")
				}
				if p.Entry.Body != "[b]hi[/b]" {
					t.Fatalf("stored body = %q, want raw BBCode", p.Entry.Body)
				}
				return
			}
		case <-deadline:
			t.Fatal("did not observe a message event")
		}
	}
}

// TestRecordEntryRoomName: a room entry records the room's readable title
// alongside its opaque ADH-... id, so persisted logs can be browsed by name.
func TestRecordEntryRoomName(t *testing.T) {
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b, Store: memstore.New()})
	room := model.ConvRef{Kind: model.ConvRoom, ID: "ADH-abc"}
	s.ensureConv(room).title = "The Tavern"

	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()

	s.recordEntry(room, "msg", "Other", "hi", nil)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed before message event")
			}
			for _, ev := range batch.Events {
				if ev.Kind != model.EvMessage {
					continue
				}
				p, ok := ev.Payload.(model.MessagePayload)
				if !ok {
					continue
				}
				if p.Entry.ConvName != "The Tavern" {
					t.Fatalf("ConvName = %q, want %q", p.Entry.ConvName, "The Tavern")
				}
				return
			}
		case <-deadline:
			t.Fatal("did not observe a message event")
		}
	}
}

// TestRecordEntryHighlight: an incoming channel message matching a configured
// highlight string is flagged on both the message and the summary, while DMs,
// non-matching channel messages, and the sender's own copy are not.
func TestRecordEntryHighlight(t *testing.T) {
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b, Settings: config.Character{Highlights: []string{"Kira"}}})
	channel := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}

	if msg, sum := recordAndCollect(t, b, s, channel, "Other", "hey KIRA!"); !msg.Highlight || !sum.Highlight || msg.Self || sum.Self {
		t.Fatalf("channel match: message=%+v summary=%+v, want non-self highlight", msg, sum)
	}
	if msg, sum := recordAndCollect(t, b, s, model.ConvRef{Kind: model.ConvDM, ID: "Other"}, "Other", "hey Kira"); msg.Highlight || sum.Highlight || msg.Self || sum.Self {
		t.Fatalf("DM must not set highlight: message=%+v summary=%+v", msg, sum)
	}
	if msg, sum := recordAndCollect(t, b, s, channel, "Other", "nothing here"); msg.Highlight || sum.Highlight || msg.Self || sum.Self {
		t.Fatalf("non-match set highlight: message=%+v summary=%+v", msg, sum)
	}
	if msg, sum := recordAndCollect(t, b, s, channel, "Vix", "hey Kira"); msg.Highlight || sum.Highlight || !msg.Self || !sum.Self {
		t.Fatalf("self copy set highlight or lost self: message=%+v summary=%+v", msg, sum)
	}
}

// recordAndCollect records one entry and returns the message payload a
// full-interest subscriber receives and the summary payload a summary-interest
// subscriber receives. A full subscriber no longer gets a summary (the message
// carries the same signal), so the two tiers are observed separately.
func recordAndCollect(t *testing.T, b *broker.Broker, s *Session, conv model.ConvRef, speaker, body string) (model.MessagePayload, model.SummaryPayload) {
	t.Helper()
	full := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer full.Close()
	summary := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestSummary})
	defer summary.Close()
	s.recordEntry(conv, "msg", speaker, body, nil)

	var msg model.MessagePayload
	var sum model.SummaryPayload
	msgSet, sumSet := false, false
	deadline := time.After(2 * time.Second)
	for !msgSet || !sumSet {
		select {
		case batch, ok := <-full.Events():
			if !ok {
				t.Fatal("subscription closed before both payloads arrived")
			}
			for _, ev := range batch.Events {
				if p, ok := ev.Payload.(model.MessagePayload); ok && ev.Kind == model.EvMessage {
					msg, msgSet = p, true
				}
			}
		case batch, ok := <-summary.Events():
			if !ok {
				t.Fatal("subscription closed before both payloads arrived")
			}
			for _, ev := range batch.Events {
				if ev.Kind != model.EvState {
					continue
				}
				sp, ok := ev.Payload.(model.StatePayload)
				if !ok || model.KeyNamespace(sp.Key) != model.StateSummary {
					continue
				}
				if p, ok := sp.Value.(model.SummaryPayload); ok {
					sum, sumSet = p, true
				}
			}
		case <-deadline:
			t.Fatalf("missing payloads: message=%v summary=%v", msgSet, sumSet)
		}
	}
	return msg, sum
}

// TestSearchPresenceLocked: name/gender/status filters, online-only, sort, and
// limit.
func TestSearchPresenceLocked(t *testing.T) {
	s := New(Config{Character: "Vix"})
	online := func(name, gender, status string) {
		s.setPresenceQuiet(name, gender, status, "")
	}
	online("Alice", "Female", "online")
	online("Alan", "Male", "looking")
	online("Bob", "Male", "online")
	s.markOffline("Carol")

	cases := []struct {
		name string
		q    model.PresenceQuery
		want []string
	}{
		{"all online", model.PresenceQuery{}, []string{"Alan", "Alice", "Bob"}},
		{"substring", model.PresenceQuery{Query: "al"}, []string{"Alan", "Alice"}},
		{"gender", model.PresenceQuery{Gender: "male"}, []string{"Alan", "Bob"}},
		{"status", model.PresenceQuery{Status: "online"}, []string{"Alice", "Bob"}},
		{"limit", model.PresenceQuery{Limit: 1}, []string{"Alan"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.searchPresenceLocked(tc.q)
			names := make([]string, len(got))
			for i, m := range got {
				names[i] = m.Name
			}
			if strings.Join(names, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v, want %v", names, tc.want)
			}
		})
	}
}

// TestSnapshotAfterActorExitDoesNotHang: a terminal disconnect must not leave
// callers blocked on a reply that will never come (which would wedge Snapshot
// and therefore every new browser connection).
func TestSnapshotAfterActorExitDoesNotHang(t *testing.T) {
	b := broker.New()
	s := New(Config{
		Character: "Vix",
		Broker:    b,
		Dial:      func(context.Context) (fchat.Conn, error) { return nil, fchat.ErrMalformed },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()

	deadline := time.After(2 * time.Second)
	disconnected := false
	for !disconnected {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed")
			}
			for _, ev := range batch.Events {
				sp, ok := stateFor(ev, model.SessionKey("Vix"))
				if !ok {
					continue
				}
				if p, ok := sp.Value.(model.SessionStatePayload); ok && p.State == "disconnected" {
					disconnected = true
				}
			}
		case <-deadline:
			t.Fatal("no terminal disconnect")
		}
	}
	// The disconnect event is emitted just before run returns; give it a moment
	// to fully exit.
	time.Sleep(50 * time.Millisecond)

	reply := make(chan model.SessionSnapshot, 1)
	go func() { reply <- s.Snapshot() }()
	select {
	case <-reply:
	case <-time.After(2 * time.Second):
		t.Fatal("Snapshot hung after the actor exited")
	}
}

// TestPresenceRendering: status messages stay raw in session state but are
// delivered as HTML on every path (search results and the session snapshot).
func TestPresenceRendering(t *testing.T) {
	renderer, err := render.New()
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	s := New(Config{Character: "Vix", Renderer: renderer})
	s.setPresenceQuiet("Other", "Male", "online", "[b]hi[/b]")
	s.setPresenceQuiet("Vix", "", "online", "[i]me[/i]")

	if got := s.st.roster[nameKey("Other")].StatusMsg; got != "[b]hi[/b]" {
		t.Fatalf("roster status mutated: %q", got)
	}
	results := s.searchPresenceLocked(model.PresenceQuery{Query: "Other"})
	if len(results) != 1 || results[0].StatusMsg != "<b>hi</b>" {
		t.Fatalf("search status = %+v, want rendered HTML", results)
	}
	snap := s.snapshotLocked()
	if snap.Self.StatusMsg != "<i>me</i>" {
		t.Fatalf("snapshot self status = %q, want rendered HTML", snap.Self.StatusMsg)
	}
}

// TestSnapshotCarriesServerLimits: the client counts bytes against chat_max and
// priv_max to warn before the core rejects a send as too_long, so the snapshot
// must expose them once VAR has reported them.
func TestSnapshotCarriesServerLimits(t *testing.T) {
	s := New(Config{Character: "Vix"})
	s.st.vars.ChatMax = 4096
	s.st.vars.PrivMax = 50000
	snap := s.snapshotLocked()
	if snap.ChatMax != 4096 || snap.PrivMax != 50000 {
		t.Fatalf("limits = %d/%d, want 4096/50000", snap.ChatMax, snap.PrivMax)
	}
}

// TestSnapshotOmitsLeftChannels: the "left" event deletes a channel from the
// client's conversation list, so the snapshot must omit it too; otherwise a
// resync (a fresh subscriber or a broker gap) resurrects a channel the
// character already left. DMs are instead gated on tracked, and broadcasts are
// not membership-scoped.
func TestSnapshotOmitsLeftChannels(t *testing.T) {
	s := New(Config{Character: "Vix"})
	joined := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	leftChannel := model.ConvRef{Kind: model.ConvOfficial, ID: "OldChannel"}
	leftRoom := model.ConvRef{Kind: model.ConvRoom, ID: "ADH-old"}
	dm := model.ConvRef{Kind: model.ConvDM, ID: "Neko"}
	untrackedDm := model.ConvRef{Kind: model.ConvDM, ID: "Stranger"}
	broadcast := model.ConvRef{Kind: model.ConvBroadcast, ID: "global"}

	s.ensureConv(joined).membership = memJoined
	s.ensureConv(leftChannel)
	s.ensureConv(leftRoom)
	s.ensureConv(dm).tracked = true
	s.ensureConv(untrackedDm)
	s.ensureConv(broadcast)

	snap := s.snapshotLocked()
	got := make(map[string]bool, len(snap.Conversations))
	for _, c := range snap.Conversations {
		got[c.Conv.Key()] = true
	}
	if !got[joined.Key()] {
		t.Fatalf("joined channel missing from snapshot: %+v", snap.Conversations)
	}
	if got[leftChannel.Key()] {
		t.Fatalf("left channel present in snapshot: %+v", snap.Conversations)
	}
	if got[leftRoom.Key()] {
		t.Fatalf("left room present in snapshot: %+v", snap.Conversations)
	}
	if !got[dm.Key()] {
		t.Fatalf("tracked DM missing from snapshot: %+v", snap.Conversations)
	}
	if got[untrackedDm.Key()] {
		t.Fatalf("untracked DM present in snapshot: %+v", snap.Conversations)
	}
	if !got[broadcast.Key()] {
		t.Fatalf("broadcast missing from snapshot: %+v", snap.Conversations)
	}
}

// TestFLNIsGlobalLeave: FLN implies a global LCH, so going offline removes the
// character from every conversation's member and op lists, not just presence.
func TestFLNIsGlobalLeave(t *testing.T) {
	s := New(Config{Character: "Vix"})
	if err := s.handle(jsonFrame("JCH", `{"channel":"Frontpage","title":"Frontpage","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("JCH self: %v", err)
	}
	if err := s.handle(jsonFrame("ICH", `{"channel":"Frontpage","users":["Kira"]}`)); err != nil {
		t.Fatalf("ICH: %v", err)
	}
	if err := s.handle(jsonFrame("COL", `{"channel":"Frontpage","oplist":["","Kira"]}`)); err != nil {
		t.Fatalf("COL: %v", err)
	}
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	if !s.st.convs[convKey(conv)].members[nameKey("Kira")] {
		t.Fatal("Kira was not added as a member")
	}

	if err := s.handle(jsonFrame("FLN", `{"character":"kira"}`)); err != nil {
		t.Fatalf("FLN: %v", err)
	}
	cs := s.st.convs[convKey(conv)]
	if cs.members[nameKey("Kira")] || cs.ops[nameKey("Kira")] {
		t.Fatalf("FLN left stale membership/op: members=%v ops=%v", cs.members, cs.ops)
	}
	if p := s.st.roster[nameKey("Kira")]; p.Online {
		t.Fatal("FLN did not mark Kira offline")
	}
}

// TestSelfFLNIgnored: the server never sends our own FLN on a live connection
// (a takeover arrives as ERR 31). The only source is the previous connection's
// teardown racing a reconnect, so ignoring it keeps us from marking ourselves
// offline and emitting a false presence change.
func TestSelfFLNIgnored(t *testing.T) {
	s := New(Config{Character: "Vix"})
	s.setPresence("Vix", "", "online", "")
	if err := s.handle(jsonFrame("FLN", `{"character":"vix"}`)); err != nil {
		t.Fatalf("FLN: %v", err)
	}
	if p := s.st.roster[nameKey("Vix")]; !p.Online {
		t.Fatal("self FLN marked us offline")
	}
}

// TestNameCaseIsUnified: differently cased spellings from different frames
// resolve to one roster entry, so membership and presence still join.
func TestNameCaseIsUnified(t *testing.T) {
	s := New(Config{Character: "Vix"})
	if err := s.handle(jsonFrame("JCH", `{"channel":"Frontpage","title":"Frontpage","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("JCH self: %v", err)
	}
	if err := s.handle(jsonFrame("ICH", `{"channel":"Frontpage","users":["Kira"]}`)); err != nil {
		t.Fatalf("ICH: %v", err)
	}
	if err := s.handle(jsonFrame("NLN", `{"identity":"kira","gender":"Female","status":"online"}`)); err != nil {
		t.Fatalf("NLN: %v", err)
	}
	if got := s.displayName(nameKey("KIRA")); got != "Kira" {
		t.Fatalf("canonical name = %q, want first-seen Kira", got)
	}
	mi := s.memberInfo("KIRA")
	if mi.Name != "Kira" || !mi.Online || mi.Gender != "Female" {
		t.Fatalf("memberInfo = %+v, want Kira online", mi)
	}
}

// TestNonAuthoritativeSpellingYieldsToAuthoritative: the login ignore list is
// lowercased and arrives before LIS. Its provisional seed must not lock the
// roster spelling once an authoritative frame names the character.
func TestNonAuthoritativeSpellingYieldsToAuthoritative(t *testing.T) {
	s := New(Config{Character: "Vix"})
	if err := s.handle(jsonFrame("IGN", `{"characters":["kira"],"action":"init"}`)); err != nil {
		t.Fatalf("IGN: %v", err)
	}
	if s.st.named[nameKey("kira")] {
		t.Fatal("a non-authoritative frame marked the spelling authoritative")
	}
	if err := s.handle(jsonFrame("NLN", `{"identity":"Kira","gender":"Female","status":"online"}`)); err != nil {
		t.Fatalf("NLN: %v", err)
	}
	if got := s.displayName(nameKey("KIRA")); got != "Kira" {
		t.Fatalf("canonical name = %q, want authoritative Kira", got)
	}
	if got := s.ignoreList(); len(got) != 1 || got[0] != "Kira" {
		t.Fatalf("ignore list = %+v, want [Kira]", got)
	}
}

// TestFriendListWithheldUntilOnline: the client is only told about friends the
// roster can name authoritatively, so an offline bookmark waits outside the
// projection until it comes online.
func TestFriendListWithheldUntilOnline(t *testing.T) {
	s := New(Config{Character: "Vix"})
	if err := s.handle(jsonFrame("FRL", `{"characters":["bestfriend"]}`)); err != nil {
		t.Fatalf("FRL: %v", err)
	}
	if got := s.friendInfosLocked(); len(got) != 0 {
		t.Fatalf("offline friend leaked into the client list: %+v", got)
	}
	if err := s.handle(jsonFrame("NLN", `{"identity":"BestFriend","gender":"Female","status":"online"}`)); err != nil {
		t.Fatalf("NLN: %v", err)
	}
	got := s.friendInfosLocked()
	if len(got) != 1 || got[0].Name != "BestFriend" || !got[0].Online {
		t.Fatalf("friend list = %+v, want BestFriend online", got)
	}
}

// TestConvMetaDMPartnerIsMember: a DM's materialized member list must contain
// the partner, so presence scoping watches them even without a roster event.
func TestConvMetaDMPartnerIsMember(t *testing.T) {
	s := New(Config{Character: "Vix"})
	dm := model.ConvRef{Kind: model.ConvDM, ID: "Kira"}
	s.ensureConv(dm)
	s.setPresenceQuiet("Kira", "Female", "online", "")

	meta := s.convMetaLocked(dm)
	if len(meta.Members) != 1 || meta.Members[0].Name != "Kira" || !meta.Members[0].Online {
		t.Fatalf("DM members = %+v, want Kira online", meta.Members)
	}
}

// TestTrackedDMsGateTheSnapshot: a DM appears in the snapshot only while
// tracked, and set_tracked flips that state (including a DM never messaged).
func TestTrackedDMsGateTheSnapshot(t *testing.T) {
	s := New(Config{Character: "Vix"})
	dm := model.ConvRef{Kind: model.ConvDM, ID: "Neko"}

	if snapshotHasConv(s.snapshotLocked(), dm.Key()) {
		t.Fatal("unknown DM unexpectedly present")
	}
	// Tracking creates the conversation and includes it in the snapshot.
	if res := s.handleCommand(model.Command{Op: model.OpSetTracked, Conv: dm, Tracked: true}); !res.Accepted {
		t.Fatalf("track rejected: %+v", res)
	}
	if cs := s.st.convs[convKey(dm)]; cs == nil || !cs.tracked {
		t.Fatalf("DM not tracked: %+v", cs)
	}
	if !snapshotHasConv(s.snapshotLocked(), dm.Key()) {
		t.Fatal("tracked DM missing from snapshot")
	}
	// Tracking only applies to DMs.
	channel := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	if res := s.handleCommand(model.Command{Op: model.OpSetTracked, Conv: channel, Tracked: true}); res.Accepted {
		t.Fatal("tracking a channel must be rejected")
	}
	// Untracking hides the DM without deleting its conversation state.
	if res := s.handleCommand(model.Command{Op: model.OpSetTracked, Conv: dm, Tracked: false}); !res.Accepted {
		t.Fatalf("untrack rejected: %+v", res)
	}
	if cs := s.st.convs[convKey(dm)]; cs == nil || cs.tracked {
		t.Fatalf("DM still tracked: %+v", cs)
	}
	if snapshotHasConv(s.snapshotLocked(), dm.Key()) {
		t.Fatal("untracked DM present in snapshot")
	}
}

// TestSetTrackedEmitsConvEvent: a tracking change reaches other clients as a
// conversation event so their sidebars stay in sync.
func TestSetTrackedEmitsConvEvent(t *testing.T) {
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b})
	dm := model.ConvRef{Kind: model.ConvDM, ID: "Neko"}

	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()

	if res := s.handleCommand(model.Command{Op: model.OpSetTracked, Conv: dm, Tracked: true}); !res.Accepted {
		t.Fatalf("track rejected: %+v", res)
	}
	waitConvState(t, sub, "Vix", dm)

	if res := s.handleCommand(model.Command{Op: model.OpSetTracked, Conv: dm, Tracked: false}); !res.Accepted {
		t.Fatalf("untrack rejected: %+v", res)
	}
	waitConvRemoved(t, sub, "Vix", dm)
}

// TestRecordEntryTracksDM: any DM message reopens a conversation, whether it
// arrived from outside or was sent from inside.
func TestRecordEntryTracksDM(t *testing.T) {
	s := New(Config{Character: "Vix"})
	dm := model.ConvRef{Kind: model.ConvDM, ID: "Neko"}
	s.recordEntry(dm, "dm", "Neko", "hi", nil)
	if cs := s.st.convs[convKey(dm)]; cs == nil || !cs.tracked {
		t.Fatalf("DM message did not track the conversation: %+v", cs)
	}
}

func snapshotHasConv(snap model.SessionSnapshot, key string) bool {
	for _, c := range snap.Conversations {
		if c.Conv.Key() == key {
			return true
		}
	}
	return false
}

func waitConvState(t *testing.T, sub *broker.Subscription, session string, conv model.ConvRef) {
	t.Helper()
	waitConvRecord(t, sub, model.ConvKey(session, conv), false)
}

func waitConvRemoved(t *testing.T, sub *broker.Subscription, session string, conv model.ConvRef) {
	t.Helper()
	waitConvRecord(t, sub, model.ConvKey(session, conv), true)
}

func waitConvRecord(t *testing.T, sub *broker.Subscription, key string, removed bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatalf("subscription closed before conv %s", key)
			}
			for _, ev := range batch.Events {
				sp, ok := stateFor(ev, key)
				if !ok || sp.Removed != removed {
					continue
				}
				return
			}
		case <-deadline:
			t.Fatalf("did not observe conv %s (removed=%v)", key, removed)
		}
	}
}

// TestRecordEntryAnnouncesTrackedTransition: the first DM message turns an
// untracked conversation tracked and announces it, so a summary-interest client
// that receives no message event still learns the conversation exists. A later
// message must not re-announce the transition.
func TestRecordEntryAnnouncesTrackedTransition(t *testing.T) {
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b})
	dm := model.ConvRef{Kind: model.ConvDM, ID: "Neko"}

	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()

	s.recordEntry(dm, "dm", "Neko", "hi", nil)
	waitConvState(t, sub, "Vix", dm)

	s.recordEntry(dm, "dm", "Neko", "again", nil)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed before the second message")
			}
			for _, ev := range batch.Events {
				if sp, ok := stateFor(ev, model.ConvKey("Vix", dm)); ok && !sp.Removed {
					t.Fatal("tracked re-announced on a second message")
				}
				if p, ok := ev.Payload.(model.MessagePayload); ev.Kind == model.EvMessage && ok && p.Entry.Body == "again" {
					return
				}
			}
		case <-deadline:
			t.Fatal("second message never delivered")
		}
	}
}
