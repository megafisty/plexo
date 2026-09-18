package session

import "strings"

// highlighter matches an incoming channel message body against the configured
// highlight strings. It is built once at session construction and is immutable
// afterwards, so the session actor may read it without synchronization.
type highlighter struct {
	needles []string // lowercased, non-empty
}

// newHighlighter normalizes the configured patterns: surrounding whitespace is
// ignored and empty patterns are dropped (an empty pattern would match every
// message). Matching is case-insensitive.
func newHighlighter(patterns []string) highlighter {
	var h highlighter
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			h.needles = append(h.needles, p)
		}
	}
	return h
}

// match reports whether body contains any configured pattern. It matches the
// wire body exactly as received; the F-Chat server HTML-escapes only &, < and >,
// so plain-word patterns are unaffected. The empty highlighter never matches.
func (h highlighter) match(body string) bool {
	if len(h.needles) == 0 {
		return false
	}
	lower := strings.ToLower(body)
	for _, n := range h.needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}
