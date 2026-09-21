package core

import (
	"context"
	"sort"
	"strings"
	"time"

	"plexo/internal/session"
)

// FriendBookmarkLister fetches the account's friend and bookmark lists from
// F-List. *Account implements it.
type FriendBookmarkLister interface {
	FetchFriendBookmarkLists(ctx context.Context) (friends, bookmarks []string, err error)
}

// friendBookmarkTimeout bounds the one combined list fetch.
const friendBookmarkTimeout = 10 * time.Second

// The account-wide friend/bookmark split is derived from the FRL union plus one
// combined REST fetch. It is fetched once per connected cohort: the first
// session to become ready triggers a background fetch, later sessions reuse it,
// and when the last session falls out of ready the flag resets so the next
// cohort refetches. Manager implements session.FriendBookmarkService.

// SessionReady registers a character as a ready session. The first ready
// session of a cohort triggers one background fetch of the account's
// friend/bookmark split; later sessions reuse it.
func (m *Manager) SessionReady(character string) {
	if m.cfg.FriendBookmarks == nil {
		return
	}
	m.fbMu.Lock()
	if m.fbReady[character] {
		m.fbMu.Unlock()
		return
	}
	m.fbReady[character] = true
	needFetch := !m.fbFetched && !m.fbFetching
	gen := m.fbGen
	if needFetch {
		m.fbFetching = true
	}
	m.fbMu.Unlock()
	if needFetch {
		go m.fetchFriendBookmarks(gen)
	}
}

// SessionGone deregisters a character that fell out of ready. When the last one
// leaves, the fetched flag resets so the next cohort refetches.
func (m *Manager) SessionGone(character string) {
	m.fbMu.Lock()
	defer m.fbMu.Unlock()
	delete(m.fbReady, character)
	if len(m.fbReady) == 0 {
		m.fbFetched = false
		m.fbFetching = false
		m.fbFriends = nil
		m.fbBookmarks = nil
		m.fbGen++
	}
}

// fetchFriendBookmarks performs the combined fetch and installs the split,
// unless the cohort it was launched for has since ended (gen mismatch). On
// success it pokes every ready session, since their earlier projection defaulted
// unclassified names to friends.
func (m *Manager) fetchFriendBookmarks(gen uint64) {
	ctx, cancel := context.WithTimeout(m.ctx, friendBookmarkTimeout)
	defer cancel()
	friends, bookmarks, err := m.cfg.FriendBookmarks.FetchFriendBookmarkLists(ctx)

	m.fbMu.Lock()
	if gen != m.fbGen {
		m.fbMu.Unlock()
		return // cohort replaced; a new fetch owns fbFetching now
	}
	m.fbFetching = false
	if err != nil {
		m.fbMu.Unlock()
		if m.cfg.Logger != nil {
			m.cfg.Logger.Debug("friend/bookmark fetch failed", "err", err)
		}
		return
	}
	m.fbFriends = nameSet(friends)
	m.fbBookmarks = nameSet(bookmarks)
	m.fbFetched = true
	m.fbMu.Unlock()
	m.notifyFriendBookmarks()
}

// notifyFriendBookmarks pokes every ready session to republish the split after
// the coordinator changed it (a REST fetch, or a client-applied bookmark).
func (m *Manager) notifyFriendBookmarks() {
	m.fbMu.Lock()
	characters := make([]string, 0, len(m.fbReady))
	for c := range m.fbReady {
		characters = append(characters, c)
	}
	m.fbMu.Unlock()
	for _, c := range characters {
		if s, err := m.session(c); err == nil {
			s.RefreshFriendBookmarks()
		}
	}
}

// ApplyFriendBookmarkRTB folds a realtime-bridge delta into the cached split and
// reports whether the split changed, so a session can republish when only the
// classification (not the FRL union) moved.
func (m *Manager) ApplyFriendBookmarkRTB(kind, name string) bool {
	if name == "" {
		return false
	}
	key := strings.ToLower(name)
	m.fbMu.Lock()
	defer m.fbMu.Unlock()
	switch kind {
	case "trackadd":
		return addName(&m.fbBookmarks, key, name)
	case "trackrem":
		return deleteName(m.fbBookmarks, key)
	case "friendadd":
		return addName(&m.fbFriends, key, name)
	case "friendremove":
		return deleteName(m.fbFriends, key)
	default:
		return false
	}
}

// FriendBookmarks returns the cached split as canonical spellings, sorted. It
// reflects everything the coordinator knows (the REST fetch plus RTB and
// client-applied changes) and is empty until any of them lands.
func (m *Manager) FriendBookmarks() (friends, bookmarks []string) {
	m.fbMu.Lock()
	defer m.fbMu.Unlock()
	return sortedNames(m.fbFriends), sortedNames(m.fbBookmarks)
}

// Contacts returns the account's classified contact graph for the session
// projection. Fetched is false until the single REST fetch succeeds; until then
// the session falls back to the FRL union. The maps are copies, safe to read off
// the coordinator's lock.
func (m *Manager) Contacts() session.ContactSplit {
	m.fbMu.Lock()
	defer m.fbMu.Unlock()
	return session.ContactSplit{
		Friends:   keyBoolSet(m.fbFriends),
		Bookmarks: keyBoolSet(m.fbBookmarks),
		Fetched:   m.fbFetched,
	}
}

// SetBookmark records a bookmark change applied through the account REST API and
// republishes the split to every ready session. F-List also emits an RTB for the
// change; applying it here makes the client's own action immediately visible and
// correct even if that frame is delayed or dropped, and the RTB is then an
// idempotent confirmation.
func (m *Manager) SetBookmark(name string, add bool) {
	kind := "trackrem"
	if add {
		kind = "trackadd"
	}
	if !m.ApplyFriendBookmarkRTB(kind, name) {
		return
	}
	m.notifyFriendBookmarks()
}

// nameSet indexes display names by their case-folded key.
func nameSet(names []string) map[string]string {
	set := make(map[string]string, len(names))
	for _, n := range names {
		if n != "" {
			set[strings.ToLower(n)] = n
		}
	}
	return set
}

// addName lazily creates the set and records key -> display. It reports whether
// the key was newly added.
func addName(set *map[string]string, key, name string) bool {
	if *set == nil {
		*set = map[string]string{}
	}
	if _, ok := (*set)[key]; ok {
		return false
	}
	(*set)[key] = name
	return true
}

// deleteName removes key from the set and reports whether it was present.
func deleteName(set map[string]string, key string) bool {
	if _, ok := set[key]; !ok {
		return false
	}
	delete(set, key)
	return true
}

// keyBoolSet projects a key -> display set to a key -> present set, as a copy.
func keyBoolSet(set map[string]string) map[string]bool {
	if len(set) == 0 {
		return nil
	}
	out := make(map[string]bool, len(set))
	for k := range set {
		out[k] = true
	}
	return out
}

func sortedNames(set map[string]string) []string {
	out := make([]string, 0, len(set))
	for _, n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
