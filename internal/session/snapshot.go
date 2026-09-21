package session

import (
	"sort"

	"plexo/internal/model"
)

// --- client-facing snapshots ---

// accountContactsLocked returns the effective contact membership and the
// authoritative classification for this session. The manager's split is
// authoritative once fetched: membership is exactly its friends and bookmarks,
// so unbookmarking a contact removes it even though the FRL union still lists
// it. Until the fetch succeeds the FRL union stands in (every name a friend),
// with any name the split already knows (a bookmark applied by the client before
// the fetch landed) folded in.
func (s *Session) accountContactsLocked() (members map[string]bool, cs ContactSplit) {
	if s.cfg.FriendBookmarks != nil {
		cs = s.cfg.FriendBookmarks.Contacts()
	}
	members = make(map[string]bool, len(s.st.friends)+len(cs.Friends)+len(cs.Bookmarks))
	if !cs.Fetched {
		for key := range s.st.friends {
			members[key] = true
		}
	}
	for key := range cs.Friends {
		members[key] = true
	}
	for key := range cs.Bookmarks {
		members[key] = true
	}
	return members, cs
}

// projectContactsLocked renders the online, authoritatively-named subset of a
// contact membership set, partitioned by kind. A name absent from both split
// sets defaults to a friend. A character that is both a friend and a bookmark
// appears in both lists.
func (s *Session) projectContactsLocked(members map[string]bool, cs ContactSplit) (friends, bookmarks []model.MemberInfo) {
	friends = []model.MemberInfo{}
	bookmarks = []model.MemberInfo{}
	for key := range members {
		p, ok := s.st.roster[key]
		if !ok || !s.st.named[key] || !p.Online {
			continue
		}
		info := s.delivery.Member(s.projectMember(p))
		_, bookmarked := cs.Bookmarks[key]
		if bookmarked {
			bookmarks = append(bookmarks, info)
		}
		if _, friend := cs.Friends[key]; friend || !bookmarked {
			friends = append(friends, info)
		}
	}
	sort.Slice(friends, func(i, j int) bool { return friends[i].Name < friends[j].Name })
	sort.Slice(bookmarks, func(i, j int) bool { return bookmarks[i].Name < bookmarks[j].Name })
	return friends, bookmarks
}

// friendBookmarkInfosLocked renders the client-facing friend and bookmark
// projection. The name set is account-wide, but the client is only ever told
// about those the roster can name authoritatively (which, in practice, means
// online): a contact the session has never seen online has no authoritative
// spelling, and emitting the provisional one would create a client record a
// later online transition would leave stale. The broker still watches the full
// membership.
func (s *Session) friendBookmarkInfosLocked() (friends, bookmarks []model.MemberInfo) {
	members, cs := s.accountContactsLocked()
	return s.projectContactsLocked(members, cs)
}

// ignoreList renders the online subset of the account ignore set, sorted for
// stable output. Like friends, an ignore with no authoritative spelling is
// withheld until it comes online; see friendBookmarkInfosLocked.
func (s *Session) ignoreList() []string {
	out := make([]string, 0, len(s.st.ignores))
	for key := range s.st.ignores {
		p, ok := s.st.roster[key]
		if !ok || !s.st.named[key] || !p.Online {
			continue
		}
		out = append(out, p.Character)
	}
	sort.Strings(out)
	return out
}

// AccountSets returns the account-wide friend, bookmark, and ignore projections
// for the snapshot. It runs on the session actor so the caller never touches
// session state. The sets are account-wide and identical across sessions; a
// session only contributes the presence its own roster holds.
func (s *Session) AccountSets() ([]model.MemberInfo, []model.MemberInfo, []string) {
	type sets struct {
		friends   []model.MemberInfo
		bookmarks []model.MemberInfo
		ignores   []string
	}
	r, ok := ask(s, func(reply chan sets) {
		friends, bookmarks := s.friendBookmarkInfosLocked()
		reply <- sets{friends: friends, bookmarks: bookmarks, ignores: s.ignoreList()}
	})
	if !ok {
		return nil, nil, nil
	}
	return r.friends, r.bookmarks, r.ignores
}

func (s *Session) snapshotLocked() model.SessionSnapshot {
	snap := model.SessionSnapshot{
		Character:      s.cfg.Character,
		State:          model.SessionState(s.st.conn),
		Reason:         s.st.reason,
		Severity:       model.Severity(s.st.severity),
		AutoRetry:      s.st.autoRetry,
		Self:           s.delivery.Presence(s.selfPresence()),
		SelfStatusText: s.st.selfStatusText,
		AdCount:        s.ads.len(),
		ChatMax:        s.st.vars.ChatMax,
		PrivMax:        s.st.vars.PrivMax,
		Conversations:  []model.ConvSummary{},
		Invites:        s.inviteListLocked(),
	}
	for _, cs := range s.st.convs {
		// A channel/room the character has left, or an untracked DM, stays in
		// st.convs so its metadata and history cursor survive, but it is not part
		// of the client's conversation list. live() is the same predicate
		// emitConversation uses, so the snapshot and events cannot disagree.
		if !cs.live() {
			continue
		}
		snap.Conversations = append(snap.Conversations, model.ConvSummary{
			Conv:         cs.ref,
			Kind:         cs.ref.Kind,
			Title:        cs.title,
			LastActivity: cs.lastActivity,
			Role:         s.selfRole(cs),
		})
	}
	sort.Slice(snap.Conversations, func(i, j int) bool {
		return snap.Conversations[i].LastActivity.After(snap.Conversations[j].LastActivity)
	})
	return snap
}

func (s *Session) convMetaLocked(ref model.ConvRef) ConvMeta {
	cs, ok := s.st.convs[convKey(ref)]
	if !ok {
		return ConvMeta{Ref: ref, Exists: false}
	}
	meta := ConvMeta{
		Ref:         cs.ref,
		Title:       cs.title,
		Description: cs.description,
		Mode:        cs.mode,
		Joined:      cs.membership == memJoined,
		Exists:      true,
		Ops:         s.opList(cs),
		Role:        s.selfRole(cs),
	}
	seen := make(map[string]bool, len(cs.members)+1)
	add := func(name string) {
		if name == "" {
			return
		}
		key := nameKey(name)
		if seen[key] {
			return
		}
		seen[key] = true
		meta.Members = append(meta.Members, s.memberInfo(name))
	}
	for key := range cs.members {
		add(s.displayName(key))
	}
	// A DM has no roster event, so its only participant is the partner.
	if ref.Kind == model.ConvDM {
		add(ref.ID)
	}
	return meta
}

// memberInfo projects the authoritative roster into a delivery MemberInfo.
// Unknown names are registered as offline placeholders so their spelling is
// retained, but presence is never invented.
func (s *Session) memberInfo(name string) model.MemberInfo {
	return s.projectMember(s.touch(name))
}

// lookupMember projects the roster into a delivery MemberInfo without
// registering an unknown name. Search results are transient, so they must not
// grow the canonical roster.
func (s *Session) lookupMember(name string) model.MemberInfo {
	p, ok := s.st.roster[nameKey(name)]
	if !ok || p.Character == "" {
		p.Character = name
	}
	return s.projectMember(p)
}

// projectMember renders one roster entry into the delivery shape.
func (s *Session) projectMember(p model.PresencePayload) model.MemberInfo {
	return model.MemberInfo{
		Name:      p.Character,
		Gender:    p.Gender,
		Status:    p.Status,
		StatusMsg: p.StatusMsg,
		Admin:     s.st.admins[nameKey(p.Character)],
		Online:    p.Online,
	}
}
