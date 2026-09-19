package session

import (
	"html"
	"sort"
	"strings"

	"plexo/internal/fchat"
	"plexo/internal/model"
)

// maxRoomTitleLen mirrors fserv's MAX_TITLE_LEN for a private room title. The
// server escapes HTML before testing the length, so the check here counts the
// escaped form.
const maxRoomTitleLen = 64

// handleRoomAdmin handles model.OpRoomAdmin: create a room or perform one
// administrative action on an existing room. The F-Chat server remains the
// authority; this validates the request shape and enum values for a fast local
// rejection, and for the verbs with no server broadcast (unban, visibility) it
// applies the local change once the frame is on the wire.
//
// The whole room-management surface is deliberately one op carrying a
// RoomAdminRequest, matching the set_ignore action-discriminator precedent, so
// the always-on command catalog stays small.
func (s *Session) handleRoomAdmin(cmd model.Command) model.Result {
	accept := func() model.Result { return model.Result{CID: cmd.CID, Accepted: true} }
	reject := func(code, msg string) model.Result {
		return model.Result{CID: cmd.CID, Accepted: false, ErrorCode: code, ErrorMsg: msg}
	}
	if cmd.Room == nil {
		return reject("missing_action", "room action is required")
	}
	a := cmd.Room

	if a.Action == "create" {
		title := strings.TrimSpace(a.Title)
		if title == "" {
			return reject("empty_title", "room title is empty")
		}
		if len(html.EscapeString(title)) > maxRoomTitleLen {
			return reject("title_too_long", "room title is too long")
		}
		// CCR creates a closed, invite-only room and force-joins us; the new
		// room's hash id arrives with the server's self JCH, which the normal
		// inbound path turns into a joined conversation.
		if err := s.queue("CCR", fchat.ChannelRef{Channel: title}); err != nil {
			return reject("room_failed", err.Error())
		}
		return accept()
	}

	if cmd.Conv.ID == "" {
		return reject("missing_conv", "room is required")
	}
	// All non-create actions address a room this session is already in. The
	// lookup is by the caller's ref, but the frame carries the canonical id.
	cs, ok := s.st.convs[convKey(cmd.Conv)]
	if !ok || !cs.inChannel() {
		return reject("bad_conv", "not in that room")
	}
	id := cs.ref.ID

	switch a.Action {
	case "destroy":
		if err := s.queueAck("KIC", fchat.ChannelRef{Channel: id}, func() {
			// A destroyed public room must leave the core-wide catalog; if it was
			// not public the removal is a harmless no-op.
			s.clearCatalogRoom(cs)
		}); err != nil {
			return reject("room_failed", err.Error())
		}
		return accept()
	case "describe":
		if s.st.vars.CdsMax > 0 && len(a.Description) > s.st.vars.CdsMax {
			return reject("too_long", "description exceeds server limit")
		}
		if err := s.queue("CDS", fchat.ChannelDescription{Channel: id, Description: a.Description}); err != nil {
			return reject("room_failed", err.Error())
		}
		return accept()
	case "mode":
		mode := strings.ToLower(strings.TrimSpace(a.Mode))
		switch mode {
		case "both", "chat", "ads":
		default:
			return reject("bad_mode", "mode must be both, chat, or ads")
		}
		if err := s.queue("RMO", fchat.RoomMode{Channel: id, Mode: mode}); err != nil {
			return reject("room_failed", err.Error())
		}
		return accept()
	case "visibility":
		status := strings.ToLower(strings.TrimSpace(a.Visibility))
		switch status {
		case "public", "private":
		default:
			return reject("bad_visibility", "visibility must be public or private")
		}
		// RST has no server broadcast (only a SYS to the requester), so the
		// published state is applied optimistically once the frame is written and
		// the catalog updated locally; the next ORS corrects any drift.
		if err := s.queueAck("RST", fchat.RoomPublic{Channel: id, Status: status}, func() {
			if status == "public" {
				cs.visibility = visPublic
				s.publishCatalogRoom(cs)
			} else {
				cs.visibility = visPrivate
				s.clearCatalogRoom(cs)
			}
		}); err != nil {
			return reject("room_failed", err.Error())
		}
		return accept()
	case "set_owner":
		character := strings.TrimSpace(a.Character)
		if character == "" {
			return reject("missing_character", "character is required")
		}
		// CSO is the only ownership transfer. The target must be online; the
		// server rejects an unknown one, which surfaces as an error event.
		if err := s.queue("CSO", fchat.ChannelCharacter{Channel: id, Character: character}); err != nil {
			return reject("room_failed", err.Error())
		}
		return accept()
	case "invite":
		character := strings.TrimSpace(a.Character)
		if character == "" {
			return reject("missing_character", "character is required")
		}
		// CIU grants access and notifies the target; it never force-joins them.
		if err := s.queue("CIU", fchat.ChannelCharacter{Channel: id, Character: character}); err != nil {
			return reject("room_failed", err.Error())
		}
		return accept()
	case "timeout":
		character := strings.TrimSpace(a.Character)
		if character == "" {
			return reject("missing_character", "character is required")
		}
		if a.Length < 1 {
			return reject("bad_timeout", "timeout length must be at least one minute")
		}
		if err := s.queue("CTU", fchat.RoomTimeout{Channel: id, Character: character, Length: a.Length}); err != nil {
			return reject("room_failed", err.Error())
		}
		return accept()
	case "add_mod", "remove_mod", "kick", "ban", "unban":
		character := strings.TrimSpace(a.Character)
		if character == "" {
			return reject("missing_character", "character is required")
		}
		frame := map[string]string{
			"add_mod":    "COA",
			"remove_mod": "COR",
			"kick":       "CKU",
			"ban":        "CBU",
			"unban":      "CUB",
		}[a.Action]
		payload := fchat.ChannelCharacter{Channel: id, Character: character}
		// CUB has no server broadcast (its reply is a SYS only the caller sees),
		// so the local ban record must be dropped when the frame is actually
		// written. The other verbs are echoed to the room and applied there.
		if a.Action == "unban" {
			if err := s.queueAck(frame, payload, func() { delete(cs.admin.bans, nameKey(character)) }); err != nil {
				return reject("room_failed", err.Error())
			}
			return accept()
		}
		if err := s.queue(frame, payload); err != nil {
			return reject("room_failed", err.Error())
		}
		return accept()
	default:
		return reject("bad_action", "unknown room action")
	}
}

