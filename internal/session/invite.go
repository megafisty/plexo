package session

import (
	"sort"
	"strings"

	"plexo/internal/fchat"
	"plexo/internal/model"
)

// applyCIU records a room invitation observed from the server. Invitations are
// delivered exactly once and the server exposes no query, so this is the only
// chance to capture one. The room need not be known, and the inviter need not
// be present; the invitation carries the room id and title a [session] deep
// link needs.
func (s *Session) applyCIU(p fchat.CIUEvent) {
	if p.Name == "" {
		return
	}
	ref := convRefForChannel(p.Name)
	title := p.Title
	// Prefer a title the session already knows (from a prior JCH, ICH, CDS, or
	// the room catalog) over the invitation's snapshot.
	if cs, ok := s.st.convs[convKey(ref)]; ok && cs.title != "" {
		title = cs.title
	}
	if p.Sender != "" {
		s.touch(p.Sender)
	}
	s.st.invites[convKey(ref)] = model.RoomInvite{Conv: ref, Title: title, InvitedBy: p.Sender}
	s.emitInvites()
}

// dropInvite removes one room invitation and republishes the list. It is
// idempotent, so accepting (the self JCH) and dismissing can both call it.
func (s *Session) dropInvite(ref model.ConvRef) {
	if _, ok := s.st.invites[convKey(ref)]; !ok {
		return
	}
	delete(s.st.invites, convKey(ref))
	s.emitInvites()
}

// inviteListLocked returns the pending invitations sorted for stable output,
// so the snapshot and the streamed state record agree and neither flaps on map
// iteration order.
func (s *Session) inviteListLocked() []model.RoomInvite {
	list := make([]model.RoomInvite, 0, len(s.st.invites))
	for _, inv := range s.st.invites {
		list = append(list, inv)
	}
	sort.Slice(list, func(i, j int) bool {
		ti, tj := strings.ToLower(list[i].Title), strings.ToLower(list[j].Title)
		if ti != tj {
			return ti < tj
		}
		return list[i].Conv.ID < list[j].Conv.ID
	})
	return list
}

// emitInvites publishes the pending invitation list as a session-scoped set-to
// state record.
func (s *Session) emitInvites() {
	s.emitState(model.InvitesKey(s.cfg.Character), model.InvitesPayload{Invites: s.inviteListLocked()})
}
