// Command tsgen generates the TypeScript boundary types from the Go
// definitions in internal/model, internal/activity, and internal/web, plus the
// protocol unions from their const declarations. TestGeneratedIsCurrent in
// cmd/tsgen keeps the committed output current, so a Go boundary change cannot
// silently leave the client types stale. ui/src/transport/protocol.ts re-exports
// the generated boundary types, so there is no hand-written mirror to drift.
//
// It covers the socket payloads and the request/response shapes the client
// reads over HTTP. Field-level semantics live in the Go source: the generator
// carries no doc comments across.
//
//go:generate go run .
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"plexo/internal/broker"
	"plexo/internal/model"
	"plexo/internal/web"
)

const (
	typesOut = "../../ui/src/transport/types.gen.ts"
	enumsOut = "../../ui/src/transport/enums.ts"
)

// tsTypeRenames maps a Go type name to its TypeScript name. It covers both
// structs whose wire name differs and enums with a client-facing name.
var tsTypeRenames = map[string]string{
	"RenderedEntry":         "Entry",
	"OfficialChannel":       "OfficialChannelInfo",
	"PublicRoom":            "PublicRoomInfo",
	"ChannelCatalogPayload": "ChannelsPayload",
	"WarpmarkView":          "Warpmark",
	"CleanupResult":         "LogCleanupResult",
	"CleanupRequest":        "LogCleanupRequest",
	"Scope":                 "LogActivityScope",
	"Session":               "LogSession",
	"Bucket":                "LogActivityBucket",
	"Participation":         "LogParticipation",
}

// rawOverrides replace a whole generated type; used where the Go struct is not
// the wire shape the client wants.
var rawOverrides = map[string]string{
	// The client iterates the mapping as a keyed table rather than the fixed
	// struct the core holds.
	"SearchMapping": "Record<string, SearchField>",
}

// overrides supply the wire shape for a type whose struct does not match what
// MarshalJSON emits; reflection cannot see a custom marshaler.
type override struct {
	name   string
	fields []field
}

var overrides = map[string]override{
	// RenderedEntry embeds Entry but marshals entryWire, which drops the raw
	// BBCode Body, the persistence-only ConvName/UpstreamID, the unused
	// ReceivedAt, and the per-container Session/Conv.
	"RenderedEntry": {
		name: "Entry",
		fields: []field{
			{name: "id", ts: "string"},
			{name: "convSeq", ts: "number"},
			{name: "kind", ts: "string"},
			{name: "speaker", ts: "string"},
			{name: "createdAtMs", ts: "number"},
			{name: "html", ts: "string"},
		},
	},
}

// fieldOverrides adjust single fields where the TS shape is intentionally
// narrower than the Go type (a client-side subset, a literal union, or a value
// the wire allows but the Go field types as generic).
var fieldOverrides = map[string]map[string]field{
	"LogConvRef":     {"kind": {ts: "LogConvKind"}},
	"LogSessionConv": {"kind": {ts: "LogConvKind"}},
	"CleanupRequest": {
		"op":         {ts: `"age" | "conversation" | "dms"`},
		"session":    {optional: true},
		"kind":       {ts: "LogConvKind", optional: true},
		"id":         {optional: true},
		"days":       {optional: true},
		"maxEntries": {optional: true},
	},
	"SearchEntry": {"id": {ts: "string | number"}},
	"SearchField": {"idtype": {ts: "SearchIDType"}},
}

// roots are the boundary types to emit, plus every named struct they reference.
var roots = []any{
	model.ConvRef{},
	model.StatePayload{},
	model.ConvStatePayload{},
	model.SessionStatePayload{},
	model.TypingPayload{},
	model.SummaryPayload{},
	model.MessagePayload{},
	model.IgnoresPayload{},
	model.InvitesPayload{},
	model.RoomInvite{},
	model.FriendsPayload{},
	model.SearchNotice{},
	model.SearchPayload{},
	model.ErrorPayload{},
	model.PresencePayload{},
	model.MemberInfo{},
	model.Ad{},
	model.AdBody{},
	model.AdChannel{},
	model.AdCampaign{},
	model.AdTargetStatus{},
	model.AdsStatus{},
	model.AdsCampaignView{},
	model.Cursor{},
	model.ConvView{},
	model.WarpmarkView{},
	model.ConvSummary{},
	model.RoomInfo{},
	model.RoomBan{},
	model.RoomAdminRequest{},
	model.SessionSnapshot{},
	model.Snapshot{},
	model.ChannelCatalogPayload{},
	model.OfficialChannel{},
	model.PublicRoom{},
	model.LogConvRef{},
	model.LogSessionConv{},
	model.LogCoverage{},
	model.CleanupResult{},
	model.Result{},
	model.SearchMapping{},
	model.SearchField{},
	model.SearchEntry{},
	model.SearchQuery{},
	web.Hello{},
	web.Subscribe{},
	web.History{},
	web.LogActivityOverview{},
	web.LogActivityDetail{},
	web.CleanupRequest{},
	web.RenderRequest{},
	web.RenderResponse{},
	broker.Batch{},
}

