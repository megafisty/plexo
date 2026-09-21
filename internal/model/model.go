// Package model defines Plexo's canonical, transport-neutral event and command
// types. Sessions produce Events; the broker fans them out; the web layer maps
// them to wire envelopes. No F-Chat specifics live here.
package model

import (
	"encoding/json"
	"html"
	"strings"
	"time"
)

// ConvKind classifies a conversation. Official channels are addressed by name,
// rooms by their hash id, and DMs by the character pair.
type ConvKind string

const (
	ConvOfficial  ConvKind = "official"
	ConvRoom      ConvKind = "room"
	ConvDM        ConvKind = "dm"
	ConvBroadcast ConvKind = "broadcast"
	// ConvWarp is a client-facing, read-only alias of a real conversation,
	// addressed "warp:<entry id>". It is never registered, persisted, or
	// streamed; the manager resolves it to a real conv plus an anchor. See
	// docs/warpmarks.md.
	ConvWarp ConvKind = "warp"
)

// LogConvKind values are the conversation kinds the log browser can address.
// Broadcasts and warp aliases are never browsable or exportable, so the log
// surface uses this narrower set than ConvKind.
const (
	LogConvOfficial = "official"
	LogConvRoom     = "room"
	LogConvDM       = "dm"
)

// RoomRole is the session's room-scoped authority in one channel or room. It
// covers only authority conferred by the room itself: ownership and the op
// list. Global-moderator status is a property of the character, not the room,
// and is delivered separately as PresencePayload.Admin, so it is deliberately
// not encoded here. It is empty for conversations that are not channels/rooms.
type RoomRole string

const (
	RoomRoleNone  RoomRole = "none"
	RoomRoleMod   RoomRole = "mod"
	RoomRoleOwner RoomRole = "owner"
)

// ConvRef identifies a conversation within a session.
type ConvRef struct {
	Kind ConvKind `json:"kind"`
	ID   string   `json:"id"`
}

// Key returns the composite key "kind:id" used in the client model.
func (c ConvRef) Key() string { return string(c.Kind) + ":" + c.ID }

// DisplayConvName picks the readable label for a conversation: a room's
// recorded title when one is known, otherwise the conversation id (already
// readable for official channels and DMs). It is the single rule behind the
// client-facing log labels and the advertisement channel label.
func DisplayConvName(conv ConvRef, name string) string {
	if conv.Kind == ConvRoom && name != "" {
		return name
	}
	return conv.ID
}

// ParseConvID parses a "kind:id" composite conversation key, the form ConvRef.Key
// produces. ok is false when the separator is missing or the kind is empty.
func ParseConvID(s string) (ConvRef, bool) {
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return ConvRef{}, false
	}
	return ConvRef{Kind: ConvKind(s[:i]), ID: s[i+1:]}, true
}

// ConvRefFromKey extracts the conversation ref encoded in a conv, summary, or
// typing state key of the form
//
//	<namespace>/<session>/<kind:id>[/<name>]
//
// The key is the single source of a state record's scope, so conv/summary/typing
// payloads no longer repeat the ref; the broker's interest gate and the client's
// apply both parse it back here. ok is false when the key carries no well-formed
// kind:id segment.
func ConvRefFromKey(key string) (ConvRef, bool) {
	// Skip the namespace and the session segment.
	first := strings.IndexByte(key, '/')
	if first < 0 {
		return ConvRef{}, false
	}
	rest := key[first+1:]
	second := strings.IndexByte(rest, '/')
	if second < 0 {
		return ConvRef{}, false
	}
	id := rest[second+1:]
	// A typing key appends "/<name>"; the composite id itself carries no '/'.
	if slash := strings.IndexByte(id, '/'); slash >= 0 {
		id = id[:slash]
	}
	return ParseConvID(id)
}

// Interest is a subscriber's interest level in a conversation.
type Interest string

const (
	InterestNone    Interest = "none"
	InterestSummary Interest = "summary"
	InterestFull    Interest = "full"
)

// SessionState enumerates the connection lifecycle states a session reports.
type SessionState string

const (
	SessionConnecting   SessionState = "connecting"
	SessionLive         SessionState = "live"
	SessionDisconnected SessionState = "disconnected"
)

// Severity classifies a disconnect for the client: normal is expected and may
// be retried, severe needs the user's attention.
type Severity string

const (
	SeverityNormal Severity = "normal"
	SeveritySevere Severity = "severe"
)

// EventKind enumerates canonical events.
type EventKind string

const (
	// EvMessage is an append-only stream entry for one (session, conv).
	EvMessage EventKind = "message"
	// EvState is a set-to record addressed by a single flat key. Every update
	// that is not a stream entry or a transient error is one of these; see
	// StatePayload and the State* key namespaces.
	EvState EventKind = "state"
	// EvConvView is a conversation materialization. It is ordered with the
	// stream (a view always precedes the entries that raced it) and never
	// coalesced.
	EvConvView EventKind = "conv_view"
	// EvError is a transient per-command signal. It is never coalesced or
	// resynced.
	EvError EventKind = "error"
)

