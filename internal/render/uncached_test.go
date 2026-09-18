package render

import (
	"strings"
	"testing"
)

// TestRenderUncachedDoesNotCache: the export path must never populate the shared
// BBCode cache, and must produce the same output as the cached path.
func TestRenderUncachedDoesNotCache(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := "[b]export[/b]"
	got, err := s.RenderUncached(body)
	if err != nil {
		t.Fatalf("RenderUncached: %v", err)
	}
	if _, ok := s.cache[body]; ok {
		t.Fatalf("RenderUncached populated the cache")
	}
	want, err := s.Render(body)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != want {
		t.Fatalf("RenderUncached = %q, want %q", got, want)
	}

	// The message path applies the emote convention, uncached.
	msg, err := s.RenderMessageUncached("/me waves")
	if err != nil {
		t.Fatalf("RenderMessageUncached: %v", err)
	}
	if msg != " waves" {
		t.Fatalf("RenderMessageUncached = %q, want %q", msg, " waves")
	}
	if _, ok := s.cache[messageBody("/me waves")]; ok {
		t.Fatalf("RenderMessageUncached populated the cache")
	}
}

// TestRenderEntryUncachedDoesNotCache: structured entry rendering, including its
// bbcode preprocess step, also stays out of the cache.
func TestRenderEntryUncachedDoesNotCache(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	data := []byte(`{"type":"unknown","message":"[b]roll[/b]"}`)
	got, err := s.RenderEntryUncached("rll", "[b]raw[/b]", data)
	if err != nil {
		t.Fatalf("RenderEntryUncached: %v", err)
	}
	if !strings.Contains(got, "<b>roll</b>") {
		t.Fatalf("RenderEntryUncached = %q, want rendered roll body", got)
	}
	if len(s.cache) != 0 {
		t.Fatalf("RenderEntryUncached populated the cache: %v", s.cache)
	}

	// A plain kind falls back to the chat-message path, uncached.
	msg, err := s.RenderEntryUncached("msg", "[b]hi[/b]", nil)
	if err != nil {
		t.Fatalf("RenderEntryUncached msg: %v", err)
	}
	if msg != ": <b>hi</b>" {
		t.Fatalf("RenderEntryUncached msg = %q", msg)
	}
	if len(s.cache) != 0 {
		t.Fatalf("plain entry render populated the cache: %v", s.cache)
	}
}
