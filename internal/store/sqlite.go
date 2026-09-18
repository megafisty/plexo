package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"plexo/internal/activity"
	"plexo/internal/model"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS timeline_entries (
  id           TEXT PRIMARY KEY,
  upstream_id  TEXT NOT NULL DEFAULT '',
  session_char TEXT NOT NULL,
  conv_kind    TEXT NOT NULL,
  conv_id      TEXT NOT NULL,
  conv_name    TEXT NOT NULL DEFAULT '',
  conv_seq     INTEGER NOT NULL,
  kind         TEXT NOT NULL,
  speaker      TEXT NOT NULL,
  body         TEXT NOT NULL,
  data         TEXT,
  created_at   INTEGER NOT NULL,
  received_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_entries_cursor
  ON timeline_entries(session_char, conv_kind, conv_id, conv_seq, created_at);
-- Superseded by log_conversations, which now answers every listing/navigation
-- query; keeping these would only add write cost on the append path.
DROP INDEX IF EXISTS idx_entries_speaker;
DROP INDEX IF EXISTS idx_entries_room_name;
DROP INDEX IF EXISTS idx_entries_conv_reverse;
-- log_conversations is a derived aggregate: one row per (session,
-- conversation), maintained on append and truncated with the history. The log
-- browser reads only this table, so browsing never scans the timeline; it is
-- rebuildable from timeline_entries.
CREATE TABLE IF NOT EXISTS log_conversations (
  session_char TEXT NOT NULL,
  conv_kind    TEXT NOT NULL,
  conv_id      TEXT NOT NULL,
  name         TEXT NOT NULL DEFAULT '',
  entry_count  INTEGER NOT NULL,
  first_ms     INTEGER NOT NULL,
  last_ms      INTEGER NOT NULL,
  first_seq    INTEGER NOT NULL,
  last_seq     INTEGER NOT NULL,
  PRIMARY KEY (session_char, conv_kind, conv_id)
);
CREATE INDEX IF NOT EXISTS idx_log_conv_reverse
  ON log_conversations(conv_kind, lower(conv_id), session_char);
-- idx_entries_self is the self-scoped activity view's covering index: own posts
-- store speaker = session_char exactly, so the partial predicate selects just
-- them and the index stays proportional to our posts, not the channel's. It
-- carries created_at (day buckets, drilldown order) and length(body) (volume),
-- so the self queries never touch the table. A query must repeat
-- "speaker = session_char" to match the predicate. See docs/activity.md.
CREATE INDEX IF NOT EXISTS idx_entries_self
  ON timeline_entries(session_char, conv_kind, conv_id, created_at, length(body))
  WHERE speaker = session_char;
CREATE TABLE IF NOT EXISTS configs (
  name       TEXT PRIMARY KEY,
  data       TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
-- warpmarks are private, per-character annotations on stored entries. The row
-- snapshots the entry's conversation/speaker/mark time so the list reads
-- without a join, and the entry is joined back in only to resolve an anchor
-- and render a snippet. Never written by the append path. The foreign key
-- makes a mark share its entry's lifetime: deleting a conversation's entries
-- (ClearHistory, or a future cleanup tool) drops the marks that annotate them.
-- It is created with the table, so a database opened before this feature needs
-- no retrofit and a fresh database gets the constraint. See docs/warpmarks.md.
CREATE TABLE IF NOT EXISTS warpmarks (
  session_char TEXT NOT NULL,
  entry_id     TEXT NOT NULL REFERENCES timeline_entries(id) ON DELETE CASCADE,
  label        TEXT NOT NULL DEFAULT '',
  conv_kind    TEXT NOT NULL,
  conv_id      TEXT NOT NULL,
  conv_name    TEXT NOT NULL DEFAULT '',
  speaker      TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  PRIMARY KEY (session_char, entry_id)
);
CREATE INDEX IF NOT EXISTS idx_warpmarks_session
  ON warpmarks(session_char, created_at);
`

// SQLiteStore is the durable Store implementation. It uses the pure-Go
// modernc.org/sqlite driver so there is no CGO dependency. WAL mode and a
// single connection keep writes serialized.
type SQLiteStore struct {
	db      *sql.DB
	insert  *sql.Stmt
	summary *sql.Stmt
}

// insertEntrySQL is prepared once and reused for every append. The conflict
// clause keeps re-appends idempotent without replacing a row: entries are
// immutable, and the boolean insert result drives the summary upsert.
const insertEntrySQL = `
	INSERT INTO timeline_entries
	  (id, upstream_id, session_char, conv_kind, conv_id, conv_name, conv_seq,
	   kind, speaker, body, data, created_at, received_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO NOTHING`

// upsertSummarySQL folds one newly inserted entry into its conversation's
// aggregate row. min()/max() are SQLite's two-argument scalar functions; the
// name only advances on a non-empty title, so a room keeps its latest title.
const upsertSummarySQL = `
	INSERT INTO log_conversations
	  (session_char, conv_kind, conv_id, name, entry_count, first_ms, last_ms, first_seq, last_seq)
	VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?)
	ON CONFLICT(session_char, conv_kind, conv_id) DO UPDATE SET
	  entry_count = entry_count + 1,
	  first_ms = min(first_ms, excluded.first_ms),
	  last_ms = max(last_ms, excluded.last_ms),
	  first_seq = min(first_seq, excluded.first_seq),
	  last_seq = max(last_seq, excluded.last_seq),
	  name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE name END`

// rebuildSummarySQL repopulates log_conversations from the timeline. It runs
// once when an existing database has entries but no aggregate (a pre-log-browser
// database), so the log browser works without a manual rebuild.
const rebuildSummarySQL = `
	INSERT INTO log_conversations
	  (session_char, conv_kind, conv_id, name, entry_count, first_ms, last_ms, first_seq, last_seq)
	SELECT session_char, conv_kind, conv_id,
	       COALESCE((
	         SELECT t2.conv_name FROM timeline_entries t2
	         WHERE t2.session_char = t.session_char
	           AND t2.conv_kind = t.conv_kind
	           AND t2.conv_id = t.conv_id
	           AND t2.conv_name <> ''
	         ORDER BY t2.conv_seq DESC LIMIT 1
	       ), ''),
	       COUNT(*), MIN(created_at), MAX(created_at), MIN(conv_seq), MAX(conv_seq)
	FROM timeline_entries t
	GROUP BY session_char, conv_kind, conv_id`

// OpenSQLite opens (and creates) a SQLite database at path. A shared-cache
// in-memory database can be requested with ":memory:".
func OpenSQLite(path string) (*SQLiteStore, error) {
	// synchronous=NORMAL is safe under WAL: a crash can lose only the last
	// transaction(s), not corrupt the database, and it avoids an fsync on every
	// message commit, which dominates the session actor's hot path.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// A single writer avoids SQLITE_BUSY churn and matches the design's
	// "one writer" assumption.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: wal: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: schema: %w", err)
	}
	insert, err := db.PrepareContext(context.Background(), insertEntrySQL)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: prepare insert: %w", err)
	}
	summary, err := db.PrepareContext(context.Background(), upsertSummarySQL)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: prepare summary: %w", err)
	}
	s := &SQLiteStore{db: db, insert: insert, summary: summary}
	if err := s.rebuildSummaryIfEmpty(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: rebuild summary: %w", err)
	}
	return s, nil
}

// rebuildSummaryIfEmpty repopulates an empty aggregate from the timeline. A
// healthy database is the common case and costs one count on the tiny aggregate
// table; the timeline is counted only when the aggregate is empty. It is a no-op
// on a fresh database too.
func (s *SQLiteStore) rebuildSummaryIfEmpty(ctx context.Context) error {
	var convs int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM log_conversations`).Scan(&convs); err != nil {
		return err
	}
	if convs > 0 {
		return nil
	}
	var entries int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM timeline_entries`).Scan(&entries); err != nil {
		return err
	}
	if entries == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, rebuildSummarySQL)
	return err
}

func (s *SQLiteStore) Append(ctx context.Context, entries []model.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	// Reuse the prepared statement inside the transaction; tx.StmtContext
	// returns a transaction-scoped statement that is closed with the tx.
	stmt := tx.StmtContext(ctx, s.insert)
	sum := tx.StmtContext(ctx, s.summary)
	for _, e := range entries {
		// NULL, not '', for plain-text kinds so the column stays cheap.
		var data any
		if len(e.Data) > 0 {
			data = string(e.Data)
		}
		res, err := stmt.ExecContext(ctx,
			e.ID, e.UpstreamID, e.Session, e.Conv.Kind, e.Conv.ID, e.ConvName, e.ConvSeq,
			e.Kind, e.Speaker, e.Body, data,
			e.CreatedAt.UnixMilli(), e.ReceivedAt.UnixMilli(),
		)
		if err != nil {
			return err
		}
		inserted, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if inserted == 0 {
			// Already persisted under this id; the aggregate already counts it.
			continue
		}
		ms := e.CreatedAt.UnixMilli()
		if _, err := sum.ExecContext(ctx,
			e.Session, e.Conv.Kind, e.Conv.ID, e.ConvName,
			ms, ms, e.ConvSeq, e.ConvSeq,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) History(ctx context.Context, q HistoryQuery) ([]model.Entry, error) {
	limit := NormalizeLimit(q.Limit)
	query := `SELECT id, upstream_id, session_char, conv_kind, conv_id, conv_name,
	                 conv_seq, kind, speaker, body, data, created_at, received_at
	          FROM timeline_entries
	          WHERE session_char = ? AND conv_kind = ? AND conv_id = ?`
	args := []any{q.Session, q.Conv.Kind, q.Conv.ID}
	descending := false
	switch {
	case q.BeforeSeq != nil:
		query += ` AND conv_seq < ? ORDER BY conv_seq DESC LIMIT ?`
		args = append(args, *q.BeforeSeq, limit)
		descending = true
	case q.AfterSeq != nil:
		query += ` AND conv_seq > ? ORDER BY conv_seq ASC LIMIT ?`
		args = append(args, *q.AfterSeq, limit)
	default:
		query += ` ORDER BY conv_seq DESC LIMIT ?`
		args = append(args, limit)
		descending = true
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Non-nil even when empty so JSON marshals as [] rather than null.
	out := []model.Entry{}
	for rows.Next() {
		var e model.Entry
		var created, received int64
		var data []byte
		if err := rows.Scan(&e.ID, &e.UpstreamID, &e.Session, &e.Conv.Kind, &e.Conv.ID,
			&e.ConvName, &e.ConvSeq, &e.Kind, &e.Speaker, &e.Body, &data, &created, &received); err != nil {
			return nil, err
		}
		if len(data) > 0 {
			e.Data = data
		}
		e.CreatedAt = time.UnixMilli(created)
		e.ReceivedAt = time.UnixMilli(received)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if descending {
		reverse(out)
	}
	return out, nil
}

func (s *SQLiteStore) MaxConvSeq(ctx context.Context, session string, conv model.ConvRef) (uint64, error) {
	var seq sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT MAX(conv_seq) FROM timeline_entries
		WHERE session_char = ? AND conv_kind = ? AND conv_id = ?`,
		session, conv.Kind, conv.ID).Scan(&seq)
	if err != nil {
		return 0, err
	}
	if !seq.Valid {
		return 0, nil
	}
	return uint64(seq.Int64), nil
}

