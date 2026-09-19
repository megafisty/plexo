package session

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"sort"
	"strconv"
	"strings"
	"time"

	"plexo/internal/model"
)

// serverVars holds the limits the server reports via VAR.
type serverVars struct {
	ChatMax int
	PrivMax int
	LfrpMax int
	CdsMax  int
}

// roomBan is one entry of a room's ban list as observed by this session. A zero
// expiry is a permanent ban. It is in-memory only: never persisted, and never
// streamed except through the on-demand RoomInfo read.
type roomBan struct {
	name        string
	banner      string
	expiresAtMs int64
}

// roomAdmin is the session's in-memory administrative view of one channel or
// room. It is never persisted and, apart from the derived self role, never
// streamed: the management surface reads it on demand through RoomInfo.
type roomAdmin struct {
	// owner is the channel owner in its canonical spelling; ownerKey is its
	// normalized form. The owner is the first entry of a COL op list and may be
	// empty.
	owner    string
	ownerKey string
	bans     map[string]roomBan
}

// roomVisibility is the session's best-effort knowledge of whether a room is
// published ("public") or closed ("private"). RST changes it but the server
// broadcasts nothing, so it is known only when this session issued the change;
// visUnknown means the session has no reliable answer (and no catalog entry is
// asserted either way).
type roomVisibility uint8

const (
	visUnknown roomVisibility = iota
	visPrivate
	visPublic
)

// convMembership tracks whether this session is currently in a channel or room.
// It is meaningful only for channels/rooms; DMs use tracked instead. The
// joining state exists so a state frame that trails our join request (an ICH or
// COL that arrives before the matching self JCH) is still accepted and stored.
type convMembership uint8

const (
	memUnknown convMembership = iota // never a participant
	memJoining                       // join requested, awaiting self JCH/ICH
	memJoined
	memLeft // self LCH
)

// convState is the actor-owned state of one conversation.
type convState struct {
	ref         model.ConvRef
	title       string
	description string
	// descriptionDirty marks a description changed since the last conversation
	// state emit. The description is sparse on the wire (see
	// model.ConvStatePayload.Description), so it is sent only when this is set.
	descriptionDirty bool
	mode             string
	members          map[string]bool // nameKey -> member
	ops              map[string]bool // nameKey -> channel op
	membership       convMembership
	// visibility is the room's published state, best-effort; see roomVisibility.
	// It is meaningful only for rooms and never streamed as conversation state.
	visibility roomVisibility
	// admin is the room's administrative view (owner, bans). It is populated
	// only from room frames and never streamed as conversation state.
	admin roomAdmin
	// tracked marks a DM the user wants visible in the client's conversation
	// list. Channels/rooms are governed by membership; DMs are tracked explicitly,
	// or implicitly by any message to or from the partner. It is deliberately
	// in-memory only: a core restart resets every DM to untracked.
	tracked      bool
	lastActivity time.Time
}

// displayName resolves a normalized roster key back to its canonical spelling.
func (s *Session) displayName(key string) string {
	if p, ok := s.st.roster[key]; ok && p.Character != "" {
		return p.Character
	}
	return key
}

// memberList returns the conversation's members in canonical spelling. Members
// are stored as normalized keys; the roster is the source of the real name.
func (s *Session) memberList(cs *convState) []string { return s.displayNames(cs.members) }

// opList returns the conversation's full room-moderator set in canonical
// spelling: the owner followed by the ordinary mods. cs.ops holds only the
// mods (ownership is tracked separately so selfRole can tell the two apart),
// but the client marks every room moderator from one set, so the owner is
// included here. The owner slot is empty for official channels, which have no
// owner.
func (s *Session) opList(cs *convState) []string {
	if cs.admin.ownerKey == "" {
		return s.displayNames(cs.ops)
	}
	set := make(map[string]bool, len(cs.ops)+1)
	set[cs.admin.ownerKey] = true
	for key := range cs.ops {
		set[key] = true
	}
	return s.displayNames(set)
}

// displayNames resolves a set of normalized roster keys to canonical spelling.
func (s *Session) displayNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, s.displayName(key))
	}
	return out
}

