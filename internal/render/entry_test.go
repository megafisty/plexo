package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func renderEntry(t *testing.T, s *Renderer, kind, body string, data []byte) string {
	t.Helper()
	h, err := s.RenderEntry(kind, body, data)
	if err != nil {
		t.Fatalf("RenderEntry(%q): %v", kind, err)
	}
	return h
}

func TestRenderEntryEmbedded(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		kind, body string
		data       string
		wantSub    []string
	}{
		{
			name: "dice",
			kind: "rll", body: "raw",
			data: `{"type":"dice","character":"Kira","rolls":["2d6","3"],"results":[9,3],"endresult":12,"message":"ignored"}`,
			wantSub: []string{
				"roll-dice", "🎲", `roll-expr">2d6 &#43; 3<`, `title="9 &#43; 3"`, `roll-total">12<`,
			},
		},
		{
			name: "dice negative",
			kind: "rll", body: "raw",
			data:    `{"type":"dice","rolls":["2d6","-3"],"results":[-3],"endresult":6,"message":""}`,
			wantSub: []string{"2d6 - 3", `roll-total">6<`},
		},
		{
			name: "bottle",
			kind: "rll", body: "raw",
			data:    `{"type":"bottle","target":"Neko","message":"ignored"}`,
			wantSub: []string{"roll-bottle", "images/avatar/neko.png", `href="https://www.f-list.net/c/Neko"`, ">Neko<"},
		},
		{
			name: "unknown type uses default with rendered message",
			kind: "rll", body: "raw",
			data:    `{"type":"mystery","message":"[b]hi[/b]"}`,
			wantSub: []string{"roll-unknown", "<b>hi</b>"},
		},
		{
			name: "missing type uses default",
			kind: "rll", body: "raw",
			data:    `{"message":"plain"}`,
			wantSub: []string{"roll-unknown", "plain"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderEntry(t, s, tc.kind, tc.body, []byte(tc.data))
			for _, sub := range tc.wantSub {
				if !strings.Contains(got, sub) {
					t.Fatalf("RenderEntry = %q, want substring %q", got, sub)
				}
			}
		})
	}
}

// TestRenderEntryFallsBackForPlainKinds: kinds without templates (and an empty
// payload) keep the chat-message path.
func TestRenderEntryFallsBackForPlainKinds(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if got := renderEntry(t, s, "msg", "hello", nil); got != ": hello" {
		t.Fatalf("msg = %q, want %q", got, ": hello")
	}
	if got := renderEntry(t, s, "dm", "/me waves", nil); got != " waves" {
		t.Fatalf("dm emote = %q", got)
	}
	// A structured kind with no payload degrades to plain BBCode, not an empty
	// template.
	if got := renderEntry(t, s, "rll", "[b]rolled[/b]", nil); got != "<b>rolled</b>" {
		t.Fatalf("rll without payload = %q", got)
	}
}

func TestRenderEntryMalformedPayload(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenderEntry("rll", "raw", []byte("{not json")); err == nil {
		t.Fatal("expected an error for malformed payload")
	}
}

// TestRenderEntryTableCustom exercises variant_switch on a non-type field and a
// custom template table loaded from disk.
func TestRenderEntryTableCustom(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entry_table.json")
	writeEntryTable(t, path, `{
		"note": {
			"variant_switch": "level",
			"preprocess": {"body": "body_html:bbcode"},
			"variants": {
				"warn": "W:{{ .body_html }}",
				"default": "D:{{ .body_html }}"
			}
		}
	}`)
	s, err := newWithPaths("", path)
	if err != nil {
		t.Fatal(err)
	}
	if got := renderEntry(t, s, "note", "x", []byte(`{"level":"warn","body":"[b]hi[/b]"}`)); got != "W:<b>hi</b>" {
		t.Fatalf("warn = %q", got)
	}
	if got := renderEntry(t, s, "note", "x", []byte(`{"level":"info","body":"[b]hi[/b]"}`)); got != "D:<b>hi</b>" {
		t.Fatalf("default = %q", got)
	}
}

func TestRenderEntryReloadKeepsOldOnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entry_table.json")
	writeEntryTable(t, path, `{"note":{"variants":{"default":"ok"}}}`)
	s, err := newWithPaths("", path)
	if err != nil {
		t.Fatal(err)
	}
	if got := renderEntry(t, s, "note", "", []byte("{}")); got != "ok" {
		t.Fatalf("before reload = %q", got)
	}
	if err := os.WriteFile(path, []byte("not json {"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err == nil {
		t.Fatal("expected reload error")
	}
	if got := renderEntry(t, s, "note", "", []byte("{}")); got != "ok" {
		t.Fatalf("after failed reload = %q, want old table", got)
	}
}

func TestRenderEntryPreprocessPipeline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entry_table.json")
	writeEntryTable(t, path, `{
		"note": {
			"preprocess": {"name": "slug:lower", "body": "body_html:bbcode"},
			"variants": {"default": "<a href=\"/c/{{ .slug }}\">{{ .name }}</a>|{{ .body_html }}"}
		}
	}`)
	s, err := newWithPaths("", path)
	if err != nil {
		t.Fatal(err)
	}
	got := renderEntry(t, s, "note", "x", []byte(`{"name":"Neko","body":"[b]hi[/b]"}`))
	want := `<a href="/c/neko">Neko</a>|<b>hi</b>`
	if got != want {
		t.Fatalf("RenderEntry = %q, want %q", got, want)
	}
}

func TestRenderEntryPreprocessSkipsMissingSource(t *testing.T) {
	// The shared rll preprocess names target, which dice payloads lack; a missing
	// source must not fail the render.
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	got := renderEntry(t, s, "rll", "raw", []byte(`{"type":"dice","rolls":["2d6"],"results":[9],"endresult":9}`))
	if !strings.Contains(got, `roll-total">9<`) {
		t.Fatalf("dice without target = %q", got)
	}
}

func TestRenderEntryPreprocessLoadErrors(t *testing.T) {
	cases := map[string]string{
		"missing colon":      `{"note":{"preprocess":{"x":"lower"},"variants":{"default":"ok"}}}`,
		"empty output field": `{"note":{"preprocess":{"x":":lower"},"variants":{"default":"ok"}}}`,
		"unknown step":       `{"note":{"preprocess":{"x":"y:nope"},"variants":{"default":"ok"}}}`,
		"empty step":         `{"note":{"preprocess":{"x":"y:"},"variants":{"default":"ok"}}}`,
		"duplicate output":   `{"note":{"preprocess":{"x":"y:lower","z":"y:lower"},"variants":{"default":"ok"}}}`,
	}
	for name, table := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "entry_table.json")
			writeEntryTable(t, path, table)
			if _, err := newWithPaths("", path); err == nil {
				t.Fatal("expected a load error")
			}
		})
	}
}

func writeEntryTable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
