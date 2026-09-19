// Package core wires sessions, persistence, and the broker into a single
// manager. It is the transport-neutral entry point the web layer uses.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"plexo/internal/broker"
	"plexo/internal/config"
	"plexo/internal/fchat"
	"plexo/internal/model"
	"plexo/internal/session"
	"plexo/internal/store"
)

// DialFactory returns a dialer for a specific character. The fake server uses
// this to hand each session an in-memory connection; `cmd/plexo` returns the
// production WebSocket dialer.
type DialFactory func(character string) session.Dialer

// MappingSource loads F-List's character field mapping data (kinks, infotags,
// and list values). A nil source disables the mapping cache, which is the
// default in tests. It is consulted once, in the background at startup.
type MappingSource interface {
	Load(ctx context.Context) (model.MappingList, error)
}

// MappingFunc adapts a function to MappingSource.
type MappingFunc func(ctx context.Context) (model.MappingList, error)

// Load implements MappingSource.
func (f MappingFunc) Load(ctx context.Context) (model.MappingList, error) { return f(ctx) }

// CredentialsPurger deletes persisted F-Chat credentials and updates the
// account state. It lets the manager reset global configuration without
// holding the account directly; *Account implements it.
type CredentialsPurger interface {
	PurgeStoredCredentials(ctx context.Context) error
}

// Config configures the manager.
type Config struct {
	Store    store.Store
	Tickets  fchat.TicketManager
	Dial     DialFactory
	Renderer model.Renderer
	// SessionRenderer, when set, returns a fresh renderer (sharing the master's
	// compiled tables, with its own cache) for each session. A nil value uses
	// Renderer for every session.
	SessionRenderer func() model.Renderer
	Logger          *slog.Logger
	// Settings resolves per-character configuration from the store. A nil
	// provider disables configuration (everything uses defaults).
	Settings *config.Provider
	// Credentials purges persisted F-Chat credentials when global configuration
	// is reset. A nil value falls back to deleting the stored document
	// directly.
	Credentials CredentialsPurger
	// Mapping loads the character field mapping data once at startup. A nil
	// source leaves the mapping cache empty.
	Mapping MappingSource
}

// Manager owns the session registry and routes commands and events.
type Manager struct {
	ctx context.Context
	cfg Config

	mu       sync.Mutex
	sessions map[string]*session.Session
	accounts map[string]string
	broker   *broker.Broker
	cat      channelCatalog

	mapMu      sync.Mutex
	mapping    model.SearchMapping
	mappingSet bool

	// delivery renders raw BBCode to wire-ready payloads for the HTTP read
	// paths. The renderer is fixed, so it is built once.
	delivery model.Delivery
}

// NewManager creates a manager and its broker. When a mapping source is
// configured, the character field mapping data is loaded in the background; a
// failure is logged and leaves the cache empty, since the data is optional.
func NewManager(ctx context.Context, cfg Config) *Manager {
	m := &Manager{
		ctx:      ctx,
		cfg:      cfg,
		sessions: map[string]*session.Session{},
		accounts: map[string]string{},
		broker:   broker.New(),
		delivery: model.NewDelivery(cfg.Renderer),
	}
	m.broker.SetViewBuilder(m)
	if cfg.Mapping != nil {
		go m.loadMapping(ctx)
	}
	return m
}

// loadMapping fetches the character field mapping data once, reduces it to the
// search-interface shape, and caches it.
func (m *Manager) loadMapping(ctx context.Context) {
	list, err := m.cfg.Mapping.Load(ctx)
	if err != nil {
		if m.cfg.Logger != nil {
			m.cfg.Logger.Warn("character mapping unavailable", "err", err)
		}
		return
	}
	search := fchat.BuildSearchMapping(list)
	m.mapMu.Lock()
	m.mapping = search
	m.mappingSet = true
	m.mapMu.Unlock()
}

// Mapping returns the cached search-interface mapping (one field per FKS filter)
// and whether it has loaded. It is core-wide, read-only, and never persisted.
func (m *Manager) Mapping() (model.SearchMapping, bool) {
	m.mapMu.Lock()
	defer m.mapMu.Unlock()
	return m.mapping, m.mappingSet
}