// State key namespaces. A state record's key encodes its scope:
//
//	account/<name>                          account-wide set (friends, ignores, catalog)
//	session/<character>                     one session's lifecycle
//	conv/<character>/<kind:id>              one conversation's metadata
//	summary/<character>/<kind:id>           one conversation's activity aggregate
//	typing/<character>/<kind:id>/<name>     one typist (ephemeral)
//	character/<name>                        one character's presence
//	search/<character>                      cached search result revision
//	invites/<character>                     pending room invitations (set-to)
const (
	StateAccount   = "account"
	StateSession   = "session"
	StateConv      = "conv"
	StateSummary   = "summary"
	StateTyping    = "typing"
	StateCharacter = "character"
	StateSearch    = "search"
	StateInvites   = "invites"
)

// InvitesKey addresses one session's pending room invitations.

// AccountKey addresses an account-wide set.
func AccountKey(name string) string { return StateAccount + "/" + name }

// SessionKey addresses one session's lifecycle state.
func SessionKey(character string) string { return StateSession + "/" + character }

// ConvKey addresses one conversation's metadata.
func ConvKey(session string, conv ConvRef) string {
	return StateConv + "/" + session + "/" + conv.Key()
}

// SummaryKey addresses one conversation's activity aggregate.
func SummaryKey(session string, conv ConvRef) string {
	return StateSummary + "/" + session + "/" + conv.Key()
}

// TypingKey addresses one typist in one conversation.
func TypingKey(session string, conv ConvRef, character string) string {
	return StateTyping + "/" + session + "/" + conv.Key() + "/" + character
}

// CharacterKey addresses one character's global presence.
func CharacterKey(name string) string { return StateCharacter + "/" + name }

// SearchKey addresses a session's cached search revision.
func SearchKey(session string) string { return StateSearch + "/" + session }

// InvitesKey addresses one session's pending room invitations.
func InvitesKey(session string) string { return StateInvites + "/" + session }

// KeyNamespace returns the leading path segment of a state key.
func KeyNamespace(key string) string {
	if i := strings.IndexByte(key, '/'); i >= 0 {
		return key[:i]
	}
	return key
}

// Event is a single canonical update for one session. Session names the
// reporting character; for a state record the scope lives in the key.
type Event struct {
	// Session is internal fan-out context (broker backlog, state keying). It is
	// not sent over the wire: the client derives it from the payload or the
	// state key, which encode the same scope.
	Session string    `json:"-"`
	Kind    EventKind `json:"kind"`
	// Time is internal ordering context; it is never sent over the wire.
	Time    time.Time `json:"-"`
	Payload any       `json:"payload"`
}

// CoalesceKey returns a key for latest-wins coalescing, or "" if the event must
// never be coalesced away. A state record coalesces on its key; stream entries
// and views never coalesce.
func (e Event) CoalesceKey() string {
	if e.Kind == EvState {
		if p, ok := e.Payload.(StatePayload); ok {
			return p.Key
		}
	}
	return ""
}

// EventType pairs a canonical event kind with a zero value of the payload type
// carried on the wire. EventTypes is the registry cmd/tsgen reflects into the
// client's discriminated Event union; adding an EventKind means adding an entry
// here so the client's union stays exhaustive.
type EventType struct {
	Kind    EventKind
	Payload any
}

// EventTypes lists every canonical event kind and its wire payload type.
var EventTypes = []EventType{
	{Kind: EvMessage, Payload: MessagePayload{}},
	{Kind: EvState, Payload: StatePayload{}},
	{Kind: EvConvView, Payload: ConvView{}},
	{Kind: EvError, Payload: ErrorPayload{}},
}

// Renderer turns a raw entry body (BBCode) into a safe HTML fragment. It is
// implemented by internal/render and injected into sessions and the manager.
type Renderer interface {
	// Render renders a status message or conversation description.
	Render(body string) (string, error)
	// RenderUncached renders a bare BBCode body without touching the cache, for
	// one-off previews that would otherwise evict the live cache's hot entries.
	RenderUncached(body string) (string, error)
	// RenderMessage renders a chat message body, applying the client-side emote
	// convention (a leading "/me" action, otherwise a ": " separator).
	RenderMessage(body string) (string, error)
	// RenderEntry renders a stored entry of the given kind, using the raw server
	// payload for structured kinds and falling back to the chat-message path for
	// plain text ones.
	RenderEntry(kind, body string, data []byte) (string, error)
	// RenderEntryUncached is RenderEntry without the BBCode cache, for one-off
	// artifacts such as a chatlog export.
	RenderEntryUncached(kind, body string, data []byte) (string, error)
}

