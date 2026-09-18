package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	htmltmpl "html/template"
	"strconv"
	"strings"
)

// entry.go is the structured-entry half of the renderer: a reloadable
// table that maps a persisted entry kind (currently "rll") to a set of HTML
// templates over the entry's raw server payload. The payload is the stored
// JSON, decoded generically, so a new structured kind needs only a table entry
// and a decoder already in the session.
//
// Table shape (entry_table.json):
//
//	{
//	  "rll": {
//	    "variant_switch": "type",
//	    "preprocess": { "target": "target_lower:lower", "message": "message_html:bbcode" },
//	    "variants": { "dice": "...", "bottle": "...", "default": "..." }
//	  }
//	}
//
// variant_switch names the payload field whose value selects a variant; an
// empty switch, a missing field, or an unknown value all fall back to
// "default". preprocess maps a source payload field to an output-field pipeline
// "dest:step|step": each step is a named string function applied left to right,
// and the result is stored under dest. A source the payload lacks is skipped, so
// one kind's rules can cover variants that lack a field. A pipeline whose last
// step is bbcode is inserted as trusted HTML; every other value (and every field
// not named in preprocess) is auto-escaped by html/template. The table is
// trusted maintenance code.

// entryTable is a compiled, immutable set of entry templates.
type entryTable struct {
	kinds map[string]*entrySpec
}

// entrySpec is one entry kind's compiled templates and preprocessing.
type entrySpec struct {
	variantSwitch string
	preprocess    []preprocessor
	variants      map[string]*htmltmpl.Template
}

// preprocessor is one compiled rule: read source, run steps in order, store the
// result under dest. Every value is a string; the last step decides whether the
// result is inserted as HTML (bbcode) or as plain text.
type preprocessor struct {
	source string
	dest   string
	steps  []string
}

type rawEntryTable map[string]rawEntryKind

type rawEntryKind struct {
	VariantSwitch string            `json:"variant_switch"`
	Preprocess    map[string]string `json:"preprocess"`
	Variants      map[string]string `json:"variants"`
}

// loadEntryTable parses and compiles the entry table. Every failure is
// returned, so a bad reload keeps the previously loaded table.
func loadEntryTable(src []byte) (*entryTable, error) {
	var raw rawEntryTable
	if err := json.Unmarshal(src, &raw); err != nil {
		return nil, fmt.Errorf("render: parse entry table: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("render: entry table has no kinds")
	}
	t := &entryTable{kinds: make(map[string]*entrySpec, len(raw))}
	for kind, rk := range raw {
		if kind == "" {
			return nil, fmt.Errorf("render: entry kind must not be empty")
		}
		if len(rk.Variants) == 0 {
			return nil, fmt.Errorf("render: entry kind %q has no variants", kind)
		}
		if _, ok := rk.Variants["default"]; !ok {
			return nil, fmt.Errorf("render: entry kind %q is missing a default variant", kind)
		}
		spec := &entrySpec{
			variantSwitch: rk.VariantSwitch,
			variants:      make(map[string]*htmltmpl.Template, len(rk.Variants)),
		}
		outputs := make(map[string]string, len(rk.Preprocess))
		for source, def := range rk.Preprocess {
			p, err := parsePreprocess(source, def)
			if err != nil {
				return nil, fmt.Errorf("render: entry kind %q: %w", kind, err)
			}
			if prev, dup := outputs[p.dest]; dup {
				return nil, fmt.Errorf("render: entry kind %q: preprocess output %q set by both %q and %q", kind, p.dest, prev, source)
			}
			outputs[p.dest] = source
			spec.preprocess = append(spec.preprocess, p)
		}
		for name, text := range rk.Variants {
			tmpl, err := htmltmpl.New(kind + "/" + name).Funcs(entryFuncs).Parse(text)
			if err != nil {
				return nil, fmt.Errorf("render: entry kind %q variant %q: %w", kind, name, err)
			}
			spec.variants[name] = tmpl
		}
		t.kinds[kind] = spec
	}
	return t, nil
}

// bodyRender is the BBCode body entrypoint a pipeline step uses. The live path
// passes the cached Render; the export path passes RenderUncached, so a
// structured entry's BBCode is rendered without touching the cache either.
type bodyRender func(string) (string, error)

// preprocessSteps is the whitelist of pipeline functions. Each maps the current
// string to the next; an unknown name is a load error. bbcode delegates to the
// supplied BBCode renderer and yields HTML; the others are plain string
// rewrites. lower reuses the BBCode table's transform so slugs fold identically.
var preprocessSteps = map[string]func(bodyRender, string) (string, error){
	"lower":  func(_ bodyRender, s string) (string, error) { return transforms["lower"](s), nil },
	"bbcode": func(render bodyRender, s string) (string, error) { return render(s) },
}

// parsePreprocess compiles one "dest:step|step" rule. The output field and every
// step name are mandatory, so a typo is a load error rather than a silent no-op.
func parsePreprocess(source, spec string) (preprocessor, error) {
	dest, pipeline, ok := strings.Cut(spec, ":")
	if !ok {
		return preprocessor{}, fmt.Errorf("preprocess %q: missing output field (want \"dest:step|step\")", source)
	}
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return preprocessor{}, fmt.Errorf("preprocess %q: empty output field", source)
	}
	names := strings.Split(pipeline, "|")
	steps := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if _, ok := preprocessSteps[name]; !ok {
			return preprocessor{}, fmt.Errorf("preprocess %q: unknown step %q", source, name)
		}
		steps = append(steps, name)
	}
	return preprocessor{source: source, dest: dest, steps: steps}, nil
}