// Broker exposes the event broker for subscriptions.
func (m *Manager) Broker() *broker.Broker { return m.broker }

// Ads returns one session's buffered LRP advertisements, oldest first. Ads are
// per-session (each character joins different channels) and ephemeral.
func (m *Manager) Ads(character string) []model.Ad {
	s, err := m.session(character)
	if err != nil {
		return nil
	}
	return s.Ads()
}

// SearchPresence searches one session's online roster on demand. Live presence
// delivery is scoped; this is the way to query the full roster.
func (m *Manager) SearchPresence(session string, q model.PresenceQuery) ([]model.MemberInfo, error) {
	s, err := m.session(session)
	if err != nil {
		return nil, err
	}
	return s.SearchPresence(q), nil
}

// Search queues an FKS on one session. It reports acceptance only; the result
// set is cached on the session and announced as a search event.
func (m *Manager) Search(session string, q model.SearchQuery) (model.Result, error) {
	s, err := m.session(session)
	if err != nil {
		return model.Result{}, err
	}
	return s.Search(q), nil
}

// SearchResults returns one session's cached FKS result set for GET
// /api/search.
func (m *Manager) SearchResults(session string) (model.SearchPayload, error) {
	s, err := m.session(session)
	if err != nil {
		return model.SearchPayload{}, err
	}
	return s.SearchResults(), nil
}

// Store exposes the persistence layer.
func (m *Manager) Store() store.Store { return m.cfg.Store }

// ReloadConfig re-reads every running session's effective configuration from
// the store and applies it in place. It is how `c`/SIGHUP and the settings API
// push changes to live sessions without a reconnect. Future logins already
// resolve the same way in Login.
func (m *Manager) ReloadConfig(ctx context.Context) error {
	if m.cfg.Settings == nil {
		return nil
	}
	m.mu.Lock()
	sessions := make([]*session.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		c, _, err := m.cfg.Settings.Character(ctx, s.Character())
		if err != nil {
			return err
		}
		s.SetSettings(c)
	}
	return nil
}

// Subscribe registers a subscriber with default options.
func (m *Manager) Subscribe() *broker.Subscription {
	return m.broker.Subscribe(broker.DefaultSubOpts())
}

// sessionKey normalizes a character name for registry lookups. Sessions are
// keyed case-insensitively so two spellings of one character cannot both log
// in; the session itself keeps the client's spelling.
func sessionKey(character string) string { return strings.ToLower(character) }

// Login starts a session for a character.
func (m *Manager) Login(account, character string) error {
	// Resolve configuration before taking the registry lock: a store read must
	// not block other logins. A failed read aborts the login rather than
	// starting a session with silently wrong settings. The whole document is
	// handed to the session, which applies whatever fields it knows.
	var settings config.Character
	if m.cfg.Settings != nil {
		c, _, err := m.cfg.Settings.Character(m.ctx, character)
		if err != nil {
			return err
		}
		settings = c
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	key := sessionKey(character)
	if _, ok := m.sessions[key]; ok {
		return fmt.Errorf("core: character %q already logged in", character)
	}
	dial := m.cfg.Dial(character)
	renderer := m.cfg.Renderer
	if m.cfg.SessionRenderer != nil {
		if r := m.cfg.SessionRenderer(); r != nil {
			renderer = r
		}
	}
	s := session.New(session.Config{
		Character:     character,
		Account:       account,
		ClientName:    "Plexo",
		ClientVersion: "0.1",
		Dial:          dial,
		Tickets:       m.cfg.Tickets,
		Store:         m.cfg.Store,
		Broker:        m.broker,
		Renderer:      renderer,
		Logger:        m.cfg.Logger,
		Settings:      settings,
		OnStale:       m.onStale,
		OnCatalog:     m.onCatalog,
		OnRoom:        m.onRoom,
	})
	s.Start(m.ctx)
	m.sessions[key] = s
	m.accounts[key] = account
	return nil
}

// Logout stops and removes a session.
func (m *Manager) Logout(character string) {
	m.mu.Lock()
	s, ok := m.sessions[sessionKey(character)]
	if ok {
		delete(m.sessions, sessionKey(character))
		delete(m.accounts, sessionKey(character))
	}
	m.mu.Unlock()
	if ok {
		s.Stop()
		// Forget the broker's per-session state and tell clients to drop the
		// subtree. The removal is published after the drop so its tombstone is the
		// only record left, letting a later resync deliver it.
		m.broker.DropSession(character)
		m.broker.Publish(model.Event{
			Kind:    model.EvState,
			Payload: model.StatePayload{Key: model.SessionKey(character), Removed: true},
		})
	}
}

// Session returns a running session.
func (m *Manager) Session(character string) (*session.Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionKey(character)]
	return s, ok
}