// renderOrEscape applies fn and falls back to escaped plain text when the
// renderer is absent or fails, so a client never receives unsanitized input.
// It is the one fallback rule behind every Render* helper.
func renderOrEscape(r Renderer, body string, fn func(Renderer) (string, error)) string {
	if r == nil {
		return html.EscapeString(body)
	}
	h, err := fn(r)
	if err != nil {
		return html.EscapeString(body)
	}
	return h
}

// RenderHTML renders body, falling back to escaped plain text when no renderer
// is configured or rendering fails, so a client never receives unsanitized
// input. An empty body short-circuits: there is nothing to parse or escape.
func RenderHTML(r Renderer, body string) string {
	if body == "" {
		return ""
	}
	return renderOrEscape(r, body, func(r Renderer) (string, error) { return r.Render(body) })
}

// RenderUncachedHTML renders a bare BBCode body through the uncached path. It
// is the cache-free analogue of RenderHTML for one-off previews; the fallback
// matches RenderHTML: escaped plain text when no renderer is configured or
// rendering fails.
func RenderUncachedHTML(r Renderer, body string) string {
	if body == "" {
		return ""
	}
	return renderOrEscape(r, body, func(r Renderer) (string, error) { return r.RenderUncached(body) })
}

// RenderMessageHTML renders a chat message body. It uses the renderer's message
// path, which applies the client emote convention (see render.RenderMessage)
// before parsing; the renderer caches the transformed body's parse. The fallback
// matches RenderHTML: escaped plain text when no renderer is configured or
// rendering fails.
func RenderMessageHTML(r Renderer, body string) string {
	if body == "" {
		return ""
	}
	return renderOrEscape(r, body, func(r Renderer) (string, error) { return r.RenderMessage(body) })
}

// RenderEntryHTML renders a stored entry through the renderer's kind-aware
// path. The fallback matches RenderHTML: escaped plain text when no renderer is
// configured or rendering fails, so an entry is never dropped from a window.
func RenderEntryHTML(r Renderer, kind, body string, data []byte) string {
	return renderOrEscape(r, body, func(r Renderer) (string, error) { return r.RenderEntry(kind, body, data) })
}

// RenderEntryUncachedHTML is RenderEntryHTML through the uncached path, for
// chatlog exports that must not touch the shared cache. The fallback matches
// RenderEntryHTML, so a render error never drops an entry from the artifact.
func RenderEntryUncachedHTML(r Renderer, kind, body string, data []byte) string {
	return renderOrEscape(r, body, func(r Renderer) (string, error) { return r.RenderEntryUncached(kind, body, data) })
}

// --- Payloads ---

// MessagePayload carries a persisted timeline entry, rendered for delivery.
type MessagePayload struct {
	// Session names the reporting character. It is carried here rather than on
	// the entry, which the enclosing view/history already scopes and no longer
	// repeats per row.
	Session   string        `json:"session"`
	Conv      ConvRef       `json:"conv"`
	Entry     RenderedEntry `json:"entry"`
	Self      bool          `json:"self"`
	Highlight bool          `json:"highlight,omitempty"`
	// CID, when non-empty, ties a self-authored entry to the command that sent
	// it, so the client can retire exactly the optimistic entry it created
	// instead of guessing by speaker.
	CID string `json:"cid,omitempty"`
}

// StatePayload is a set-to update addressed by a single flat key. Value is the
// namespaced payload (ConvStatePayload, PresencePayload, SessionStatePayload,
// FriendsPayload, ...). Removed marks a key that no longer exists, so a client
// drops it (and, for session/, its whole subtree). The key is both the
// coalescing identity and the unit of resync.
type StatePayload struct {
	Key     string `json:"key"`
	Value   any    `json:"value,omitempty"`
	Removed bool   `json:"removed,omitempty"`
}

// ConvStatePayload is a conversation's set-to metadata. Membership ops are
// carried by the state record itself: Removed marks a conversation the
// character has left or that is gone. A DM carries no member list; its partner
// is implied by the conversation id.
type ConvStatePayload struct {
	Title string `json:"title,omitempty"`
	// Description is the room description, rendered to HTML on the wire. It is
	// sparse rather than set-to so an unchanged description is not resent on
	// every roster or mode update: nil means unchanged and the client keeps its
	// copy, a pointer to "" clears it, and a value replaces it. In session state
	// the pointed-to string is raw BBCode; Delivery renders it once at the
	// boundary.
	Description *string  `json:"description,omitempty"`
	Mode        string   `json:"mode,omitempty"`
	Members     []string `json:"members,omitempty"`
	// Ops is the full room-moderator set, the owner plus the ordinary mods, in
	// set-to form. It can legitimately be empty (a channel with no ops), so it is
	// always present: an omitted field would be indistinguishable from
	// "unchanged" and leave a stale op list on the client.
	Ops []string `json:"ops"`
	// Role is the reporting session's own room-scoped authority, so the client
	// can offer management affordances without a separate fetch. It is set for
	// channels/rooms and omitted otherwise; none is explicit so a demotion is
	// authoritative rather than unknown. Global-moderator status is derived from
	// presence.Admin, not from this field.
	Role RoomRole `json:"role,omitempty"`
}

