package web

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"plexo/internal/activity"
	"plexo/internal/config"
	"plexo/internal/core"
	"plexo/internal/export"
	"plexo/internal/model"
	"plexo/internal/store"
)

// History is the response body for GET /api/history, with rendered entries.
type History struct {
	Session string                `json:"session"`
	Conv    model.ConvRef         `json:"conv"`
	Entries []model.RenderedEntry `json:"entries"`
}

// LogActivityOverview is the day-bucket activity view over a whole
// conversation span. UnitMs is the fixed bucket width (one local day). Scope
// echoes the resolved scope; Participation is set when the scope was decided
// from a participant sample.
type LogActivityOverview struct {
	Scope         activity.Scope          `json:"scope"`
	UnitMs        int64                   `json:"unitMs"`
	Total         int64                   `json:"total"`
	Buckets       []activity.Bucket       `json:"buckets"`
	Participation *activity.Participation `json:"participation,omitempty"`
}

// LogActivityDetail is the drilled-down session view of one bounded range.
// Summary measures the whole range as one span for the "N messages, X active"
// readout; Sessions segments it into roleplay cores and casual chat.
type LogActivityDetail struct {
	Scope    activity.Scope     `json:"scope"`
	Sessions []activity.Session `json:"sessions"`
	Summary  activity.Session   `json:"summary"`
}

// api serves the request/response reads over HTTP instead of the live
// WebSocket, so large or paginated payloads never share the event socket. It is
// guarded by the same session cookie as the UI.
type api struct {
	manager *core.Manager
	auth    *SessionAuth
}

// guard enforces GET plus the session cookie. It reports whether the handler
// should continue.
func (a *api) guard(w http.ResponseWriter, r *http.Request) bool {
	return a.guardMethods(w, r, http.MethodGet)
}

// guardMethods enforces an allowed method plus the session cookie. It reports
// whether the handler should continue.
func (a *api) guardMethods(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	allowed := false
	for _, m := range methods {
		if r.Method == m {
			allowed = true
			break
		}
	}
	if !allowed {
		w.Header().Set("Allow", strings.Join(methods, ", "))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if !a.auth.Authenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

// requireSession parses the required `session` query parameter, writing a 400
// and returning ok=false when it is missing.
func requireSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	session := r.URL.Query().Get("session")
	if session == "" {
		http.Error(w, "session is required", http.StatusBadRequest)
		return "", false
	}
	return session, true
}

// sessionConv parses the `session`, `conv_kind`, and `conv_id` query
// parameters and validates the kind with validKind. It writes a 400 and returns
// ok=false when anything is missing or invalid. It is the single guard for the
// history, coverage, export, and activity endpoints.
func sessionConv(w http.ResponseWriter, r *http.Request, validKind func(model.ConvKind) bool) (string, model.ConvRef, bool) {
	q := r.URL.Query()
	session := q.Get("session")
	conv := model.ConvRef{Kind: model.ConvKind(q.Get("conv_kind")), ID: q.Get("conv_id")}
	if session == "" || conv.ID == "" || !validKind(conv.Kind) {
		http.Error(w, "session and a valid conv_kind/conv_id are required", http.StatusBadRequest)
		return "", model.ConvRef{}, false
	}
	return session, conv, true
}

// history serves GET /api/history?session=&conv_kind=&conv_id=&before_seq=&after_seq=&limit=.
func (a *api) history(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r) {
		return
	}
	q := r.URL.Query()
	session, conv, ok := sessionConv(w, r, validConvKind)
	if !ok {
		return
	}
	before, ok := parseSeq(w, q.Get("before_seq"))
	if !ok {
		return
	}
	after, ok := parseSeq(w, q.Get("after_seq"))
	if !ok {
		return
	}
	limit, ok := parseLimit(w, q.Get("limit"))
	if !ok {
		return
	}
	entries, err := a.manager.History(r.Context(), session, conv, before, after, limit)
	if err != nil {
		writeHistoryError(w, err)
		return
	}
	if entries == nil {
		entries = []model.RenderedEntry{}
	}
	writeJSONCached(w, r, cachePrivateRevalidate, History{Session: session, Conv: conv, Entries: entries})
}