// live reports whether a conversation belongs in the client's conversation
// list: joined channels/rooms, tracked DMs, and broadcasts (always). It is the
// single predicate behind both the snapshot and emitConversation, so the two
// can never disagree about what exists.
func (cs *convState) live() bool {
	switch cs.ref.Kind {
	case model.ConvOfficial, model.ConvRoom:
		return cs.membership == memJoined
	case model.ConvDM:
		return cs.tracked
	default:
		return true
	}
}

// inChannel reports whether the session is joined to, or joining, a
// channel/room. It is the gate for channel-scoped frames: per docs/fchat.md,
// state for a channel we are not in is ignored rather than applied and hidden.
func (cs *convState) inChannel() bool {
	return cs.membership == memJoined || cs.membership == memJoining
}

// channelConv resolves a channel frame to an existing conversation only when
// the session is in (or joining) it, so frames for a channel we are not in
// cannot create or mutate conversation state.
func (s *Session) channelConv(channel string) (*convState, bool) {
	cs, ok := s.st.convs[convKey(convRefForChannel(channel))]
	if !ok || !cs.inChannel() {
		return nil, false
	}
	return cs, true
}

// nameKey normalizes a character name for the session's case-insensitive maps.
// F-Chat treats names case-insensitively and different frames may spell the
// same character differently; normalizing on ingest keeps one entry each.
func nameKey(name string) string { return strings.ToLower(name) }

// convKey normalizes a conversation for the session's case-insensitive maps.
// F-Chat treats official channel names and room ADH-... ids case-insensitively
// (the server lowercases channel lookups), and some state frames echo the
// caller's casing. Keying channels without regard to case keeps a lowercase
// adh-... frame on the same conversation as the canonical ADH-... room instead
// of forking it. DMs and broadcasts keep their exact ids.
func convKey(ref model.ConvRef) string {
	switch ref.Kind {
	case model.ConvOfficial, model.ConvRoom:
		return string(ref.Kind) + ":" + strings.ToLower(ref.ID)
	default:
		return ref.Key()
	}
}

// state is the session's authoritative state. It is only mutated from the
// session actor goroutine; external readers go through request channels.
type state struct {
	conn      string // connecting | live | disconnected
	reason    string
	severity  string
	autoRetry bool
	phase     string // idle | idn_sent | identified | ready

	vars serverVars

	// selfName is this session's own character; its presence lives in roster
	// like everyone else's. selfKey is its normalized form, precomputed because
	// the room-role and op lookups compare against it.
	selfName string
	selfKey  string
	// roster is the authoritative character registry. LIS seeds it, NLN/STA
	// upsert presence, FLN marks offline, and it holds the canonical spelling
	// of every name the session has seen (members, friends, ops). Keyed by
	// nameKey so protocol case differences cannot split an entry.
	roster  map[string]model.PresencePayload
	admins  map[string]bool
	friends map[string]bool
	ignores map[string]bool

	convs  map[string]*convState
	typing map[string]map[string]time.Time

	// invites are pending room invitations keyed by convKey. They are delivered
	// once by the server (there is no query), session-scoped, and dropped when
	// accepted or dismissed.
	invites map[string]model.RoomInvite

	// search is the cached, enriched FKS result set (latest-wins) and searchRev
	// is its revision. Presence in a row is a snapshot taken when the reply
	// arrived, never refreshed: the result set is a point-in-time match against
	// the online roster. It is connection-scoped, so a disconnect clears it.
	search    []model.MemberInfo
	searchRev uint64

	// convSeq is the highest assigned conv_seq per conversation. It is seeded
	// from the store when a conversation is first seen, so sequence numbers
	// stay monotonic across restarts and history cursors do not collide.
	convSeq   map[string]uint64
	seqLoaded map[string]bool

	// selfStatusText is the raw status message the user last submitted through
	// set_status. It is ephemeral: never persisted and never re-emitted on
	// login. The status editor reads it through the snapshot to prefill and
	// re-send the text. Status itself lives in the roster like any presence.
	selfStatusText string
}

func newState(self string) *state {
	st := &state{
		conn:      "idle",
		phase:     "idle",
		selfName:  self,
		selfKey:   nameKey(self),
		roster:    map[string]model.PresencePayload{},
		admins:    map[string]bool{},
		friends:   map[string]bool{},
		ignores:   map[string]bool{},
		convs:     map[string]*convState{},
		typing:    map[string]map[string]time.Time{},
		invites:   map[string]model.RoomInvite{},
		search:    []model.MemberInfo{},
		convSeq:   map[string]uint64{},
		seqLoaded: map[string]bool{},
	}
	st.roster[nameKey(self)] = model.PresencePayload{Character: self}
	return st
}

