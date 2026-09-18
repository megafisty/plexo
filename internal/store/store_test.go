package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"plexo/internal/activity"
	"plexo/internal/model"
	"plexo/internal/store"
	"plexo/test/memstore"

	_ "modernc.org/sqlite"
)

func sampleEntries() []model.Entry {
	base := time.Unix(1_700_000_000, 0)
	var out []model.Entry
	for i := 1; i <= 5; i++ {
		out = append(out, model.Entry{
			ID:        "e" + string(rune('0'+i)),
			Session:   "Vix",
			Conv:      model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"},
			ConvSeq:   uint64(i),
			Kind:      "msg",
			Speaker:   "Other",
			Body:      "m",
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		})
	}
	return out
}

func exerciseStore(t *testing.T, s store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := s.Append(ctx, sampleEntries()); err != nil {
		t.Fatalf("Append: %v", err)
	}

	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	latest, err := s.History(ctx, store.HistoryQuery{Session: "Vix", Conv: conv, Limit: 2})
	if err != nil {
		t.Fatalf("History latest: %v", err)
	}
	if len(latest) != 2 || latest[0].ConvSeq != 4 || latest[1].ConvSeq != 5 {
		t.Fatalf("latest window = %+v", latest)
	}

	before := uint64(4)
	page, err := s.History(ctx, store.HistoryQuery{Session: "Vix", Conv: conv, BeforeSeq: &before, Limit: 2})
	if err != nil {
		t.Fatalf("History before: %v", err)
	}
	if len(page) != 2 || page[0].ConvSeq != 2 || page[1].ConvSeq != 3 {
		t.Fatalf("before window = %+v", page)
	}

	after := uint64(3)
	tail, err := s.History(ctx, store.HistoryQuery{Session: "Vix", Conv: conv, AfterSeq: &after, Limit: 10})
	if err != nil {
		t.Fatalf("History after: %v", err)
	}
	if len(tail) != 2 || tail[0].ConvSeq != 4 || tail[1].ConvSeq != 5 {
		t.Fatalf("after window = %+v", tail)
	}

	max, err := s.MaxConvSeq(ctx, "Vix", conv)
	if err != nil || max != 5 {
		t.Fatalf("MaxConvSeq = %d, %v", max, err)
	}

	// Config: missing documents report ErrNotFound; put/get/delete/clear
	// round-trip, and names are independent.
	if _, err := s.ConfigGet(ctx, "!global"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ConfigGet missing = %v, want ErrNotFound", err)
	}
	if err := s.ConfigPut(ctx, "!global", []byte(`{"highlights":["x"]}`)); err != nil {
		t.Fatalf("ConfigPut global: %v", err)
	}
	if err := s.ConfigPut(ctx, "vix", []byte(`{"highlights":["y"]}`)); err != nil {
		t.Fatalf("ConfigPut character: %v", err)
	}
	if got, err := s.ConfigGet(ctx, "vix"); err != nil || string(got) != `{"highlights":["y"]}` {
		t.Fatalf("ConfigGet character = %q, %v", got, err)
	}
	if err := s.ConfigPut(ctx, "vix", []byte(`{"highlights":["z"]}`)); err != nil {
		t.Fatalf("ConfigPut overwrite: %v", err)
	}
	if got, err := s.ConfigGet(ctx, "vix"); err != nil || string(got) != `{"highlights":["z"]}` {
		t.Fatalf("ConfigGet after overwrite = %q, %v", got, err)
	}
	if err := s.ConfigDelete(ctx, "!global"); err != nil {
		t.Fatalf("ConfigDelete: %v", err)
	}
	if _, err := s.ConfigGet(ctx, "!global"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ConfigGet after delete = %v, want ErrNotFound", err)
	}
	if err := s.ConfigClear(ctx); err != nil {
		t.Fatalf("ConfigClear: %v", err)
	}
	if _, err := s.ConfigGet(ctx, "vix"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ConfigGet after clear = %v, want ErrNotFound", err)
	}

	// A room's readable title round-trips alongside its opaque ADH-... id.
	room := model.ConvRef{Kind: model.ConvRoom, ID: "ADH-abc"}
	if err := s.Append(ctx, []model.Entry{{
		ID:        "r1",
		Session:   "Vix",
		Conv:      room,
		ConvName:  "The Tavern",
		ConvSeq:   1,
		Kind:      "msg",
		Speaker:   "Other",
		Body:      "hi",
		CreatedAt: time.Unix(1_700_000_000, 0).Add(time.Second),
	}}); err != nil {
		t.Fatalf("Append room: %v", err)
	}
	roomHistory, err := s.History(ctx, store.HistoryQuery{Session: "Vix", Conv: room})
	if err != nil || len(roomHistory) != 1 || roomHistory[0].ConvName != "The Tavern" {
		t.Fatalf("room History = %+v, %v", roomHistory, err)
	}

	// The two-sided log index resolves each end independently. Vix has one
	// official channel and one room by now.
	chars, err := s.LogCharacters(ctx)
	if err != nil || len(chars) != 1 || chars[0] != "Vix" {
		t.Fatalf("LogCharacters = %v, %v", chars, err)
	}
	convs, err := s.LogConvs(ctx, "Vix")
	if err != nil {
		t.Fatalf("LogConvs: %v", err)
	}
	if len(convs) != 2 || convs[0].Conv != conv || convs[0].Name != "" {
		t.Fatalf("LogConvs[0] = %+v", convs)
	}
	if convs[1].Conv != room || convs[1].Name != "The Tavern" {
		t.Fatalf("LogConvs[1] = %+v", convs)
	}

	// Reverse: a conversation resolves to its own characters, with the exact
	// stored id and (for rooms) the readable title, matched case-insensitively.
	byChannel, err := s.LogSessionsForConv(ctx, model.ConvOfficial, "frontpage")
	if err != nil || len(byChannel) != 1 || byChannel[0].Session != "Vix" || byChannel[0].Conv != conv {
		t.Fatalf("LogSessionsForConv official = %+v, %v", byChannel, err)
	}
	byRoom, err := s.LogSessionsForConv(ctx, model.ConvRoom, "adh-ABC")
	if err != nil || len(byRoom) != 1 || byRoom[0].Conv != room || byRoom[0].Name != "The Tavern" {
		t.Fatalf("LogSessionsForConv room = %+v, %v", byRoom, err)
	}

	// The global list dedupes by kind and case-folded id and carries the
	// latest room title.
	all, err := s.LogAllConvs(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("LogAllConvs = %+v, %v", all, err)
	}
	if all[0].Conv.Kind != model.ConvOfficial || all[0].Name != "" {
		t.Fatalf("LogAllConvs[0] = %+v", all[0])
	}
	if all[1].Conv.Kind != model.ConvRoom || all[1].Name != "The Tavern" {
		t.Fatalf("LogAllConvs[1] = %+v", all[1])
	}

	// Coverage and the streaming range read back the same span.
	base := time.Unix(1_700_000_000, 0)
	cov, err := s.LogCoverage(ctx, "Vix", conv)
	if err != nil {
		t.Fatalf("LogCoverage: %v", err)
	}
	if cov.Count != 5 || cov.FirstSeq != 1 || cov.LastSeq != 5 ||
		cov.FirstMs != base.Add(time.Second).UnixMilli() || cov.LastMs != base.Add(5*time.Second).UnixMilli() {
		t.Fatalf("LogCoverage = %+v", cov)
	}
	// Re-appending an existing id is a no-op and must not inflate the aggregate.
	if err := s.Append(ctx, sampleEntries()[:1]); err != nil {
		t.Fatalf("re-Append: %v", err)
	}
	if again, err := s.LogCoverage(ctx, "Vix", conv); err != nil || again.Count != 5 {
		t.Fatalf("LogCoverage after re-append = %+v, %v", again, err)
	}
	fromMs := base.Add(2 * time.Second).UnixMilli()
	toMs := base.Add(4 * time.Second).UnixMilli()
	if n, err := s.LogRangeCount(ctx, "Vix", conv, fromMs, toMs); err != nil || n != 3 {
		t.Fatalf("LogRangeCount = %d, %v", n, err)
	}
	start, err := s.LogRangeStart(ctx, "Vix", conv, fromMs)
	if err != nil || start != 2 {
		t.Fatalf("LogRangeStart = %d, %v", start, err)
	}
	page, err = s.LogRange(ctx, store.LogRangeQuery{Session: "Vix", Conv: conv, AfterSeq: 1, Limit: 2})
	if err != nil || len(page) != 2 || page[0].ConvSeq != 2 || page[1].ConvSeq != 3 {
		t.Fatalf("LogRange = %+v, %v", page, err)
	}

	// The activity overview and drilldown read the same timeline: the five
	// one-byte entries all land in one local day, and the drilldown preserves
	// order and body lengths.
	dayBuckets, err := s.LogDayCounts(ctx, "Vix", conv, cov.FirstMs, cov.LastMs, 0, false)
	if err != nil || len(dayBuckets) != 1 || dayBuckets[0].Count != 5 {
		t.Fatalf("LogDayCounts = %+v, %v", dayBuckets, err)
	}
	points, err := s.LogActivityRange(ctx, "Vix", conv, cov.FirstMs, cov.LastMs, false)
	if err != nil || len(points) != 5 || points[0].BodyLen != 1 || points[4].AtMs != cov.LastMs {
		t.Fatalf("LogActivityRange = %+v, %v", points, err)
	}

	// A structured payload round-trips in the nullable data column; plain kinds
	// leave it NULL.
	if err := s.Append(ctx, []model.Entry{{
		ID:        "rll1",
		Session:   "Vix",
		Conv:      conv,
		ConvSeq:   6,
		Kind:      "rll",
		Speaker:   "Other",
		Body:      "raw",
		Data:      []byte(`{"type":"dice"}`),
		CreatedAt: time.Unix(1_700_000_000, 0).Add(6 * time.Second),
	}}); err != nil {
		t.Fatalf("Append roll: %v", err)
	}
	rollHistory, err := s.History(ctx, store.HistoryQuery{Session: "Vix", Conv: conv, Limit: 1})
	if err != nil || len(rollHistory) != 1 || string(rollHistory[0].Data) != `{"type":"dice"}` {
		t.Fatalf("roll History = %+v, %v", rollHistory, err)
	}

	// Warpmarks upsert a per-character annotation and resolve their entry on
	// read. Newest mark first (a snapshot store tolerates danglers; SQLite's
	// foreign key is exercised separately).
	if err := s.WarpmarkPut(ctx, store.Warpmark{
		Session: "Vix", EntryID: "e2", Label: "the good bit",
		Conv: conv, Speaker: "Other", CreatedAt: base.Add(time.Minute),
	}); err != nil {
		t.Fatalf("WarpmarkPut: %v", err)
	}
	marks, err := s.Warpmarks(ctx, "Vix")
	if err != nil || len(marks) != 1 {
		t.Fatalf("Warpmarks = %+v, %v", marks, err)
	}
	if marks[0].EntryID != "e2" || marks[0].Label != "the good bit" ||
		marks[0].Seq != 2 || marks[0].Missing || marks[0].Speaker != "Other" {
		t.Fatalf("Warpmarks[0] = %+v", marks[0])
	}
	// Re-marking edits the label but keeps the mark time, so order is stable.
	if err := s.WarpmarkPut(ctx, store.Warpmark{
		Session: "Vix", EntryID: "e2", Label: "renamed",
		Conv: conv, Speaker: "Other", CreatedAt: base.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("WarpmarkPut edit: %v", err)
	}
	marks, _ = s.Warpmarks(ctx, "Vix")
	if len(marks) != 1 || marks[0].Label != "renamed" ||
		!marks[0].CreatedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("Warpmarks after edit = %+v", marks)
	}
	if e, err := s.EntryByID(ctx, "e2"); err != nil || e.ConvSeq != 2 || e.Conv != conv {
		t.Fatalf("EntryByID = %+v, %v", e, err)
	}
	if _, err := s.EntryByID(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("EntryByID missing = %v, want ErrNotFound", err)
	}
	// A second mark on another real entry, ordered by mark time.
	if err := s.WarpmarkPut(ctx, store.Warpmark{
		Session: "Vix", EntryID: "e1", Label: "earlier",
		Conv: conv, Speaker: "Other", CreatedAt: base.Add(30 * time.Second),
	}); err != nil {
		t.Fatalf("WarpmarkPut second: %v", err)
	}
	if marks, _ = s.Warpmarks(ctx, "Vix"); len(marks) != 2 ||
		marks[0].EntryID != "e2" || marks[1].EntryID != "e1" {
		t.Fatalf("Warpmarks order = %+v", marks)
	}
	// Delete is idempotent.
	if err := s.WarpmarkDelete(ctx, "Vix", "e1"); err != nil {
		t.Fatalf("WarpmarkDelete: %v", err)
	}
	if err := s.WarpmarkDelete(ctx, "Vix", "e1"); err != nil {
		t.Fatalf("WarpmarkDelete again: %v", err)
	}
	if marks, _ = s.Warpmarks(ctx, "Vix"); len(marks) != 1 || marks[0].EntryID != "e2" {
		t.Fatalf("Warpmarks after delete = %+v", marks)
	}

	// ClearHistory empties the timeline and resets the derived cursor.
	if err := s.ClearHistory(ctx); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}
	// Marks annotate entries, so clearing history clears them too.
	if marks, err := s.Warpmarks(ctx, "Vix"); err != nil || len(marks) != 0 {
		t.Fatalf("Warpmarks after clear = %+v, %v", marks, err)
	}
	if got, err := s.History(ctx, store.HistoryQuery{Session: "Vix", Conv: conv}); err != nil || len(got) != 0 {
		t.Fatalf("History after clear = %+v, %v", got, err)
	}
	if max, err := s.MaxConvSeq(ctx, "Vix", conv); err != nil || max != 0 {
		t.Fatalf("MaxConvSeq after clear = %d, %v", max, err)
	}
}