// LogCharacters lists every own character with persisted history.
func (s *SQLiteStore) LogCharacters(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT session_char FROM log_conversations
		ORDER BY lower(session_char)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var session string
		if err := rows.Scan(&session); err != nil {
			return nil, err
		}
		out = append(out, session)
	}
	return out, rows.Err()
}

// LogConvs lists one session's conversations with persisted history, ordered by
// kind and then by id. A session keeps one canonical spelling per conversation,
// so exact id ordering is enough; the display name is the latest non-empty room
// title recorded for it.
func (s *SQLiteStore) LogConvs(ctx context.Context, session string) ([]LogConv, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT conv_kind, conv_id, name FROM log_conversations
		WHERE session_char = ?
		ORDER BY conv_kind, conv_id`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogConv{}
	for rows.Next() {
		var kind, id, name string
		if err := rows.Scan(&kind, &id, &name); err != nil {
			return nil, err
		}
		out = append(out, LogConv{Conv: model.ConvRef{Kind: model.ConvKind(kind), ID: id}, Name: name})
	}
	return out, rows.Err()
}

// LogAllConvs lists every conversation across all sessions, deduped by kind and
// case-folded id. The display name is the most recent non-empty title recorded
// by any session. The table has one row per (session, conversation), so this is
// tiny relative to the timeline.
func (s *SQLiteStore) LogAllConvs(ctx context.Context) ([]LogConv, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT conv_kind, conv_id, name, last_seq
		FROM log_conversations
		ORDER BY conv_kind, lower(conv_id), last_seq DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type acc struct {
		conv    LogConv
		nameSeq int64
	}
	seen := map[string]*acc{}
	order := []string{}
	for rows.Next() {
		var kind, id, name string
		var lastSeq int64
		if err := rows.Scan(&kind, &id, &name, &lastSeq); err != nil {
			return nil, err
		}
		key := kind + "\x00" + strings.ToLower(id)
		a := seen[key]
		if a == nil {
			a = &acc{
				conv:    LogConv{Conv: model.ConvRef{Kind: model.ConvKind(kind), ID: id}},
				nameSeq: -1,
			}
			seen[key] = a
			order = append(order, key)
		}
		if name != "" && lastSeq > a.nameSeq {
			a.conv.Name = name
			a.nameSeq = lastSeq
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]LogConv, 0, len(order))
	for _, key := range order {
		out = append(out, seen[key].conv)
	}
	return out, nil
}

// LogSessionsForConv resolves a conversation to the own characters that have
// history in it, matching conv_id case-insensitively for every kind.
func (s *SQLiteStore) LogSessionsForConv(ctx context.Context, kind model.ConvKind, id string) ([]LogSessionConv, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_char, conv_id, name FROM log_conversations
		WHERE conv_kind = ? AND lower(conv_id) = lower(?)
		ORDER BY lower(session_char), conv_id`, kind, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogSessionConv{}
	for rows.Next() {
		var session, convID, name string
		if err := rows.Scan(&session, &convID, &name); err != nil {
			return nil, err
		}
		out = append(out, LogSessionConv{
			Session: session,
			Conv:    model.ConvRef{Kind: kind, ID: convID},
			Name:    name,
		})
	}
	return out, rows.Err()
}

