package session

import (
	"testing"
	"time"

	"plexo/internal/broker"
	"plexo/internal/fchat"
	"plexo/internal/model"
	"plexo/test/memstore"
)

// TestConvRefForChannelCaseInsensitive: the ADH- prefix match must not depend
// on case. F-Chat lowercases channel lookups, so a lowercase adh-... frame is
// still a room, not an official channel.
func TestConvRefForChannelCaseInsensitive(t *testing.T) {
	for _, id := range []string{"ADH-abc123", "adh-abc123", "aDh-AbC123"} {
		if ref := convRefForChannel(id); ref.Kind != model.ConvRoom {
			t.Errorf("convRefForChannel(%q).Kind = %q, want %q", id, ref.Kind, model.ConvRoom)
		}
	}
	if ref := convRefForChannel("Frontpage"); ref.Kind != model.ConvOfficial {
		t.Errorf("convRefForChannel(Frontpage).Kind = %q, want %q", ref.Kind, model.ConvOfficial)
	}
}

// TestChannelIdentityCaseInsensitive reproduces the phantom-channel bug: JCH
// carries the canonical ADH-... id, while some relayed state frames (CDS, COL,
// mode changes) echo the caller's casing. Both spellings must resolve to one
// conversation, or a lowercase "official channel" appears beside the real room.
func TestChannelIdentityCaseInsensitive(t *testing.T) {
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b, Store: memstore.New()})

	if err := s.handle(jsonFrame("JCH", `{"channel":"ADH-AbC123","title":"Secret Room","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("JCH: %v", err)
	}
	if err := s.handle(jsonFrame("CDS", `{"channel":"adh-abc123","description":"hello"}`)); err != nil {
		t.Fatalf("CDS: %v", err)
	}
	if err := s.handle(jsonFrame("COL", `{"channel":"adh-abc123","oplist":["Vix"]}`)); err != nil {
		t.Fatalf("COL: %v", err)
	}

	if len(s.st.convs) != 1 {
		t.Fatalf("convs = %d, want 1: %+v", len(s.st.convs), s.st.convs)
	}
	cs := s.st.convs[convKey(model.ConvRef{Kind: model.ConvRoom, ID: "ADH-AbC123"})]
	if cs == nil {
		t.Fatalf("no conversation for the canonical room id: %+v", s.st.convs)
	}
	if cs.ref.Kind != model.ConvRoom || cs.ref.ID != "ADH-AbC123" {
		t.Fatalf("conv ref = %+v, want first-seen canonical room ref", cs.ref)
	}
	if cs.title != "Secret Room" || cs.description != "hello" {
		t.Fatalf("frames did not merge: title=%q description=%q", cs.title, cs.description)
	}
}

// TestOfficialChannelCaseUnified: the same rule applies to official channels,
// whose names are also case-insensitive server-side.
func TestOfficialChannelCaseUnified(t *testing.T) {
	s := New(Config{Character: "Vix"})
	if err := s.handle(jsonFrame("JCH", `{"channel":"Frontpage","title":"Frontpage","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("JCH self: %v", err)
	}
	if err := s.handle(jsonFrame("ICH", `{"channel":"Frontpage","users":["Kira"]}`)); err != nil {
		t.Fatalf("ICH: %v", err)
	}
	if err := s.handle(jsonFrame("COL", `{"channel":"frontpage","oplist":["Kira"]}`)); err != nil {
		t.Fatalf("COL: %v", err)
	}
	if len(s.st.convs) != 1 {
		t.Fatalf("convs = %d, want 1: %+v", len(s.st.convs), s.st.convs)
	}
	cs := s.st.convs[convKey(model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"})]
	if cs == nil || cs.ref.ID != "Frontpage" {
		t.Fatalf("official conv = %+v, want first-seen Frontpage", cs)
	}
	if !cs.ops[nameKey("Kira")] {
		t.Fatal("lowercase COL did not merge into the official channel")
	}
}

// TestLeaveClearsMembership: leaving a channel drops the roster we can no longer
// trust, so the state agrees with the client-facing snapshot (which omits
// unjoined conversations). A rejoin rehydrates through ICH/COL.
func TestLeaveClearsMembership(t *testing.T) {
	s := New(Config{Character: "Vix"})
	if err := s.handle(jsonFrame("JCH", `{"channel":"Frontpage","title":"Frontpage","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("JCH self: %v", err)
	}
	if err := s.handle(jsonFrame("ICH", `{"channel":"Frontpage","users":["Kira","Vix"]}`)); err != nil {
		t.Fatalf("ICH: %v", err)
	}
	if err := s.handle(jsonFrame("COL", `{"channel":"Frontpage","oplist":["Kira"]}`)); err != nil {
		t.Fatalf("COL: %v", err)
	}

	if err := s.handle(jsonFrame("LCH", `{"channel":"Frontpage","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("LCH: %v", err)
	}
	cs := s.st.convs[convKey(model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"})]
	if cs.membership != memLeft || len(cs.members) != 0 || len(cs.ops) != 0 {
		t.Fatalf("leave kept membership: membership=%v members=%v ops=%v", cs.membership, cs.members, cs.ops)
	}
}

// TestLeftChannelNotReAnnounced reproduces the resurrection bug: leaving a
// channel, then an FLN for a member (FLN acts as a global LCH) or an
// out-of-order membership frame must not emit a conversation event that the
// client would use to re-add the row. Only the leave itself may be announced.
func TestLeftChannelNotReAnnounced(t *testing.T) {
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b, Store: memstore.New()})
	if err := s.handle(jsonFrame("JCH", `{"channel":"Frontpage","title":"Frontpage","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("JCH self: %v", err)
	}
	if err := s.handle(jsonFrame("ICH", `{"channel":"Frontpage","users":["Kira","Vix"]}`)); err != nil {
		t.Fatalf("ICH: %v", err)
	}

	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()

	if err := s.handle(jsonFrame("LCH", `{"channel":"Frontpage","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("LCH: %v", err)
	}
	for _, f := range []fchat.Frame{
		jsonFrame("FLN", `{"character":"kira"}`),
		jsonFrame("ICH", `{"channel":"Frontpage","users":["Kira"]}`),
		jsonFrame("CDS", `{"channel":"Frontpage","description":"x"}`),
		jsonFrame("COL", `{"channel":"Frontpage","oplist":["Kira"]}`),
		jsonFrame("JCH", `{"channel":"Frontpage","title":"Frontpage","character":{"identity":"Kira"}}`),
	} {
		if err := s.handle(f); err != nil {
			t.Fatalf("%s: %v", f.Code, err)
		}
	}

	// Drain the subscription; any conv event other than the leave means the
	// client would resurrect the conversation.
	deadline := time.After(250 * time.Millisecond)
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed")
			}
			for _, ev := range batch.Events {
				if ev.Kind != model.EvState {
					continue
				}
				sp, ok := ev.Payload.(model.StatePayload)
				if !ok || model.KeyNamespace(sp.Key) != model.StateConv {
					continue
				}
				if !sp.Removed {
					t.Fatalf("left conversation re-announced: %+v", sp)
				}
			}
		case <-deadline:
			return
		}
	}
}

// TestRMOUpdatesMode: the server signals a channel mode change with RMO rather
// than a fresh ICH, so the session must update the conversation's mode. Without
// it the client keeps composing the wrong message kind, which the server then
// rejects after the optimistic entry was already recorded.
func TestRMOUpdatesMode(t *testing.T) {
	s := New(Config{Character: "Vix", Broker: broker.New(), Store: memstore.New()})
	if err := s.handle(jsonFrame("JCH", `{"channel":"Frontpage","title":"Frontpage","mode":"both","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("JCH: %v", err)
	}
	if err := s.handle(jsonFrame("RMO", `{"channel":"frontpage","mode":"ads"}`)); err != nil {
		t.Fatalf("RMO: %v", err)
	}
	cs := s.st.convs[convKey(model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"})]
	if cs == nil {
		t.Fatal("conversation missing after RMO")
	}
	if cs.mode != "ads" {
		t.Fatalf("mode = %q, want ads", cs.mode)
	}
}

// TestChannelOpDeltas: COL is the full op list (set-to), and COA/COR are the
// documented incremental updates. A COR that removes the last op must clear it,
// not leave a stale entry.
func TestChannelOpDeltas(t *testing.T) {
	s := New(Config{Character: "Vix"})
	if err := s.handle(jsonFrame("JCH", `{"channel":"Frontpage","title":"Frontpage","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("JCH: %v", err)
	}
	if err := s.handle(jsonFrame("COL", `{"channel":"Frontpage","oplist":["Vix"]}`)); err != nil {
		t.Fatalf("COL: %v", err)
	}
	if err := s.handle(jsonFrame("COA", `{"channel":"Frontpage","character":"Kira"}`)); err != nil {
		t.Fatalf("COA: %v", err)
	}
	cs := s.st.convs[convKey(model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"})]
	if !cs.ops[nameKey("Vix")] || !cs.ops[nameKey("Kira")] {
		t.Fatalf("ops after COA = %v, want Vix and Kira", cs.ops)
	}
	if err := s.handle(jsonFrame("COR", `{"channel":"Frontpage","character":"Kira"}`)); err != nil {
		t.Fatalf("COR: %v", err)
	}
	if cs.ops[nameKey("Kira")] {
		t.Fatalf("COR left Kira as op: %v", cs.ops)
	}
	// An empty COL replaces the set entirely.
	if err := s.handle(jsonFrame("COL", `{"channel":"Frontpage","oplist":[]}`)); err != nil {
		t.Fatalf("COL: %v", err)
	}
	if len(cs.ops) != 0 {
		t.Fatalf("empty COL left ops: %v", cs.ops)
	}
}

// TestFramesForUnjoinedChannelIgnored: channel-scoped frames for a channel the
// session is not in are dropped, not applied and hidden (docs/fchat.md). No
// conversation state may be created for them.
func TestFramesForUnjoinedChannelIgnored(t *testing.T) {
	s := New(Config{Character: "Vix"})
	for _, f := range []fchat.Frame{
		jsonFrame("ICH", `{"channel":"Frontpage","users":["Kira"]}`),
		jsonFrame("CDS", `{"channel":"Frontpage","description":"x"}`),
		jsonFrame("COL", `{"channel":"Frontpage","oplist":["Kira"]}`),
		jsonFrame("COA", `{"channel":"Frontpage","character":"Kira"}`),
		jsonFrame("COR", `{"channel":"Frontpage","character":"Kira"}`),
		jsonFrame("RMO", `{"channel":"Frontpage","mode":"ads"}`),
		jsonFrame("MSG", `{"channel":"Frontpage","character":"Kira","message":"hi"}`),
	} {
		if err := s.handle(f); err != nil {
			t.Fatalf("%s: %v", f.Code, err)
		}
	}
	if len(s.st.convs) != 0 {
		t.Fatalf("unjoined channel frames created state: %+v", s.st.convs)
	}
}

// TestOutOfOrderICHBeforeJCH: an ICH that trails the join request but precedes
// self JCH must keep its roster. The joining state, not a bare joined flag,
// makes that possible; the channel stays non-live until JCH confirms.
func TestOutOfOrderICHBeforeJCH(t *testing.T) {
	s := New(Config{Character: "Vix"})
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	// Simulate the state OpJoin establishes before any reply.
	s.ensureConv(conv).membership = memJoining

	if err := s.handle(jsonFrame("ICH", `{"channel":"Frontpage","users":["Kira","Vix"]}`)); err != nil {
		t.Fatalf("ICH: %v", err)
	}
	cs := s.st.convs[convKey(conv)]
	if !cs.members[nameKey("Kira")] {
		t.Fatal("ICH before JCH dropped its roster")
	}
	if cs.live() {
		t.Fatal("channel went live before self JCH")
	}
	if err := s.handle(jsonFrame("JCH", `{"channel":"Frontpage","title":"Frontpage","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("JCH: %v", err)
	}
	if !cs.live() {
		t.Fatal("self JCH did not confirm the join")
	}
}

// TestFLNSkipsUnjoinedConvs: the implied global leave only touches conversations
// we are in (or joining); a left/unknown conversation is not mutated.
func TestFLNSkipsUnjoinedConvs(t *testing.T) {
	s := New(Config{Character: "Vix"})

	joined := s.ensureConv(model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"})
	joined.membership = memJoined
	joined.members[nameKey("Kira")] = true

	joining := s.ensureConv(model.ConvRef{Kind: model.ConvOfficial, ID: "Joining"})
	joining.membership = memJoining
	joining.members[nameKey("Kira")] = true

	left := s.ensureConv(model.ConvRef{Kind: model.ConvOfficial, ID: "OldChannel"})
	left.membership = memLeft
	left.members[nameKey("Kira")] = true

	s.removeFromAllConvs("Kira")

	if joined.members[nameKey("Kira")] {
		t.Fatal("FLN did not sweep a joined conversation")
	}
	if joining.members[nameKey("Kira")] {
		t.Fatal("FLN did not sweep a joining conversation")
	}
	if !left.members[nameKey("Kira")] {
		t.Fatal("FLN mutated a conversation we are not in")
	}
}
