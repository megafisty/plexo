package memstore

import (
	"context"
	"sort"
	"strings"
	"sync"

	"plexo/internal/activity"
	"plexo/internal/model"
	"plexo/internal/store"
)

// MemStore is an in-memory Store used by tests. It implements the same
// semantics as the SQLite store, including cursor-based history queries.
type MemStore struct {
	mu        sync.RWMutex
	entries   []model.Entry
	ids       map[string]bool
	configs   map[string][]byte
	warpmarks map[string]store.Warpmark
}

// New returns an empty in-memory store.
func New() *MemStore {
	return &MemStore{
		ids:       map[string]bool{},
		configs:   map[string][]byte{},
		warpmarks: map[string]store.Warpmark{},
	}
}

func (m *MemStore) Append(_ context.Context, entries []model.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range entries {
		// Match SQLite's INSERT ... ON CONFLICT(id) DO NOTHING: entries are
		// immutable, so a re-append of an existing id is a no-op.
		if m.ids[e.ID] {
			continue
		}
		m.ids[e.ID] = true
		m.entries = append(m.entries, e)
	}
	return nil
}

func (m *MemStore) History(_ context.Context, q store.HistoryQuery) ([]model.Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var matched []model.Entry
	for _, e := range m.entries {
		if e.Session != q.Session || e.Conv != q.Conv {
			continue
		}
		matched = append(matched, e)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ConvSeq < matched[j].ConvSeq })

	limit := store.ResolveHistoryLimit(q.Limit)
	var out []model.Entry
	switch {
	case q.BeforeSeq != nil:
		// newest entries strictly below BeforeSeq, returned ascending
		for i := len(matched) - 1; i >= 0 && len(out) < limit; i-- {
			if matched[i].ConvSeq < *q.BeforeSeq {
				out = append(out, matched[i])
			}
		}
		reverse(out)
	case q.AfterSeq != nil:
		for _, e := range matched {
			if e.ConvSeq > *q.AfterSeq && len(out) < limit {
				out = append(out, e)
			}
		}
	default:
		start := len(matched) - limit
		if start < 0 {
			start = 0
		}
		out = append(out, matched[start:]...)
	}
	return out, nil
}

func (m *MemStore) MaxConvSeq(_ context.Context, session string, conv model.ConvRef) (uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var max uint64
	for _, e := range m.entries {
		if e.Session == session && e.Conv == conv && e.ConvSeq > max {
			max = e.ConvSeq
		}
	}
	return max, nil
}

// ConvSeqMaxima returns the highest conv_seq per conversation for one session,
// mirroring the SQLite grouped query.
func (m *MemStore) ConvSeqMaxima(_ context.Context, session string) (map[model.ConvRef]uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[model.ConvRef]uint64{}
	for _, e := range m.entries {
		if e.Session == session && e.ConvSeq > out[e.Conv] {
			out[e.Conv] = e.ConvSeq
		}
	}
	return out, nil
}

func (m *MemStore) LogCharacters(_ context.Context) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := map[string]bool{}
	out := []string{}
	for _, e := range m.entries {
		if !seen[e.Session] {
			seen[e.Session] = true
			out = append(out, e.Session)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out, nil
}

func (m *MemStore) LogConvs(_ context.Context, session string) ([]store.LogConv, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	type key struct {
		kind model.ConvKind
		id   string
	}
	type acc struct {
		name    string
		nameSeq uint64
	}
	by := map[key]acc{}
	for _, e := range m.entries {
		if e.Session != session {
			continue
		}
		k := key{e.Conv.Kind, e.Conv.ID}
		a := by[k]
		if e.ConvName != "" && (a.nameSeq == 0 || e.ConvSeq > a.nameSeq) {
			a.name = e.ConvName
			a.nameSeq = e.ConvSeq
		}
		by[k] = a
	}
	out := make([]store.LogConv, 0, len(by))
	for k, a := range by {
		out = append(out, store.LogConv{Conv: model.ConvRef{Kind: k.kind, ID: k.id}, Name: a.name})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Conv.Kind != out[j].Conv.Kind {
			return out[i].Conv.Kind < out[j].Conv.Kind
		}
		return out[i].Conv.ID < out[j].Conv.ID
	})
	return out, nil
}