// LogCoverage reports one conversation's entry count and time/seq extent from
// the aggregate, so it is O(1) regardless of history size.
func (s *SQLiteStore) LogCoverage(ctx context.Context, session string, conv model.ConvRef) (LogExtent, error) {
	var (
		ext                                LogExtent
		firstMs, lastMs, firstSeq, lastSeq int64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT entry_count, first_ms, last_ms, first_seq, last_seq, name
		FROM log_conversations
		WHERE session_char = ? AND conv_kind = ? AND conv_id = ?`,
		session, conv.Kind, conv.ID).Scan(
		&ext.Count, &firstMs, &lastMs, &firstSeq, &lastSeq, &ext.Name)
	if err == sql.ErrNoRows {
		return LogExtent{}, nil
	}
	if err != nil {
		return LogExtent{}, err
	}
	ext.FirstMs = firstMs
	ext.LastMs = lastMs
	ext.FirstSeq = uint64(firstSeq)
	ext.LastSeq = uint64(lastSeq)
	return ext, nil
}

// LogRangeCount counts entries in an inclusive created_at range.
func (s *SQLiteStore) LogRangeCount(ctx context.Context, session string, conv model.ConvRef, fromMs, toMs int64) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM timeline_entries
		WHERE session_char = ? AND conv_kind = ? AND conv_id = ?
		  AND created_at >= ? AND created_at <= ?`,
		session, conv.Kind, conv.ID, fromMs, toMs).Scan(&count)
	return count, err
}