// logCharactersResponse, logConvsResponse, and logSessionsResponse are the
// three shapes of the two-sided log index. Only one field is ever set.
type logCharactersResponse struct {
	Characters []string `json:"characters"`
}

type logConvsResponse struct {
	Conversations []model.LogConvRef `json:"conversations"`
}

type logSessionsResponse struct {
	Characters []model.LogSessionConv `json:"characters"`
}

// logIndex serves GET /api/logs/index, the chatlog browser's two-sided
// navigation index. It resolves one end at a time so the client never has to
// merge lists itself: with no parameters it lists own characters that have
// history; with conversations=1 it lists every conversation across those
// characters; with session it lists one character's conversations; and with
// conv_kind and conv_id it resolves the reverse direction to the own characters
// with history in that conversation. Each reverse result carries the session's
// exact stored id, so it addresses /api/logs/export without re-deriving casing.
// Broadcasts are never listed.
func (a *api) logIndex(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r) {
		return
	}
	q := r.URL.Query()
	session := q.Get("session")
	kind := q.Get("conv_kind")
	convID := q.Get("conv_id")
	all := q.Get("conversations")
	switch {
	case session == "" && kind == "" && convID == "" && all == "":
		chars, err := a.manager.LogCharacters(r.Context())
		if err != nil {
			http.Error(w, "log index unavailable", http.StatusInternalServerError)
			return
		}
		if chars == nil {
			chars = []string{}
		}
		writeJSONCached(w, r, cachePrivateRevalidate, logCharactersResponse{Characters: chars})
	case kind == "" && convID == "" && (session != "") != (all != ""):
		// One side at a time: either one character's conversations or every
		// conversation across all characters.
		var convs []model.LogConvRef
		var err error
		if session != "" {
			convs, err = a.manager.LogConvs(r.Context(), session)
		} else {
			convs, err = a.manager.LogAllConvs(r.Context())
		}
		if err != nil {
			http.Error(w, "log index unavailable", http.StatusInternalServerError)
			return
		}
		if convs == nil {
			convs = []model.LogConvRef{}
		}
		writeJSONCached(w, r, cachePrivateRevalidate, logConvsResponse{Conversations: convs})
	case session == "" && kind != "" && convID != "" && all == "":
		convs, status := a.logSessions(r, kind, convID)
		if status != 0 {
			http.Error(w, http.StatusText(status), status)
			return
		}
		writeJSONCached(w, r, cachePrivateRevalidate, logSessionsResponse{Characters: convs})
	default:
		http.Error(w, "provide session, conversations, or conv_kind and conv_id, but not several", http.StatusBadRequest)
	}
}

// logSessions performs the reverse lookup. It returns the conversations and an
// HTTP status (zero on success).
func (a *api) logSessions(r *http.Request, kind, id string) ([]model.LogSessionConv, int) {
	if !validLogConvKind(model.ConvKind(kind)) {
		return nil, http.StatusBadRequest
	}
	convs, err := a.manager.LogSessions(r.Context(), model.ConvKind(kind), id)
	if err != nil {
		return nil, http.StatusInternalServerError
	}
	return dedupeLogSessions(convs), 0
}

// dedupeLogSessions folds the official/room union and keeps a stable order so
// the client sees one entry per (character, conversation).
func dedupeLogSessions(in []model.LogSessionConv) []model.LogSessionConv {
	out := make([]model.LogSessionConv, 0, len(in))
	seen := map[string]bool{}
	for _, c := range in {
		key := strings.ToLower(c.Session) + "\x00" + string(c.Kind) + "\x00" + strings.ToLower(c.ID)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if !strings.EqualFold(out[i].Session, out[j].Session) {
			return strings.ToLower(out[i].Session) < strings.ToLower(out[j].Session)
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID)
	})
	return out
}

