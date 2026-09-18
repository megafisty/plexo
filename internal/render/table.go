package render

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Table is a compiled, immutable BBCode tag table. It maps a tag name to the
// templates and guards used to render that tag. A Table is built once per load
// and only ever read afterwards, so rendering never needs a lock.
type Table struct {
	tags map[string]*tagSpec
}

// tagSpec is one entry of the tag table.
//
// A tag with a ParamCheck and/or a ContentCheck whose value fails recovers as
// literal source with its content still parsed (see parser.go); there is no
// separate invalid template.
type tagSpec struct {
	Valid        *template
	ParamCheck   func(string) bool
	ContentCheck func(string) bool
	// ParamTransform/ContentTransform rewrite a value immediately before it is
	// substituted (checks run on the original). Used for slug tags, whose
	// image URLs are lowercase and case-sensitive, so user-typed uppercase
	// names must be folded. Applied per substitution, not per placeholder.
	ParamTransform   func(string) string
	ContentTransform func(string) string
	Void             bool
}

// transformParam applies the tag's param transform, if any. It returns the
// input unchanged when no transform is configured.
func (s *tagSpec) transformParam(param string) string {
	if s.ParamTransform == nil {
		return param
	}
	return s.ParamTransform(param)
}

// transformContent applies the tag's content transform, if any. The content is
// already-rendered HTML; transforms are selected on the assumption that they
// are HTML-safe (lowercasing leaves every emitted entity valid).
func (s *tagSpec) transformContent(content []byte) []byte {
	if s.ContentTransform == nil {
		return content
	}
	return []byte(s.ContentTransform(string(content)))
}

// rawTable is the JSON on-disk shape of table.json. Validators are named
// references into the built-in registry below, because they are trusted Go
// predicates rather than data.
type rawTable struct {
	Tags map[string]rawTag `json:"tags"`
}

type rawTag struct {
	Valid            string `json:"valid"`
	ParamCheck       string `json:"param_check,omitempty"`
	ContentCheck     string `json:"content_check,omitempty"`
	ParamTransform   string `json:"param_transform,omitempty"`
	ContentTransform string `json:"content_transform,omitempty"`
	Void             bool   `json:"void,omitempty"`
}

// checks is the whitelist of guard predicates a table may reference. Keeping
// them in Go means a table can only ever select a reviewed validator; a typo is
// a load error, not an unguarded template.
var checks = map[string]func(string) bool{
	"color": validColor,
	"url":   validURL,
	"slug":  validSlug,
}

// transforms is the whitelist of value rewrites a table may reference. Like
// checks, they live in Go so a table can only select a reviewed transform.
var transforms = map[string]func(string) string{
	"lower": strings.ToLower,
}

// loadTable parses and compiles a table from its JSON source. Every failure is
// returned, so a bad reload can leave the previously loaded table in place.
func loadTable(src []byte) (*Table, error) {
	var raw rawTable
	if err := json.Unmarshal(src, &raw); err != nil {
		return nil, fmt.Errorf("bbcode: parse table: %w", err)
	}
	if len(raw.Tags) == 0 {
		return nil, fmt.Errorf("bbcode: table has no tags")
	}
	t := &Table{tags: make(map[string]*tagSpec, len(raw.Tags))}
	for name, rt := range raw.Tags {
		if name == "" {
			return nil, fmt.Errorf("bbcode: tag name must not be empty")
		}
		if rt.Valid == "" {
			return nil, fmt.Errorf("bbcode: tag %q: valid template is required", name)
		}
		spec := &tagSpec{Void: rt.Void}

		valid, err := compileTemplate(rt.Valid)
		if err != nil {
			return nil, fmt.Errorf("bbcode: tag %q: valid: %w", name, err)
		}
		spec.Valid = valid

		if rt.ParamCheck != "" {
			fn, ok := checks[rt.ParamCheck]
			if !ok {
				return nil, fmt.Errorf("bbcode: tag %q: unknown param_check %q", name, rt.ParamCheck)
			}
			spec.ParamCheck = fn
		}
		if rt.ContentCheck != "" {
			fn, ok := checks[rt.ContentCheck]
			if !ok {
				return nil, fmt.Errorf("bbcode: tag %q: unknown content_check %q", name, rt.ContentCheck)
			}
			if rt.Void {
				return nil, fmt.Errorf("bbcode: tag %q: void tag cannot have a content_check", name)
			}
			spec.ContentCheck = fn
		}
		if rt.ParamTransform != "" {
			fn, ok := transforms[rt.ParamTransform]
			if !ok {
				return nil, fmt.Errorf("bbcode: tag %q: unknown param_transform %q", name, rt.ParamTransform)
			}
			spec.ParamTransform = fn
		}
		if rt.ContentTransform != "" {
			fn, ok := transforms[rt.ContentTransform]
			if !ok {
				return nil, fmt.Errorf("bbcode: tag %q: unknown content_transform %q", name, rt.ContentTransform)
			}
			if rt.Void {
				return nil, fmt.Errorf("bbcode: tag %q: void tag cannot have a content_transform", name)
			}
			spec.ContentTransform = fn
		}
		t.tags[name] = spec
	}
	return t, nil
}