// LogRangeStart returns the first conv_seq with created_at >= fromMs.
func (s *SQLiteStore) LogRangeStart(ctx context.Context, session string, conv model.ConvRef, fromMs int64) (uint64, error) {
	var seq sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT MIN(conv_seq) FROM timeline_entries
		WHERE session_char = ? AND conv_kind = ? AND conv_id = ? AND created_at >= ?`,
		session, conv.Kind, conv.ID, fromMs).Scan(&seq)
	if err != nil {
		return 0, err
	}
	if !seq.Valid {
		return 0, nil
	}
	return uint64(seq.Int64), nil
}

// LogRange reads raw entries in conv_seq order after AfterSeq, for streaming an
// export.
func (s *SQLiteStore) LogRange(ctx context.Context, q LogRangeQuery) ([]model.Entry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, upstream_id, session_char, conv_kind, conv_id, conv_name,
		       conv_seq, kind, speaker, body, data, created_at, received_at
		FROM timeline_entries
		WHERE session_char = ? AND conv_kind = ? AND conv_id = ? AND conv_seq > ?
		ORDER BY conv_seq ASC LIMIT ?`,
		q.Session, q.Conv.Kind, q.Conv.ID, q.AfterSeq, normalizeLogRangeLimit(q.Limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Entry{}
	for rows.Next() {
		var e model.Entry
		var created, received int64
		var data []byte
		if err := rows.Scan(&e.ID, &e.UpstreamID, &e.Session, &e.Conv.Kind, &e.Conv.ID,
			&e.ConvName, &e.ConvSeq, &e.Kind, &e.Speaker, &e.Body, &data, &created, &received); err != nil {
			return nil, err
		}
		if len(data) > 0 {
			e.Data = data
		}
		e.CreatedAt = time.UnixMilli(created)
		e.ReceivedAt = time.UnixMilli(received)
		out = append(out, e)
	}
	return out, rows.Err()
}

// LogDayCounts buckets one conversation's entries by local day. The bucket
// expression is computed over the covering index (session_char, conv_kind,
// conv_id, conv_seq, created_at) — or the partial self index when selfOnly is
// set — so no body or table row is read. Days with no messages are zero-filled
// so the client can index the result directly.
func (s *SQLiteStore) LogDayCounts(ctx context.Context, session string, conv model.ConvRef, fromMs, toMs int64, tzOffsetMin int, selfOnly bool) ([]activity.Bucket, error) {
	off := int64(tzOffsetMin) * 60000
	startDay := floorDiv(fromMs+off, activity.DayMs)
	endDay := floorDiv(toMs+off, activity.DayMs)
	n := int(endDay - startDay + 1)
	if n <= 0 {
		return nil, nil
	}
	counts := make([]int64, n)
	rows, err := s.db.QueryContext(ctx, `
		SELECT (created_at + ?) / ? AS day, COUNT(*)
		FROM timeline_entries
		WHERE session_char = ? AND conv_kind = ? AND conv_id = ?
		  AND created_at >= ? AND created_at <= ?`+selfClause(selfOnly)+`
		GROUP BY day ORDER BY day`,
		off, activity.DayMs, session, conv.Kind, conv.ID, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var day, count int64
		if err := rows.Scan(&day, &count); err != nil {
			return nil, err
		}
		if i := day - startDay; i >= 0 && i < int64(n) {
			counts[i] += count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]activity.Bucket, n)
	for i := range counts {
		out[i] = activity.Bucket{StartMs: (startDay+int64(i))*activity.DayMs - off, Count: counts[i]}
	}
	return out, nil
}

// LogActivityRange reads the drilldown points for one bounded range: when each
// entry happened and how large its body is, in time order (conv_seq is
// time-monotonic). Conversation scope reads length(body) from the table; self
// scope reads it from the partial covering index and never touches the table.
func (s *SQLiteStore) LogActivityRange(ctx context.Context, session string, conv model.ConvRef, fromMs, toMs int64, selfOnly bool) ([]activity.Point, error) {
	order := "conv_seq ASC"
	if selfOnly {
		order = "created_at ASC" // the self index is ordered by created_at
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT created_at, length(body)
		FROM timeline_entries
		WHERE session_char = ? AND conv_kind = ? AND conv_id = ?
		  AND created_at >= ? AND created_at <= ?`+selfClause(selfOnly)+`
		ORDER BY `+order,
		session, conv.Kind, conv.ID, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []activity.Point{}
	for rows.Next() {
		var at, n int64
		if err := rows.Scan(&at, &n); err != nil {
			return nil, err
		}
		out = append(out, activity.Point{AtMs: at, BodyLen: int(n)})
	}
	return out, rows.Err()
}

// LogRecentSpeakers reads the newest entries in [sinceMs, ...] and returns their
// speakers, newest first, capped at limit. It backs the scope switch.
func (s *SQLiteStore) LogRecentSpeakers(ctx context.Context, session string, conv model.ConvRef, sinceMs int64, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT speaker
		FROM timeline_entries
		WHERE session_char = ? AND conv_kind = ? AND conv_id = ? AND created_at >= ?
		ORDER BY conv_seq DESC LIMIT ?`,
		session, conv.Kind, conv.ID, sinceMs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var speaker string
		if err := rows.Scan(&speaker); err != nil {
			return nil, err
		}
		out = append(out, speaker)
	}
	return out, rows.Err()
}

// selfClause returns the predicate that restricts a query to our own posts. It
// must be the same expression as idx_entries_self's partial-index predicate for
// SQLite to use that index.
func selfClause(selfOnly bool) string {
	if selfOnly {
		return " AND speaker = session_char"
	}
	return ""
}

// floorDiv divides a by b, rounding toward negative infinity, so local-day
// indices stay contiguous when the tz offset shifts a boundary before the epoch.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func (s *SQLiteStore) ClearHistory(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if _, err := tx.ExecContext(ctx, `DELETE FROM timeline_entries`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM log_conversations`); err != nil {
		return err
	}
	// Marks annotate entries; the foreign key cascades this too, but delete
	// explicitly so an in-memory store mirrors the behavior.
	if _, err := tx.ExecContext(ctx, `DELETE FROM warpmarks`); err != nil {
		return err
	}
	return tx.Commit()
}