// logCoverage serves GET /api/logs/coverage?session=&conv_kind=&conv_id=. It
// reports the persisted span of one conversation so the browser can narrow the
// export range before committing to it.
func (a *api) logCoverage(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r) {
		return
	}
	session, conv, ok := sessionConv(w, r, validLogConvKind)
	if !ok {
		return
	}
	coverage, err := a.manager.LogCoverage(r.Context(), session, conv)
	if err != nil {
		http.Error(w, "coverage unavailable", http.StatusInternalServerError)
		return
	}
	writeJSONCached(w, r, cachePrivateRevalidate, coverage)
}

// logExport serves GET
// /api/logs/export?session=&conv_kind=&conv_id=&from=&to=&tz=. It streams a
// self-contained HTML chatlog with inline display; the browser renders it and
// the user can save it. from/to are required so an export is never unbounded by
// mistake; tz is the display offset in minutes east of UTC (default 0).
func (a *api) logExport(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r) {
		return
	}
	q := r.URL.Query()
	session, conv, ok := sessionConv(w, r, validLogConvKind)
	if !ok {
		return
	}
	from, ok := parseMillis(w, q.Get("from"), "from")
	if !ok {
		return
	}
	to, ok := parseMillis(w, q.Get("to"), "to")
	if !ok {
		return
	}
	if to < from {
		http.Error(w, "to must not precede from", http.StatusBadRequest)
		return
	}
	tz, ok := parseTZOffset(w, q.Get("tz"))
	if !ok {
		return
	}

	coverage, err := a.manager.LogCoverage(r.Context(), session, conv)
	if err != nil {
		http.Error(w, "export unavailable", http.StatusInternalServerError)
		return
	}
	opts := export.Options{
		Session:     session,
		Conv:        conv,
		FromMs:      from,
		ToMs:        to,
		TzOffsetMin: tz,
		Now:         time.Now(),
		Name:        coverage.Name,
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{
		"filename": export.Filename(opts, opts.Name),
	}))
	if err := export.WriteHTML(r.Context(), w, a.manager.Store(), a.manager.Renderer(), opts); err != nil {
		// The status and part of the body are already sent; the writer left a
		// comment in the artifact, and there is nothing left to change.
		return
	}
}

// maxCleanupBody bounds a cleanup request; it carries a few scalars, so a
// larger body is a client bug.
const maxCleanupBody = 16 << 10

// CleanupRequest is the chatlog cleanup body. Op selects the rule; apply false
// previews. Kind/ID select the conversation for op "conversation".
type CleanupRequest struct {
	Op         string         `json:"op"`
	Session    string         `json:"session"`
	Kind       model.ConvKind `json:"kind"`
	ID         string         `json:"id"`
	Days       int            `json:"days"`
	MaxEntries int            `json:"maxEntries"`
	Apply      bool           `json:"apply"`
}