// session resolves a character to a running session, or an "unknown session"
// error for the read paths that need one. It is the single lookup rule behind
// Search/SearchPresence/SearchResults/ConvView.
func (m *Manager) session(character string) (*session.Session, error) {
	s, ok := m.Session(character)
	if !ok {
		return nil, fmt.Errorf("core: unknown session %q", character)
	}
	return s, nil
}

// Dispatch routes a command to the owning session. The set_interest op is a
// subscription concern and must be handled by the transport layer.
func (m *Manager) Dispatch(cmd model.Command) model.Result {
	switch cmd.Op {
	case model.OpSetInterest:
		return model.Result{CID: cmd.CID, Accepted: false, ErrorCode: "wrong_layer", ErrorMsg: "set_interest is handled by the subscription"}
	case model.OpReconnect:
		m.mu.Lock()
		account, ok := m.accounts[sessionKey(cmd.Session)]
		m.mu.Unlock()
		if !ok {
			return model.Result{CID: cmd.CID, Accepted: false, ErrorCode: "unknown_session"}
		}
		m.Logout(cmd.Session)
		if err := m.Login(account, cmd.Session); err != nil {
			return model.Result{CID: cmd.CID, Accepted: false, ErrorCode: "reconnect_failed", ErrorMsg: err.Error()}
		}
		return model.Result{CID: cmd.CID, Accepted: true}
	}

	s, ok := m.Session(cmd.Session)
	if !ok {
		return model.Result{CID: cmd.CID, Accepted: false, ErrorCode: "unknown_session"}
	}
	return s.Send(cmd)
}

// Snapshot returns sessions and conversation summaries only; histories are
// materialized on demand.
func (m *Manager) Snapshot() model.Snapshot {
	m.mu.Lock()
	sessions := make([]*session.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	// Deterministic order: client-facing session order and the account-set pick
	// below both depend on it.
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Character() < sessions[j].Character()
	})

	snap := model.Snapshot{Sessions: make([]model.SessionSnapshot, 0, len(sessions))}
	snap.Catalog = m.catalogSnapshot()
	for _, s := range sessions {
		snap.Sessions = append(snap.Sessions, s.Snapshot())
	}
	sort.Slice(snap.Sessions, func(i, j int) bool {
		return snap.Sessions[i].Character < snap.Sessions[j].Character
	})
	// Friends and ignores are account-wide, so the snapshot carries one copy
	// rather than repeating them on every session. Pick the first sorted session
	// that reports either set, so the result is deterministic and matches the
	// wire order the client used to scan.
	for _, s := range sessions {
		friends, ignores := s.AccountSets()
		if len(friends) > 0 || len(ignores) > 0 {
			snap.Friends = friends
			snap.Ignores = ignores
			break
		}
	}
	return snap
}

// SessionSnapshot returns one session's snapshot, for a targeted resync. The
// bool is false once the session is gone.
func (m *Manager) SessionSnapshot(character string) (model.SessionSnapshot, bool) {
	s, err := m.session(character)
	if err != nil {
		return model.SessionSnapshot{}, false
	}
	return s.Snapshot(), true
}

// RoomInfo returns the on-demand management view of one channel or room. The
// bool is false for an unknown session or a conversation the session is not in.
// It is read over HTTP, never streamed as conversation state.
func (m *Manager) RoomInfo(character string, conv model.ConvRef) (model.RoomInfo, bool) {
	s, err := m.session(character)
	if err != nil {
		return model.RoomInfo{}, false
	}
	return s.RoomInfo(conv)
}

