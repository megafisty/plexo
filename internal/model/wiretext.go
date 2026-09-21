package model

import "strings"

// DecodeWireEntities reverses the HTML escaping the F-Chat server applies to
// text fields before sending them (fserv's UnicodeTools::escapeHTML): & -> &amp;,
// < -> &lt;, > -> &gt;. It covers every wire field that reaches the user as
// plain text — message bodies, status messages, channel descriptions, and room
// titles — so the official client's escaping is not displayed literally (a
// typed '>' arriving as "&gt;", a room "Rock &amp; Roll").
//
// Decoding is single-level: a user who types "&lt;" arrives as "&amp;lt;",
// decodes to "&lt;", and is re-escaped on output/display to "&lt;". Only these
// three entities are recognized; other entity syntax is left literal so the
// mapping stays exactly inverse to the server's escape and cannot invent
// characters the official client would not produce.
//
// It lives in model rather than fchat because the renderer (which must not
// depend on the protocol package) needs the same normalization before parsing.
func DecodeWireEntities(s string) string {
	if strings.IndexByte(s, '&') < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '&' {
			switch {
			case strings.HasPrefix(s[i:], "&amp;"):
				b.WriteByte('&')
				i += 5
				continue
			case strings.HasPrefix(s[i:], "&lt;"):
				b.WriteByte('<')
				i += 4
				continue
			case strings.HasPrefix(s[i:], "&gt;"):
				b.WriteByte('>')
				i += 4
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
