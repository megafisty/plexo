package session

import (
	"errors"
	"strings"

	"plexo/internal/fchat"
	"plexo/internal/model"
)

// handleCommand handles a UI command against the session's state.
func (s *Session) handleCommand(cmd model.Command) model.Result {
	accept := func() model.Result { return model.Result{CID: cmd.CID, Accepted: true} }
	reject := func(code, msg string) model.Result {
		return model.Result{CID: cmd.CID, Accepted: false, ErrorCode: code, ErrorMsg: msg}
	}

	switch cmd.Op {
	case model.OpSendMessage:
		if strings.TrimSpace(cmd.Body) == "" {
			return reject("empty", "message is empty")
		}
		// fserv applies priv_max to DMs and chat_max to channel messages. It
		// also applies priv_max to private/pubprivate channels, but those are
		// admin-only and the session model does not track channel type, so a
		// channel message is checked against chat_max; the rare over-long post
		// is left for the server to reject.
		limit := s.st.vars.ChatMax
		if cmd.Conv.Kind == model.ConvDM {
			limit = s.st.vars.PrivMax
		}
		if limit > 0 && len(cmd.Body) > limit {
			return reject("too_long", "message exceeds server limit")
		}
		// The F-Chat server delivers a message to everyone except its sender:
		// `Channel::sendToChannel` skips the source, and `event.PRI` sends only
		// to the target. Record our own copy only after the frame is actually
		// written, so a failed socket write never persists and displays a message
		// the server never received; the recorded copy gets a canonical conv_seq
		// and timestamp and is delivered as a self event the client uses to retire
		// its optimistic entry.
		code, kind := "MSG", "msg"
		var payload any = fchat.ChannelMsg{Channel: cmd.Conv.ID, Message: cmd.Body}
		if cmd.Conv.Kind == model.ConvDM {
			code, kind = "PRI", "dm"
			payload = fchat.PrivateMsg{Recipient: cmd.Conv.ID, Message: cmd.Body}
		}
		conv, body := cmd.Conv, cmd.Body
		cid := cmd.CID
		if err := s.queueAck(code, payload, func() {
			s.recordEntryCID(conv, kind, s.cfg.Character, body, nil, cid)
		}); err != nil {
			return reject("send_failed", err.Error())
		}
		return accept()
	case model.OpSendTyping:
		// TPN is private-message-only; the server never relays channel typing.
		if cmd.Conv.Kind != model.ConvDM {
			return reject("bad_conv", "typing applies to private conversations only")
		}
		switch cmd.Status {
		case "typing", "paused", "clear":
		default:
			return reject("bad_status", "status must be typing, paused, or clear")
		}
		if cmd.Conv.ID == "" {
			return reject("missing_character", "conversation is required")
		}
		// The outbound TPN names the recipient; the server resolves the sender
		// from the connection and flips the field when delivering to the peer.
		if err := s.queue("TPN", fchat.TypingNotification{Character: cmd.Conv.ID, Status: cmd.Status}); err != nil {
			return reject("typing_failed", err.Error())
		}
		return accept()
	case model.OpSendLRP:
		if s.st.vars.LfrpMax > 0 && len(cmd.Body) > s.st.vars.LfrpMax {
			return reject("too_long", "advertisement exceeds server limit")
		}
		if err := s.queue("LRP", fchat.ChannelMsg{Channel: cmd.Conv.ID, Message: cmd.Body}); err != nil {
			return reject("send_failed", err.Error())
		}
		return accept()
	case model.OpJoin:
		if err := s.queue("JCH", fchat.ChannelRef{Channel: cmd.Conv.ID}); err != nil {
			return reject("join_failed", err.Error())
		}
		// Mark the conversation joining so a state frame that trails the reply
		// (an ICH or COL that arrives before self JCH) is accepted and stored.
		// The actor is single-threaded, so no reply can be processed before this
		// returns.
		cs := s.ensureConv(cmd.Conv)
		if cs.membership != memJoined {
			cs.membership = memJoining
		}
		return accept()
	case model.OpLeave:
		if err := s.queue("LCH", fchat.ChannelRef{Channel: cmd.Conv.ID}); err != nil {
			return reject("leave_failed", err.Error())
		}
		return accept()
	case model.OpSetStatus:
		if cmd.Status == "" {
			return reject("missing_status", "status is required")
		}
		// Crown is granted by a moderator and cannot be set by the character;
		// it is displayed when the server sends it, never emitted.
		if strings.EqualFold(cmd.Status, "crown") {
			return reject("reserved_status", "crown is granted by a moderator")
		}
		// The server echoes STA, but only after it accepts the frame. Apply the
		// optimistic self update once the frame is actually on the wire, so a
		// failed socket write cannot leave a status the server never saw. The
		// raw text is remembered for the editor; it is never persisted or
		// re-emitted on login.
		status, statusMsg := cmd.Status, cmd.StatusMsg
		if err := s.queueAck("STA", fchat.StatusUpdate{Status: status, StatusMsg: statusMsg}, func() {
			s.st.selfStatusText = statusMsg
			s.setPresence(s.cfg.Character, "", status, statusMsg)
		}); err != nil {
			return reject("status_failed", err.Error())
		}
		return accept()
	case model.OpSetIgnore:
		switch cmd.Action {
		case "add", "delete":
			if cmd.Character == "" {
				return reject("missing_character", "character is required")
			}
			if err := s.queue("IGN", fchat.IgnoreUpdate{Character: cmd.Character, Action: cmd.Action}); err != nil {
				return reject("ignore_failed", err.Error())
			}
		case "list":
			if err := s.queue("IGN", fchat.IgnoreList{Action: "list"}); err != nil {
				return reject("ignore_failed", err.Error())
			}
		default:
			return reject("bad_action", "action must be add, delete, or list")
		}
		return accept()
	case model.OpSetTracked:
		if cmd.Conv.Kind != model.ConvDM {
			return reject("bad_conv", "tracking applies to private conversations only")
		}
		if cmd.Conv.ID == "" {
			return reject("missing_character", "conversation is required")
		}
		key := convKey(cmd.Conv)
		cs, ok := s.st.convs[key]
		if !ok {
			if !cmd.Tracked {
				return accept() // nothing to untrack
			}
			cs = s.ensureConv(cmd.Conv)
		}
		if cs.tracked == cmd.Tracked {
			return accept()
		}
		cs.tracked = cmd.Tracked
		if cs.tracked {
			// Tell every connected client to show the DM. This also covers a DM
			// opened before its first message, whose only other event would be a
			// conv_view to the requesting client. The dedicated op keeps `updated`
			// a pure update, so the client never has to create on a state change.
			s.emitConversation(cs, "tracked")
		} else {
			// Drop typing before hiding it; a stale indicator must not resurface
			// if the DM is tracked again later.
			delete(s.st.typing, key)
			s.emitConversation(cs, "gone")
		}
		return accept()
	default:
		return reject("unsupported", "unsupported op")
	}
}