func (m *MemStore) LogAllConvs(_ context.Context) ([]store.LogConv, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	type acc struct {
		kind    model.ConvKind
		id      string
		name    string
		nameSeq uint64
	}
	by := map[string]acc{}
	order := []string{}
	for _, e := range m.entries {
		k := string(e.Conv.Kind) + "\x00" + strings.ToLower(e.Conv.ID)
		a, ok := by[k]
		if !ok {
			a = acc{kind: e.Conv.Kind, id: e.Conv.ID}
			order = append(order, k)
		}
		if e.ConvName != "" && (a.nameSeq == 0 || e.ConvSeq > a.nameSeq) {
			a.name = e.ConvName
			a.nameSeq = e.ConvSeq
		}
		by[k] = a
	}
	out := make([]store.LogConv, 0, len(order))
	for _, k := range order {
		a := by[k]
		out = append(out, store.LogConv{Conv: model.ConvRef{Kind: a.kind, ID: a.id}, Name: a.name})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Conv.Kind != out[j].Conv.Kind {
			return out[i].Conv.Kind < out[j].Conv.Kind
		}
		return strings.ToLower(out[i].Conv.ID) < strings.ToLower(out[j].Conv.ID)
	})
	return out, nil
}

func (m *MemStore) LogSessionsForConv(_ context.Context, kind model.ConvKind, id string) ([]store.LogSessionConv, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	fold := strings.ToLower(id)
	type key struct {
		session string
		id      string
	}
	type acc struct {
		name    string
		nameSeq uint64
	}
	by := map[key]acc{}
	for _, e := range m.entries {
		if e.Conv.Kind != kind || strings.ToLower(e.Conv.ID) != fold {
			continue
		}
		k := key{e.Session, e.Conv.ID}
		a := by[k]
		if e.ConvName != "" && (a.nameSeq == 0 || e.ConvSeq > a.nameSeq) {
			a.name = e.ConvName
			a.nameSeq = e.ConvSeq
		}
		by[k] = a
	}
	out := make([]store.LogSessionConv, 0, len(by))
	for k, a := range by {
		out = append(out, store.LogSessionConv{
			Session: k.session,
			Conv:    model.ConvRef{Kind: kind, ID: k.id},
			Name:    a.name,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !strings.EqualFold(out[i].Session, out[j].Session) {
			return strings.ToLower(out[i].Session) < strings.ToLower(out[j].Session)
		}
		return out[i].Conv.ID < out[j].Conv.ID
	})
	return out, nil
}

func (m *MemStore) LogCoverage(_ context.Context, session string, conv model.ConvRef) (store.LogExtent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var ext store.LogExtent
	var nameSeq uint64
	for _, e := range m.entries {
		if e.Session != session || e.Conv != conv {
			continue
		}
		ms := e.CreatedAt.UnixMilli()
		if ext.Count == 0 || ms < ext.FirstMs {
			ext.FirstMs = ms
		}
		if ext.Count == 0 || ms > ext.LastMs {
			ext.LastMs = ms
		}
		if ext.Count == 0 || e.ConvSeq < ext.FirstSeq {
			ext.FirstSeq = e.ConvSeq
		}
		if e.ConvSeq > ext.LastSeq {
			ext.LastSeq = e.ConvSeq
		}
		if e.ConvName != "" && (nameSeq == 0 || e.ConvSeq > nameSeq) {
			ext.Name = e.ConvName
			nameSeq = e.ConvSeq
		}
		ext.Count++
	}
	return ext, nil
}

func (m *MemStore) LogRangeCount(_ context.Context, session string, conv model.ConvRef, fromMs, toMs int64) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var count int64
	for _, e := range m.entries {
		if e.Session != session || e.Conv != conv {
			continue
		}
		ms := e.CreatedAt.UnixMilli()
		if ms >= fromMs && ms <= toMs {
			count++
		}
	}
	return count, nil
}