// pruneWhere builds the WHERE predicate selecting the timeline entries a
// PruneQuery deletes. alias is the timeline_entries alias the predicate
// refers to, so callers can use the same predicate in a SELECT or a DELETE.
func pruneWhere(q PruneQuery, alias string) (string, []any) {
	switch {
	case q.Conv != nil:
		where := alias + ".session_char = ? AND " + alias + ".conv_kind = ? AND " + alias + ".conv_id = ?"
		args := []any{q.Session, q.Conv.Kind, q.Conv.ID}
		if q.OlderThanMs > 0 {
			where += " AND " + alias + ".created_at < ?"
			args = append(args, q.OlderThanMs)
		}
		return where, args
	case q.DMsOnly:
		// Whole DM conversations are selected from the aggregate, then every
		// entry of a selected conversation is deleted.
		where := alias + ".conv_kind = 'dm' AND EXISTS (" +
			"SELECT 1 FROM log_conversations lc" +
			" WHERE lc.session_char = " + alias + ".session_char" +
			" AND lc.conv_kind = " + alias + ".conv_kind" +
			" AND lc.conv_id = " + alias + ".conv_id" +
			" AND lc.last_ms < ? AND lc.entry_count < ?)"
		args := []any{q.OlderThanMs, q.MaxEntries}
		if q.Session != "" {
			where += " AND " + alias + ".session_char = ?"
			args = append(args, q.Session)
		}
		return where, args
	default:
		where := alias + ".created_at < ?"
		args := []any{q.OlderThanMs}
		if q.Session != "" {
			where += " AND " + alias + ".session_char = ?"
			args = append(args, q.Session)
		}
		return where, args
	}
}

