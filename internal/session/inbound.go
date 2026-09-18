package session

import (
	"context"
	"encoding/json"
	"strings"

	"plexo/internal/fchat"
	"plexo/internal/model"
)

// --- inbound protocol handling ---

func (s *Session) handle(cmd fchat.Frame) error {
	switch cmd.Code {
	case "IDN":
		p, err := fchat.Decode[fchat.IDNEvent](cmd)
		if err != nil {
			return &protocolError{"IDN: " + err.Error()}
		}
		if !strings.EqualFold(p.Character, s.cfg.Character) {
			return &protocolError{"IDN character mismatch"}
		}
		s.st.phase = "identified"
	case "HLO":
		p, err := fchat.Decode[fchat.HLOEvent](cmd)
		if err == nil {
			s.log().Debug("server hello", "character", s.cfg.Character, "message", p.Message)
		}
	case "PIN":
		// The server sends an argument-less PIN and requires an argument-less
		// PIN back to keep the connection alive; three missed responses (90s)
		// disconnect us. Reply exactly once per received ping.
		if err := s.queue("PIN", nil); err != nil {
			s.log().Debug("ping response failed", "character", s.cfg.Character, "err", err)
		}
		// PIN doubles as the periodic tick for core-wide catalog refreshes.
		if s.cfg.OnStale != nil {
			s.cfg.OnStale(s)
		}
	case "NLN":
		p, err := fchat.Decode[fchat.NLNEvent](cmd)
		if err != nil {
			return &protocolError{"NLN: " + err.Error()}
		}
		if strings.EqualFold(p.Identity, s.cfg.Character) {
			s.setPresence(p.Identity, p.Gender, p.Status, "")
			if s.st.phase == "identified" || s.st.phase == "idn_sent" {
				s.st.phase = "ready"
				s.emitSessionState("live", "", "", false)
				// First chance after login to refresh core-wide catalogs; PINs
				// may not arrive for a while.
				if s.cfg.OnStale != nil {
					s.cfg.OnStale(s)
				}
				// Best-effort rejoin of the character's configured channels
				// and rooms.
				s.autoJoin()
			}
			return nil
		}
		s.setPresence(p.Identity, p.Gender, p.Status, "")
	case "FLN":
		p, err := fchat.Decode[fchat.FLNEvent](cmd)
		if err != nil {
			return &protocolError{"FLN: " + err.Error()}
		}
		if strings.EqualFold(p.Character, s.cfg.Character) {
			// The server never sends our own FLN on a live connection (a takeover
			// arrives as ERR 31). The only source is the previous connection's
			// teardown racing a reconnect, so ignore it rather than marking
			// ourselves offline and emitting a false presence change.
			break
		}
		// FLN is a global LCH: the character left every channel at once.
		s.markOffline(p.Character)
		s.removeFromAllConvs(p.Character)
		// A gone character cannot be typing; retire any pending indicator, since
		// no clear TPN will follow.
		s.clearTypingFor(p.Character)
	case "STA":
		p, err := fchat.Decode[fchat.STAEvent](cmd)
		if err != nil {
			return &protocolError{"STA: " + err.Error()}
		}
		s.setPresence(p.Character, "", p.Status, p.StatusMsg)
	case "CON":
		// Roster snapshot complete; nothing further to do yet.
	case "LIS":
		p, err := fchat.Decode[fchat.LISEvent](cmd)
		if err != nil {
			return &protocolError{"LIS: " + err.Error()}
		}
		var touchedFriends []string
		for _, row := range p.Characters {
			if len(row) < 4 {
				continue
			}
			s.setPresenceQuiet(row[0], row[1], row[2], row[3])
			// LIS is the authoritative roster and hydrates quietly. The friends
			// set itself is unchanged by a roster refresh, so only the affected
			// friends' presence streams: the friends event is de-duplicated by
			// name set and would not carry a presence-only change.
			if s.st.friends[nameKey(row[0])] {
				touchedFriends = append(touchedFriends, row[0])
			}
		}
		for _, name := range touchedFriends {
			s.emitPresence(s.touch(name))
		}
	case "ADL":
		p, err := fchat.Decode[fchat.ADLEvent](cmd)
		if err != nil {
			return &protocolError{"ADL: " + err.Error()}
		}
		// ADL is the full global-moderator list, so it replaces the set: a
		// moderator dropped from the list must not stay flagged. AOP/DOP are the
		// documented deltas applied on top of it.
		admins := make(map[string]bool, len(p.Ops))
		for _, op := range p.Ops {
			if op == "" {
				continue
			}
			s.touch(op)
			admins[nameKey(op)] = true
		}
		s.st.admins = admins
	case "AOP":
		p, err := fchat.Decode[fchat.AOPEvent](cmd)
		if err != nil {
			return &protocolError{"AOP: " + err.Error()}
		}
		if p.Character == "" {
			break
		}
		s.touch(p.Character)
		s.st.admins[nameKey(p.Character)] = true
		s.emitPresence(s.touch(p.Character))
	case "DOP":
		p, err := fchat.Decode[fchat.DOPEvent](cmd)
		if err != nil {
			return &protocolError{"DOP: " + err.Error()}
		}
		if p.Character == "" {
			break
		}
		delete(s.st.admins, nameKey(p.Character))
		s.emitPresence(s.touch(p.Character))
	case "FRL":
		p, err := fchat.Decode[fchat.FRLEvent](cmd)
		if err != nil {
			return &protocolError{"FRL: " + err.Error()}
		}
		// FRL is the documented union of the account's bookmarks and friends.
		// Set-to, never merge: a reconnect sends the full list again. The
		// account-wide list syncs once per connection; later changes arrive
		// as a fresh FRL or stream via presence events.
		friends := make(map[string]bool, len(p.Characters))
		for _, f := range p.Characters {
			if f == "" {
				continue
			}
			s.touch(f)
			friends[nameKey(f)] = true
		}
		s.st.friends = friends
		s.emitAccountState(model.AccountKey("friends"), model.FriendsPayload{Friends: s.friendInfosLocked()})
		// The friends event is de-duplicated by name set, so on a reconnect whose
		// set is unchanged it is dropped; stream the friends' presence directly so
		// the refresh still reaches connected clients.
		s.emitFriendPresence()
	case "RTB":
		// Realtime bridge: a bookmark or friendship changed on the website.
		// The FRL union is the account's friends+bookmarks, so both kinds of
		// delta update the same set. Unknown types are ignored per protocol
		// advisories (friend requests are not part of the union).
		p, err := fchat.Decode[fchat.RTBEvent](cmd)
		if err != nil {
			return &protocolError{"RTB: " + err.Error()}
		}
		if p.Name == "" {
			break
		}
		changed := false
		key := nameKey(p.Name)
		switch p.Type {
		case "trackadd", "friendadd":
			if !s.st.friends[key] {
				s.touch(p.Name)
				s.st.friends[key] = true
				changed = true
			}
		case "trackrem", "friendremove":
			if s.st.friends[key] {
				delete(s.st.friends, key)
				changed = true
			}
		default:
			s.log().Debug("ignoring RTB", "type", p.Type, "name", p.Name)
		}
		if changed {
			s.emitAccountState(model.AccountKey("friends"), model.FriendsPayload{Friends: s.friendInfosLocked()})
		}
	case "IGN":
		// The server pushes the full ignore list on login (action "init") and
		// streams add/delete deltas after that. "list" is aliased to "init".
		p, err := fchat.Decode[fchat.IgnoreEvent](cmd)
		if err != nil {
			return &protocolError{"IGN: " + err.Error()}
		}
		changed := false
		switch p.Action {
		case "init", "list":
			next := make(map[string]bool, len(p.Characters))
			for _, name := range p.Characters {
				if name == "" {
					continue
				}
				s.touch(name)
				next[nameKey(name)] = true
			}
			s.st.ignores = next
			changed = true
		case "add":
			key := nameKey(p.Character)
			if p.Character != "" && !s.st.ignores[key] {
				s.touch(p.Character)
				s.st.ignores[key] = true
				changed = true
			}
		case "delete", "remove":
			if s.st.ignores[nameKey(p.Character)] {
				delete(s.st.ignores, nameKey(p.Character))
				changed = true
			}
		}
		if changed {
			s.emitAccountState(model.AccountKey("ignores"), model.IgnoresPayload{Ignores: s.ignoreList()})
		}
	case "FKS":
		// Character search reply. The core keeps no query state, but it caches
		// the enriched result set (latest-wins) so any client can pull it later;
		// subscribers are told only that new results are available. Each name is
		// enriched with the presence the session already holds (LIS hydrates the
		// whole online roster), read-only so transient results do not grow it.
		p, err := fchat.Decode[fchat.FKSEvent](cmd)
		if err != nil {
			return &protocolError{"FKS: " + err.Error()}
		}
		characters := p.Characters
		if characters == nil {
			characters = []string{}
		}
		members := make([]model.MemberInfo, 0, len(characters))
		for _, name := range characters {
			members = append(members, s.delivery.Member(s.lookupMember(name)))
		}
		s.publishSearch(members)
	case "CHA":
		p, err := fchat.Decode[fchat.CHAEvent](cmd)
		if err != nil {
			return &protocolError{"CHA: " + err.Error()}
		}
		if s.cfg.OnCatalog != nil {
			list := make([]model.OfficialChannel, 0, len(p.Channels))
			for _, ch := range p.Channels {
				list = append(list, model.OfficialChannel{Name: ch.Name, Characters: ch.Characters})
			}
			s.cfg.OnCatalog(s.cfg.Character, list, nil)
		}
	case "ORS":
		p, err := fchat.Decode[fchat.ORSEvent](cmd)
		if err != nil {
			return &protocolError{"ORS: " + err.Error()}
		}
		if s.cfg.OnCatalog != nil {
			list := make([]model.PublicRoom, 0, len(p.Channels))
			for _, ch := range p.Channels {
				list = append(list, model.PublicRoom{Name: ch.Name, Title: ch.Title, Characters: ch.Characters})
			}
			s.cfg.OnCatalog(s.cfg.Character, nil, list)
		}
	case "VAR":
		return s.handleVAR(cmd)
	case "JCH":
		p, err := fchat.Decode[fchat.JCHEvent](cmd)
		if err != nil {
			return &protocolError{"JCH: " + err.Error()}
		}
		var cs *convState
		if strings.EqualFold(p.Character.Name, s.cfg.Character) {
			// An unsolicited JCH for our own character is a real join.
			cs = s.ensureConv(convRefForChannel(p.Channel))
			cs.membership = memJoined
		} else if existing, ok := s.channelConv(p.Channel); ok {
			cs = existing
		} else {
			break
		}
		if p.Title != "" {
			cs.title = p.Title
		}
		if p.Mode != "" {
			cs.mode = p.Mode
		}
		added := ""
		if p.Character.Name != "" {
			if s.addMember(cs, p.Character.Name) {
				added = p.Character.Name
			}
		}
		cs.lastActivity = s.now()
		s.emitConversation(cs, "joined")
		// The member list carries names only, so stream the newly added member's
		// presence after the conversation event (which seeds the broker's watch
		// set). New members would otherwise render unknown until their next status
		// change, because LIS hydration is quiet.
		if added != "" {
			s.emitPresence(s.touch(added))
		}
	case "ICH":
		p, err := fchat.Decode[fchat.ICHEvent](cmd)
		if err != nil {
			return &protocolError{"ICH: " + err.Error()}
		}
		cs, ok := s.channelConv(p.Channel)
		if !ok {
			// An ICH that lists us is itself evidence of the join; it can trail
			// the matching self JCH on a reordered stream. Accept the roster, but
			// stay non-live until JCH confirms, so the client is not told the
			// channel exists prematurely.
			if !usersContain(p.Users, s.cfg.Character) {
				break
			}
			cs = s.ensureConv(convRefForChannel(p.Channel))
			cs.membership = memJoining
		}
		if p.Mode != "" {
			cs.mode = p.Mode
		}
		var added []string
		for _, u := range p.Users {
			if s.addMember(cs, u.Name) {
				added = append(added, u.Name)
			}
		}
		s.emitConversation(cs, "updated")
		// Stream the presence of members this frame introduced; see JCH above.
		for _, name := range added {
			s.emitPresence(s.touch(name))
		}
	case "LCH":
		p, err := fchat.Decode[fchat.LCHEvent](cmd)
		if err != nil {
			return &protocolError{"LCH: " + err.Error()}
		}
		if strings.EqualFold(p.Character.Name, s.cfg.Character) {
			cs := s.ensureConv(convRefForChannel(p.Channel))
			cs.membership = memLeft
			// The server stops maintaining the roster the moment we leave, so
			// drop the membership we can no longer trust. A rejoin rehydrates it
			// through ICH (users) and COL (ops); keeping the old names would let
			// a later FLN delete a member and re-announce a conversation we are
			// no longer in. The frame gate already ignores later updates.
			cs.members = map[string]bool{}
			cs.ops = map[string]bool{}
			s.emitConversation(cs, "left")
			break
		}
		cs, ok := s.channelConv(p.Channel)
		if !ok {
			break
		}
		key := nameKey(p.Character.Name)
		delete(cs.members, key)
		delete(cs.ops, key)
		s.emitConversation(cs, "updated")
	case "CDS":
		p, err := fchat.Decode[fchat.CDSEvent](cmd)
		if err != nil {
			return &protocolError{"CDS: " + err.Error()}
		}
		cs, ok := s.channelConv(p.Channel)
		if !ok {
			break
		}
		cs.description = p.Description
		s.emitConversation(cs, "updated")
	case "RMO":
		p, err := fchat.Decode[fchat.RMOEvent](cmd)
		if err != nil {
			return &protocolError{"RMO: " + err.Error()}
		}
		// A moderator changed the channel's message mode. The server does not
		// re-send ICH, so without this the session would keep enforcing the old
		// mode (and let the client compose the wrong kind of message).
		cs, ok := s.channelConv(p.Channel)
		if !ok {
			break
		}
		if p.Mode != "" {
			cs.mode = p.Mode
		}
		s.emitConversation(cs, "updated")
	case "COL":
		p, err := fchat.Decode[fchat.COLEvent](cmd)
		if err != nil {
			return &protocolError{"COL: " + err.Error()}
		}
		cs, ok := s.channelConv(p.Channel)
		if !ok {
			break
		}
		cs.ops = map[string]bool{}
		for _, o := range p.OpList {
			if o != "" {
				s.touch(o)
				cs.ops[nameKey(o)] = true
			}
		}
		s.emitConversation(cs, "updated")
	case "COA":
		p, err := fchat.Decode[fchat.COAEvent](cmd)
		if err != nil {
			return &protocolError{"COA: " + err.Error()}
		}
		if p.Character == "" {
			break
		}
		cs, ok := s.channelConv(p.Channel)
		if !ok {
			break
		}
		s.touch(p.Character)
		cs.ops[nameKey(p.Character)] = true
		s.emitConversation(cs, "updated")
	case "COR":
		p, err := fchat.Decode[fchat.COREvent](cmd)
		if err != nil {
			return &protocolError{"COR: " + err.Error()}
		}
		if p.Character == "" {
			break
		}
		cs, ok := s.channelConv(p.Channel)
		if !ok {
			break
		}
		delete(cs.ops, nameKey(p.Character))
		s.emitConversation(cs, "updated")
	case "MSG":
		p, err := fchat.Decode[fchat.MSGEvent](cmd)
		if err != nil {
			return &protocolError{"MSG: " + err.Error()}
		}
		// Our own outbound channel messages are recorded when sent; the server
		// never echoes them, but a self-authored MSG would duplicate the
		// canonical entry, so drop it.
		if strings.EqualFold(p.Character, s.cfg.Character) {
			break
		}
		// A channel message for a channel we are not in is ignored (docs/fchat.md).
		if _, ok := s.channelConv(p.Channel); !ok {
			break
		}
		s.recordEntry(convRefForChannel(p.Channel), "msg", p.Character, p.Message, nil)
	case "PRI":
		p, err := fchat.Decode[fchat.PRIEvent](cmd)
		if err != nil {
			return &protocolError{"PRI: " + err.Error()}
		}
		// Our own outbound DMs are recorded when sent. The server never echoes
		// them (it excludes the sender), but if one ever were echoed it would
		// duplicate the canonical entry, so drop it.
		if strings.EqualFold(p.Character, s.cfg.Character) {
			break
		}
		s.recordEntry(model.ConvRef{Kind: model.ConvDM, ID: p.Character}, "dm", p.Character, p.Message, nil)
		// A sent private message ends the sender's typing state; the protocol
		// omits the clear TPN after a send.
		s.applyTyping(model.ConvRef{Kind: model.ConvDM, ID: p.Character}, p.Character, false, false)
	case "LRP":
		p, err := fchat.Decode[fchat.LRPEvent](cmd)
		if err != nil {
			return &protocolError{"LRP: " + err.Error()}
		}
		// Render the advertisement's BBCode here, on the actor, so the client
		// never parses it. Message holds the rendered HTML from this point on.
		ad := model.Ad{
			Character:  p.Character,
			Channel:    s.adChannel(p.Channel),
			Message:    s.delivery.Message(p.Message),
			ReceivedAt: s.now(),
		}
		s.ads.add(ad)
	case "RLL":
		p, err := fchat.Decode[fchat.RLLEvent](cmd)
		if err != nil {
			return &protocolError{"RLL: " + err.Error()}
		}
		// A channel roll carries the channel; a DM roll carries the recipient
		// instead and is sent to both parties. Route by recipient, using the
		// same "who is the partner?" rule as TPN: on the target's copy the
		// recipient is us, so the partner is the speaker. Our own roll must be
		// kept (there is no outbound RLL command, so the server echo is the only
		// copy), unlike MSG/PRI which the core records when sent.
		conv := convRefForChannel(p.Channel)
		if p.Recipient != "" {
			partner := p.Recipient
			if strings.EqualFold(partner, s.cfg.Character) {
				partner = p.Character
			}
			conv = model.ConvRef{Kind: model.ConvDM, ID: partner}
		}
		// Persist the whole server payload for fidelity; rendering reads it.
		s.recordEntry(conv, "rll", p.Character, p.Message, cmd.Data)
	case "TPN":
		p, err := fchat.Decode[fchat.TPNEvent](cmd)
		if err != nil {
			return &protocolError{"TPN: " + err.Error()}
		}
		// TPN is private-message-only: the server sends it while a character is
		// composing a DM to us, and it identifies no channel. The signal
		// therefore belongs to that character's DM conversation — never to a
		// channel, even one we both happen to be in.
		if p.Character == "" || strings.EqualFold(p.Character, s.cfg.Character) {
			break
		}
		ref := model.ConvRef{Kind: model.ConvDM, ID: p.Character}
		// Materialize the DM so presence scoping and the conversation list know
		// the partner even before the first message arrives.
		s.ensureConv(ref)
		switch p.Status {
		case "typing":
			s.applyTyping(ref, p.Character, true, false)
		case "paused":
			// Text is waiting to be sent, but no keystrokes are in flight.
			s.applyTyping(ref, p.Character, true, true)
		default: // "clear", or an unknown status: not typing.
			s.applyTyping(ref, p.Character, false, false)
		}
	case "BRO":
		p, err := fchat.Decode[fchat.BROEvent](cmd)
		if err != nil {
			return &protocolError{"BRO: " + err.Error()}
		}
		s.recordEntry(model.ConvRef{Kind: model.ConvBroadcast, ID: "global"}, "broadcast", p.Character, p.Message, nil)
	case "SYS":
		// Per-connection notices are not shared content and not persisted.
	case "ERR":
		p, err := fchat.Decode[fchat.EREvent](cmd)
		if err != nil {
			return &protocolError{"ERR: " + err.Error()}
		}
		if de := fatalERR(p); de != nil {
			return de
		}
		// A search that matched nothing answers ERR 18. It is a successful
		// empty result, not an error, so it caches an empty result set and
		// announces it.
		if p.EffectiveCode() == 18 {
			s.publishSearch([]model.MemberInfo{})
			return nil
		}
		// Per-command failure: surface it and stay connected.
		s.emit(model.EvError, model.ErrorPayload{Session: s.cfg.Character, Code: p.EffectiveCode(), Message: p.Message})
	default:
		// Unknown command: discard, per the protocol advisories.
	}
	return nil
}

