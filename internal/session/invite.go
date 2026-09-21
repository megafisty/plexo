package session

import (
	"slices"
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
	// CIU titles arrive HTML-escaped like every room title; decode once.
	title := model.DecodeWireEntities(p.Title)
	// Prefer a title the session already knows (from a prior JCH, ICH, CDS, or
	// the room catalog) over the invitation's snapshot.
	if cs, ok := s.st.convs[convKey(ref)]; ok && cs.title != "" {
		title = cs.title
	}
	sender := ""
	if p.Sender != "" {
		sender = s.canonicalName(p.Sender)
	}
	s.st.invites[convKey(ref)] = model.RoomInvite{Conv: ref, Title: title, InvitedBy: sender}
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
	// The sort key is computed once per invitation rather than per comparison.
	// Lowercased title first, then the exact id, mirroring the streamed set-to
	// order so the snapshot and the state record never flap.
	type keyedInvite struct {
		key    string
		invite model.RoomInvite
	}
	keyed := make([]keyedInvite, 0, len(s.st.invites))
	for _, inv := range s.st.invites {
		keyed = append(keyed, keyedInvite{
			key:    strings.ToLower(inv.Title) + "\x00" + inv.Conv.ID,
			invite: inv,
		})
	}
	slices.SortFunc(keyed, func(a, b keyedInvite) int { return strings.Compare(a.key, b.key) })
	list := make([]model.RoomInvite, len(keyed))
	for i := range keyed {
		list[i] = keyed[i].invite
	}
	return list
}

// emitInvites publishes the pending invitation list as a session-scoped set-to
// state record.
func (s *Session) emitInvites() {
	s.emitState(model.InvitesKey(s.cfg.Character), model.InvitesPayload{Invites: s.inviteListLocked()})
}