// SessionStatePayload reports connection lifecycle.
type SessionStatePayload struct {
	State     SessionState `json:"state"` // connecting | live | disconnected
	Reason    string       `json:"reason,omitempty"`
	Severity  Severity     `json:"severity,omitempty"`
	AutoRetry bool         `json:"autoRetry,omitempty"`
}

// PresencePayload describes a character's global presence.
type PresencePayload struct {
	Character string `json:"character"`
	Gender    string `json:"gender,omitempty"`
	Status    string `json:"status,omitempty"`
	// StatusMsg is raw BBCode in session state; delivered payloads carry it
	// rendered to HTML (the client never sees the source).
	StatusMsg string `json:"statusMsg,omitempty"`
	// Admin marks a global F-Chat moderator (from the server's ADL). It is
	// always resolved at emission time, so false is authoritative, not unknown.
	Admin  bool `json:"admin"`
	Online bool `json:"online"`
}

// TypingPayload is ephemeral and never persisted. The conversation and the
// typist are encoded in the state key (TypingKey), so the payload carries only
// the signal. Paused marks the partner's "has entered text but is not currently
// typing" state, so the client can distinguish it from an active typist.
type TypingPayload struct {
	On     bool `json:"on"`
	Paused bool `json:"paused,omitempty"`
}

