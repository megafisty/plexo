// Package store persists chat history. F-Chat has no server-side logs or
// search, so this is the sole source of truth.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"plexo/internal/activity"
	"plexo/internal/model"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("store: not found")

// HistoryQuery selects a window of a single conversation's timeline. Results
// are returned in ascending ConvSeq order. When neither Before nor After is
// set, the most recent Limit entries are returned.
type HistoryQuery struct {
	Session   string
	Conv      model.ConvRef
	BeforeSeq *uint64
	AfterSeq  *uint64
	Limit     int
}

// LogConv is one conversation present in the persisted timeline for one
// session. Name is the readable title recorded for rooms (empty when the
// conversation id is already readable).
type LogConv struct {
	Conv model.ConvRef
	Name string
}

// LogSessionConv is one own character's history in one conversation, carrying
// the exact stored conversation id so the export endpoint can be addressed
// without re-deriving the casing the session happened to record.
type LogSessionConv struct {
	Session string
	Conv    model.ConvRef
	Name    string
}

// LogExtent summarizes one conversation's persisted span.
type LogExtent struct {
	Count    int64
	FirstMs  int64
	LastMs   int64
	FirstSeq uint64
	LastSeq  uint64
	// Name is the most recent readable title recorded for a room, empty
	// otherwise.
	Name string
}

// LogRangeQuery selects a page of one conversation's raw timeline entries in
// conv_seq order. AfterSeq is exclusive; zero starts at the first entry.
type LogRangeQuery struct {
	Session  string
	Conv     model.ConvRef
	AfterSeq uint64
	Limit    int
}

// PruneQuery selects timeline entries for a cleanup deletion. Exactly one of
// the three modes applies:
//
//   - Conv set: one conversation (Session required). OlderThanMs == 0 deletes
//     the whole conversation; otherwise only entries older than the cutoff.
//   - DMsOnly set: whole DM conversations matching the one-off rule, i.e. the
//     conversation is inactive before OlderThanMs and holds fewer than
//     MaxEntries entries.
//   - neither: entries older than OlderThanMs in every conversation (Session
//     optionally narrows to one own character).
//
// OlderThanMs is an exclusive epoch-millisecond bound on created_at (or, for
// DMsOnly, on the conversation's last activity).
type PruneQuery struct {
	Session     string
	Conv        *model.ConvRef
	OlderThanMs int64
	DMsOnly     bool
	MaxEntries  int64
}

// PruneResult is the impact of a PruneQuery, whether previewed or applied.
type PruneResult struct {
	Conversations int64
	Entries       int64
	Warpmarks     int64
	BodyBytes     int64
}

// VacuumResult reports a maintenance vacuum. When Vacuumed is false the size
// figures are unchanged and no rewrite happened.
type VacuumResult struct {
	Vacuumed       bool
	BytesBefore    int64
	BytesAfter     int64
	BytesReclaimed int64
}

// Warpmark is a stored, per-character annotation on one timeline entry. It
// snapshots the entry's conversation, speaker, and mark time so the list is
// self-contained, and carries the entry resolved from the timeline when it
// still exists (Missing marks the gap). See docs/warpmarks.md.
type Warpmark struct {
	Session   string
	EntryID   string
	Label     string
	Conv      model.ConvRef
	ConvName  string
	Speaker   string
	CreatedAt time.Time

	// Resolved from timeline_entries; zero and Missing when the entry is gone.
	Seq     uint64
	Kind    string
	Body    string
	Data    json.RawMessage
	Missing bool
}