// ConvView implements broker.ViewBuilder: it combines session-owned metadata
// with a window of persisted history. When since is non-zero the client already
// holds history up to that conv_seq; a small gap is returned as a delta window
// (no metadata, which streams separately at summary interest), and a large one
// falls back to a full newest window.
func (m *Manager) ConvView(ctx context.Context, character string, conv model.ConvRef, limit int, since uint64) (model.ConvView, error) {
	s, err := m.session(character)
	if err != nil {
		return model.ConvView{}, err
	}
	meta, _ := s.ConvMeta(conv)

	limit = store.NormalizeLimit(limit)

	if since > 0 {
		delta, err := m.cfg.Store.History(ctx, store.HistoryQuery{
			Session:  character,
			Conv:     conv,
			AfterSeq: &since,
			Limit:    limit + 1,
		})
		if err != nil {
			return model.ConvView{}, err
		}
		if len(delta) <= limit {
			head, err := m.cfg.Store.MaxConvSeq(ctx, character, conv)
			if err != nil {
				return model.ConvView{}, err
			}
			return model.ConvView{
				Session: character,
				Conv:    conv,
				Window:  m.delivery.Window(delta),
				Cursor:  model.Cursor{AsOfSeq: head},
				Delta:   true,
				Role:    meta.Role,
			}, nil
		}
	}

	// Request one extra to detect whether older history exists.
	window, err := m.cfg.Store.History(ctx, store.HistoryQuery{
		Session: character,
		Conv:    conv,
		Limit:   limit + 1,
	})
	if err != nil {
		return model.ConvView{}, err
	}
	window, hasOlder := newestWindow(window, limit)
	head, err := m.cfg.Store.MaxConvSeq(ctx, character, conv)
	if err != nil {
		return model.ConvView{}, err
	}

	view := model.ConvView{
		Session:     character,
		Conv:        conv,
		Title:       meta.Title,
		Description: m.delivery.Status(meta.Description),
		Mode:        meta.Mode,
		Members:     m.delivery.Members(meta.Members),
		Window:      m.delivery.Window(window),
		Cursor:      model.Cursor{AsOfSeq: head, HasOlder: hasOlder},
		Role:        meta.Role,
	}
	if len(window) > 0 {
		view.Cursor.OldestSeq = window[0].ConvSeq
	}
	return view, nil
}

// History pages a conversation's persisted timeline, newest-window by default
// or surrounding a cursor. It is read-only and request/response shaped, so the
// web layer serves it over HTTP rather than the live socket. A nil before and
// after returns the newest entries; otherwise the page is anchored at the
// cursor.
func (m *Manager) History(ctx context.Context, session string, conv model.ConvRef, before, after *uint64, limit int) ([]model.RenderedEntry, error) {
	limit = store.NormalizeLimit(limit)
	// A warp conversation is a read-only alias: resolve it to a real conv plus
	// the mark's anchor, and seed a tail page ending at the mark when no cursor
	// is given. See docs/warpmarks.md.
	if conv.Kind == model.ConvWarp {
		resolved, anchor, err := m.resolveWarp(ctx, session, conv.ID)
		if err != nil {
			return nil, err
		}
		if after != nil {
			return nil, fmt.Errorf("%w: a warp conversation has no newer side", ErrWarpmarkInvalid)
		}
		conv = resolved
		if before == nil {
			seq := anchor + 1
			before = &seq
		}
	}
	// Request one extra to report whether more history exists.
	entries, err := m.cfg.Store.History(ctx, store.HistoryQuery{
		Session:   session,
		Conv:      conv,
		BeforeSeq: before,
		AfterSeq:  after,
		Limit:     limit + 1,
	})
	if err != nil {
		return nil, err
	}
	if len(entries) > limit {
		// The extra entry is a sentinel that more history exists. A forward
		// (after) page drops its newest sentinel; the newest/before direction
		// drops its oldest, which is the newestWindow convention.
		if after != nil {
			entries = entries[:limit]
		} else {
			entries, _ = newestWindow(entries, limit)
		}
	}
	return m.delivery.Window(entries), nil
}