func (s *Session) handleVAR(cmd fchat.Frame) error {
	p, err := fchat.Decode[fchat.VAREvent](cmd)
	if err != nil {
		return &protocolError{"VAR: " + err.Error()}
	}
	var num float64
	if err := json.Unmarshal(p.Value, &num); err != nil {
		return nil // non-numeric variables (arrays, strings) are ignored
	}
	switch p.Variable {
	case "chat_max":
		s.st.vars.ChatMax = int(num)
	case "priv_max":
		s.st.vars.PrivMax = int(num)
	case "lfrp_max":
		s.st.vars.LfrpMax = int(num)
	}
	return nil
}

// usersContain reports whether an ICH user list names character (any casing).
func usersContain(users []fchat.NameOrIdentity, character string) bool {
	for _, u := range users {
		if strings.EqualFold(u.Name, character) {
			return true
		}
	}
	return false
}

// addMember records name as a member of cs (registering its roster spelling)
// and reports whether it was newly added. The conversation's own character is
// never listed as a member. It is the single membership-add rule for JCH/ICH.
func (s *Session) addMember(cs *convState, name string) (added bool) {
	if name == "" {
		return false
	}
	key := nameKey(name)
	if !cs.members[key] && !strings.EqualFold(name, s.cfg.Character) {
		added = true
	}
	s.touch(name)
	cs.members[key] = true
	return added
}