// ErrorPayload reports a per-command server error that does not end the
// session (for example a rejected join or a flood-limited message). Session-
// ending errors surface as a session_state event with reason/severity instead.
type ErrorPayload struct {
	// Session names the reporting session, so the client can clear a pending
	// conversation open. It is explicit because an error is the one event whose
	// payload does not otherwise carry its session.
	Session string `json:"session,omitempty"`
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// FriendsPayload is the account's split friend and bookmark lists. The chat
// server's FRL frame is the documented union of both; the core classifies it
// using a REST fetch plus realtime-bridge deltas, and the client never manages
// the lists. Entries carry the presence known at hydration so the client can
// seed its character map; later changes stream as presence events. A character
// that is both a friend and a bookmark appears in both lists; the client union
// helper deduplicates.
type FriendsPayload struct {
	Friends   []MemberInfo `json:"friends"`
	Bookmarks []MemberInfo `json:"bookmarks"`
}

// IgnoresPayload is the account's ignore list, set-to semantics.
type IgnoresPayload struct {
	Ignores []string `json:"ignores"`
}

// SearchNotice announces that a session's cached FKS result set changed. It
// carries no rows: the enriched results are pulled over HTTP (GET /api/search)
// so a large payload is never fanned out over the event socket. Revision orders
// notices so a client can discard a fetch that is older than the latest one it
// saw.
type SearchNotice struct {
	Revision uint64 `json:"revision"`
}

// SearchPayload is a session's cached FKS result set, returned by GET
// /api/search. Each result carries the presence the session held when the FKS
// reply arrived (LIS hydrates the whole online roster), so a client renders the
// row statically without merging transient results into its character
// registry. Presence is deliberately not re-enriched: the set is a point-in-
// time match against the online roster, so refreshing it would describe
// characters that no longer match. It is ephemeral and never persisted.
type SearchPayload struct {
	Characters []MemberInfo `json:"characters"`
	Revision   uint64       `json:"revision"`
}

// Ad is an ephemeral LRP advertisement. Ads are not part of any conversation
// timeline: they live in a bounded, in-memory buffer and are never persisted.
// Message is already rendered HTML: the session renders the advertisement's
// BBCode when it arrives, so the client never parses it.
type Ad struct {
	Character  string    `json:"character"`
	Channel    string    `json:"channel"`
	Message    string    `json:"message"`
	ReceivedAt time.Time `json:"receivedAt"`
}

// SummaryPayload gives a non-materialized conversation's aggregate state. The
// conversation is encoded in the state key (SummaryKey).
type SummaryPayload struct {
	Title string `json:"title,omitempty"`
	// Self marks the sender's own copy. Summary is the only activity signal a
	// background (summary-interest) conversation sees, so clients need it to
	// avoid notifying on their own messages.
	Self         bool      `json:"self,omitempty"`
	Highlight    bool      `json:"highlight,omitempty"`
	LastActivity time.Time `json:"lastActivity"`
}

// LogConvRef identifies one conversation in the log index and carries its
// display name: a room's readable title, otherwise the conversation id.
type LogConvRef struct {
	Kind ConvKind `json:"kind"`
	ID   string   `json:"id"`
	Name string   `json:"name"`
}

// LogSessionConv is one own character's history in one conversation. ID is the
// exact stored conversation id (which may differ in casing from another
// session's copy), so the export endpoint can be addressed without re-deriving
// it.
type LogSessionConv struct {
	Session string   `json:"session"`
	Kind    ConvKind `json:"kind"`
	ID      string   `json:"id"`
	Name    string   `json:"name"`
}

// LogCoverage summarizes a conversation's persisted span: entry count, the
// first and last entry's created_at in milliseconds, and its conv_seq bounds.
// A zero count means the conversation has no persisted history. Name is the
// display label (a room's title, otherwise the id).
type LogCoverage struct {
	Count    int64  `json:"count"`
	FirstMs  int64  `json:"firstMs"`
	LastMs   int64  `json:"lastMs"`
	FirstSeq uint64 `json:"firstSeq"`
	LastSeq  uint64 `json:"lastSeq"`
	Name     string `json:"name"`
}

// CleanupResult is the impact of a chatlog cleanup operation, whether previewed
// or applied. Vacuumed and BytesReclaimed are meaningful only on apply.
type CleanupResult struct {
	Conversations  int64 `json:"conversations"`
	Entries        int64 `json:"entries"`
	Warpmarks      int64 `json:"warpmarks"`
	BodyBytes      int64 `json:"bodyBytes"`
	Vacuumed       bool  `json:"vacuumed"`
	BytesReclaimed int64 `json:"bytesReclaimed"`
}

// Entry is a durable, shared timeline item: something other players can also
// see. State (presence/membership) and ephemeral signals (typing) are never
// stored here. It is persisted by store and delivered to the UI.
type Entry struct {
	ID         string  `json:"id"`
	UpstreamID string  `json:"upstreamId,omitempty"`
	Session    string  `json:"session"`
	Conv       ConvRef `json:"conv"`
	ConvName   string  `json:"-"` // readable room title; persistence-only
	ConvSeq    uint64  `json:"convSeq"`
	Kind       string  `json:"kind"`
	Speaker    string  `json:"speaker"`
	Body       string  `json:"body"`
	// Data is the raw server payload for structured entry kinds (currently RLL);
	// nil for plain text kinds. It is persistence-only: the wire carries the
	// rendered HTML, never this, so the client stays free of structured parsing.
	Data       json.RawMessage `json:"-"`
	CreatedAt  time.Time       `json:"createdAt"`
	ReceivedAt time.Time       `json:"receivedAt"`
}

// RenderedEntry is a delivery-only view of an Entry with its body rendered to
// HTML. Storage keeps the un-rendered Entry; rendering happens when an event or
// materialization is built. It still embeds Entry in memory, but MarshalJSON
// omits the raw Body and the per-row scope (session, conv) that the enclosing
// message, view, or history response already carries.
type RenderedEntry struct {
	Entry
	HTML string `json:"html"`
}

// entryWire mirrors the delivered subset of Entry: the fields the client reads,
// with the raw BBCode Body, the persistence-only ConvName/UpstreamID, the
// unused ReceivedAt, and the per-container Session/Conv omitted. Times are epoch
// milliseconds so the client parses a number instead of a date string.
type entryWire struct {
	ID          string `json:"id"`
	ConvSeq     uint64 `json:"convSeq"`
	Kind        string `json:"kind"`
	Speaker     string `json:"speaker"`
	CreatedAtMs int64  `json:"createdAtMs"`
	HTML        string `json:"html"`
}

// MarshalJSON drops the raw BBCode body and the per-row scope so a window does
// not ship a second copy of what its container already carries.
func (r RenderedEntry) MarshalJSON() ([]byte, error) {
	return json.Marshal(entryWire{
		ID:          r.ID,
		ConvSeq:     r.ConvSeq,
		Kind:        r.Kind,
		Speaker:     r.Speaker,
		CreatedAtMs: r.CreatedAt.UnixMilli(),
		HTML:        r.HTML,
	})
}

// MemberInfo is a roster entry included in a conversation view.
type MemberInfo struct {
	Name   string `json:"name"`
	Gender string `json:"gender,omitempty"`
	Status string `json:"status,omitempty"`
	// StatusMsg carries rendered HTML on the wire; MemberInfo is a delivery
	// type, so it never holds raw BBCode.
	StatusMsg string `json:"statusMsg,omitempty"`
	// Admin marks a global F-Chat moderator (from the server's ADL). It is
	// always resolved at emission time, so false is authoritative, not unknown.
	Admin  bool `json:"admin"`
	Online bool `json:"online"`
}

// Cursor bounds a materialized window in conv_seq space.
type Cursor struct {
	AsOfSeq   uint64 `json:"asOfSeq"`
	OldestSeq uint64 `json:"oldestSeq"`
	HasOlder  bool   `json:"hasOlder"`
}

// ConvView is a composite materialization of one conversation. Delta is true
// when Window carries only the entries after the client's cursor (a repeat
// visit): the client merges it into its retained window instead of replacing
// it, and Members may be omitted because conversation metadata streams at
// summary interest too.
type ConvView struct {
	Session string  `json:"session"`
	Conv    ConvRef `json:"conv"`
	Title   string  `json:"title,omitempty"`
	// Description carries rendered HTML, never raw BBCode.
	Description string       `json:"description,omitempty"`
	Mode        string       `json:"mode,omitempty"`
	Members     []MemberInfo `json:"members,omitempty"`
	// Ops is the full room-moderator set (owner plus mods). It mirrors
	// ConvStatePayload.Ops and is set only on a full materialization; a delta
	// view omits it and the client keeps the ops it already holds.
	Ops    []string        `json:"ops,omitempty"`
	Window []RenderedEntry `json:"window"`
	Cursor Cursor          `json:"cursor"`
	Delta  bool            `json:"delta,omitempty"`
	// Role mirrors ConvStatePayload.Role for a channel/room materialization.
	Role RoomRole `json:"role,omitempty"`
}

// WarpmarkView is the delivery view of one warpmark: the stored annotation plus
// the marked entry resolved to a conversation, a sequence, and rendered HTML.
// ConvSeq and HTML are zero/empty and Missing is true when the entry no longer
// exists, so the client can list the mark with its snapshot context but cannot
// open it.
type WarpmarkView struct {
	Session   string    `json:"session"`
	EntryID   string    `json:"entryId"`
	Label     string    `json:"label"`
	Conv      ConvRef   `json:"conv"`
	ConvName  string    `json:"convName,omitempty"`
	Speaker   string    `json:"speaker"`
	CreatedAt time.Time `json:"createdAt"`
	ConvSeq   uint64    `json:"convSeq,omitempty"`
	HTML      string    `json:"html,omitempty"`
	Missing   bool      `json:"missing,omitempty"`
}

// RoomBan is one entry of a room's ban list, as observed by the session. A zero
// ExpiresAtMs is a permanent ban; a non-zero one is a timeout expiry. Banner is
// the character that issued it.
type RoomBan struct {
	Name        string `json:"name"`
	Banner      string `json:"banner,omitempty"`
	ExpiresAtMs int64  `json:"expiresAtMs,omitempty"`
}

// RoomInfo is the on-demand management view of one channel or room, served by
// GET /api/room. It is never streamed as conversation state: owner, ops, bans,
// and the caller's role are fetched only when a management pane opens. Bans are
// the session's best-effort in-memory set (from CBU/CTU broadcasts and local
// unban acks), not an authoritative server read.
type RoomInfo struct {
	Conv  ConvRef `json:"conv"`
	Title string  `json:"title,omitempty"`
	// Description is the rendered HTML shown to clients. RawDescription is the
	// editable BBCode source, so a management pane can prefill its editor and a
	// save does not double-escape; it mirrors SessionSnapshot.selfStatusText.
	Description    string    `json:"description,omitempty"`
	RawDescription string    `json:"rawDescription,omitempty"`
	Mode           string    `json:"mode,omitempty"`
	Owner          string    `json:"owner,omitempty"`
	Ops            []string  `json:"ops"`
	SelfRole       RoomRole  `json:"selfRole"`
	Bans           []RoomBan `json:"bans"`
	CdsMax         int       `json:"cdsMax,omitempty"`
	TitleMax       int       `json:"titleMax,omitempty"`
	// Visibility is the room's published state, "public" or "private", when the
	// session knows it (it changes only through RST, which has no broadcast) and
	// empty when unknown. It is omitted for official channels, which are always
	// public.
	Visibility string `json:"visibility,omitempty"`
}

// RoomInvite is one pending invitation to a room, delivered to the client as a
// session-scoped set-to state list. Name is the room id (ADH-...) and Title its
// display title, exactly what a [session] deep link needs. Invitations are
// one-shot (the server offers no query) and are dropped when accepted or
// dismissed.
type RoomInvite struct {
	Conv      ConvRef `json:"conv"`
	Title     string  `json:"title,omitempty"`
	InvitedBy string  `json:"invitedBy,omitempty"`
}

// InvitesPayload is a session's pending room invitation list, set-to. It is
// published under InvitesKey and also seeded inline in SessionSnapshot, so a
// fresh client sees outstanding invitations without a resync.
type InvitesPayload struct {
	Invites []RoomInvite `json:"invites"`
}

// ConvSummary is the lightweight per-conversation state in a snapshot.
type ConvSummary struct {
	Conv         ConvRef   `json:"conv"`
	Kind         ConvKind  `json:"kind"`
	Title        string    `json:"title,omitempty"`
	LastActivity time.Time `json:"lastActivity"`
	// Role is the reporting session's room-scoped authority, so a freshly loaded
	// client knows which rooms it can manage without any extra request. Omitted
	// for non-channel conversations.
	Role RoomRole `json:"role,omitempty"`
}

// SessionSnapshot is the client-facing state of one session.
type SessionSnapshot struct {
	Character string          `json:"character"`
	State     SessionState    `json:"state"`
	Reason    string          `json:"reason,omitempty"`
	Severity  Severity        `json:"severity,omitempty"`
	AutoRetry bool            `json:"autoRetry,omitempty"`
	Self      PresencePayload `json:"self"`
	// SelfStatusText is the raw status message the user last submitted through
	// set_status. It is ephemeral (never persisted, never re-emitted on login)
	// and exists only so the status editor can prefill and re-send it. It is
	// never rendered by the client; the live status text travels rendered in
	// Self.StatusMsg.
	SelfStatusText string `json:"selfStatusText,omitempty"`
	AdCount        int    `json:"adCount"`
	// ChatMax and PrivMax are the server's byte limits for channel and DM
	// messages (from VAR). They let the client warn before the core rejects a
	// send as too_long. Zero means the server has not reported them yet.
	ChatMax       int           `json:"chatMax,omitempty"`
	PrivMax       int           `json:"privMax,omitempty"`
	Conversations []ConvSummary `json:"conversations"`
	// Invites are this session's pending room invitations, seeded here so a
	// fresh client renders them and streamed thereafter under InvitesKey.
	Invites []RoomInvite `json:"invites"`
}

// Snapshot carries sessions and conversation summaries only; histories are
// materialized on demand. Friends, bookmarks, and ignores are account-wide, so
// they are carried once here rather than repeated on every session.
type Snapshot struct {
	Sessions []SessionSnapshot `json:"sessions"`
	// Friends, Bookmarks, and Ignores are the account's friend/bookmark and
	// ignore lists, with the presence the reporting session's roster holds. They
	// are core-account-wide and identical across sessions, so the snapshot
	// carries one copy shared by every session.
	Friends   []MemberInfo `json:"friends,omitempty"`
	Bookmarks []MemberInfo `json:"bookmarks,omitempty"`
	Ignores   []string     `json:"ignores,omitempty"`
	// Catalog is the core-wide official channel and public room lists. Empty
	// until the first CHA/ORS round trip completes.
	Catalog ChannelCatalogPayload `json:"catalog"`
}

// OfficialChannel is one entry of the core-wide official channel list.
type OfficialChannel struct {
	Name       string `json:"name"`
	Characters int    `json:"characters"`
}

// PublicRoom is one entry of the core-wide public room list.
type PublicRoom struct {
	Name       string `json:"name"`
	Title      string `json:"title"`
	Characters int    `json:"characters"`
}

// ChannelCatalogPayload is the whole catalog; events always carry both lists
// (set-to semantics). The catalog is core-wide, never persisted, and shared by
// all sessions and subscribers.
type ChannelCatalogPayload struct {
	Official []OfficialChannel `json:"official"`
	Rooms    []PublicRoom      `json:"rooms"`
}

// MappingList is F-List's character field mapping data: the lookup tables behind
// a character's profile fields (kinks, infotags, and the list values an infotag
// of type "list" can take). It is core-wide, read-only, fetched once per core
// lifetime, and never persisted. Field names and types mirror the
// mapping-list endpoint, which returns every property as a string.
type MappingList struct {
	Kinks         []MappingKink         `json:"kinks"`
	KinkGroups    []MappingKinkGroup    `json:"kink_groups"`
	Infotags      []MappingInfotag      `json:"infotags"`
	InfotagGroups []MappingInfotagGroup `json:"infotag_groups"`
	ListItems     []MappingListItem     `json:"listitems"`
}

// MappingKink is one kink.
type MappingKink struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	GroupID     string `json:"group_id"`
}