// rowQuerier is the subset of *sql.DB and *sql.Tx the prune impact queries
// need, so the same read runs in and out of a transaction.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// pruneImpact counts what a predicate selects: entries, their body bytes, the
// conversations they span, and the warpmarks that annotate them.
func pruneImpact(ctx context.Context, q rowQuerier, where string, args []any) (PruneResult, error) {
	var res PruneResult
	err := q.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(length(t.body) + COALESCE(length(t.data), 0)), 0)
		FROM timeline_entries t
		WHERE `+where, args...).Scan(&res.Entries, &res.BodyBytes)
	if err != nil {
		return PruneResult{}, err
	}
	if res.Entries > 0 {
		if err := q.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM (
			  SELECT 1 FROM timeline_entries t WHERE `+where+
			` GROUP BY t.session_char, t.conv_kind, t.conv_id)`, args...).Scan(&res.Conversations); err != nil {
			return PruneResult{}, err
		}
	}
	err = q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM warpmarks w
		WHERE EXISTS (SELECT 1 FROM timeline_entries t
		              WHERE t.id = w.entry_id AND `+where+`)`, args...).Scan(&res.Warpmarks)
	if err != nil {
		return PruneResult{}, err
	}
	return res, nil
}

func (s *SQLiteStore) PrunePreview(ctx context.Context, q PruneQuery) (PruneResult, error) {
	where, args := pruneWhere(q, "t")
	return pruneImpact(ctx, s.db, where, args)
}

// PruneHistory deletes the selected entries and reconstructs the log aggregate
// from what remains, all in one transaction so a concurrent append either
// predates or follows the whole cleanup. The warpmark foreign key cascades.
func (s *SQLiteStore) PruneHistory(ctx context.Context, q PruneQuery) (PruneResult, error) {
	where, args := pruneWhere(q, "t")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PruneResult{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	res, err := pruneImpact(ctx, tx, where, args)
	if err != nil {
		return PruneResult{}, err
	}
	if res.Entries == 0 {
		return res, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM timeline_entries AS t WHERE `+where, args...); err != nil {
		return PruneResult{}, err
	}
	// log_conversations is derived. A full rebuild is O(conversations), so it
	// is cheaper than per-conversation bookkeeping and it drops the rows of
	// conversations that no longer have any entries.
	if _, err := tx.ExecContext(ctx, `DELETE FROM log_conversations`); err != nil {
		return PruneResult{}, err
	}
	if _, err := tx.ExecContext(ctx, rebuildSummarySQL); err != nil {
		return PruneResult{}, err
	}
	return res, tx.Commit()
}