type field struct {
	name     string
	ts       string
	optional bool
}

type jsonOpts struct {
	omitempty bool
	str       bool
}

type generator struct {
	enumTypes map[string]string // Go type name -> TS union name
	enumNames map[string]bool   // every generated TS union name
	usedEnums map[string]bool   // unions referenced by the emitted types
	seen      map[string]reflect.Type
}

func main() {
	types, err := Generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tsgen:", err)
		os.Exit(1)
	}
	enums, err := GenerateEnums()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tsgen:", err)
		os.Exit(1)
	}
	for path, content := range map[string][]byte{typesOut: types, enumsOut: enums} {
		if err := os.WriteFile(filepath.Clean(path), content, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "tsgen:", err)
			os.Exit(1)
		}
	}
}

// Generate renders ui/src/transport/types.gen.ts.
func Generate() ([]byte, error) {
	defs, err := parseEnums()
	if err != nil {
		return nil, err
	}
	g := &generator{
		enumTypes: map[string]string{},
		enumNames: map[string]bool{},
		usedEnums: map[string]bool{},
		seen:      map[string]reflect.Type{},
	}
	for _, e := range defs {
		g.enumNames[e.TSName] = true
		if e.GoName != "" {
			g.enumTypes[e.GoName] = e.TSName
		}
	}

	for _, r := range roots {
		g.collect(reflect.TypeOf(r))
	}
	for _, et := range model.EventTypes {
		g.collect(reflect.TypeOf(et.Payload))
	}

	names := make([]string, 0, len(g.seen))
	for n := range g.seen {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return tsName(names[i]) < tsName(names[j]) })

	var body bytes.Buffer
	for _, goName := range names {
		g.emitType(&body, goName, g.seen[goName])
	}
	body.WriteString("export type Event =")
	for _, et := range model.EventTypes {
		payload, _ := g.tsType(reflect.TypeOf(et.Payload))
		// Session is internal fan-out context and never crosses the wire; the
		// payload or the state key carries the scope instead.
		fmt.Fprintf(&body, "\n\t| { kind: %q; payload: %s }", string(et.Kind), payload)
	}
	body.WriteString(";\n")

	var out bytes.Buffer
	out.WriteString("// Code generated by cmd/tsgen. DO NOT EDIT.\n")
	out.WriteString("// Source of truth: internal/model, internal/activity, and internal/web; run `go generate ./cmd/tsgen` after changing them.\n")
	out.WriteString("//\n")
	out.WriteString("// JSON numbers are TypeScript numbers (int64 included): the wire is JSON and\n")
	out.WriteString("// JSON.parse yields doubles, so a bigint would not describe what arrives.\n\n")
	if len(g.usedEnums) > 0 {
		used := make([]string, 0, len(g.usedEnums))
		for n := range g.usedEnums {
			used = append(used, n)
		}
		sort.Strings(used)
		fmt.Fprintf(&out, "import type { %s } from \"./enums.js\";\n\n", strings.Join(used, ", "))
	}
	out.Write(body.Bytes())
	return out.Bytes(), nil
}

// emitType writes one interface (or a raw type alias for an override).
func (g *generator) emitType(b *bytes.Buffer, goName string, t reflect.Type) {
	name := tsName(goName)
	if raw, ok := rawOverrides[goName]; ok {
		fmt.Fprintf(b, "export type %s = %s;\n\n", name, raw)
		return
	}
	if ov, ok := overrides[goName]; ok {
		fmt.Fprintf(b, "export interface %s {\n", ov.name)
		for _, f := range ov.fields {
			opt := ""
			if f.optional {
				opt = "?"
			}
			fmt.Fprintf(b, "\t%s%s: %s;\n", f.name, opt, f.ts)
		}
		b.WriteString("}\n\n")
		return
	}
	fmt.Fprintf(b, "export interface %s {\n", name)
	for _, f := range g.structFields(goName, t) {
		opt := ""
		if f.optional {
			opt = "?"
		}
		fmt.Fprintf(b, "\t%s%s: %s;\n", f.name, opt, f.ts)
	}
	b.WriteString("}\n\n")
}