// logCleanup serves POST /api/logs/cleanup. It previews (apply false) or
// performs (apply true) one cleanup rule and returns the impact, including the
// warpmarks lost and any space reclaimed. It is the Cleanup tab's only write.
func (a *api) logCleanup(w http.ResponseWriter, r *http.Request) {
	if !a.guardMethods(w, r, http.MethodPost) {
		return
	}
	var req CleanupRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCleanupBody)).Decode(&req); err != nil {
		http.Error(w, "invalid cleanup request", http.StatusBadRequest)
		return
	}
	q := core.CleanupQuery{
		Op:         req.Op,
		Session:    req.Session,
		Days:       req.Days,
		MaxEntries: req.MaxEntries,
		Apply:      req.Apply,
	}
	if req.Kind != "" || req.ID != "" {
		q.Conv = &model.ConvRef{Kind: req.Kind, ID: req.ID}
	}
	res, err := a.manager.Cleanup(r.Context(), q)
	if err != nil {
		writeCleanupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// writeCleanupError maps a cleanup failure: rejected input is 400, anything
// else is a server fault.
func writeCleanupError(w http.ResponseWriter, err error) {
	if errors.Is(err, core.ErrCleanupInvalid) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Error(w, "cleanup failed", http.StatusInternalServerError)
}

// logActivity serves GET
// /api/logs/activity?session=&conv_kind=&conv_id=&tz=&scope=
// [&from=&to=&gap_min=], where scope is auto (default), conversation, or self.
// Without from/to it returns the whole conversation's day-bucket counts, the
// overview that says which days are worth drilling into. With both it segments
// that range into activity sessions and reports their intensity. Both modes read
// the store directly; no live session is required.
func (a *api) logActivity(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r) {
		return
	}
	q := r.URL.Query()
	session, conv, ok := sessionConv(w, r, validLogConvKind)
	if !ok {
		return
	}
	tz, ok := parseTZOffset(w, q.Get("tz"))
	if !ok {
		return
	}

	coverage, err := a.manager.LogCoverage(r.Context(), session, conv)
	if err != nil {
		http.Error(w, "activity unavailable", http.StatusInternalServerError)
		return
	}
	scope, participation, ok := a.resolveScope(r, session, conv, coverage, q.Get("scope"))
	if !ok {
		http.Error(w, "invalid scope", http.StatusBadRequest)
		return
	}

	rawFrom, rawTo := q.Get("from"), q.Get("to")
	if rawFrom == "" && rawTo == "" {
		a.logActivityOverview(w, r, session, conv, coverage, scope, participation, tz)
		return
	}
	if rawFrom == "" || rawTo == "" {
		http.Error(w, "from and to must be given together", http.StatusBadRequest)
		return
	}
	from, ok := parseMillis(w, rawFrom, "from")
	if !ok {
		return
	}
	to, ok := parseMillis(w, rawTo, "to")
	if !ok {
		return
	}
	if to < from {
		http.Error(w, "to must not precede from", http.StatusBadRequest)
		return
	}
	if to-from > maxDrilldownMs {
		http.Error(w, "range too large", http.StatusBadRequest)
		return
	}

	cfg := activity.ConfigForScope(scope)
	if raw := q.Get("gap_min"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 240 {
			http.Error(w, "invalid gap_min", http.StatusBadRequest)
			return
		}
		cfg.GapTightMs = int64(v) * 60000
		if cfg.SlackGapMs < 2*cfg.GapTightMs {
			cfg.SlackGapMs = 2 * cfg.GapTightMs
		}
		if cfg.CoreGapMs < 2*cfg.GapTightMs {
			cfg.CoreGapMs = 2 * cfg.GapTightMs
		}
	}

	points, err := a.manager.Store().LogActivityRange(r.Context(), session, conv, from, to, scope == activity.ScopeSelf)
	if err != nil {
		http.Error(w, "activity unavailable", http.StatusInternalServerError)
		return
	}
	sessions := activity.Segment(points, cfg)
	if sessions == nil {
		sessions = []activity.Session{}
	}
	writeJSONCached(w, r, cachePrivateRevalidate, LogActivityDetail{
		Scope:    scope,
		Sessions: sessions,
		Summary:  activity.Summarize(points, from, to, cfg),
	})
}

// logActivityOverview renders the day histogram over a conversation's whole
// persisted span, resolving the span from the coverage aggregate so the client
// does not have to send it. Scope selects whether it counts every speaker or
// only our own posts.
func (a *api) logActivityOverview(w http.ResponseWriter, r *http.Request, session string, conv model.ConvRef, coverage model.LogCoverage, scope activity.Scope, participation activity.Participation, tz int) {
	overview := LogActivityOverview{Scope: scope, UnitMs: activity.DayMs, Buckets: []activity.Bucket{}}
	if participation.Sampled > 0 {
		p := participation
		overview.Participation = &p
	}
	if coverage.Count > 0 {
		buckets, err := a.manager.Store().LogDayCounts(r.Context(), session, conv, coverage.FirstMs, coverage.LastMs, tz, scope == activity.ScopeSelf)
		if err != nil {
			http.Error(w, "activity unavailable", http.StatusInternalServerError)
			return
		}
		overview.Buckets = buckets
		for _, b := range buckets {
			overview.Total += b.Count
		}
	}
	writeJSONCached(w, r, cachePrivateRevalidate, overview)
}