// --- state helpers (actor-only) ---

func (s *Session) ensureConv(ref model.ConvRef) *convState {
	key := convKey(ref)
	cs, ok := s.st.convs[key]
	if !ok {
		// The first spelling seen wins; later frames with a different casing
		// resolve to this same convState and keep cs.ref.
		cs = &convState{ref: ref, members: map[string]bool{}, ops: map[string]bool{}, admin: roomAdmin{bans: map[string]roomBan{}}}
		s.st.convs[key] = cs
		s.seedSeq(key, ref)
	}
	return cs
}

// seedSeq loads the conversation's highest stored conv_seq once, so sequence
// assignment continues where the store left off.
func (s *Session) seedSeq(key string, ref model.ConvRef) {
	if s.st.seqLoaded[key] || s.cfg.Store == nil {
		s.st.seqLoaded[key] = true
		return
	}
	if max, err := s.cfg.Store.MaxConvSeq(context.Background(), s.cfg.Character, ref); err == nil {
		s.st.convSeq[key] = max
	}
	s.st.seqLoaded[key] = true
}

// touch ensures name has a roster entry, preserving the first spelling seen.
// Membership, friend, and op frames reference characters by name only; the
// roster is where that name and any known presence are resolved.
func (s *Session) touch(name string) model.PresencePayload {
	if name == "" {
		return model.PresencePayload{}
	}
	p := s.st.roster[nameKey(name)]
	if p.Character == "" {
		p.Character = name
		s.st.roster[nameKey(name)] = p
	}
	return p
}

// putPresence merges a presence update into the authoritative roster. Non-empty
// gender and status override the previous value; StatusMsg is set-to. The
// hydration burst emits nothing, so emit is explicit.
func (s *Session) putPresence(name, gender, status, statusMsg string, online, emit bool) model.PresencePayload {
	if name == "" {
		return model.PresencePayload{}
	}
	key := nameKey(name)
	p := s.st.roster[key]
	if p.Character == "" {
		p.Character = name
	}
	if gender != "" {
		p.Gender = gender
	}
	if status != "" {
		p.Status = status
	}
	p.StatusMsg = statusMsg
	p.Online = online
	s.st.roster[key] = p
	if emit {
		s.emitPresence(p)
	}
	return p
}

func (s *Session) setPresence(name, gender, status, statusMsg string) {
	s.putPresence(name, gender, status, statusMsg, true, true)
}

// setPresenceQuiet updates the roster without emitting. The initial LIS
// hydration burst uses it: initial presence reaches clients inline in member
// lists, and only later NLN/FLN/STA changes stream (scoped by the broker).
func (s *Session) setPresenceQuiet(name, gender, status, statusMsg string) {
	s.putPresence(name, gender, status, statusMsg, true, false)
}

// markOffline keeps the last known gender (offline character links keep their
// color) but clears status/statusMsg, which are meaningless offline and would
// otherwise render a stale "Online" line under an offline mark. NLN re-supplies
// status on return.
func (s *Session) markOffline(name string) {
	key := nameKey(name)
	p := s.touch(name)
	p.Online = false
	p.Status = ""
	p.StatusMsg = ""
	s.st.roster[key] = p
	s.emitPresence(p)
}

// selfPresence returns this session's own roster entry with its global-admin
// flag resolved, so the snapshot agrees with the streamed presence events.
func (s *Session) selfPresence() model.PresencePayload {
	p, ok := s.st.roster[nameKey(s.st.selfName)]
	if !ok {
		p.Character = s.st.selfName
	}
	p.Admin = s.st.admins[nameKey(p.Character)]
	return p
}

// emitPresence publishes a roster entry with its admin flag resolved now, so a
// later ADL is not hidden behind a payload that baked in an earlier answer.
func (s *Session) emitPresence(p model.PresencePayload) {
	p.Admin = s.st.admins[nameKey(p.Character)]
	s.emitState(model.CharacterKey(p.Character), s.delivery.Presence(p))
}