func (m *MemStore) LogRangeStart(_ context.Context, session string, conv model.ConvRef, fromMs int64) (uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var start uint64
	for _, e := range m.entries {
		if e.Session != session || e.Conv != conv {
			continue
		}
		if e.CreatedAt.UnixMilli() >= fromMs && (start == 0 || e.ConvSeq < start) {
			start = e.ConvSeq
		}
	}
	return start, nil
}

func (m *MemStore) LogRange(_ context.Context, q store.LogRangeQuery) ([]model.Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	limited := q.Limit
	if limited <= 0 {
		limited = 500
	}
	var out []model.Entry
	for _, e := range m.entries {
		if e.Session != q.Session || e.Conv != q.Conv || e.ConvSeq <= q.AfterSeq {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConvSeq < out[j].ConvSeq })
	if len(out) > limited {
		out = out[:limited]
	}
	return out, nil
}

func (m *MemStore) LogDayCounts(_ context.Context, session string, conv model.ConvRef, fromMs, toMs int64, tzOffsetMin int, selfOnly bool) ([]activity.Bucket, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return activity.DayCounts(m.pointsLocked(session, conv, selfOnly), fromMs, toMs, tzOffsetMin), nil
}

func (m *MemStore) LogActivityRange(_ context.Context, session string, conv model.ConvRef, fromMs, toMs int64, selfOnly bool) ([]activity.Point, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []activity.Point
	for _, p := range m.pointsLocked(session, conv, selfOnly) {
		if p.AtMs >= fromMs && p.AtMs <= toMs {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *MemStore) LogRecentSpeakers(_ context.Context, session string, conv model.ConvRef, sinceMs int64, limit int) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if limit <= 0 {
		return nil, nil
	}
	entries := m.entriesLocked(session, conv)
	out := []string{}
	for i := len(entries) - 1; i >= 0 && len(out) < limit; i-- {
		if entries[i].CreatedAt.UnixMilli() < sinceMs {
			break
		}
		out = append(out, entries[i].Speaker)
	}
	return out, nil
}

// entriesLocked returns one conversation's entries in conv_seq (time) order.
// Callers hold at least a read lock.
func (m *MemStore) entriesLocked(session string, conv model.ConvRef) []model.Entry {
	var matched []model.Entry
	for _, e := range m.entries {
		if e.Session == session && e.Conv == conv {
			matched = append(matched, e)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ConvSeq < matched[j].ConvSeq })
	return matched
}

// pointsLocked reduces one conversation's entries to activity points. Callers
// hold at least a read lock.
func (m *MemStore) pointsLocked(session string, conv model.ConvRef, selfOnly bool) []activity.Point {
	var out []activity.Point
	for _, e := range m.entriesLocked(session, conv) {
		if selfOnly && e.Speaker != session {
			continue
		}
		out = append(out, activity.Point{AtMs: e.CreatedAt.UnixMilli(), BodyLen: len(e.Body)})
	}
	return out
}

func (m *MemStore) ClearHistory(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = nil
	m.ids = map[string]bool{}
	m.warpmarks = map[string]store.Warpmark{}
	return nil
}

func (m *MemStore) PrunePreview(_ context.Context, q store.PruneQuery) (store.PruneResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pruneResultLocked(q), nil
}

func (m *MemStore) PruneHistory(_ context.Context, q store.PruneQuery) (store.PruneResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := m.pruneResultLocked(q)
	if res.Entries == 0 {
		return res, nil
	}
	kept := make([]model.Entry, 0, len(m.entries)-int(res.Entries))
	ids := make(map[string]bool, len(m.ids)-int(res.Entries))
	for _, e := range m.entries {
		if m.pruneMatchLocked(e, q) {
			continue
		}
		kept = append(kept, e)
		ids[e.ID] = true
	}
	m.entries = kept
	m.ids = ids
	// Mirror the SQLite foreign key's cascade: a mark whose entry was pruned
	// is gone too.
	for key, mk := range m.warpmarks {
		if !ids[mk.EntryID] {
			delete(m.warpmarks, key)
		}
	}
	return res, nil
}

// Vacuum is a no-op for the in-memory store; there is no file to rewrite.
func (m *MemStore) Vacuum(context.Context) (store.VacuumResult, error) {
	return store.VacuumResult{}, nil
}

// pruneResultLocked counts the entries a query would delete, the conversations
// they span, and the warpmarks annotating them. The caller holds a lock.
func (m *MemStore) pruneResultLocked(q store.PruneQuery) store.PruneResult {
	var res store.PruneResult
	convs := map[string]bool{}
	for _, e := range m.entries {
		if !m.pruneMatchLocked(e, q) {
			continue
		}
		res.Entries++
		res.BodyBytes += int64(len(e.Body) + len(e.Data))
		convs[e.Session+"\x00"+string(e.Conv.Kind)+"\x00"+e.Conv.ID] = true
	}
	res.Conversations = int64(len(convs))
	for _, mk := range m.warpmarks {
		for _, e := range m.entries {
			if e.ID == mk.EntryID && m.pruneMatchLocked(e, q) {
				res.Warpmarks++
				break
			}
		}
	}
	return res
}

// pruneMatchLocked reports whether one entry is selected by a prune query. The
// caller holds a lock. The DMsOnly branch resolves the conversation's last
// activity and size from the whole entry set, matching the aggregate table.
func (m *MemStore) pruneMatchLocked(e model.Entry, q store.PruneQuery) bool {
	if q.Conv != nil {
		if e.Session != q.Session || e.Conv != *q.Conv {
			return false
		}
		return q.OlderThanMs == 0 || e.CreatedAt.UnixMilli() < q.OlderThanMs
	}
	if q.DMsOnly {
		if e.Conv.Kind != model.ConvDM {
			return false
		}
		if q.Session != "" && e.Session != q.Session {
			return false
		}
		var count, last int64
		for _, o := range m.entries {
			if o.Session != e.Session || o.Conv != e.Conv {
				continue
			}
			count++
			if ms := o.CreatedAt.UnixMilli(); ms > last {
				last = ms
			}
		}
		return last < q.OlderThanMs && count < q.MaxEntries
	}
	if q.Session != "" && e.Session != q.Session {
		return false
	}
	return e.CreatedAt.UnixMilli() < q.OlderThanMs
}

func (m *MemStore) ConfigGet(_ context.Context, name string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.configs[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return append([]byte(nil), data...), nil
}

func (m *MemStore) ConfigPut(_ context.Context, name string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs[name] = append([]byte(nil), data...)
	return nil
}

func (m *MemStore) ConfigDelete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.configs, name)
	return nil
}