// MappingKinkGroup is one kink group.
type MappingKinkGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// MappingInfotag is one infotag. Type is one of text, number, or list; List
// names the list that populates it when Type is list.
type MappingInfotag struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	List    string `json:"list"`
	GroupID string `json:"group_id"`
}

// MappingInfotagGroup is one infotag group.
type MappingInfotagGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// MappingListItem is one value of a list-populated infotag. Name is the list
// it belongs to (e.g. "gender", "orientation"); Value is the display value.
type MappingListItem struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SearchMapping is the search-interface view of the character field mapping
// data: one SearchField per FKS filter, precomputed once at load so the UI can
// iterate the fields and render a multi-select for each without touching the
// raw mapping tables. The outer key is the FKS payload key and equals
// SearchField.Field. It is core-wide, read-only, and never persisted.
type SearchMapping struct {
	Kinks        SearchField `json:"kinks"`
	Genders      SearchField `json:"genders"`
	Orientations SearchField `json:"orientations"`
	Languages    SearchField `json:"languages"`
	FurryPrefs   SearchField `json:"furryprefs"`
	Roles        SearchField `json:"roles"`
}

// SearchField.IDType values. Kinks are numeric kink ids; every other FKS
// filter takes the option's value string.
const (
	SearchIDNumber = "number"
	SearchIDString = "string"
)

