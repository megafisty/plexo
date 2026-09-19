package session

import (
	"context"
	"testing"
	"time"

	"plexo/internal/fchat"
	"plexo/internal/model"
)

// newRoomSession builds a session with a live outbound channel and context so
// queue() does not race an already-closed done().
func newRoomSession(t *testing.T) *Session {
	t.Helper()
	s := New(Config{Character: "Vix"})
	s.out = make(chan outbound, 16)
	s.mu.Lock()
	s.ctx = context.Background()
	s.mu.Unlock()
	return s
}

// joinRoomTest drives a self JCH so the session is joined to a room.
func joinRoomTest(t *testing.T, s *Session, id, title string) {
	t.Helper()
	if err := s.handle(jsonFrame("JCH", `{"channel":"`+id+`","title":"`+title+`","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("JCH: %v", err)
	}
}

func roomConv(id string) model.ConvRef { return model.ConvRef{Kind: model.ConvRoom, ID: id} }

func nextFrame(t *testing.T, s *Session) outbound {
	t.Helper()
	select {
	case ob := <-s.out:
		return ob
	case <-time.After(time.Second):
		t.Fatal("no frame queued")
		return outbound{}
	}
}

// TestRoomAdminCreateQueuesCCR: create validates the title and maps to CCR.
func TestRoomAdminCreateQueuesCCR(t *testing.T) {
	s := newRoomSession(t)
	res := s.handleCommand(model.Command{
		Op:   model.OpRoomAdmin,
		Room: &model.RoomAdminRequest{Action: "create", Title: "My Room"},
	})
	if !res.Accepted {
		t.Fatalf("create rejected: %+v", res)
	}
	ob := nextFrame(t, s)
	if ob.wire.Code != "CCR" {
		t.Fatalf("frame = %s, want CCR", ob.wire.Code)
	}
	p, err := fchat.Decode[fchat.ChannelRef](ob.wire)
	if err != nil {
		t.Fatalf("decode CCR: %v", err)
	}
	if p.Channel != "My Room" {
		t.Fatalf("CCR channel = %q, want the title", p.Channel)
	}

	if res := s.handleCommand(model.Command{Op: model.OpRoomAdmin, Room: &model.RoomAdminRequest{Action: "create"}}); res.ErrorCode != "empty_title" {
		t.Fatalf("empty title code = %q, want empty_title", res.ErrorCode)
	}
	long := make([]byte, maxRoomTitleLen+1)
	for i := range long {
		long[i] = 'x'
	}
	if res := s.handleCommand(model.Command{Op: model.OpRoomAdmin, Room: &model.RoomAdminRequest{Action: "create", Title: string(long)}}); res.ErrorCode != "title_too_long" {
		t.Fatalf("long title code = %q, want title_too_long", res.ErrorCode)
	}
}

// TestRoomAdminDescribeQueuesCDS: describe maps to CDS and enforces cds_max.
func TestRoomAdminDescribeQueuesCDS(t *testing.T) {
	s := newRoomSession(t)
	s.st.vars.CdsMax = 10
	joinRoomTest(t, s, "ADH-abc", "Secret")

	res := s.handleCommand(model.Command{
		Op:   model.OpRoomAdmin,
		Conv: roomConv("ADH-abc"),
		Room: &model.RoomAdminRequest{Action: "describe", Description: "hi"},
	})
	if !res.Accepted {
		t.Fatalf("describe rejected: %+v", res)
	}
	ob := nextFrame(t, s)
	if ob.wire.Code != "CDS" {
		t.Fatalf("frame = %s, want CDS", ob.wire.Code)
	}
	p, _ := fchat.Decode[fchat.ChannelDescription](ob.wire)
	if p.Channel != "ADH-abc" || p.Description != "hi" {
		t.Fatalf("CDS payload = %+v", p)
	}

	res = s.handleCommand(model.Command{
		Op:   model.OpRoomAdmin,
		Conv: roomConv("ADH-abc"),
		Room: &model.RoomAdminRequest{Action: "describe", Description: "01234567890"},
	})
	if res.ErrorCode != "too_long" {
		t.Fatalf("over-long description code = %q, want too_long", res.ErrorCode)
	}
}

// TestRoomAdminModerationFrames: add_mod/remove_mod/kick/ban map to COA/COR/
// CKU/CBU with the canonical room id, and reject an action outside a joined
// room.
func TestRoomAdminModerationFrames(t *testing.T) {
	cases := map[string]string{
		"add_mod":    "COA",
		"remove_mod": "COR",
		"kick":       "CKU",
		"ban":        "CBU",
	}
	for action, code := range cases {
		s := newRoomSession(t)
		joinRoomTest(t, s, "ADH-abc", "Secret")
		res := s.handleCommand(model.Command{
			Op:   model.OpRoomAdmin,
			Conv: roomConv("ADH-abc"),
			Room: &model.RoomAdminRequest{Action: action, Character: "Bob"},
		})
		if !res.Accepted {
			t.Fatalf("%s rejected: %+v", action, res)
		}
		ob := nextFrame(t, s)
		if ob.wire.Code != code {
			t.Fatalf("%s frame = %s, want %s", action, ob.wire.Code, code)
		}
		p, _ := fchat.Decode[fchat.ChannelCharacter](ob.wire)
		if p.Channel != "ADH-abc" || p.Character != "Bob" {
			t.Fatalf("%s payload = %+v", action, p)
		}
	}

	s := newRoomSession(t)
	if res := s.handleCommand(model.Command{
		Op:   model.OpRoomAdmin,
		Conv: roomConv("ADH-nope"),
		Room: &model.RoomAdminRequest{Action: "kick", Character: "Bob"},
	}); res.ErrorCode != "bad_conv" {
		t.Fatalf("not-in-room code = %q, want bad_conv", res.ErrorCode)
	}
	if res := s.handleCommand(model.Command{
		Op:   model.OpRoomAdmin,
		Conv: roomConv("ADH-nope"),
		Room: &model.RoomAdminRequest{Action: "bogus"},
	}); res.ErrorCode != "bad_conv" {
		// bad_conv wins because the room check precedes the action switch.
		t.Fatalf("code = %q, want bad_conv", res.ErrorCode)
	}
}

// TestRoomAdminUnbanAppliesLocalRemoval: CUB has no server broadcast, so the
// local ban record is dropped only once the frame is on the wire.
func TestRoomAdminUnbanAppliesLocalRemoval(t *testing.T) {
	s := newRoomSession(t)
	joinRoomTest(t, s, "ADH-abc", "Secret")
	if err := s.handle(jsonFrame("CBU", `{"channel":"ADH-abc","character":"Bob","operator":"Vix"}`)); err != nil {
		t.Fatalf("CBU: %v", err)
	}
	cs := s.st.convs[convKey(roomConv("ADH-abc"))]
	if _, ok := cs.admin.bans["bob"]; !ok {
		t.Fatalf("ban not recorded: %+v", cs.admin.bans)
	}

	res := s.handleCommand(model.Command{
		Op:   model.OpRoomAdmin,
		Conv: roomConv("ADH-abc"),
		Room: &model.RoomAdminRequest{Action: "unban", Character: "Bob"},
	})
	if !res.Accepted {
		t.Fatalf("unban rejected: %+v", res)
	}
	ob := nextFrame(t, s)
	if ob.wire.Code != "CUB" {
		t.Fatalf("frame = %s, want CUB", ob.wire.Code)
	}
	if _, ok := cs.admin.bans["bob"]; !ok {
		t.Fatal("ban removed before the frame was written")
	}
	if ob.onSent != nil {
		ob.onSent()
	}
	if _, ok := cs.admin.bans["bob"]; ok {
		t.Fatal("ban not removed after onSent")
	}
}

// TestCOLOwnerAndRole: the first COL entry is the owner; the rest are mods, and
// the derived self role distinguishes owner from mod from none.
func TestCOLOwnerAndRole(t *testing.T) {
	s := newRoomSession(t)
	joinRoomTest(t, s, "ADH-abc", "Secret")
	if err := s.handle(jsonFrame("COL", `{"channel":"ADH-abc","oplist":["Kira","Vix"]}`)); err != nil {
		t.Fatalf("COL: %v", err)
	}
	cs := s.st.convs[convKey(roomConv("ADH-abc"))]
	if cs.admin.owner != "Kira" || cs.admin.ownerKey != "kira" {
		t.Fatalf("owner = %q/%q, want Kira/kira", cs.admin.owner, cs.admin.ownerKey)
	}
	if !cs.ops["vix"] || cs.ops["kira"] {
		t.Fatalf("ops = %+v, want only vix", cs.ops)
	}
	if got := s.selfRole(cs); got != model.RoomRoleMod {
		t.Fatalf("selfRole = %q, want mod", got)
	}

	if err := s.handle(jsonFrame("COL", `{"channel":"ADH-abc","oplist":["Vix",""]}`)); err != nil {
		t.Fatalf("COL owner: %v", err)
	}
	if got := s.selfRole(cs); got != model.RoomRoleOwner {
		t.Fatalf("selfRole after ownership = %q, want owner", got)
	}

	if err := s.handle(jsonFrame("COL", `{"channel":"ADH-abc","oplist":["Kira"]}`)); err != nil {
		t.Fatalf("COL none: %v", err)
	}
	if got := s.selfRole(cs); got != model.RoomRoleNone {
		t.Fatalf("selfRole none = %q, want none", got)
	}
}

// TestCSOSetsOwner: a CSO frame transfers ownership immediately, before the
// following COL.
func TestCSOSetsOwner(t *testing.T) {
	s := newRoomSession(t)
	joinRoomTest(t, s, "ADH-abc", "Secret")
	if err := s.handle(jsonFrame("CSO", `{"channel":"ADH-abc","character":"Vix"}`)); err != nil {
		t.Fatalf("CSO: %v", err)
	}
	cs := s.st.convs[convKey(roomConv("ADH-abc"))]
	if cs.admin.owner != "Vix" {
		t.Fatalf("owner = %q, want Vix", cs.admin.owner)
	}
	if got := s.selfRole(cs); got != model.RoomRoleOwner {
		t.Fatalf("selfRole = %q, want owner", got)
	}
}

// TestRoomBanRecordingAndPruning: CBU records a permanent ban, CTU a timed one,
// and the on-demand read prunes expired timeouts.
func TestRoomBanRecordingAndPruning(t *testing.T) {
	s := newRoomSession(t)
	joinRoomTest(t, s, "ADH-abc", "Secret")
	if err := s.handle(jsonFrame("CBU", `{"channel":"ADH-abc","character":"Bob","operator":"Vix"}`)); err != nil {
		t.Fatalf("CBU: %v", err)
	}
	if err := s.handle(jsonFrame("CTU", `{"channel":"ADH-abc","character":"Neko","operator":"Vix","length":5}`)); err != nil {
		t.Fatalf("CTU: %v", err)
	}
	cs := s.st.convs[convKey(roomConv("ADH-abc"))]
	if b := cs.admin.bans["bob"]; b.expiresAtMs != 0 || b.banner != "Vix" {
		t.Fatalf("permanent ban = %+v", b)
	}
	if b := cs.admin.bans["neko"]; b.expiresAtMs == 0 {
		t.Fatalf("timeout ban has no expiry: %+v", b)
	}

	// Force the timeout into the past; the read must prune it.
	b := cs.admin.bans["neko"]
	b.expiresAtMs = s.now().UnixMilli() - 1
	cs.admin.bans["neko"] = b
	info, ok := s.roomInfoLocked(roomConv("ADH-abc"))
	if !ok {
		t.Fatal("roomInfo not found")
	}
	if len(info.Bans) != 1 || info.Bans[0].Name != "Bob" {
		t.Fatalf("bans = %+v, want only Bob", info.Bans)
	}
	if _, ok := cs.admin.bans["neko"]; ok {
		t.Fatal("expired timeout not pruned")
	}
}

// TestRoomAdminBadAction: an unknown action is rejected once the room check
// passes, and a missing action payload is rejected up front.
func TestRoomAdminBadAction(t *testing.T) {
	s := newRoomSession(t)
	joinRoomTest(t, s, "ADH-abc", "Secret")
	if res := s.handleCommand(model.Command{
		Op:   model.OpRoomAdmin,
		Conv: roomConv("ADH-abc"),
		Room: &model.RoomAdminRequest{Action: "bogus"},
	}); res.ErrorCode != "bad_action" {
		t.Fatalf("code = %q, want bad_action", res.ErrorCode)
	}
	if res := s.handleCommand(model.Command{Op: model.OpRoomAdmin, Conv: roomConv("ADH-abc")}); res.ErrorCode != "missing_action" {
		t.Fatalf("code = %q, want missing_action", res.ErrorCode)
	}
}

// TestRoomAdminModeQueuesRMO: mode validates the enum and maps to RMO.
func TestRoomAdminModeQueuesRMO(t *testing.T) {
	s := newRoomSession(t)
	joinRoomTest(t, s, "ADH-abc", "Secret")
	res := s.handleCommand(model.Command{
		Op:   model.OpRoomAdmin,
		Conv: roomConv("ADH-abc"),
		Room: &model.RoomAdminRequest{Action: "mode", Mode: "ads"},
	})
	if !res.Accepted {
		t.Fatalf("mode rejected: %+v", res)
	}
	ob := nextFrame(t, s)
	if ob.wire.Code != "RMO" {
		t.Fatalf("frame = %s, want RMO", ob.wire.Code)
	}
	p, _ := fchat.Decode[fchat.RoomMode](ob.wire)
	if p.Channel != "ADH-abc" || p.Mode != "ads" {
		t.Fatalf("payload = %+v", p)
	}
	if res := s.handleCommand(model.Command{
		Op:   model.OpRoomAdmin,
		Conv: roomConv("ADH-abc"),
		Room: &model.RoomAdminRequest{Action: "mode", Mode: "nope"},
	}); res.ErrorCode != "bad_mode" {
		t.Fatalf("code = %q, want bad_mode", res.ErrorCode)
	}
}

// TestRoomAdminVisibilityUpdatesLocalState: RST has no server broadcast, so the
// published state and the core catalog are updated optimistically once the
// frame is written.
func TestRoomAdminVisibilityUpdatesLocalState(t *testing.T) {
	s := newRoomSession(t)
	joinRoomTest(t, s, "ADH-abc", "Secret")
	var (
		gotRoom    model.PublicRoom
		gotPresent bool
		calls      int
	)
	s.cfg.OnRoom = func(_ string, room model.PublicRoom, present bool) {
		calls++
		gotRoom = room
		gotPresent = present
	}
	res := s.handleCommand(model.Command{
		Op:   model.OpRoomAdmin,
		Conv: roomConv("ADH-abc"),
		Room: &model.RoomAdminRequest{Action: "visibility", Visibility: "public"},
	})
	if !res.Accepted {
		t.Fatalf("visibility rejected: %+v", res)
	}
	ob := nextFrame(t, s)
	if ob.wire.Code != "RST" {
		t.Fatalf("frame = %s, want RST", ob.wire.Code)
	}
	p, _ := fchat.Decode[fchat.RoomPublic](ob.wire)
	if p.Channel != "ADH-abc" || p.Status != "public" {
		t.Fatalf("payload = %+v", p)
	}
	cs := s.st.convs[convKey(roomConv("ADH-abc"))]
	if cs.visibility != visUnknown {
		t.Fatal("visibility applied before the frame was written")
	}
	ob.onSent()
	if cs.visibility != visPublic {
		t.Fatalf("visibility = %v, want public", cs.visibility)
	}
	if calls != 1 || !gotPresent || gotRoom.Name != "ADH-abc" {
		t.Fatalf("OnRoom = %d/%v/%+v", calls, gotPresent, gotRoom)
	}
	if info, _ := s.roomInfoLocked(roomConv("ADH-abc")); info.Visibility != "public" {
		t.Fatalf("room info visibility = %q, want public", info.Visibility)
	}

	res = s.handleCommand(model.Command{
		Op:   model.OpRoomAdmin,
		Conv: roomConv("ADH-abc"),
		Room: &model.RoomAdminRequest{Action: "visibility", Visibility: "private"},
	})
	if !res.Accepted {
		t.Fatalf("unpublish rejected: %+v", res)
	}
	ob = nextFrame(t, s)
	ob.onSent()
	if cs.visibility != visPrivate {
		t.Fatalf("visibility = %v, want private", cs.visibility)
	}
	if gotPresent {
		t.Fatal("OnRoom reported present=true on unpublish")
	}
	if res := s.handleCommand(model.Command{
		Op:   model.OpRoomAdmin,
		Conv: roomConv("ADH-abc"),
		Room: &model.RoomAdminRequest{Action: "visibility", Visibility: "sideways"},
	}); res.ErrorCode != "bad_visibility" {
		t.Fatalf("code = %q, want bad_visibility", res.ErrorCode)
	}
}

// TestRoomAdminOwnerInviteTimeoutFrames: set_owner/invite map to CSO/CIU, and
// timeout to CTU with the length in minutes.
func TestRoomAdminOwnerInviteTimeoutFrames(t *testing.T) {
	for _, tc := range []struct {
		action string
		code   string
	}{
		{"set_owner", "CSO"},
		{"invite", "CIU"},
	} {
		s := newRoomSession(t)
		joinRoomTest(t, s, "ADH-abc", "Secret")
		res := s.handleCommand(model.Command{
			Op:   model.OpRoomAdmin,
			Conv: roomConv("ADH-abc"),
			Room: &model.RoomAdminRequest{Action: tc.action, Character: "Kira"},
		})
		if !res.Accepted {
			t.Fatalf("%s rejected: %+v", tc.action, res)
		}
		ob := nextFrame(t, s)
		if ob.wire.Code != tc.code {
			t.Fatalf("%s frame = %s, want %s", tc.action, ob.wire.Code, tc.code)
		}
		p, _ := fchat.Decode[fchat.ChannelCharacter](ob.wire)
		if p.Channel != "ADH-abc" || p.Character != "Kira" {
			t.Fatalf("%s payload = %+v", tc.action, p)
		}
	}

	s := newRoomSession(t)
	joinRoomTest(t, s, "ADH-abc", "Secret")
	res := s.handleCommand(model.Command{
		Op:   model.OpRoomAdmin,
		Conv: roomConv("ADH-abc"),
		Room: &model.RoomAdminRequest{Action: "timeout", Character: "Kira", Length: 5},
	})
	if !res.Accepted {
		t.Fatalf("timeout rejected: %+v", res)
	}
	ob := nextFrame(t, s)
	if ob.wire.Code != "CTU" {
		t.Fatalf("frame = %s, want CTU", ob.wire.Code)
	}
	p, _ := fchat.Decode[fchat.RoomTimeout](ob.wire)
	if p.Channel != "ADH-abc" || p.Character != "Kira" || p.Length != 5 {
		t.Fatalf("payload = %+v", p)
	}
	if res := s.handleCommand(model.Command{
		Op:   model.OpRoomAdmin,
		Conv: roomConv("ADH-abc"),
		Room: &model.RoomAdminRequest{Action: "timeout", Character: "Kira"},
	}); res.ErrorCode != "bad_timeout" {
		t.Fatalf("code = %q, want bad_timeout", res.ErrorCode)
	}
}

// TestCIURecordsAndDismissesInvite: an inbound CIU becomes a session-scoped
// invite, is seeded into the snapshot, and is dropped by dismiss_invite.
func TestCIURecordsAndDismissesInvite(t *testing.T) {
	s := newRoomSession(t)
	if err := s.handle(jsonFrame("CIU", `{"sender":"Kira","title":"Secret Lair","name":"ADH-secret"}`)); err != nil {
		t.Fatalf("CIU: %v", err)
	}
	list := s.inviteListLocked()
	if len(list) != 1 || list[0].Conv.ID != "ADH-secret" || list[0].Title != "Secret Lair" || list[0].InvitedBy != "Kira" {
		t.Fatalf("invites = %+v", list)
	}
	if p := s.st.roster["kira"]; p.Character != "Kira" {
		t.Fatalf("inviter not touched: %+v", p)
	}
	if snap := s.snapshotLocked(); len(snap.Invites) != 1 {
		t.Fatalf("snapshot invites = %+v", snap.Invites)
	}

	res := s.handleCommand(model.Command{CID: "d", Op: model.OpDismissInvite, Conv: roomConv("ADH-secret")})
	if !res.Accepted {
		t.Fatalf("dismiss rejected: %+v", res)
	}
	if len(s.inviteListLocked()) != 0 {
		t.Fatal("invite not dismissed")
	}
	if res := s.handleCommand(model.Command{Op: model.OpDismissInvite, Conv: roomConv("ADH-secret")}); !res.Accepted {
		t.Fatalf("repeat dismiss rejected: %+v", res)
	}
	if res := s.handleCommand(model.Command{Op: model.OpDismissInvite}); res.ErrorCode != "missing_conv" {
		t.Fatalf("code = %q, want missing_conv", res.ErrorCode)
	}
}

// TestSelfJoinClearsInvite: accepting an invitation (the self JCH) retires it.
func TestSelfJoinClearsInvite(t *testing.T) {
	s := newRoomSession(t)
	if err := s.handle(jsonFrame("CIU", `{"sender":"Kira","title":"Secret Lair","name":"ADH-secret"}`)); err != nil {
		t.Fatalf("CIU: %v", err)
	}
	joinRoomTest(t, s, "ADH-secret", "Secret Lair")
	if len(s.inviteListLocked()) != 0 {
		t.Fatal("invite not cleared on join")
	}
}

// TestRoomInfoProjectsRoleAndLimits: the on-demand read reports the owner, ops,
// role, and limits without streaming them.
func TestRoomInfoProjectsRoleAndLimits(t *testing.T) {
	s := newRoomSession(t)
	s.st.vars.CdsMax = 50000
	joinRoomTest(t, s, "ADH-abc", "Secret")
	if err := s.handle(jsonFrame("COL", `{"channel":"ADH-abc","oplist":["Vix","Kira"]}`)); err != nil {
		t.Fatalf("COL: %v", err)
	}
	info, ok := s.roomInfoLocked(roomConv("ADH-abc"))
	if !ok {
		t.Fatal("roomInfo not found")
	}
	if info.Owner != "Vix" || info.SelfRole != model.RoomRoleOwner {
		t.Fatalf("owner/role = %q/%q, want Vix/owner", info.Owner, info.SelfRole)
	}
	if info.CdsMax != 50000 || info.TitleMax != maxRoomTitleLen {
		t.Fatalf("limits = %d/%d", info.CdsMax, info.TitleMax)
	}
	if info.Ops == nil || info.Bans == nil {
		t.Fatalf("ops/bans must be non-nil set-to lists: %+v", info)
	}
}
