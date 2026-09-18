package render

import "strings"

// maxDepth caps tag nesting. Deeper opens are treated like unknown tags (the
// source is emitted literally and parsing continues), so hostile input cannot
// exhaust the goroutine stack.
const maxDepth = 128

// noparseTag is the one tag whose content is emitted as HTML-escaped source
// instead of being parsed. The behavior is hardcoded in the parser, not
// selectable from the table, so no other tag can skip parsing. The table entry
// only supplies the surrounding template.
const noparseTag = "noparse"

// noparseClose is the exact close tag noparse looks for. The first match ends
// the run (noparse does not nest), matching the case-sensitive tag matching
// used everywhere else.
const noparseClose = "[/" + noparseTag + "]"

// parser is the fused parser/renderer. It walks the raw body once and appends
// HTML straight into an output buffer; there is no intermediate tree. The only
// state is a scratch buffer reused for the rare multi-{content} templates.
type parser struct {
	table   *Table
	scratch []byte
}

// renderBody renders one BBCode body to HTML using table t.
func renderBody(body string, t *Table) []byte {
	body = decodeWireEntities(body)
	p := parser{table: t}
	out := make([]byte, 0, len(body)+len(body)/8+16)
	out, _, _ = p.seq(out, body, 0, "", 0)
	return out
}

// seq appends the rendered form of body[i:] to out, stopping at end of input or
// at a close tag matching stop. It returns the updated out, the offset just past
// the matched close tag (or len(body)), and whether a matching close was seen.
//
// Fusion works by streaming: for a wrap tag the opening literal is appended
// before recursing and the closing literal after, with the rendered content
// landing directly between them.
//
// Recovery: malformed input never becomes an error marker and never swallows a
// subtree. Instead the raw source of the offending construct is emitted
// (HTML-escaped) and parsing resumes immediately after it, so a hand-typed
// mistake stays readable and the surrounding text and later tags still render.
// A valid open whose close never arrives is rolled back to where the tag began
// and replaced with its raw source, keeping whatever followed it.
func (p *parser) seq(out []byte, body string, i int, stop string, depth int) ([]byte, int, bool) {
	for i < len(body) {
		rel := strings.IndexByte(body[i:], '[')
		if rel < 0 {
			out = appendEscaped(out, body[i:])
			return out, len(body), false
		}
		open := i + rel
		if open > i {
			out = appendEscaped(out, body[i:open])
		}

		rel = strings.IndexByte(body[open:], ']')
		if rel < 0 {
			// An unterminated '[' is literal text; emit it and move on.
			out = appendEscaped(out, body[open:open+1])
			i = open + 1
			continue
		}
		end := open + rel
		inner := body[open+1 : end]

		if len(inner) != 0 && inner[0] == '/' {
			name := inner[1:]
			if stop != "" && name == stop {
				return out, end + 1, true
			}
			// A stray or mismatched close is literal text; keep going after it.
			out = appendSource(out, body, open, end+1)
			i = end + 1
			continue
		}

		name, param := inner, ""
		if eq := strings.IndexByte(inner, '='); eq >= 0 {
			name, param = inner[:eq], inner[eq+1:]
		}

		spec := p.table.tags[name]
		if spec == nil || depth >= maxDepth {
			// Unknown tag or too deep: fail fast, keep the source readable, and
			// resume after the tag so its content and later siblings parse.
			out = appendSource(out, body, open, end+1)
			i = end + 1
			continue
		}

		// noparse is structural and hardcoded: its content is emitted as
		// HTML-escaped source and never parsed, so typed BBCode shows literally.
		// No table field can select this for another tag. The entry's template
		// still owns the markup, but its params are ignored.
		if name == noparseTag {
			rest := body[end+1:]
			if cls := strings.Index(rest, noparseClose); cls >= 0 {
				escaped := appendEscaped(p.scratch[:0], rest[:cls])
				out = spec.Valid.emit(out, "", escaped)
				i = end + 1 + cls + len(noparseClose)
				continue
			}
			// No close: same rollback as any other valid open whose close never
			// arrives, so the construct stays visible and parsing resumes at end.
			out = appendSource(out, body, open, len(body))
			i = len(body)
			continue
		}

		if spec.Void {
			if spec.ParamCheck != nil && !spec.ParamCheck(param) {
				out = appendSource(out, body, open, end+1)
				i = end + 1
				continue
			}
			out = spec.Valid.emit(out, spec.transformParam(param), nil)
			i = end + 1
			continue
		}

		if spec.ParamCheck != nil && !spec.ParamCheck(param) {
			// Known tag, bad param: show the opening tag as written and resume
			// right after it, so its content and later siblings still render.
			// Checking here (before any content buffering) is the same recovery
			// a failed content guard uses below, and the cheapest of the two:
			// no content has to be parsed or rolled back to reject the tag.
			out = appendSource(out, body, open, end+1)
			i = end + 1
			continue
		}

		// Fast path: no content guard or transform, and the chosen template has a
		// single {content}. The opening literal streams out, the content streams
		// in directly, then the closing literal.
		if spec.ContentCheck == nil && spec.ContentTransform == nil {
			tpl := spec.Valid
			param = spec.transformParam(param)
			if tpl.shape == shapeWrap {
				mark := len(out)
				out = tpl.emitPrefix(out, param)
				next, closed := 0, false
				out, next, closed = p.seq(out, body, end+1, name, depth+1)
				if !closed {
					out = appendSource(out[:mark], body, open, next)
					i = next
					continue
				}
				out = tpl.emitSuffix(out, param)
				i = next
				continue
			}
			// shapeLiteral (content discarded) or shapeMulti (content used more
			// than once): render the content, then rebuild from it.
			out, i = p.tagWithContent(out, body, open, end, name, depth, param, tpl)
			continue
		}

		// Buffered path: a content guard and/or content transform needs the whole
		// content first, so render it, then pick a template and rebuild. Guards
		// run on the untransformed content; the transform only affects output.
		mark := len(out)
		var next int
		var closed bool
		out, next, closed = p.seq(out, body, end+1, name, depth+1)
		content := out[mark:]
		if spec.ContentCheck != nil && !spec.ContentCheck(string(content)) {
			// Known tag, bad content: same recovery as a bad param, but the
			// content is already rendered, so keep it and only re-emit the
			// opening tag (and, when present, the consumed close) as source.
			// This never swallows the subtree and costs no second parse.
			p.scratch = appendSource(p.scratch[:0], body, open, end+1)
			p.scratch = append(p.scratch, content...)
			if closed {
				p.scratch = appendSource(p.scratch, body, next-len(name)-3, next)
			}
			out = append(out[:mark], p.scratch...)
			i = next
			continue
		}
		if !closed {
			// No close: show the raw construct and continue after it.
			out = appendSource(out[:mark], body, open, next)
			i = next
			continue
		}
		out = p.rebuild(out, mark, spec.Valid, spec.transformParam(param), spec.transformContent(content))
		i = next
	}
	return out, i, false
}