// removeFromAllConvs applies FLN's implied global leave: a character that goes
// offline is no longer a member of any conversation we are in or joining. A
// channel/room we have left (or never joined) is skipped: its roster is no
// longer maintained, so it must not be mutated or re-announced.
func (s *Session) removeFromAllConvs(name string) {
	key := nameKey(name)
	for _, cs := range s.st.convs {
		if !cs.inChannel() {
			continue
		}
		if !cs.members[key] && !cs.ops[key] {
			continue
		}
		delete(cs.members, key)
		delete(cs.ops, key)
		s.emitConversation(cs, "updated")
	}
}

func convRefForChannel(channel string) model.ConvRef {
	// Channel identity is case-insensitive: F-Chat lowercases channel lookups,
	// and several state frames (COL, CDS, mode changes) echo the caller's
	// casing rather than the canonical name. Match the ADH- prefix without
	// regard to case so a lowercase adh-... frame is still a room, not an
	// official channel.
	if len(channel) >= 4 && strings.EqualFold(channel[:4], "ADH-") {
		return model.ConvRef{Kind: model.ConvRoom, ID: channel}
	}
	return model.ConvRef{Kind: model.ConvOfficial, ID: channel}
}

// adChannel resolves the channel label shown on an advertisement. A room's
// ADH-... id is opaque, so prefer the room's readable title when the session
// already knows it — the same resolution recordEntry applies to entries. An
// unknown room, or an official channel, keeps its channel name.
func (s *Session) adChannel(channel string) string {
	conv := convRefForChannel(channel)
	if conv.Kind != model.ConvRoom {
		return channel
	}
	if cs, ok := s.st.convs[convKey(conv)]; ok && cs.title != "" {
		return cs.title
	}
	return channel
}

