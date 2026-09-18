package session

import "testing"

func TestHighlighter(t *testing.T) {
	h := newHighlighter([]string{"Kira", "  ", "secret phrase", ""})
	cases := []struct {
		body string
		want bool
	}{
		{"hello kira", true},                // case-insensitive
		{"KIRA!", true},                     // substring, not whole word
		{"tell me the Secret Phrase", true}, // whitespace preserved inside a pattern
		{"nothing here", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := h.match(tc.body); got != tc.want {
			t.Errorf("match(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestHighlighterEmptyNeverMatches(t *testing.T) {
	h := newHighlighter(nil)
	if h.match("anything at all") {
		t.Fatal("empty highlighter matched")
	}
	// Whitespace-only patterns are dropped, not turned into a match-all.
	h = newHighlighter([]string{"", "   ", "\t"})
	if h.match("anything at all") {
		t.Fatal("blank-only highlighter matched")
	}
}
