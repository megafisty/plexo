// Package render turns stored entry sources into display HTML. A Renderer
// shares two reloadable tables across its clones:
//
//   - BBCode: parsing and rendering are fused into one Go pass (see parser.go);
//     the tag set lives in a JSON table (table.json), compiled once into
//     templates and guards and published atomically on reload.
//   - Entries: structured entry kinds (currently RLL dice/bottle) rendered from
//     a kind-keyed JSON template table (entry_table.json) over the stored
//     server payload, with listed fields preprocessed through the BBCode path.
//
// Both tables are trusted maintenance code, so no sandboxing is attempted.
package render

import (
	_ "embed"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

//go:embed table.json
var embedded []byte

//go:embed entry_table.json
var embeddedEntry []byte

// tables is the immutable compiled form of both JSON tables. A renderer and
// all its clones share one *tables through an atomic pointer; publishing a new
// one on Reload reaches every clone at once. gen is bumped on each publish so
// each instance can drop its own cache when it observes a new generation.
type tables struct {
	table   *Table
	entries *entryTable
	gen     uint64
}

// Renderer renders BBCode bodies and structured entries to HTML and caches
// BBCode results. It is safe for concurrent use. Compiled tables are immutable
// and shared between an instance and its clones; each instance owns its cache
// and its own lock, so live rendering never contends across sessions.
type Renderer struct {
	path      string // on-disk BBCode table path; empty means use the embedded copy
	entryPath string // on-disk entry template path; empty means use the embedded copy

	// shared points at the immutable compiled tables. Clones share it, so a
	// reload publishes to every instance without a cross-session lock.
	shared *atomic.Pointer[tables]

	mu    sync.Mutex
	cache map[string]string
	gen   uint64
	max   int
}

// Canonical on-disk table paths. Both tables are embedded in the binary; when a
// file also exists at the path at runtime it takes precedence, so editing it in
// a dev checkout is enough.
const (
	DefaultPath      = "internal/render/table.json"
	DefaultEntryPath = "internal/render/entry_table.json"
)

// New compiles both tables, preferring the on-disk copies when they exist and
// otherwise falling back to the embedded copies.
func New() (*Renderer, error) {
	return newWithPaths(DefaultPath, DefaultEntryPath)
}

func newWithPaths(path, entryPath string) (*Renderer, error) {
	s := &Renderer{
		path:      path,
		entryPath: entryPath,
		max:       2048,
		cache:     make(map[string]string),
		shared:    &atomic.Pointer[tables]{},
	}
	t, err := s.loadTables()
	if err != nil {
		return nil, err
	}
	s.shared.Store(t)
	return s, nil
}

// loadTables compiles both tables from the instance's source paths.
func (s *Renderer) loadTables() (*tables, error) {
	t, err := loadTable(s.source())
	if err != nil {
		return nil, err
	}
	et, err := loadEntryTable(s.entrySource())
	if err != nil {
		return nil, err
	}
	return &tables{table: t, entries: et}, nil
}

// Clone returns a renderer that shares this instance's compiled tables but owns
// its own cache. Each session gets one so renders never contend on a shared
// lock, while a Reload still reaches every clone through the shared pointer.
func (s *Renderer) Clone() *Renderer {
	return &Renderer{
		path:      s.path,
		entryPath: s.entryPath,
		max:       s.max,
		cache:     make(map[string]string),
		shared:    s.shared,
	}
}

func (s *Renderer) source() []byte {
	if s.path != "" {
		if b, err := os.ReadFile(s.path); err == nil {
			return b
		}
	}
	return embedded
}

func (s *Renderer) entrySource() []byte {
	if s.entryPath != "" {
		if b, err := os.ReadFile(s.entryPath); err == nil {
			return b
		}
	}
	return embeddedEntry
}

// Render returns the HTML for one BBCode body. Results are cached by body. The
// table snapshot and its generation are read before taking the instance lock,
// so a reload landing mid-render publishes a new generation that the next call
// observes and flushes the cache for. Parses are sub-microsecond, so the
// per-instance lock does not meaningfully serialize rendering.
func (s *Renderer) Render(body string) (string, error) {
	t := s.shared.Load()
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.gen != t.gen {
		// A reload published new tables; an old-table result must not be served
		// under the new generation.
		s.cache = make(map[string]string)
		s.gen = t.gen
	}
	if h, ok := s.cache[body]; ok {
		return h, nil
	}
	h := string(renderBody(body, t.table))
	// Evict a single entry rather than clearing the whole cache, so a long run
	// with many distinct bodies does not repeatedly flush hot entries.
	if len(s.cache) >= s.max {
		for k := range s.cache {
			delete(s.cache, k)
			break
		}
	}
	s.cache[body] = h
	return h, nil
}

// RenderUncached renders one BBCode body without touching the cache. It is for
// one-off artifacts (a chatlog export reads an arbitrarily large, mostly-cold
// range) that would otherwise evict the live cache's hot entries for no reuse.
// The table is read as one immutable snapshot, so a reload landing mid-render
// cannot mix table versions.
func (s *Renderer) RenderUncached(body string) (string, error) {
	return string(renderBody(body, s.shared.Load().table)), nil
}

// RenderMessage returns the HTML for one chat message body, applying F-Chat's
// client-side emote convention first. Because Plexo's client already renders
// the speaker inline before the body, a leading "/me" action loses its prefix
// so the text flows after the name; every other message gains a ": " separator
// so it reads "Name: text". The transformed body is what Render caches, so the
// parse is still shared and paid once.
func (s *Renderer) RenderMessage(body string) (string, error) {
	if body == "" {
		return "", nil
	}
	return s.Render(messageBody(body))
}

// RenderMessageUncached is RenderMessage without the cache, for exports.
func (s *Renderer) RenderMessageUncached(body string) (string, error) {
	if body == "" {
		return "", nil
	}
	return s.RenderUncached(messageBody(body))
}

// messageBody applies the emote convention to one message body. A body that
// begins with the "/me" token (followed by whitespace or nothing) loses the
// token; the separating whitespace is kept, so the client's "Name" followed by
// " waves" reads "Name waves". Any other body is prefixed with ": ".
func messageBody(body string) string {
	rest, ok := strings.CutPrefix(body, "/me")
	if ok && (rest == "" || isSpace(rest[0])) {
		return rest
	}
	return ": " + body
}

func isSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

// Reload recompiles both tables from disk and publishes them to this renderer
// and every clone. Each instance drops its cache when it next observes the new
// generation. On error the previous tables stay in place, so a bad edit never
// breaks the running process.
func (s *Renderer) Reload() error {
	t, err := s.loadTables()
	if err != nil {
		return err
	}
	if prev := s.shared.Load(); prev != nil {
		t.gen = prev.gen + 1
	}
	s.shared.Store(t)
	return nil
}

// Path returns the configured BBCode table path, or "" when using the embedded
// copy.
func (s *Renderer) Path() string { return s.path }

// EntryPath returns the configured entry-template path, or "" when using the
// embedded copy.
func (s *Renderer) EntryPath() string { return s.entryPath }
