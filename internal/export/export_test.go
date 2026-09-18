package export

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"plexo/internal/model"
	"plexo/internal/render"
	"plexo/test/memstore"
)

func sampleStore(t *testing.T) *memstore.MemStore {
	t.Helper()
	st := memstore.New()
	epoch := time.Unix(1_700_000_000, 0)
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	if err := st.Append(context.Background(), []model.Entry{
		{ID: "a", Session: "Vix", Conv: conv, ConvSeq: 1, Kind: "msg", Speaker: "Kira", Body: "[b]hi[/b]", CreatedAt: epoch},
		{ID: "b", Session: "Vix", Conv: conv, ConvSeq: 2, Kind: "msg", Speaker: "Vix", Body: "/me waves", CreatedAt: epoch.Add(24 * time.Hour)},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	return st
}

func TestWriteHTML(t *testing.T) {
	r, err := render.New()
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	st := sampleStore(t)
	epoch := time.Unix(1_700_000_000, 0)
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	opts := Options{
		Session: "Vix",
		Conv:    conv,
		FromMs:  epoch.UnixMilli(),
		ToMs:    epoch.Add(48 * time.Hour).UnixMilli(),
		Now:     epoch,
	}
	var buf bytes.Buffer
	if err := WriteHTML(context.Background(), &buf, st, r, opts); err != nil {
		t.Fatalf("WriteHTML: %v", err)
	}
	page := buf.String()
	for _, want := range []string{
		"<!doctype html>", "<style>", "Frontpage", "2 messages",
		"2023-11-14", "2023-11-15", // two day separators
		"Kira", ": <b>hi</b>", // plain message, rendered
		"is-self", " waves", // self flag and emote transform
		"<hr class=\"log-end\">",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q\n%s", want, page)
		}
	}
	if strings.Contains(page, "[b]hi[/b]") {
		t.Errorf("page leaked raw BBCode")
	}
}

func TestWriteHTMLEmptyRange(t *testing.T) {
	r, err := render.New()
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	st := sampleStore(t)
	epoch := time.Unix(1_700_000_000, 0)
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	opts := Options{
		Session: "Vix",
		Conv:    conv,
		// A range after every entry: a valid, empty artifact, not an error.
		FromMs: epoch.Add(72 * time.Hour).UnixMilli(),
		ToMs:   epoch.Add(96 * time.Hour).UnixMilli(),
		Now:    epoch,
	}
	var buf bytes.Buffer
	if err := WriteHTML(context.Background(), &buf, st, r, opts); err != nil {
		t.Fatalf("WriteHTML: %v", err)
	}
	page := buf.String()
	if !strings.Contains(page, "No messages in this range.") {
		t.Fatalf("empty export missing notice:\n%s", page)
	}
	if !strings.Contains(page, "<hr class=\"log-end\">") {
		t.Fatalf("empty export missing ending mark")
	}
}

func TestWriteHTMLNilRendererFallsBackToEscapedText(t *testing.T) {
	st := sampleStore(t)
	epoch := time.Unix(1_700_000_000, 0)
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	opts := Options{Session: "Vix", Conv: conv, FromMs: epoch.UnixMilli(), ToMs: epoch.Add(48 * time.Hour).UnixMilli(), Now: epoch}
	var buf bytes.Buffer
	if err := WriteHTML(context.Background(), &buf, st, nil, opts); err != nil {
		t.Fatalf("WriteHTML: %v", err)
	}
	// With no renderer the body is escaped, not parsed: the BBCode source is left
	// literal and the emote convention is not applied.
	page := buf.String()
	if !strings.Contains(page, "[b]hi[/b]") {
		t.Fatalf("nil renderer did not escape the body:\n%s", page)
	}
	if strings.Contains(page, ": <b>hi</b>") {
		t.Fatalf("nil renderer parsed the body:\n%s", page)
	}
}

func TestFilename(t *testing.T) {
	opts := Options{
		Session: "Vix",
		FromMs:  time.Unix(1_700_000_000, 0).UnixMilli(),
		ToMs:    time.Unix(1_702_000_000, 0).UnixMilli(),
	}
	got := Filename(opts, "The Tavern!")
	if got != "plexo-Vix-The-Tavern-20231114-20231208.html" {
		t.Fatalf("Filename = %q", got)
	}
	if name := Filename(opts, "///"); name != "plexo-Vix-log-20231114-20231208.html" {
		t.Fatalf("Filename empty component = %q", name)
	}
}
