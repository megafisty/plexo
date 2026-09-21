package session

import "plexo/internal/model"

// emit publishes a canonical event for this session.
func (s *Session) emit(kind model.EventKind, payload any) {
	if s.cfg.Broker == nil {
		return
	}
	s.cfg.Broker.Publish(model.Event{Session: s.cfg.Character, Kind: kind, Time: s.now(), Payload: payload})
}

// emitState publishes one set-to state record. The key encodes the scope (see
// model.State*); the broker stores the latest value per key and fans it out.
func (s *Session) emitState(key string, value any) {
	if s.cfg.Broker == nil {
		return
	}
	s.cfg.Broker.Publish(model.Event{
		Session: s.cfg.Character,
		Kind:    model.EvState,
		Time:    s.now(),
		Payload: model.StatePayload{Key: key, Value: value},
	})
}

// emitStateRemoved publishes a tombstone for a state key, so a client drops it
// (and, for session/, its whole subtree) on the next apply or resync.
func (s *Session) emitStateRemoved(key string) {
	if s.cfg.Broker == nil {
		return
	}
	s.cfg.Broker.Publish(model.Event{
		Session: s.cfg.Character,
		Kind:    model.EvState,
		Time:    s.now(),
		Payload: model.StatePayload{Key: key, Removed: true},
	})
}

func (s *Session) emitSessionState(conn, reason, severity string, auto bool) {
	s.st.conn = conn
	s.st.reason = reason
	s.st.severity = severity
	s.st.autoRetry = auto
	s.emitState(model.SessionKey(s.cfg.Character), model.SessionStatePayload{
		State: model.SessionState(conn), Reason: reason, Severity: model.Severity(severity), AutoRetry: auto,
	})
}

// emitFriendPresence streams the current presence of every contact. The friends
// record carries the name set (set-to) and is de-duplicated by that set, so a
// reconnect whose set is unchanged produces no event; this refreshes the
// presence clients would otherwise miss.
func (s *Session) emitFriendPresence() {
	members, _ := s.accountContactsLocked()
	for key := range members {
		s.emitPresence(s.touch(s.displayName(key)))
	}
}

// emitAccountSets republishes the client-facing friend/bookmark and ignore
// projections. Both are filtered to characters the roster can name
// authoritatively, so a login hydration burst or an online/offline transition
// must refresh them. The broker de-duplicates an unchanged set, so the extra
// emits are cheap.
func (s *Session) emitAccountSets() {
	members, cs := s.accountContactsLocked()
	s.emitFriendBookmarks(members, cs)
	s.emitState(model.AccountKey("ignores"), model.IgnoresPayload{Ignores: s.ignoreList()})
}

// emitFriendBookmarks publishes the classified projection of a membership set
// already resolved by accountContactsLocked, so callers can reuse one resolution.
func (s *Session) emitFriendBookmarks(members map[string]bool, cs ContactSplit) {
	friends, bookmarks := s.projectContactsLocked(members, cs)
	s.emitState(model.AccountKey("friends"), model.FriendsPayload{Friends: friends, Bookmarks: bookmarks})
}

// refreshAccountSets republishes whichever account projection name belongs to,
// after a presence transition made its authoritative spelling available (or
// retired it). A name in neither set is a no-op.
func (s *Session) refreshAccountSets(name string) {
	key := nameKey(name)
	members, cs := s.accountContactsLocked()
	if members[key] {
		s.emitFriendBookmarks(members, cs)
	}
	if s.st.ignores[key] {
		s.emitState(model.AccountKey("ignores"), model.IgnoresPayload{Ignores: s.ignoreList()})
	}
}

// syncFriendWatch hands the broker the full account contact membership for
// presence scoping. The client-facing payload is filtered to online characters,
// but the broker must still watch offline contacts so their return is delivered
// to a client that only has the online subset.
func (s *Session) syncFriendWatch() {
	if s.cfg.Broker == nil {
		return
	}
	members, _ := s.accountContactsLocked()
	names := make([]string, 0, len(members))
	for key := range members {
		names = append(names, key)
	}
	s.cfg.Broker.SetAccountFriends(names)
}

func (s *Session) emitConversation(cs *convState, op string) {
	key := model.ConvKey(s.cfg.Character, cs.ref)
	// A conversation the character is not in stays in st.convs so its metadata
	// and history cursor survive, but it is not part of the client's
	// conversation list. A leave is a removal; an out-of-order update for a
	// conversation we are not in is dropped so it cannot resurrect it on the
	// client. live() mirrors the snapshot's rule.
	if op == "left" || op == "gone" {
		s.emitStateRemoved(key)
		return
	}
	if !cs.live() {
		return
	}
	// The description is sparse on the wire: send it only when it changed since
	// the last emit, so roster and mode updates do not carry it. A cleared
	// description is a pointer to "", not a nil (unchanged) field.
	var description *string
	if cs.descriptionDirty {
		d := cs.description
		description = &d
		cs.descriptionDirty = false
	}
	s.emitState(key, s.delivery.ConversationState(model.ConvStatePayload{
		Title:       cs.title,
		Description: description,
		Mode:        cs.mode,
		Members:     s.memberList(cs),
		Ops:         s.opList(cs),
		Role:        s.selfRole(cs),
	}))
}