// Scope switch thresholds: participation is sampled over the conversation's
// busiest recent D days, and a log larger than largeRoomEntries with too small a
// sample to judge is treated as a big room.
const (
	scopeSampleDays = 7
	scopeSampleMax  = 5000
	largeRoomEntry  = 20000
)

// resolveScope picks the activity scope for a conversation. An explicit scope
// always wins. Otherwise DMs are conversation scope, official channels are self
// scope, and a room is decided by its recent participants — falling back to its
// size when the sample is too small. ok is false for an unknown scope value.
func (a *api) resolveScope(r *http.Request, session string, conv model.ConvRef, coverage model.LogCoverage, raw string) (activity.Scope, activity.Participation, bool) {
	switch raw {
	case string(activity.ScopeConversation):
		return activity.ScopeConversation, activity.Participation{}, true
	case string(activity.ScopeSelf):
		return activity.ScopeSelf, activity.Participation{}, true
	case "", "auto":
	default:
		return activity.ScopeConversation, activity.Participation{}, false
	}

	switch conv.Kind {
	case model.ConvDM:
		return activity.ScopeConversation, activity.Participation{}, true
	case model.ConvOfficial:
		return activity.ScopeSelf, activity.Participation{}, true
	}
	if coverage.Count == 0 {
		return activity.ScopeConversation, activity.Participation{}, true
	}

	sinceMs := coverage.LastMs - scopeSampleDays*activity.DayMs
	speakers, err := a.manager.Store().LogRecentSpeakers(r.Context(), session, conv, sinceMs, scopeSampleMax)
	if err != nil {
		return activity.ScopeConversation, activity.Participation{}, true
	}
	cfg := activity.DefaultScopeConfig()
	p := activity.MeasureParticipation(speakers, cfg)
	if scope, decided := activity.ChooseScope(p, cfg); decided {
		return scope, p, true
	}
	if coverage.Count > largeRoomEntry {
		return activity.ScopeSelf, p, true
	}
	return activity.ScopeConversation, p, true
}

// maxDrilldownMs bounds a drilldown so a client cannot ask the analytics to
// scan an unbounded span; the overview is the cheap way to choose a sub-range.
const maxDrilldownMs = 31 * activity.DayMs

// parseMillis parses a required epoch-millisecond query value.
func parseMillis(w http.ResponseWriter, raw, field string) (int64, bool) {
	if raw == "" {
		http.Error(w, field+" is required", http.StatusBadRequest)
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		http.Error(w, "invalid "+field, http.StatusBadRequest)
		return 0, false
	}
	return v, true
}

// parseTZOffset parses an optional display timezone as minutes east of UTC,
// defaulting to UTC when absent.
func parseTZOffset(w http.ResponseWriter, raw string) (int, bool) {
	if raw == "" {
		return 0, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < -1440 || v > 1440 {
		http.Error(w, "invalid tz", http.StatusBadRequest)
		return 0, false
	}
	return v, true
}

// validLogConvKind reports whether a conversation kind is browsable/exportable.
// Broadcasts are never part of the log browser.
func validLogConvKind(kind model.ConvKind) bool {
	switch kind {
	case model.ConvOfficial, model.ConvRoom, model.ConvDM:
		return true
	default:
		return false
	}
}

// ads serves GET /api/ads?session=.
func (a *api) ads(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r) {
		return
	}
	session, ok := requireSession(w, r)
	if !ok {
		return
	}
	ads := a.manager.Ads(session)
	if ads == nil {
		ads = []model.Ad{}
	}
	writeJSON(w, http.StatusOK, ads)
}

