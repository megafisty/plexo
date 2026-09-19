package session

import (
	"sort"

	"plexo/internal/model"
)

// --- client-facing snapshots ---

// friendInfosLocked renders the friends/bookmarks name set with whatever
// presence the roster holds. Friends are account-wide, so this is a pure
// projection: the roster is the only place presence lives.
func (s *Session) friendInfosLocked() []model.MemberInfo {
	out := make([]model.MemberInfo, 0, len(s.st.friends))
	for key := range s.st.friends {
		out = append(out, s.delivery.Member(s.memberInfo(s.displayName(key))))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ignoreList renders the account ignore set, sorted for stable output.
func (s *Session) ignoreList() []string {
	out := make([]string, 0, len(s.st.ignores))
	for key := range s.st.ignores {
		out = append(out, s.displayName(key))
	}
	sort.Strings(out)
	return out
}

// AccountSets returns the account-wide friends and ignore projections for the
// snapshot. It runs on the session actor so the caller never touches session
// state. The sets are account-wide and identical across sessions; a session
// only contributes the presence its own roster holds.
func (s *Session) AccountSets() ([]model.MemberInfo, []string) {
	type sets struct {
		friends []model.MemberInfo
		ignores []string
	}
	r, ok := ask(s, func(reply chan sets) {
		reply <- sets{friends: s.friendInfosLocked(), ignores: s.ignoreList()}
	})
	if !ok {
		return nil, nil
	}
	return r.friends, r.ignores
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
	if !ok {
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