// Store is the persistence interface. Implementations must be safe for
// concurrent use.
type Store interface {
	// Append durably stores entries. Implementations may batch internally.
	Append(ctx context.Context, entries []model.Entry) error
	// History returns a window of a conversation's timeline.
	History(ctx context.Context, q HistoryQuery) ([]model.Entry, error)
	// MaxConvSeq returns the highest conv_seq stored for a conversation, or 0.
	MaxConvSeq(ctx context.Context, session string, conv model.ConvRef) (uint64, error)
	// ConvSeqMaxima returns the highest conv_seq stored for every conversation
	// of one session, keyed by conversation. It seeds a session's sequence
	// counters in one read instead of one query per conversation.
	ConvSeqMaxima(ctx context.Context, session string) (map[model.ConvRef]uint64, error)
	// ClearHistory deletes every persisted timeline entry.
	ClearHistory(ctx context.Context) error
	// PrunePreview reports what PruneHistory would delete, without writing.
	PrunePreview(ctx context.Context, q PruneQuery) (PruneResult, error)
	// PruneHistory deletes the entries selected by q and rebuilds the log
	// aggregate in one transaction. Warpmarks on the deleted entries cascade.
	PruneHistory(ctx context.Context, q PruneQuery) (PruneResult, error)
	// Vacuum reclaims free pages when enough have accumulated, reporting
	// whether it ran and the before/after database size.
	Vacuum(ctx context.Context) (VacuumResult, error)

	// LogCharacters lists every own character with persisted history, ordered
	// case-insensitively. It is the entry point of the two-sided log index.
	LogCharacters(ctx context.Context) ([]string, error)
	// LogConvs lists one session's conversations with persisted history,
	// ordered by kind and then by id.
	LogConvs(ctx context.Context, session string) ([]LogConv, error)
	// LogAllConvs lists every conversation across all sessions, deduped by kind
	// and case-folded id, for the log browser's global conversation list.
	LogAllConvs(ctx context.Context) ([]LogConv, error)
	// LogSessionsForConv resolves the other side of the log index: the own
	// characters with persisted history in one conversation. kind must be
	// 'dm', 'official', or 'room'; id matches conv_id case-insensitively.
	LogSessionsForConv(ctx context.Context, kind model.ConvKind, id string) ([]LogSessionConv, error)
	// LogCoverage reports the entry count and time/seq extent of one
	// conversation.
	LogCoverage(ctx context.Context, session string, conv model.ConvRef) (LogExtent, error)
	// LogRangeCount counts entries in an inclusive created_at range.
	LogRangeCount(ctx context.Context, session string, conv model.ConvRef, fromMs, toMs int64) (int64, error)
	// LogRangeStart returns the first conv_seq whose created_at is >= fromMs, or
	// zero when none.
	LogRangeStart(ctx context.Context, session string, conv model.ConvRef, fromMs int64) (uint64, error)
	// LogRange reads raw entries in conv_seq order for streaming an export.
	LogRange(ctx context.Context, q LogRangeQuery) ([]model.Entry, error)
	// LogDayCounts returns per-local-day message counts for one conversation over
	// the inclusive range [fromMs, toMs], aligned to tzOffsetMin minutes east of
	// UTC. Empty days are zero-filled, so the result is directly renderable. When
	// selfOnly is set it counts only our own posts, served from the partial self
	// index.
	LogDayCounts(ctx context.Context, session string, conv model.ConvRef, fromMs, toMs int64, tzOffsetMin int, selfOnly bool) ([]activity.Bucket, error)
	// LogActivityRange returns one activity.Point per entry in the inclusive
	// range [fromMs, toMs] in time order, for the activity drilldown. It reads
	// created_at and length(body) only, never the body itself. When selfOnly is
	// set it returns only our own posts.
	LogActivityRange(ctx context.Context, session string, conv model.ConvRef, fromMs, toMs int64, selfOnly bool) ([]activity.Point, error)
	// LogRecentSpeakers returns the speakers of the most recent entries in
	// [sinceMs, ...], newest first, for the scope switch's participation
	// estimate. limit bounds how far back it reads.
	LogRecentSpeakers(ctx context.Context, session string, conv model.ConvRef, sinceMs int64, limit int) ([]string, error)

	// ConfigGet returns the raw JSON document stored under name, or ErrNotFound
	// when none is stored. Keys are owned by the config package (e.g. "!global",
	// or a lowercased character name); the store treats them as opaque.
	ConfigGet(ctx context.Context, name string) ([]byte, error)
	// ConfigPut upserts the raw JSON document stored under name.
	ConfigPut(ctx context.Context, name string, data []byte) error
	// ConfigDelete removes one document.
	ConfigDelete(ctx context.Context, name string) error
	// ConfigClear removes every configuration document.
	ConfigClear(ctx context.Context) error

	// Warpmarks lists one session's warpmarks, newest mark first, each joined
	// with its timeline entry when the entry still exists (Missing when not).
	Warpmarks(ctx context.Context, session string) ([]Warpmark, error)
	// WarpmarkPut upserts a warpmark keyed by (session, entry id).
	WarpmarkPut(ctx context.Context, m Warpmark) error
	// WarpmarkDelete removes one warpmark; a missing mark is not an error.
	WarpmarkDelete(ctx context.Context, session, entryID string) error
	// EntryByID returns one stored timeline entry by id, or ErrNotFound.
	EntryByID(ctx context.Context, id string) (model.Entry, error)

	// Close releases resources.
	Close() error
}

// NormalizeLimit applies the default and maximum history window size.
func NormalizeLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 1000 {
		return 1000
	}
	return limit
}

// normalizeLogRangeLimit applies the default and maximum page size for the
// streaming log export read. It is larger than a history page because the
// caller consumes the whole range and wants few round trips.
func normalizeLogRangeLimit(limit int) int {
	if limit <= 0 {
		return 500
	}
	if limit > 2000 {
		return 2000
	}
	return limit
}

// reverse reverses a slice in place.
func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