// recordEntry persists a shared-content entry and publishes it. Sequence
// assignment happens once in nextSeq, before persist+publish, so history pages
// and live events share a single cursor.
func (s *Session) recordEntry(conv model.ConvRef, kind, speaker, body string, data []byte) {
	s.recordEntryCID(conv, kind, speaker, body, data, "")
}

// recordEntryCID is recordEntry with an optional command correlation id. A
// self-authored entry carries its originating command's CID so the client can
// retire exactly the optimistic row it created.
func (s *Session) recordEntryCID(conv model.ConvRef, kind, speaker, body string, data []byte, cid string) {
	cs := s.ensureConv(conv)
	// Adopt the conversation's canonical (first-seen) spelling so a frame that
	// echoed a different casing does not fork the persisted history.
	conv = cs.ref
	// Any DM traffic — incoming or sent — (re)marks the conversation tracked,
	// so a closed DM opens again on its next message and survives a UI reload.
	// Announce a fresh transition: a client at summary interest gets no message
	// event, so `tracked` is its only cue that the conversation now exists.
	if conv.Kind == model.ConvDM && !cs.tracked {
		cs.tracked = true
		s.emitConversation(cs, "tracked")
	}
	seq := s.nextSeq(conv)
	now := s.now()
	// A room's ADH-... id is opaque; store its readable title alongside the
	// entry so the persisted history can be browsed without the hash.
	convName := ""
	if conv.Kind == model.ConvRoom {
		convName = cs.title
	}
	entry := model.Entry{
		ID:         newID(now),
		Session:    s.cfg.Character,
		Conv:       conv,
		ConvName:   convName,
		ConvSeq:    seq,
		Kind:       kind,
		Speaker:    speaker,
		Body:       body,
		CreatedAt:  now,
		ReceivedAt: now,
	}
	// Structured kinds store the raw server payload for fidelity. Copy it: the
	// frame buffer is reused after the actor returns.
	if len(data) > 0 {
		entry.Data = append(entry.Data, data...)
	}
	if s.cfg.Store != nil {
		if err := s.cfg.Store.Append(context.Background(), []model.Entry{entry}); err != nil {
			// The entry is already published; log so the history gap is visible.
			s.log().Warn("store append failed",
				"character", s.cfg.Character, "conv", conv.Key(), "seq", seq, "err", err)
		}
	}
	cs.lastActivity = now
	self := strings.EqualFold(speaker, s.cfg.Character)
	// Highlights are a property of channel traffic: an incoming official/room
	// message whose body matches the configured set. DMs are already elevated
	// by kind, broadcasts are not channel traffic, and the sender's own copy
	// must never trigger one.
	highlight := !self &&
		(conv.Kind == model.ConvOfficial || conv.Kind == model.ConvRoom) &&
		s.highlights.match(body)
	s.emit(model.EvMessage, model.MessagePayload{
		Conv:      conv,
		Entry:     s.delivery.Entry(entry),
		Self:      self,
		Highlight: highlight,
		CID:       cid,
	})
	s.emitState(model.SummaryKey(s.cfg.Character, conv), model.SummaryPayload{Conv: conv, Title: cs.title, Self: self, Highlight: highlight, LastActivity: now})
}