// RenderEntry renders a stored entry to HTML. Structured kinds with a template
// use it over the raw payload; every other kind (and any kind the table does
// not know) falls back to the chat-message path, so the renderer never drops an
// entry.
func (s *Renderer) RenderEntry(kind, body string, data []byte) (string, error) {
	return s.renderEntry(kind, body, data, s.Render)
}

// RenderEntryUncached is RenderEntry without the BBCode cache. It is for
// one-off artifacts such as a chatlog export, whose bodies are mostly cold and
// must not evict the live cache.
func (s *Renderer) RenderEntryUncached(kind, body string, data []byte) (string, error) {
	return s.renderEntry(kind, body, data, s.RenderUncached)
}

// renderEntry implements both entry paths, parameterized by the BBCode render
// used for the message fallback and for a "bbcode" preprocess step. The entry
// table is read as one immutable snapshot, so a reload cannot swap it
// mid-render.
func (s *Renderer) renderEntry(kind, body string, data []byte, render bodyRender) (string, error) {
	entries := s.shared.Load().entries
	if entries == nil {
		return renderMessage(render, body)
	}
	spec, ok := entries.kinds[kind]
	if !ok {
		return renderMessage(render, body)
	}
	// A structured kind with no stored payload (a row predating the payload
	// column) degrades to plain BBCode rather than an empty template.
	if len(data) == 0 {
		return render(body)
	}
	return spec.render(render, data)
}

// renderMessage applies the chat emote convention before rendering.
func renderMessage(render bodyRender, body string) (string, error) {
	if body == "" {
		return "", nil
	}
	return render(messageBody(body))
}

// render executes the selected variant over the decoded payload. A parse or
// execution failure is returned so the caller can fall back to escaped text.
func (spec *entrySpec) render(render bodyRender, data []byte) (string, error) {
	fields := map[string]any{}
	if len(data) > 0 {
		dec := json.NewDecoder(bytes.NewReader(data))
		// UseNumber keeps ids and results exact and free of float formatting.
		dec.UseNumber()
		if err := dec.Decode(&fields); err != nil {
			return "", fmt.Errorf("render: entry payload: %w", err)
		}
	}

	// Run each preprocess pipeline and store its result under the output field. A
	// source the payload does not carry is skipped, so one kind's rules can cover
	// variants that lack a field. A failed step aborts the whole render.
	for _, p := range spec.preprocess {
		src, ok := fields[p.source]
		if !ok {
			continue
		}
		s := stringify(src)
		for _, name := range p.steps {
			next, err := preprocessSteps[name](render, s)
			if err != nil {
				return "", fmt.Errorf("render: entry preprocess %q: %s: %w", p.source, name, err)
			}
			s = next
		}
		if p.steps[len(p.steps)-1] == "bbcode" {
			fields[p.dest] = htmltmpl.HTML(s)
		} else {
			fields[p.dest] = s
		}
	}

	variant := "default"
	if spec.variantSwitch != "" {
		if name := stringify(fields[spec.variantSwitch]); name != "" {
			variant = name
		}
	}
	tmpl := spec.variants[variant]
	if tmpl == nil {
		tmpl = spec.variants["default"]
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, fields); err != nil {
		return "", fmt.Errorf("render: execute variant %q: %w", variant, err)
	}
	return buf.String(), nil
}

// entryFuncs is the whitelist of template helpers. Keeping them in Go mirrors
// the BBCode table's guard whitelist: a table can only select reviewed
// functions, and an unknown one is a load error.
var entryFuncs = htmltmpl.FuncMap{
	"int":    toInt,
	"signed": signedJoin,
	"join":   joinAny,
	"add":    func(a, b int) int { return a + b },
}

// stringify renders a decoded JSON scalar for template output or a switch.
func stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

// toInt coerces a decoded JSON number or numeric string to int.
func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n)
		}
		if f, err := x.Float64(); err == nil {
			return int(f)
		}
	case string:
		if n, err := strconv.Atoi(x); err == nil {
			return n
		}
	}
	return 0
}

// signedJoin joins a list with explicit signs, so ["2d6", "-3"] reads
// "2d6 - 3" and [9, -3] reads "9 - 3" rather than " + -3".
func signedJoin(v any) string {
	list, ok := v.([]any)
	if !ok {
		return stringify(v)
	}
	var b strings.Builder
	for i, e := range list {
		s := stringify(e)
		if i == 0 {
			b.WriteString(s)
			continue
		}
		if strings.HasPrefix(s, "-") {
			b.WriteString(" - ")
			b.WriteString(strings.TrimPrefix(s, "-"))
		} else {
			b.WriteString(" + ")
			b.WriteString(s)
		}
	}
	return b.String()
}

// joinAny joins a list's elements with sep.
func joinAny(v any, sep string) string {
	list, ok := v.([]any)
	if !ok {
		return stringify(v)
	}
	parts := make([]string, len(list))
	for i, e := range list {
		parts[i] = stringify(e)
	}
	return strings.Join(parts, sep)
}