// vacuumMinFreeBytes is the free-space floor below which a vacuum is not worth
// its full-database rewrite. It is a policy constant, not a user preference.
const vacuumMinFreeBytes = 1 << 20

// Vacuum rewrites the database when at least vacuumMinFreeBytes of free pages
// have accumulated, then truncates the WAL so the on-disk footprint actually
// shrinks. VACUUM cannot run inside a transaction, so this is always a separate
// step after a prune commits.
func (s *SQLiteStore) Vacuum(ctx context.Context) (VacuumResult, error) {
	pageSize, err := s.pragmaInt(ctx, "page_size")
	if err != nil {
		return VacuumResult{}, err
	}
	free, err := s.pragmaInt(ctx, "freelist_count")
	if err != nil {
		return VacuumResult{}, err
	}
	before, err := s.dbBytes(ctx, pageSize)
	if err != nil {
		return VacuumResult{}, err
	}
	if free*pageSize < vacuumMinFreeBytes {
		return VacuumResult{BytesBefore: before, BytesAfter: before}, nil
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		// The prune already committed; a failed vacuum is reported, not fatal.
		return VacuumResult{BytesBefore: before, BytesAfter: before}, err
	}
	// Best effort: a checkpoint failure does not change the row outcome.
	_, _ = s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	after, err := s.dbBytes(ctx, pageSize)
	if err != nil {
		return VacuumResult{}, err
	}
	var reclaimed int64
	if after < before {
		reclaimed = before - after
	}
	return VacuumResult{Vacuumed: true, BytesBefore: before, BytesAfter: after, BytesReclaimed: reclaimed}, nil
}