// publishCatalogRoom reflects a room this session just published in the
// core-wide catalog, so the open-rooms list updates immediately instead of at
// the next ORS. The member count is the one this session knows; the next full
// refresh corrects any drift.
func (s *Session) publishCatalogRoom(cs *convState) {
	if s.cfg.OnRoom == nil || cs.ref.Kind != model.ConvRoom {
		return
	}
	s.cfg.OnRoom(s.cfg.Character, model.PublicRoom{
		Name:       cs.ref.ID,
		Title:      cs.title,
		Characters: len(cs.members),
	}, true)
}

// clearCatalogRoom drops a room this session knows to be public from the
// core-wide catalog (RST private or destroy). Removing a room that was never
// catalogued is a no-op in the manager.
func (s *Session) clearCatalogRoom(cs *convState) {
	if s.cfg.OnRoom == nil || cs.ref.Kind != model.ConvRoom {
		return
	}
	s.cfg.OnRoom(s.cfg.Character, model.PublicRoom{Name: cs.ref.ID}, false)
}

// applyCOL records a room's op list. The protocol documents the first entry as
// the channel owner and permits it to be empty; the remaining entries are
// ordinary mods. It replaces the op set in one shot.
func (s *Session) applyCOL(cs *convState, oplist []string) {
	if len(oplist) > 0 {
		cs.admin.owner = oplist[0]
		cs.admin.ownerKey = nameKey(oplist[0])
		oplist = oplist[1:]
	}
	cs.ops = make(map[string]bool, len(oplist))
	for _, o := range oplist {
		if o == "" {
			continue
		}
		s.touch(o)
		cs.ops[nameKey(o)] = true
	}
}

// applyCSO records an owner change. The server follows it with a fresh COL, but
// tracking it immediately keeps the role current between the two frames and
// keeps the outgoing owner out of the mod set.
func (s *Session) applyCSO(cs *convState, owner string) {
	cs.admin.owner = owner
	cs.admin.ownerKey = nameKey(owner)
	if owner != "" {
		s.touch(owner)
	}
	delete(cs.ops, nameKey(owner))
}

// applyRoomBan records a ban or timeout observed from the room. banner is the
// operator; a zero expiry is permanent.
func (s *Session) applyRoomBan(cs *convState, name, banner string, expiresAtMs int64) {
	if name == "" {
		return
	}
	if cs.admin.bans == nil {
		cs.admin.bans = map[string]roomBan{}
	}
	cs.admin.bans[nameKey(name)] = roomBan{name: name, banner: banner, expiresAtMs: expiresAtMs}
}

// selfRole returns the session's room-scoped authority. Global-moderator status
// is deliberately excluded: it is a property of the character, not the room,
// and is already delivered as presence.admin.
func (s *Session) selfRole(cs *convState) model.RoomRole {
	switch cs.ref.Kind {
	case model.ConvOfficial, model.ConvRoom:
	default:
		return ""
	}
	switch {
	case cs.admin.ownerKey != "" && cs.admin.ownerKey == s.st.selfKey:
		return model.RoomRoleOwner
	case cs.ops[s.st.selfKey]:
		return model.RoomRoleMod
	default:
		return model.RoomRoleNone
	}
}

// roomInfoLocked builds the on-demand management view of one conversation. It
// is served over HTTP and never streamed as conversation state. Expired
// timeouts are pruned as they are read.
func (s *Session) roomInfoLocked(ref model.ConvRef) (model.RoomInfo, bool) {
	cs, ok := s.st.convs[convKey(ref)]
	if !ok || !cs.inChannel() {
		return model.RoomInfo{}, false
	}
	now := s.now().UnixMilli()
	bans := make([]model.RoomBan, 0, len(cs.admin.bans))
	for key, b := range cs.admin.bans {
		if b.expiresAtMs != 0 && b.expiresAtMs <= now {
			delete(cs.admin.bans, key)
			continue
		}
		bans = append(bans, model.RoomBan{Name: b.name, Banner: b.banner, ExpiresAtMs: b.expiresAtMs})
	}
	sort.Slice(bans, func(i, j int) bool { return strings.ToLower(bans[i].Name) < strings.ToLower(bans[j].Name) })
	// Visibility is only meaningful for rooms; official channels are always
	// public and carry no RST state.
	visibility := ""
	if cs.ref.Kind == model.ConvRoom {
		switch cs.visibility {
		case visPublic:
			visibility = "public"
		case visPrivate:
			visibility = "private"
		}
	}
	return model.RoomInfo{
		Conv:        cs.ref,
		Title:       cs.title,
		Description: s.delivery.Status(cs.description),
		Mode:        cs.mode,
		Owner:       cs.admin.owner,
		Ops:         s.opList(cs),
		SelfRole:    s.selfRole(cs),
		Bans:        bans,
		CdsMax:      s.st.vars.CdsMax,
		TitleMax:    maxRoomTitleLen,
		Visibility:  visibility,
	}, true
}