// SearchField is one FKS search filter and the options the UI offers for it.
// Field is the key to place the selection under in the FKS payload.
type SearchField struct {
	Name    string        `json:"name"`
	Field   string        `json:"field"`
	IDType  string        `json:"idtype"`
	Entries []SearchEntry `json:"entries"`
}

// SearchEntry is one selectable option. ID is a number for kinks and the value
// string for the enum filters; Name is what the UI shows.
type SearchEntry struct {
	Name string `json:"name"`
	ID   any    `json:"id"`
}

// Command is a request from the UI. Every user action is one explicit
// primitive; the set of valid ops and who handles them is defined in
// commands.go. F-Chat credentials are carried only by set_credentials and are
// parsed by the web layer directly from the envelope; they never reach this
// struct, so they cannot leak into sessions, logs, or results.
type Command struct {
	CID       string   `json:"cid"`
	Session   string   `json:"session"`
	Character string   `json:"character,omitempty"`
	Account   string   `json:"account,omitempty"`
	Action    string   `json:"action,omitempty"`
	Op        Op       `json:"op"`
	Conv      ConvRef  `json:"conv,omitempty"`
	Body      string   `json:"body,omitempty"`
	Status    string   `json:"status,omitempty"`
	StatusMsg string   `json:"statusMsg,omitempty"`
	Level     Interest `json:"level,omitempty"`
	// Since is set by set_interest: the highest conv_seq the client already
	// holds. When non-zero the broker resumes full interest without a full
	// re-materialization, catching the client up with a delta window instead.
	Since uint64 `json:"since,omitempty"`
	// Tracked is set by set_tracked: true to show a DM in the client's
	// conversation list, false to hide it.
	Tracked bool `json:"tracked,omitempty"`
	// Room is set by room_admin: the action and its parameters. It is nested
	// because the action set shares little shape.
	Room *RoomAdminRequest `json:"room,omitempty"`
}