func (m *MemStore) ConfigClear(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs = map[string][]byte{}
	return nil
}

func (m *MemStore) Close() error {
	return nil
}

// warpmarkKey is the (session, entry id) identity.
func warpmarkKey(session, entryID string) string { return session + "\x00" + entryID }

func (m *MemStore) Warpmarks(_ context.Context, session string) ([]store.Warpmark, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []store.Warpmark{}
	for _, mk := range m.warpmarks {
		if mk.Session != session {
			continue
		}
		resolved := mk
		found := false
		for _, e := range m.entries {
			if e.ID != mk.EntryID {
				continue
			}
			resolved.Seq = e.ConvSeq
			resolved.Kind = e.Kind
			resolved.Body = e.Body
			resolved.Data = append([]byte(nil), e.Data...)
			found = true
			break
		}
		resolved.Missing = !found
		out = append(out, resolved)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].EntryID > out[j].EntryID
	})
	return out, nil
}

func (m *MemStore) WarpmarkPut(_ context.Context, mk store.Warpmark) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := warpmarkKey(mk.Session, mk.EntryID)
	if prev, ok := m.warpmarks[key]; ok {
		mk.CreatedAt = prev.CreatedAt // edits keep the original mark time
	}
	m.warpmarks[key] = mk
	return nil
}

func (m *MemStore) WarpmarkDelete(_ context.Context, session, entryID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.warpmarks, warpmarkKey(session, entryID))
	return nil
}

func (m *MemStore) EntryByID(_ context.Context, id string) (model.Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.entries {
		if e.ID == id {
			return e, nil
		}
	}
	return model.Entry{}, store.ErrNotFound
}

func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