// newestWindow trims a limit+1 store page to the newest `limit` entries and
// reports whether an older entry (the sentinel) was present. Shared by ConvView
// and the newest/before direction of History so the paging convention lives in
// one place.
func newestWindow(entries []model.Entry, limit int) ([]model.Entry, bool) {
	if len(entries) > limit {
		return entries[len(entries)-limit:], true
	}
	return entries, false
}

// ErrWarpmarkInvalid marks client input rejected by warpmark validation: an
// empty session, a missing entry id, or an over-long label. The web layer maps
// it to 400.
var ErrWarpmarkInvalid = errors.New("core: invalid warpmark")

// maxWarpmarkLabel is the label cap, matching the settings limits in
// docs/settings.md. A label may be empty; the client renders a default.
const maxWarpmarkLabel = 128

// resolveWarp maps a warp conversation's entry id to the real conversation and
// the marked entry's sequence. It reuses the shared ownership check.
func (m *Manager) resolveWarp(ctx context.Context, session, entryID string) (model.ConvRef, uint64, error) {
	entry, err := m.loadOwnedEntry(ctx, session, entryID)
	if err != nil {
		return model.ConvRef{}, 0, err
	}
	return entry.Conv, entry.ConvSeq, nil
}

// loadOwnedEntry fetches an entry and verifies it belongs to session, so one
// character cannot address another's history through a crafted id. An empty id
// is invalid input; a foreign entry reads as not found. It is the single
// ownership rule for warp resolution and warpmark creation.
func (m *Manager) loadOwnedEntry(ctx context.Context, session, entryID string) (model.Entry, error) {
	if entryID == "" {
		return model.Entry{}, fmt.Errorf("%w: entry id is required", ErrWarpmarkInvalid)
	}
	entry, err := m.cfg.Store.EntryByID(ctx, entryID)
	if err != nil {
		return model.Entry{}, err
	}
	if entry.Session != session {
		return model.Entry{}, store.ErrNotFound
	}
	return entry, nil
}

// Warpmarks lists one session's warpmarks, newest mark first, each with its
// snippet rendered through the uncached path so a one-off list render cannot
// evict the live BBCode cache. A mark whose entry is gone still lists, with
// Missing set and no snippet.
func (m *Manager) Warpmarks(ctx context.Context, session string) ([]model.WarpmarkView, error) {
	if session == "" {
		return nil, fmt.Errorf("%w: session is required", ErrWarpmarkInvalid)
	}
	marks, err := m.cfg.Store.Warpmarks(ctx, session)
	if err != nil {
		return nil, err
	}
	out := make([]model.WarpmarkView, 0, len(marks))
	for _, mk := range marks {
		view := model.WarpmarkView{
			Session:   mk.Session,
			EntryID:   mk.EntryID,
			Label:     mk.Label,
			Conv:      mk.Conv,
			ConvName:  mk.ConvName,
			Speaker:   mk.Speaker,
			CreatedAt: mk.CreatedAt,
			Missing:   mk.Missing,
		}
		if !mk.Missing {
			view.ConvSeq = mk.Seq
			view.HTML = m.delivery.EntryUncachedHTML(mk.Kind, mk.Body, mk.Data)
		}
		out = append(out, view)
	}
	return out, nil
}

// PutWarpmark creates or replaces one session's mark on an entry, snapshotting
// the entry's conversation, speaker, and mark time. The entry must exist and
// belong to session; an unknown or foreign entry is store.ErrNotFound.
func (m *Manager) PutWarpmark(ctx context.Context, session, entryID, label string) error {
	label = strings.TrimSpace(label)
	if len([]rune(label)) > maxWarpmarkLabel {
		return fmt.Errorf("%w: label exceeds %d characters", ErrWarpmarkInvalid, maxWarpmarkLabel)
	}
	entry, err := m.loadOwnedEntry(ctx, session, entryID)
	if err != nil {
		return err
	}
	return m.cfg.Store.WarpmarkPut(ctx, store.Warpmark{
		Session:   session,
		EntryID:   entryID,
		Label:     label,
		Conv:      entry.Conv,
		ConvName:  entry.ConvName,
		Speaker:   entry.Speaker,
		CreatedAt: time.Now(),
	})
}