// pragmaInt reads a scalar PRAGMA. name is a compile-time constant, never user
// input, so it is interpolated rather than parameterized (PRAGMA does not take
// bind parameters).
func (s *SQLiteStore) pragmaInt(ctx context.Context, name string) (int64, error) {
	var v int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// dbBytes returns the database size in bytes from page_count and the given
// page size.
func (s *SQLiteStore) dbBytes(ctx context.Context, pageSize int64) (int64, error) {
	pages, err := s.pragmaInt(ctx, "page_count")
	if err != nil {
		return 0, err
	}
	return pages * pageSize, nil
}

func (s *SQLiteStore) ConfigGet(ctx context.Context, name string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM configs WHERE name = ?`, name).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (s *SQLiteStore) ConfigPut(ctx context.Context, name string, data []byte) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO configs (name, data, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		name, string(data), time.Now().UnixMilli())
	return err
}

func (s *SQLiteStore) ConfigDelete(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM configs WHERE name = ?`, name)
	return err
}

func (s *SQLiteStore) ConfigClear(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM configs`)
	return err
}

func (s *SQLiteStore) Warpmarks(ctx context.Context, session string) ([]Warpmark, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.session_char, w.entry_id, w.label, w.conv_kind, w.conv_id,
		       w.conv_name, w.speaker, w.created_at,
		       t.conv_seq, t.kind, t.body, t.data
		FROM warpmarks w
		LEFT JOIN timeline_entries t ON t.id = w.entry_id
		WHERE w.session_char = ?
		ORDER BY w.created_at DESC, w.entry_id DESC`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Non-nil even when empty so JSON marshals as [] rather than null.
	out := []Warpmark{}
	for rows.Next() {
		var m Warpmark
		var created int64
		var seq sql.NullInt64
		var kind, body sql.NullString
		var data []byte
		if err := rows.Scan(&m.Session, &m.EntryID, &m.Label, &m.Conv.Kind, &m.Conv.ID,
			&m.ConvName, &m.Speaker, &created, &seq, &kind, &body, &data); err != nil {
			return nil, err
		}
		m.CreatedAt = time.UnixMilli(created)
		if !seq.Valid {
			m.Missing = true
		} else {
			m.Seq = uint64(seq.Int64)
			m.Kind = kind.String
			m.Body = body.String
			if len(data) > 0 {
				m.Data = data
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) WarpmarkPut(ctx context.Context, m Warpmark) error {
	// Re-marking edits the label but keeps the original mark time, so the list
	// order does not jump when a label is corrected.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO warpmarks
		  (session_char, entry_id, label, conv_kind, conv_id, conv_name, speaker, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_char, entry_id) DO UPDATE SET label = excluded.label`,
		m.Session, m.EntryID, m.Label, m.Conv.Kind, m.Conv.ID, m.ConvName, m.Speaker, m.CreatedAt.UnixMilli())
	return err
}

func (s *SQLiteStore) WarpmarkDelete(ctx context.Context, session, entryID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM warpmarks WHERE session_char = ? AND entry_id = ?`, session, entryID)
	return err
}

func (s *SQLiteStore) EntryByID(ctx context.Context, id string) (model.Entry, error) {
	var e model.Entry
	var created, received int64
	var data []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id, upstream_id, session_char, conv_kind, conv_id, conv_name,
		       conv_seq, kind, speaker, body, data, created_at, received_at
		FROM timeline_entries WHERE id = ?`, id).Scan(
		&e.ID, &e.UpstreamID, &e.Session, &e.Conv.Kind, &e.Conv.ID, &e.ConvName,
		&e.ConvSeq, &e.Kind, &e.Speaker, &e.Body, &data, &created, &received)
	if err == sql.ErrNoRows {
		return model.Entry{}, ErrNotFound
	}
	if err != nil {
		return model.Entry{}, err
	}
	if len(data) > 0 {
		e.Data = data
	}
	e.CreatedAt = time.UnixMilli(created)
	e.ReceivedAt = time.UnixMilli(received)
	return e, nil
}

func (s *SQLiteStore) Close() error {
	_ = s.insert.Close()
	return s.db.Close()
}