// RequestOfficialChannels sends CHA, asking the server for the official
// channel list. The reply arrives as a CHA frame and is routed to OnCatalog.
func (s *Session) RequestOfficialChannels() error {
	return s.queue("CHA", nil)
}

// RequestPublicRooms sends ORS, asking the server for the public room list.
// The reply arrives as an ORS frame and is routed to OnCatalog.
func (s *Session) RequestPublicRooms() error {
	return s.queue("ORS", nil)
}

// autoJoin sends a JCH for each configured channel or room. It is best-effort
// and runs on every ready transition, so a reconnect rejoins the same set; a
// failed send is discarded.
func (s *Session) autoJoin() {
	for _, j := range s.settings.AutoJoin {
		if err := s.queue("JCH", fchat.ChannelRef{Channel: j.ID}); err != nil {
			s.log().Debug("auto-join failed", "character", s.cfg.Character, "channel", j.ID, "err", err)
		}
	}
}

func (s *Session) queue(code string, payload any) error {
	return s.queueAck(code, payload, nil)
}

// queueAck enqueues a frame and schedules onSent to run on the actor after the
// frame is successfully written. Use it for any outbound frame whose local
// effect must not be recorded if the send never reaches the server.
func (s *Session) queueAck(code string, payload any, onSent func()) error {
	wire, err := fchat.New(code, payload)
	if err != nil {
		return err
	}
	return s.enqueue(outbound{wire: wire, onSent: onSent})
}

// enqueue hands an encoded frame to the writer.
func (s *Session) enqueue(ob outbound) error {
	if s.out == nil {
		return errors.New("not connected")
	}
	select {
	case s.out <- ob:
		return nil
	case <-s.done():
		return errors.New("not connected")
	}
}