// DeleteWarpmark removes one session's mark. A missing mark is not an error, so
// a double delete is idempotent.
func (m *Manager) DeleteWarpmark(ctx context.Context, session, entryID string) error {
	if session == "" || entryID == "" {
		return fmt.Errorf("%w: session and entry id are required", ErrWarpmarkInvalid)
	}
	return m.cfg.Store.WarpmarkDelete(ctx, session, entryID)
}

// LogCharacters lists every own character with persisted history. It is the
// starting point of the two-sided log browser, independent of any live session.
func (m *Manager) LogCharacters(ctx context.Context) ([]string, error) {
	return m.cfg.Store.LogCharacters(ctx)
}

// LogConvs lists one own character's conversations with persisted history. It
// is one side of the log browser's two-sided index; the other side is
// LogSessions. The display name is a room's title, otherwise the id.
func (m *Manager) LogConvs(ctx context.Context, session string) ([]model.LogConvRef, error) {
	rows, err := m.cfg.Store.LogConvs(ctx, session)
	if err != nil {
		return nil, err
	}
	return logConvRefs(rows), nil
}

// LogAllConvs lists every conversation across all own characters, deduped by
// kind and case-folded id. It seeds the log browser's conversation list before
// a character is chosen. The display name is a room's title, otherwise the id.
func (m *Manager) LogAllConvs(ctx context.Context) ([]model.LogConvRef, error) {
	rows, err := m.cfg.Store.LogAllConvs(ctx)
	if err != nil {
		return nil, err
	}
	return logConvRefs(rows), nil
}

// logConvRefs maps store log-conversation rows to the delivery shape.
func logConvRefs(rows []store.LogConv) []model.LogConvRef {
	out := make([]model.LogConvRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, model.LogConvRef{Kind: r.Conv.Kind, ID: r.Conv.ID, Name: logDisplayName(r.Conv, r.Name)})
	}
	return out
}

// LogSessions resolves a conversation to the own characters that have history
// in it: the reverse direction of the log browser. kind must be 'dm',
// 'official', or 'room'; id matches conv_id case-insensitively.
func (m *Manager) LogSessions(ctx context.Context, kind model.ConvKind, id string) ([]model.LogSessionConv, error) {
	rows, err := m.cfg.Store.LogSessionsForConv(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	out := make([]model.LogSessionConv, 0, len(rows))
	for _, r := range rows {
		out = append(out, model.LogSessionConv{
			Session: r.Session,
			Kind:    r.Conv.Kind,
			ID:      r.Conv.ID,
			Name:    logDisplayName(r.Conv, r.Name),
		})
	}
	return out, nil
}

// LogCoverage reports one conversation's persisted span so the browser can
// narrow the date range before exporting.
func (m *Manager) LogCoverage(ctx context.Context, session string, conv model.ConvRef) (model.LogCoverage, error) {
	ext, err := m.cfg.Store.LogCoverage(ctx, session, conv)
	if err != nil {
		return model.LogCoverage{}, err
	}
	return model.LogCoverage{
		Count:    ext.Count,
		FirstMs:  ext.FirstMs,
		LastMs:   ext.LastMs,
		FirstSeq: ext.FirstSeq,
		LastSeq:  ext.LastSeq,
		Name:     logDisplayName(conv, ext.Name),
	}, nil
}

// Renderer returns the shared renderer. The export writer uses its uncached
// entrypoint so a large artifact never evicts the live cache.
func (m *Manager) Renderer() model.Renderer { return m.cfg.Renderer }

// logDisplayName picks the readable label for a conversation: a room's recorded
// title, otherwise the id (already readable for channels and DMs).
func logDisplayName(conv model.ConvRef, name string) string {
	if conv.Kind == model.ConvRoom && name != "" {
		return name
	}
	return conv.ID
}