func (s *Session) applyTyping(ref model.ConvRef, character string, on, paused bool) {
	key := convKey(ref)
	set := s.st.typing[key]
	if on {
		if set == nil {
			set = map[string]time.Time{}
			s.st.typing[key] = set
		}
		set[character] = s.now()
	} else {
		// Turn-off is a no-op when the character was never typing; the caller
		// uses it after every sent DM to retire the sender without a clear TPN.
		if set == nil {
			return
		}
		if _, ok := set[character]; !ok {
			return
		}
		delete(set, character)
		if len(set) == 0 {
			delete(s.st.typing, key)
		}
	}
	s.emitState(model.TypingKey(s.cfg.Character, ref, character), model.TypingPayload{Conv: ref, Character: character, On: on, Paused: on && paused})
}

// typingRef reconstructs the conversation ref for a typing-map key, falling
// back to parsing the key when the conversation is no longer known.
func (s *Session) typingRef(key string) model.ConvRef {
	if cs, ok := s.st.convs[key]; ok {
		return cs.ref
	}
	if i := strings.IndexByte(key, ':'); i >= 0 {
		return model.ConvRef{Kind: model.ConvKind(key[:i]), ID: key[i+1:]}
	}
	return model.ConvRef{}
}

// dropTyping removes typing entries for which match reports true (nil matches
// all) and emits an off event for each, so clients do not keep stale
// indicators across a reconnect or an FLN.
func (s *Session) dropTyping(match func(character string) bool) {
	for key, set := range s.st.typing {
		ref := s.typingRef(key)
		for name := range set {
			if match != nil && !match(name) {
				continue
			}
			delete(set, name)
			s.emitState(model.TypingKey(s.cfg.Character, ref, name), model.TypingPayload{Conv: ref, Character: name, On: false})
		}
		if len(set) == 0 {
			delete(s.st.typing, key)
		}
	}
}

// clearTyping drops all ephemeral typing state and emits an off event for each
// active typist, so clients do not keep stale indicators across a reconnect.
func (s *Session) clearTyping() { s.dropTyping(nil) }

// clearTypingFor retires one character's typing state in every conversation.
// A character that went offline cannot be typing, and its client can no longer
// send the clear, so FLN is the last chance to drop the indicator.
func (s *Session) clearTypingFor(character string) {
	want := nameKey(character)
	s.dropTyping(func(name string) bool { return nameKey(name) == want })
}

func newID(t time.Time) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	// Millisecond prefix plus a random hex tail. strconv avoids the
	// reflection/formatting cost of fmt.Sprintf on this hot path; the random
	// tail keeps IDs unique across restarts.
	return strconv.FormatInt(t.UnixMilli(), 10) + strconv.FormatUint(binary.BigEndian.Uint64(b[:]), 16)
}

// nextSeq assigns the conversation's next persisted sequence. Assignment
// happens here, once, before the entry is both stored and published, so
// history pages and live events share a single cursor. The starting value was
// seeded from the store in ensureConv.
func (s *Session) nextSeq(ref model.ConvRef) uint64 {
	key := convKey(ref)
	s.st.convSeq[key]++
	return s.st.convSeq[key]
}

// Presence search caps, so a broad query cannot return the whole roster.
const (
	defaultPresenceSearchLimit = 100
	maxPresenceSearchLimit     = 500
)

// searchPresenceLocked filters the online roster by name substring, gender,
// and status. All filters are optional; offline characters are excluded.
func (s *Session) searchPresenceLocked(q model.PresenceQuery) []model.MemberInfo {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultPresenceSearchLimit
	}
	if limit > maxPresenceSearchLimit {
		limit = maxPresenceSearchLimit
	}
	needle := strings.ToLower(strings.TrimSpace(q.Query))
	gender := strings.TrimSpace(q.Gender)
	status := strings.TrimSpace(q.Status)

	out := make([]model.MemberInfo, 0, limit)
	for _, p := range s.st.roster {
		if !p.Online {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(p.Character), needle) {
			continue
		}
		if gender != "" && !strings.EqualFold(p.Gender, gender) {
			continue
		}
		if status != "" && !strings.EqualFold(p.Status, status) {
			continue
		}
		out = append(out, s.delivery.Member(model.MemberInfo{
			Name:      p.Character,
			Gender:    p.Gender,
			Status:    p.Status,
			StatusMsg: p.StatusMsg,
			Admin:     s.st.admins[nameKey(p.Character)],
			Online:    true,
		}))
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
