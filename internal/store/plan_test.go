package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestSelfIndexPlan: the self-scoped drilldown must use idx_entries_self rather
// than fall back to a table scan. The query repeats the partial index's
// predicate (`speaker = session_char`) so the planner can match it; this test
// guards that the two stay in sync.
func TestSelfIndexPlan(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "plexo.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer s.Close()

	queries := map[string]string{
		"range": `
			SELECT created_at, length(body)
			FROM timeline_entries
			WHERE session_char = ? AND conv_kind = ? AND conv_id = ?
			  AND created_at >= ? AND created_at <= ? AND speaker = session_char
			ORDER BY created_at ASC`,
		"day counts": `
			SELECT (created_at + ?) / ? AS day, COUNT(*)
			FROM timeline_entries
			WHERE session_char = ? AND conv_kind = ? AND conv_id = ?
			  AND created_at >= ? AND created_at <= ? AND speaker = session_char
			GROUP BY day ORDER BY day`,
	}
	for name, q := range queries {
		plan := explain(t, s, q)
		if !strings.Contains(plan, "idx_entries_self") {
			t.Errorf("%s plan does not use idx_entries_self:\n%s", name, plan)
		}
	}
}

// explain returns the EXPLAIN QUERY PLAN text for q with placeholder arguments.
func explain(t *testing.T, s *SQLiteStore, q string) string {
	t.Helper()
	// The parameter counts differ per query; EXPLAIN tolerates unused/extra
	// bindings poorly, so bind a generous fixed set with SQLite's numbered
	// parameters avoided by passing one value per '?'.
	args := make([]any, strings.Count(q, "?"))
	for i := range args {
		switch i {
		case 0, 1:
			args[i] = int64(0)
		default:
			args[i] = ""
		}
	}
	rows, err := s.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	return plan.String()
}
