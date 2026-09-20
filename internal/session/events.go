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

// emitFriendPresence streams the current presence of every friend. The friends
// record carries the name set (set-to) and is de-duplicated by that set, so a
// reconnect whose set is unchanged produces no event; this refreshes the
// presence clients would otherwise miss.
func (s *Session) emitFriendPresence() {
	for key := range s.st.friends {
		s.emitPresence(s.touch(s.displayName(key)))
	}
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
		Conv:        cs.ref,
		Title:       cs.title,
		Description: description,
		Mode:        cs.mode,
		Members:     s.memberList(cs),
		Ops:         s.opList(cs),
		Role:        s.selfRole(cs),
	}))
}