// collect adds t and every named struct it references to seen. Embedded structs
// are flattened into their parent rather than emitted separately, matching how
// encoding/json promotes their fields. Event is skipped: it is emitted as the
// discriminated union built from model.EventTypes.
func (g *generator) collect(t reflect.Type) {
	t = deref(t)
	if t.Kind() != reflect.Struct {
		return
	}
	name := t.Name()
	if name == "" || name == "Time" || name == "RawMessage" || name == "Event" {
		return
	}
	if _, ok := g.seen[name]; ok {
		return
	}
	g.seen[name] = t
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && !hasJSONName(f) {
			g.collectEmbedded(f.Type)
			continue
		}
		if _, _, ok := jsonName(f); !ok {
			continue
		}
		g.collect(f.Type)
	}
}

// collectEmbedded walks an embedded struct's fields without registering the
// embedded type itself, so its promoted fields are collected but no duplicate
// interface is emitted for the base type.
func (g *generator) collectEmbedded(t reflect.Type) {
	t = deref(t)
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && !hasJSONName(f) {
			g.collectEmbedded(f.Type)
			continue
		}
		if _, _, ok := jsonName(f); !ok {
			continue
		}
		g.collect(f.Type)
	}
}

// structFields maps a Go struct's exported, JSON-visible fields to TypeScript,
// flattening embedded structs.
func (g *generator) structFields(goName string, t reflect.Type) []field {
	var out []field
	g.appendFields(goName, t, &out)
	return out
}

func (g *generator) appendFields(goName string, t reflect.Type, out *[]field) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && !hasJSONName(f) {
			g.appendFields(goName, deref(f.Type), out)
			continue
		}
		name, opts, ok := jsonName(f)
		if !ok {
			continue
		}
		ts, _ := g.tsType(f.Type)
		if opts.str {
			ts = "string"
		}
		fld := field{name: name, ts: ts, optional: opts.omitempty || f.Type.Kind() == reflect.Pointer}
		if ov, ok := fieldOverrides[goName][name]; ok {
			if ov.ts != "" {
				fld.ts = ov.ts
				if g.enumNames[ov.ts] {
					g.usedEnums[ov.ts] = true
				}
			}
			if ov.optional {
				fld.optional = true
			}
		}
		*out = append(*out, fld)
	}
}

// jsonName returns the wire name of a field and its JSON options, or false for
// unexported and json:"-" fields.
func jsonName(f reflect.StructField) (string, jsonOpts, bool) {
	if f.PkgPath != "" {
		return "", jsonOpts{}, false
	}
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", jsonOpts{}, false
	}
	base, rest, _ := strings.Cut(tag, ",")
	var opts jsonOpts
	for _, o := range strings.Split(rest, ",") {
		switch o {
		case "omitempty":
			opts.omitempty = true
		case "string":
			opts.str = true
		}
	}
	if base == "" {
		base = f.Name
	}
	return base, opts, true
}

// hasJSONName reports whether an embedded field carries an explicit JSON name,
// in which case encoding/json treats it as a named field rather than promoting
// its members.
func hasJSONName(f reflect.StructField) bool {
	base, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	return base != "" && base != "-"
}

// tsType maps a Go type to TypeScript. The bool reports whether the type is a
// named struct the caller should collect separately.
func (g *generator) tsType(t reflect.Type) (string, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Name() == "RawMessage" {
		return "unknown", false
	}
	if name, ok := g.enumTypes[t.Name()]; ok {
		g.usedEnums[name] = true
		return name, false
	}
	if r, ok := tsTypeRenames[t.Name()]; ok && t.Kind() == reflect.Struct {
		return r, false
	}
	switch t.Kind() {
	case reflect.String:
		return "string", false
	case reflect.Bool:
		return "boolean", false
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number", false
	case reflect.Slice, reflect.Array:
		elem, _ := g.tsType(t.Elem())
		return elem + "[]", false
	case reflect.Map:
		elem, _ := g.tsType(t.Elem())
		return "Record<string, " + elem + ">", false
	case reflect.Interface:
		return "unknown", false
	case reflect.Struct:
		if t == reflect.TypeOf(time.Time{}) || t.Name() == "Time" {
			return "string", false
		}
		return tsName(t.Name()), true
	default:
		return "unknown", false
	}
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	return t
}

func tsName(goName string) string {
	if r, ok := tsTypeRenames[goName]; ok {
		return r
	}
	return goName
}
