package core_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"plexo/internal/core"
	"plexo/internal/model"
	"plexo/internal/render"
	"plexo/test/memstore"
)

// TestLoadHistoryAfterSeqPagination guards the sentinel-truncation bug: a
// forward (afterSeq) page must keep the entries closest to the cursor, not
// drop the earliest one.
func TestLoadHistoryAfterSeqPagination(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	base := time.Unix(1_700_000_000, 0)
	for i := 1; i <= 5; i++ {
		if err := st.Append(ctx, []model.Entry{{
			ID: "e" + strconv.Itoa(i), Session: "Vix", Conv: conv, ConvSeq: uint64(i),
			Kind: "msg", Speaker: "x", Body: "m",
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	m := core.NewManager(ctx, core.Config{Store: st})

	after := uint64(1)
	entries, err := m.History(ctx, "Vix", conv, nil, &after, 2)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(entries) != 2 || entries[0].ConvSeq != 2 || entries[1].ConvSeq != 3 {
		t.Fatalf("after page = %+v, want seqs [2 3]", entries)
	}

	// The before direction must still return the newest window below the cursor.
	before := uint64(4)
	entries, err = m.History(ctx, "Vix", conv, &before, nil, 2)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(entries) != 2 || entries[0].ConvSeq != 2 || entries[1].ConvSeq != 3 {
		t.Fatalf("before page = %+v, want seqs [2 3]", entries)
	}
}

// TestLoadHistoryRendersHTML: paged history is delivered with rendered HTML and
// the raw BBCode body intact.
func TestLoadHistoryRendersHTML(t *testing.T) {
	renderer, err := render.New()
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	ctx := context.Background()
	st := memstore.New()
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	if err := st.Append(ctx, []model.Entry{{
		ID: "e1", Session: "Vix", Conv: conv, ConvSeq: 1,
		Kind: "msg", Speaker: "x", Body: "[b]hi[/b]", CreatedAt: time.Now(),
	}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	m := core.NewManager(ctx, core.Config{Store: st, Renderer: renderer})

	entries, err := m.History(ctx, "Vix", conv, nil, nil, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1", len(entries))
	}
	if entries[0].HTML != ": <b>hi</b>" {
		t.Fatalf("HTML = %q, want %q", entries[0].HTML, ": <b>hi</b>")
	}
	if entries[0].Body != "[b]hi[/b]" {
		t.Fatalf("body = %q, want raw BBCode", entries[0].Body)
	}
}