// tagWithContent renders body[end+1:]'s content into out, then replaces it with
// the expansion of tpl. Used for templates that cannot be streamed: those that
// discard the content (shapeLiteral) or use it more than once (shapeMulti). A
// missing close falls back to the raw source from the open tag onward. It
// returns the new output and the offset just past the matched close tag.
func (p *parser) tagWithContent(out []byte, body string, open, end int, name string, depth int, param string, tpl *template) ([]byte, int) {
	mark := len(out)
	out, next, closed := p.seq(out, body, end+1, name, depth+1)
	if !closed {
		return appendSource(out[:mark], body, open, next), next
	}
	return p.rebuild(out, mark, tpl, param, out[mark:]), next
}

// emit appends a whole template with the given substitutions. Content is
// inserted verbatim (it is already escaped HTML); param is escaped here.
func (t *template) emit(out []byte, param string, content []byte) []byte {
	for i := range t.segs {
		sg := &t.segs[i]
		switch sg.kind {
		case segLit:
			out = append(out, sg.lit...)
		case segParam:
			out = appendEscaped(out, param)
		case segContent:
			out = append(out, content...)
		}
	}
	return out
}

// emitPrefix appends everything before the single {content}.
func (t *template) emitPrefix(out []byte, param string) []byte {
	for i := 0; i < t.contentIdx; i++ {
		sg := &t.segs[i]
		switch sg.kind {
		case segLit:
			out = append(out, sg.lit...)
		case segParam:
			out = appendEscaped(out, param)
		}
	}
	return out
}

// emitSuffix appends everything after the single {content}.
func (t *template) emitSuffix(out []byte, param string) []byte {
	for i := t.contentIdx + 1; i < len(t.segs); i++ {
		sg := &t.segs[i]
		switch sg.kind {
		case segLit:
			out = append(out, sg.lit...)
		case segParam:
			out = appendEscaped(out, param)
		}
	}
	return out
}

// rebuild replaces the rendered content at out[mark:] with the expansion of t,
// which may reference the content more than once. The content is copied through
// scratch before out is truncated, so the source stays intact.
func (p *parser) rebuild(out []byte, mark int, t *template, param string, content []byte) []byte {
	p.scratch = t.emit(p.scratch[:0], param, content)
	out = append(out[:mark], p.scratch...)
	return out
}

// decodeWireEntities reverses the HTML escaping the F-Chat server applies to
// message bodies, status messages, and channel descriptions before sending
// them (fserv's UnicodeTools::escapeHTML): & -> &amp;, < -> &lt;, > -> &gt;.
// The renderer escapes on output, so without this step the server's entities
// are escaped a second time and a typed '>' displays as the literal "&gt;".
//
// Decoding is single-level: a user who types "&lt;" arrives as "&amp;lt;",
// decodes to "&lt;", and renders back to "&amp;lt;" (displaying "&lt;").
// Only these three entities are recognized; other entity syntax is left
// literal so the mapping stays exactly inverse to the server's escape and
// cannot invent characters the official client would not produce.
func decodeWireEntities(s string) string {
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

// appendSource appends the raw source of a malformed construct, body[start:end],
// HTML-escaped. It keeps hand-typed tag text visible instead of replacing it
// with an error marker, so the reader can still see (and fix) what was meant.
func appendSource(dst []byte, body string, start, end int) []byte {
	return appendEscaped(dst, body[start:end])
}

// appendEscaped appends s with Go html.EscapeString semantics: &, ', <, > and "
// become entities. Runs of ordinary bytes are copied in bulk.
func appendEscaped(dst []byte, s string) []byte {
	start := 0
	for i := 0; i < len(s); i++ {
		var esc string
		switch s[i] {
		case '&':
			esc = "&amp;"
		case '\'':
			esc = "&#39;"
		case '<':
			esc = "&lt;"
		case '>':
			esc = "&gt;"
		case '"':
			esc = "&#34;"
		default:
			continue
		}
		dst = append(dst, s[start:i]...)
		dst = append(dst, esc...)
		start = i + 1
	}
	return append(dst, s[start:]...)
}