// --- template compilation -------------------------------------------------

// template is a compiled tag template: an ordered list of literal text and
// placeholder substitutions. {param} is HTML-escaped when substituted; {content}
// is already-rendered, safe HTML and is inserted verbatim.
type template struct {
	segs       []segment
	shape      uint8
	contentIdx int // index of the single {content}, when shape == shapeWrap
}

type segment struct {
	kind uint8
	lit  string
}

const (
	segLit uint8 = iota
	segParam
	segContent
)

// Shapes let the renderer fuse parsing and rendering:
//
//	shapeWrap    one {content}: the opening literal(s) stream out, the content
//	             streams straight into the output, then the closing literal(s).
//	shapeLiteral no {content}: the content is parsed and discarded.
//	shapeMulti   several {content}: the content is rendered once and the
//	             template is rebuilt from it (e.g. [eicon]).
const (
	shapeLiteral uint8 = iota
	shapeWrap
	shapeMulti
)

const (
	paramTok   = "{param}"
	contentTok = "{content}"
)

// compileTemplate turns a template string into segments and classifies it. Any
// brace that does not begin a known placeholder is an error, so a typo like
// {conten} fails the load instead of silently rendering literally.
func compileTemplate(s string) (*template, error) {
	var segs []segment
	for {
		k := strings.IndexByte(s, '{')
		if k < 0 {
			if s != "" {
				segs = append(segs, segment{kind: segLit, lit: s})
			}
			break
		}
		if k > 0 {
			segs = append(segs, segment{kind: segLit, lit: s[:k]})
		}
		s = s[k:]
		switch {
		case strings.HasPrefix(s, paramTok):
			segs = append(segs, segment{kind: segParam})
			s = s[len(paramTok):]
		case strings.HasPrefix(s, contentTok):
			segs = append(segs, segment{kind: segContent})
			s = s[len(contentTok):]
		default:
			return nil, fmt.Errorf("unexpected placeholder near %q", snippet(s))
		}
	}

	t := &template{segs: segs}
	count, contentIdx := 0, -1
	for i, sg := range segs {
		if sg.kind == segContent {
			count++
			contentIdx = i
		}
	}
	switch {
	case count == 0:
		t.shape = shapeLiteral
	case count == 1:
		t.shape = shapeWrap
		t.contentIdx = contentIdx
	default:
		t.shape = shapeMulti
	}
	return t, nil
}

func snippet(s string) string {
	const max = 16
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// --- built-in validators --------------------------------------------------

func validColor(c string) bool {
	if c == "" {
		return false
	}
	if c[0] == '#' {
		rest := c[1:]
		if len(rest) < 3 || len(rest) > 8 {
			return false
		}
		for i := 0; i < len(rest); i++ {
			b := rest[i]
			if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F') {
				return false
			}
		}
		return true
	}
	for i := 0; i < len(c); i++ {
		b := c[i]
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z') {
			return false
		}
	}
	return true
}

func validURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

func validSlug(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		// F-List URL slugs (character/icon names) may contain spaces
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
			b >= '0' && b <= '9' || b == '_' || b == '-' || b == ' ') {
			return false
		}
	}
	return true
}