// presence serves GET /api/presence?session=&q=&gender=&status=&limit=.
func (a *api) presence(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r) {
		return
	}
	q := r.URL.Query()
	session, ok := requireSession(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, q.Get("limit"))
	if !ok {
		return
	}
	results, err := a.manager.SearchPresence(session, model.PresenceQuery{
		Query:  q.Get("q"),
		Gender: q.Get("gender"),
		Status: q.Get("status"),
		Limit:  limit,
	})
	if err != nil {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	if results == nil {
		results = []model.MemberInfo{}
	}
	writeJSON(w, http.StatusOK, results)
}

// mapping serves GET /api/mapping: the core's cached, precomputed search field
// mapping (one field per FKS filter with its selectable options). It is
// read-only, core-wide, fetched once at startup, reduced to the search shape,
// and never persisted, so it is 503 until the load lands.
func (a *api) mapping(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r) {
		return
	}
	mapping, ok := a.manager.Mapping()
	if !ok {
		http.Error(w, "mapping unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSONCached(w, r, cachePrivateRevalidate, mapping)
}

// maxSearchBody bounds a search request body. It carries only filter ids, so a
// larger body is a client bug, not a legitimate query.
const maxSearchBody = 64 << 10

// search serves GET and POST /api/search?session=. POST queues an FKS on the
// session's connection and returns 202; the reply is cached on the session and
// announced as a search event. GET returns that cached result set, so clients
// pull the (large, enriched) rows over HTTP instead of the event socket.
func (a *api) search(w http.ResponseWriter, r *http.Request) {
	if !a.guardMethods(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	session, ok := requireSession(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		payload, err := a.manager.SearchResults(session)
		if err != nil {
			// The manager returns an error only for an unknown session.
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		if payload.Characters == nil {
			payload.Characters = []model.MemberInfo{}
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}
	var q model.SearchQuery
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSearchBody)).Decode(&q); err != nil {
		http.Error(w, "invalid search query", http.StatusBadRequest)
		return
	}
	res, err := a.manager.Search(session, q)
	switch {
	case err != nil:
		// The manager returns an error only for an unknown session.
		http.Error(w, "unknown session", http.StatusNotFound)
	case !res.Accepted:
		http.Error(w, res.ErrorMsg, http.StatusConflict)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// maxSettingsBody bounds a settings request body. The documented caps fit far
// under this; a larger body is a client bug, not a legitimate document.
const maxSettingsBody = 256 << 10

// settings serves GET /api/settings?session=<char>. The session is optional:
// without it the character document is the zero value with HasCharacter false.
func (a *api) settings(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r) {
		return
	}
	view, err := a.manager.Settings(r.Context(), r.URL.Query().Get("session"))
	if err != nil {
		writeSettingsError(w, err)
		return
	}
	writeJSONCached(w, r, cachePrivateRevalidate, view)
}

// settingsGlobal serves PUT and DELETE /api/settings/global.
func (a *api) settingsGlobal(w http.ResponseWriter, r *http.Request) {
	if !a.guardMethods(w, r, http.MethodPut, http.MethodDelete) {
		return
	}
	switch r.Method {
	case http.MethodPut:
		var g config.Global
		if !decodeSettingsBody(w, r, &g) {
			return
		}
		if err := a.manager.SetGlobalSettings(r.Context(), g); err != nil {
			writeSettingsError(w, err)
			return
		}
	case http.MethodDelete:
		if err := a.manager.ResetGlobalSettings(r.Context()); err != nil {
			writeSettingsError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// settingsCharacter serves PUT and DELETE /api/settings/character?session=<char>.
// The character need not be logged in; requiring one is a UI guardrail.
func (a *api) settingsCharacter(w http.ResponseWriter, r *http.Request) {
	if !a.guardMethods(w, r, http.MethodPut, http.MethodDelete) {
		return
	}
	session, ok := requireSession(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodPut:
		var c config.Character
		if !decodeSettingsBody(w, r, &c) {
			return
		}
		if err := a.manager.SetCharacterSettings(r.Context(), session, c); err != nil {
			writeSettingsError(w, err)
			return
		}
	case http.MethodDelete:
		if err := a.manager.ResetCharacterSettings(r.Context(), session); err != nil {
			writeSettingsError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodeSettingsBody reads one settings document. Unknown fields are ignored so
// a newer client can send fields this core does not know yet.
func decodeSettingsBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSettingsBody)).Decode(v); err != nil {
		http.Error(w, "invalid settings document", http.StatusBadRequest)
		return false
	}
	return true
}

// writeSettingsError maps the core settings API's errors to HTTP: a missing
// provider is 503, rejected client input is 400, and anything else is 500.
func writeSettingsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrSettingsUnavailable):
		http.Error(w, "settings unavailable", http.StatusServiceUnavailable)
	case errors.Is(err, config.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, "settings write failed", http.StatusInternalServerError)
	}
}

func validConvKind(kind model.ConvKind) bool {
	switch kind {
	case model.ConvOfficial, model.ConvRoom, model.ConvDM, model.ConvBroadcast, model.ConvWarp:
		return true
	default:
		return false
	}
}

// maxWarpmarkBody bounds a warpmark create body; it carries one label, so a
// larger body is a client bug.
const maxWarpmarkBody = 16 << 10

// warpmarksResponse is the list shape for GET /api/warpmarks.
type warpmarksResponse struct {
	Warpmarks []model.WarpmarkView `json:"warpmarks"`
}

// warpmarks serves the per-character warpmark list and its mutations.
//
//	GET    /api/warpmarks?session=<char>
//	POST   /api/warpmarks   {session, entryId, label}
//	DELETE /api/warpmarks?session=<char>&entryId=<id>
func (a *api) warpmarks(w http.ResponseWriter, r *http.Request) {
	if !a.guardMethods(w, r, http.MethodGet, http.MethodPost, http.MethodDelete) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		marks, err := a.manager.Warpmarks(r.Context(), r.URL.Query().Get("session"))
		if err != nil {
			writeWarpmarkError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, warpmarksResponse{Warpmarks: marks})
	case http.MethodPost:
		var body struct {
			Session string `json:"session"`
			EntryID string `json:"entryId"`
			Label   string `json:"label"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWarpmarkBody)).Decode(&body); err != nil {
			http.Error(w, "invalid warpmark body", http.StatusBadRequest)
			return
		}
		if err := a.manager.PutWarpmark(r.Context(), body.Session, body.EntryID, body.Label); err != nil {
			writeWarpmarkError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		q := r.URL.Query()
		if err := a.manager.DeleteWarpmark(r.Context(), q.Get("session"), q.Get("entryId")); err != nil {
			writeWarpmarkError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// writeWarpmarkError maps warpmark errors: rejected input is 400, an unknown or
// foreign entry is 404, anything else is 500.
func writeWarpmarkError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrWarpmarkInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "unknown entry", http.StatusNotFound)
	default:
		http.Error(w, "warpmark request failed", http.StatusInternalServerError)
	}
}

// writeHistoryError maps a history read failure. A warp alias shares the
// warpmark validation errors; everything else is a server fault.
func writeHistoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrWarpmarkInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "unknown entry", http.StatusNotFound)
	default:
		http.Error(w, "history unavailable", http.StatusInternalServerError)
	}
}

// parseSeq parses an optional uint64 cursor; an empty value yields nil.
func parseSeq(w http.ResponseWriter, raw string) (*uint64, bool) {
	if raw == "" {
		return nil, true
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		http.Error(w, "invalid sequence cursor", http.StatusBadRequest)
		return nil, false
	}
	return &v, true
}

// parseLimit parses an optional non-negative limit; empty or zero means default.
func parseLimit(w http.ResponseWriter, raw string) (int, bool) {
	if raw == "" {
		return 0, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		http.Error(w, "invalid limit", http.StatusBadRequest)
		return 0, false
	}
	return v, true
}