// RoomAdminRequest is the payload of OpRoomAdmin. Action selects the operation;
// only the fields that action needs are set. The core validates the shape and
// the F-Chat server remains the authority on whether the caller may perform it.
type RoomAdminRequest struct {
	// Action is one of: create, destroy, describe, add_mod, remove_mod, kick,
	// ban, unban, mode, visibility, set_owner, invite, timeout.
	Action string `json:"action"`
	// Character is the target of add_mod/remove_mod/kick/ban/unban/set_owner/
	// invite/timeout.
	Character string `json:"character,omitempty"`
	// Title is the new room's title (create only).
	Title string `json:"title,omitempty"`
	// Description is the new description (describe only).
	Description string `json:"description,omitempty"`
	// Mode is the new message mode (mode only): both, chat, or ads.
	Mode string `json:"mode,omitempty"`
	// Visibility is the new published state (visibility only): public or private.
	Visibility string `json:"visibility,omitempty"`
	// Length is the timeout duration in minutes (timeout only). The wire field
	// is in minutes; the server multiplies by 60.
	Length int `json:"length,omitempty"`
}

// PresenceQuery filters the online roster. Empty fields match everything;
// Limit is clamped by the session. It backs the HTTP presence search.
type PresenceQuery struct {
	Query  string
	Gender string
	Status string
	Limit  int
}

// SearchQuery is an FKS filter set: the POST body of /api/search and the
// shape the core translates into fchat.FKSRequest. Kinks are numeric kink
// ids; the enum filters are the value strings from the mapping. It mirrors
// fchat.FKSRequest but lives in model so the web layer never imports fchat.
type SearchQuery struct {
	Kinks        []int    `json:"kinks"`
	Genders      []string `json:"genders,omitempty"`
	Orientations []string `json:"orientations,omitempty"`
	Languages    []string `json:"languages,omitempty"`
	FurryPrefs   []string `json:"furryprefs,omitempty"`
	Roles        []string `json:"roles,omitempty"`
}

// Spec returns the command's catalog entry.
func (c Command) Spec() (CommandSpec, bool) { return LookupCommand(c.Op) }
