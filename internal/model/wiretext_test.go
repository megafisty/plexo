package model

import "testing"

// TestDecodeWireEntities: the decode is exactly the inverse of fserv's
// escapeHTML and single-level, so a typed "&lt;" survives as the literal text
// "&lt;" while a server-escaped "&amp;" becomes "&".
func TestDecodeWireEntities(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Secret Room", "Secret Room"},
		{"amp", "Rock &amp; Roll", "Rock & Roll"},
		{"lt", "a &lt; b", "a < b"},
		{"gt", "a &gt; b", "a > b"},
		{"single_level", "&amp;lt;", "&lt;"},
		{"mixed", "a &amp; b &lt; c &gt; d", "a & b < c > d"},
		{"unknown_left_literal", "AT&amp;T &copy;", "AT&T &copy;"},
		{"bare_amp", "a & b", "a & b"},
	}
	for _, c := range cases {
		if got := DecodeWireEntities(c.in); got != c.want {
			t.Errorf("%s: DecodeWireEntities(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