func TestMemStore(t *testing.T) { exerciseStore(t, memstore.New()) }

func TestSQLiteStore(t *testing.T) {
	s, err := store.OpenSQLite(filepath.Join(t.TempDir(), "plexo.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer s.Close()
	exerciseStore(t, s)
}

// TestSQLiteLogDayCountsMatchesOracle seeds a multi-day DM and checks the SQL
// day bucketing against the pure activity oracle at several timezone offsets,
// including zero-filled empty days.
func TestSQLiteLogDayCountsMatchesOracle(t *testing.T) {
	ctx := context.Background()
	s, err := store.OpenSQLite(filepath.Join(t.TempDir(), "plexo.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer s.Close()

	base := time.Date(2024, 3, 1, 22, 0, 0, 0, time.UTC)
	conv := model.ConvRef{Kind: model.ConvDM, ID: "Vix"}
	// Day 0 has three entries late in the UTC day; day 2 has two; day 1 is empty.
	times := []time.Time{
		base, base.Add(10 * time.Minute), base.Add(20 * time.Minute),
		base.AddDate(0, 0, 2), base.AddDate(0, 0, 2).Add(30 * time.Minute),
	}
	var entries []model.Entry
	for i, at := range times {
		entries = append(entries, model.Entry{
			ID:        fmt.Sprintf("a%d", i),
			Session:   "Vix",
			Conv:      conv,
			ConvSeq:   uint64(i + 1),
			Kind:      "msg",
			Speaker:   "Other",
			Body:      strings.Repeat("x", 100*(i+1)),
			CreatedAt: at,
		})
	}
	if err := s.Append(ctx, entries); err != nil {
		t.Fatalf("Append: %v", err)
	}
	from := times[0].UnixMilli()
	to := times[len(times)-1].UnixMilli()

	points, err := s.LogActivityRange(ctx, "Vix", conv, from, to, false)
	if err != nil || len(points) != len(times) {
		t.Fatalf("LogActivityRange = %+v, %v", points, err)
	}
	for _, tz := range []int{0, -300, 120, 600} {
		got, err := s.LogDayCounts(ctx, "Vix", conv, from, to, tz, false)
		if err != nil {
			t.Fatalf("LogDayCounts tz=%d: %v", tz, err)
		}
		want := activity.DayCounts(points, from, to, tz)
		if len(got) != len(want) {
			t.Fatalf("tz=%d: len = %d, want %d (%+v vs %+v)", tz, len(got), len(want), got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("tz=%d bucket %d = %+v, want %+v", tz, i, got[i], want[i])
			}
		}
	}
}

// TestSQLiteRebuildSummary: a database written before the log_conversations
// aggregate existed is repaired on open, so its history stays browsable.
func TestSQLiteRebuildSummary(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "plexo.db")

	s, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if err := s.Append(ctx, sampleEntries()); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Simulate the old schema: entries present, aggregate absent.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM log_conversations`); err != nil {
		t.Fatalf("clear summary: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s2, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	chars, err := s2.LogCharacters(ctx)
	if err != nil || len(chars) != 1 || chars[0] != "Vix" {
		t.Fatalf("LogCharacters after rebuild = %v, %v", chars, err)
	}
	cov, err := s2.LogCoverage(ctx, "Vix", model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"})
	if err != nil || cov.Count != 5 {
		t.Fatalf("LogCoverage after rebuild = %+v, %v", cov, err)
	}
}

// TestSQLiteSelfActivity: the self-scoped queries count only our own posts, and
// the participation sample returns recent speakers newest-first.
func TestSQLiteSelfActivity(t *testing.T) {
	ctx := context.Background()
	s, err := store.OpenSQLite(filepath.Join(t.TempDir(), "plexo.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer s.Close()

	conv := model.ConvRef{Kind: model.ConvRoom, ID: "ADH-x"}
	base := time.Date(2024, 5, 1, 20, 0, 0, 0, time.UTC)
	var entries []model.Entry
	add := func(id, speaker string, at time.Time, body int) {
		entries = append(entries, model.Entry{
			ID: id, Session: "Vix", Conv: conv, ConvSeq: uint64(len(entries) + 1),
			Kind: "msg", Speaker: speaker, Body: strings.Repeat("x", body), CreatedAt: at,
		})
	}
	add("e1", "Kira", base, 40)
	add("e2", "Vix", base.Add(time.Minute), 500)
	add("e3", "Kira", base.Add(2*time.Minute), 40)
	add("e4", "Vix", base.Add(3*time.Minute), 500)
	if err := s.Append(ctx, entries); err != nil {
		t.Fatalf("Append: %v", err)
	}
	from, to := base.UnixMilli(), base.Add(3*time.Minute).UnixMilli()

	self, err := s.LogActivityRange(ctx, "Vix", conv, from, to, true)
	if err != nil || len(self) != 2 {
		t.Fatalf("self LogActivityRange = %+v, %v", self, err)
	}
	for _, p := range self {
		if p.BodyLen != 500 {
			t.Errorf("self point bodyLen = %d, want 500", p.BodyLen)
		}
	}
	buckets, err := s.LogDayCounts(ctx, "Vix", conv, from, to, 0, true)
	if err != nil || len(buckets) != 1 || buckets[0].Count != 2 {
		t.Fatalf("self LogDayCounts = %+v, %v", buckets, err)
	}

	speakers, err := s.LogRecentSpeakers(ctx, "Vix", conv, from, 10)
	if err != nil || len(speakers) != 4 {
		t.Fatalf("LogRecentSpeakers = %+v, %v", speakers, err)
	}
	if speakers[0] != "Vix" || speakers[3] != "Kira" {
		t.Errorf("LogRecentSpeakers order = %v, want newest first", speakers)
	}
}

// TestSQLiteWarpmarkUpgrade rewinds a database to the pre-warpmark schema and
// checks that reopening adds the table. The schema is a single additive script
// of IF NOT EXISTS statements, so an existing database needs no recreate.
func TestSQLiteWarpmarkUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plexo.db")
	s, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	if err := s.Append(ctx, []model.Entry{{
		ID: "e1", Session: "Vix", Conv: model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"},
		ConvSeq: 1, Kind: "msg", Speaker: "Other", Body: "hi", CreatedAt: time.Unix(1_700_000_000, 0),
	}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Drop the feature's objects to simulate a database opened before it existed.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_warpmarks_session`,
		`DROP TABLE IF EXISTS warpmarks`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("drop: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	s2, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if err := s2.WarpmarkPut(ctx, store.Warpmark{
		Session: "Vix", EntryID: "e1", Label: "x",
		Conv:    model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"},
		Speaker: "Other", CreatedAt: time.Unix(1_700_000_000, 0),
	}); err != nil {
		t.Fatalf("WarpmarkPut after upgrade: %v", err)
	}
	marks, err := s2.Warpmarks(ctx, "Vix")
	if err != nil || len(marks) != 1 || marks[0].Seq != 1 || marks[0].Missing {
		t.Fatalf("Warpmarks after upgrade = %+v, %v", marks, err)
	}
}

// TestSQLiteWarpmarkCascade checks the foreign key: deleting one entry (as a
// cleanup tool would) drops the marks that annotate it, so history cleanup
// cannot leave dangling marks behind.
func TestSQLiteWarpmarkCascade(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "plexo.db")
	s, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	if err := s.Append(ctx, []model.Entry{
		{ID: "e1", Session: "Vix", Conv: conv, ConvSeq: 1, Kind: "msg", Speaker: "x", Body: "a", CreatedAt: time.Unix(1_700_000_000, 0)},
		{ID: "e2", Session: "Vix", Conv: conv, ConvSeq: 2, Kind: "msg", Speaker: "x", Body: "b", CreatedAt: time.Unix(1_700_000_001, 0)},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	for _, id := range []string{"e1", "e2"} {
		if err := s.WarpmarkPut(ctx, store.Warpmark{
			Session: "Vix", EntryID: id, Label: id, Conv: conv, Speaker: "x",
			CreatedAt: time.Unix(1_700_000_000, 0),
		}); err != nil {
			t.Fatalf("put %s: %v", id, err)
		}
	}
	// The foreign key rejects a mark on an entry that does not exist.
	if err := s.WarpmarkPut(ctx, store.Warpmark{
		Session: "Vix", EntryID: "nope", Label: "x", Conv: conv, Speaker: "x",
		CreatedAt: time.Unix(1_700_000_000, 0),
	}); err == nil {
		t.Fatal("WarpmarkPut for a missing entry succeeded, want foreign-key error")
	}

	// Delete one entry directly, as a per-conversation cleanup would.
	raw, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `DELETE FROM timeline_entries WHERE id = ?`, "e1"); err != nil {
		raw.Close()
		t.Fatalf("delete entry: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	marks, err := s.Warpmarks(ctx, "Vix")
	if err != nil || len(marks) != 1 || marks[0].EntryID != "e2" || marks[0].Missing {
		t.Fatalf("Warpmarks after cascade = %+v, %v", marks, err)
	}
}

// TestMemStoreWarpmarkDangling covers the in-memory store's read tolerance for a
// mark whose entry is absent. SQLite cannot reach this state through its own
// API (the foreign key is enforced), but the list must still render a mark from
// its snapshot if one ever appears out of band.
func TestMemStoreWarpmarkDangling(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	if err := s.WarpmarkPut(ctx, store.Warpmark{
		Session: "Vix", EntryID: "gone", Label: "lost", Conv: conv,
		Speaker: "x", CreatedAt: time.Unix(1_700_000_000, 0),
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	marks, err := s.Warpmarks(ctx, "Vix")
	if err != nil || len(marks) != 1 || !marks[0].Missing || marks[0].Seq != 0 {
		t.Fatalf("Warpmarks = %+v, %v", marks, err)
	}
}

// pruneBase is a fixed instant so every prune scenario is deterministic.
var pruneBase = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// seedPrune fills a store with four conversations whose ages and sizes make
// each cleanup rule selectable: an official channel and a room, plus a small
// stale DM and a larger recent DM. One warpmark annotates an old entry.
func seedPrune(t *testing.T, s store.Store) {
	t.Helper()
	official := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	room := model.ConvRef{Kind: model.ConvRoom, ID: "ADH-tavern"}
	kira := model.ConvRef{Kind: model.ConvDM, ID: "Kira"}
	nova := model.ConvRef{Kind: model.ConvDM, ID: "Nova"}
	add := func(id string, session string, conv model.ConvRef, seq int, days int) model.Entry {
		return model.Entry{
			ID: id, Session: session, Conv: conv, ConvSeq: uint64(seq),
			Kind: "msg", Speaker: "Other", Body: "m",
			CreatedAt: pruneBase.AddDate(0, 0, days),
		}
	}
	var entries []model.Entry
	for i := 1; i <= 5; i++ {
		entries = append(entries, add(fmt.Sprintf("e%d", i), "Vix", official, i, i))
	}
	entries = append(entries, add("r1", "Vix", room, 1, 1), add("r2", "Vix", room, 2, 5))
	for i := 1; i <= 3; i++ {
		entries = append(entries, add(fmt.Sprintf("k%d", i), "Vix", kira, i, i))
	}
	for i := 1; i <= 6; i++ {
		entries = append(entries, add(fmt.Sprintf("n%d", i), "Vix", nova, i, i))
	}
	if err := s.Append(context.Background(), entries); err != nil {
		t.Fatalf("seed Append: %v", err)
	}
	if err := s.WarpmarkPut(context.Background(), store.Warpmark{
		Session: "Vix", EntryID: "e2", Label: "old", Conv: official,
		Speaker: "Other", CreatedAt: pruneBase,
	}); err != nil {
		t.Fatalf("seed WarpmarkPut: %v", err)
	}
}

func countEntries(t *testing.T, s store.Store, session string, conv model.ConvRef) int64 {
	t.Helper()
	ext, err := s.LogCoverage(context.Background(), session, conv)
	if err != nil {
		t.Fatalf("LogCoverage: %v", err)
	}
	return ext.Count
}

// exercisePrune runs the same prune scenarios against any Store so the SQLite
// implementation and the in-memory mirror stay behaviorally aligned.
func exercisePrune(t *testing.T, newStore func(t *testing.T) store.Store) {
	ctx := context.Background()
	official := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	room := model.ConvRef{Kind: model.ConvRoom, ID: "ADH-tavern"}
	kira := model.ConvRef{Kind: model.ConvDM, ID: "Kira"}
	nova := model.ConvRef{Kind: model.ConvDM, ID: "Nova"}

	t.Run("age preview and apply", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()
		seedPrune(t, s)
		// Older than 4 days: e1-3, r1, k1-3, n1-3 = 10 entries across 4 convs.
		cutoff := pruneBase.AddDate(0, 0, 4).UnixMilli()
		q := store.PruneQuery{OlderThanMs: cutoff}

		preview, err := s.PrunePreview(ctx, q)
		if err != nil {
			t.Fatalf("PrunePreview: %v", err)
		}
		if preview.Entries != 10 || preview.Conversations != 4 || preview.Warpmarks != 1 || preview.BodyBytes != 10 {
			t.Fatalf("preview = %+v", preview)
		}
		// A preview must not delete anything.
		if got := countEntries(t, s, "Vix", official); got != 5 {
			t.Fatalf("preview deleted entries: Frontpage count = %d", got)
		}

		applied, err := s.PruneHistory(ctx, q)
		if err != nil {
			t.Fatalf("PruneHistory: %v", err)
		}
		if applied != preview {
			t.Fatalf("apply = %+v, preview = %+v", applied, preview)
		}
		if got := countEntries(t, s, "Vix", official); got != 2 {
			t.Fatalf("Frontpage count = %d, want 2", got)
		}
		if got := countEntries(t, s, "Vix", room); got != 1 {
			t.Fatalf("room count = %d, want 1", got)
		}
		if got := countEntries(t, s, "Vix", kira); got != 0 {
			t.Fatalf("Kira count = %d, want 0", got)
		}
		if got := countEntries(t, s, "Vix", nova); got != 3 {
			t.Fatalf("Nova count = %d, want 3", got)
		}
		// A conversation with no remaining entries is dropped from the index.
		all, err := s.LogAllConvs(ctx)
		if err != nil {
			t.Fatalf("LogAllConvs: %v", err)
		}
		for _, c := range all {
			if c.Conv == kira {
				t.Fatalf("emptied Kira still in log index: %+v", all)
			}
		}
		if len(all) != 3 {
			t.Fatalf("LogAllConvs = %+v, want 3 conversations", all)
		}
		// Deleting the marked entry cascades to its warpmark.
		if marks, err := s.Warpmarks(ctx, "Vix"); err != nil || len(marks) != 0 {
			t.Fatalf("Warpmarks after prune = %+v, %v", marks, err)
		}
		// The sequence floor is preserved, so new messages do not reuse cursors.
		if max, err := s.MaxConvSeq(ctx, "Vix", official); err != nil || max != 5 {
			t.Fatalf("MaxConvSeq after prune = %d, %v", max, err)
		}
	})

	t.Run("conversation entire", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()
		seedPrune(t, s)
		q := store.PruneQuery{Session: "Vix", Conv: &room}
		preview, err := s.PrunePreview(ctx, q)
		if err != nil {
			t.Fatalf("PrunePreview: %v", err)
		}
		if preview.Entries != 2 || preview.Conversations != 1 {
			t.Fatalf("preview = %+v", preview)
		}
		if _, err := s.PruneHistory(ctx, q); err != nil {
			t.Fatalf("PruneHistory: %v", err)
		}
		if got := countEntries(t, s, "Vix", room); got != 0 {
			t.Fatalf("room count = %d, want 0", got)
		}
		// Other conversations are untouched.
		if got := countEntries(t, s, "Vix", official); got != 5 {
			t.Fatalf("Frontpage count = %d, want 5", got)
		}
	})

	t.Run("conversation age", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()
		seedPrune(t, s)
		cutoff := pruneBase.AddDate(0, 0, 3).UnixMilli()
		q := store.PruneQuery{Session: "Vix", Conv: &official, OlderThanMs: cutoff}
		applied, err := s.PruneHistory(ctx, q)
		if err != nil {
			t.Fatalf("PruneHistory: %v", err)
		}
		if applied.Entries != 2 || applied.Conversations != 1 {
			t.Fatalf("applied = %+v", applied)
		}
		if got := countEntries(t, s, "Vix", official); got != 3 {
			t.Fatalf("Frontpage count = %d, want 3", got)
		}
		if got := countEntries(t, s, "Vix", room); got != 2 {
			t.Fatalf("room count = %d, want 2", got)
		}
	})

	t.Run("one-off dms", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()
		seedPrune(t, s)
		// Inactive before 4 days ago and fewer than 5 entries: only Kira (last
		// at 3 days, count 3). Nova is recent and larger.
		q := store.PruneQuery{OlderThanMs: pruneBase.AddDate(0, 0, 4).UnixMilli(), DMsOnly: true, MaxEntries: 5}
		preview, err := s.PrunePreview(ctx, q)
		if err != nil {
			t.Fatalf("PrunePreview: %v", err)
		}
		if preview.Entries != 3 || preview.Conversations != 1 {
			t.Fatalf("preview = %+v", preview)
		}
		if _, err := s.PruneHistory(ctx, q); err != nil {
			t.Fatalf("PruneHistory: %v", err)
		}
		if got := countEntries(t, s, "Vix", kira); got != 0 {
			t.Fatalf("Kira count = %d, want 0", got)
		}
		if got := countEntries(t, s, "Vix", nova); got != 6 {
			t.Fatalf("Nova count = %d, want 6", got)
		}
	})

	t.Run("one-off dms size boundary", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()
		seedPrune(t, s)
		// Kira has exactly 3 entries; "fewer than 3" excludes it.
		tight := store.PruneQuery{OlderThanMs: pruneBase.AddDate(0, 0, 10).UnixMilli(), DMsOnly: true, MaxEntries: 3}
		if preview, err := s.PrunePreview(ctx, tight); err != nil || preview.Entries != 0 {
			t.Fatalf("tight preview = %+v, %v", preview, err)
		}
		loose := tight
		loose.MaxEntries = 4
		if preview, err := s.PrunePreview(ctx, loose); err != nil || preview.Entries != 3 {
			t.Fatalf("loose preview = %+v, %v", preview, err)
		}
	})

	t.Run("no match is a no-op", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()
		seedPrune(t, s)
		res, err := s.PruneHistory(ctx, store.PruneQuery{OlderThanMs: pruneBase.UnixMilli()})
		if err != nil {
			t.Fatalf("PruneHistory: %v", err)
		}
		if res.Entries != 0 || res.Conversations != 0 || res.Warpmarks != 0 {
			t.Fatalf("empty prune = %+v", res)
		}
		if _, err := s.Vacuum(ctx); err != nil {
			t.Fatalf("Vacuum: %v", err)
		}
	})
}

func TestMemStorePrune(t *testing.T) {
	exercisePrune(t, func(t *testing.T) store.Store { return memstore.New() })
}

func TestSQLitePrune(t *testing.T) {
	exercisePrune(t, func(t *testing.T) store.Store {
		s, err := store.OpenSQLite(filepath.Join(t.TempDir(), "plexo.db"))
		if err != nil {
			t.Fatalf("OpenSQLite: %v", err)
		}
		return s
	})
}
