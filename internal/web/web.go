// Package web serves the Plexo web client. The UI assets are always embedded
// in the binary; when the on-disk UI directory exists at runtime it takes
// precedence and is served fresh on every request, so edits show up on reload
// without rebuilding.
package web

import (
	"log/slog"
	"net/http"
	"os"

	"plexo/internal/core"
	"plexo/ui"
)

// DefaultDir is the canonical on-disk UI directory.
const DefaultDir = "ui"

// Handler serves the UI, preferring the DefaultDir directory on disk and
// falling back to the embedded copy.
func Handler() http.Handler { return handlerFor(DefaultDir) }

func handlerFor(dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			http.FileServer(http.Dir(dir)).ServeHTTP(w, r)
			return
		}
		http.FileServer(http.FS(ui.FS)).ServeHTTP(w, r)
	})
}

// Options configures the server's core-facing endpoints. A zero Options serves
// only the static client.
type Options struct {
	Manager *core.Manager
	Account *core.Account
	Logger  *slog.Logger

	// PlexoPassword guards browser access. Empty means auth is not required.
	PlexoPassword string
}

// NewServer returns an HTTP server serving the UI on addr, together with the
// browser bridge when Manager and Account are set (nil otherwise). The bridge
// is returned so callers can read the connected-client list. When Manager and
// Account are set, /ws is upgraded to the core protocol.
func NewServer(addr string, opts Options) (*http.Server, *Bridge) {
	auth := NewSessionAuth(opts.PlexoPassword)
	mux := http.NewServeMux()
	mux.Handle("/", Handler())
	mux.Handle("/api/session", http.HandlerFunc(auth.Handle))
	var bridge *Bridge
	if opts.Manager != nil && opts.Account != nil {
		bridge = NewBridge(opts.Manager, opts.Account, auth, opts.Logger)
		mux.Handle("/ws", bridge.Handler())
		reads := &api{manager: opts.Manager, auth: auth}
		// The read endpoints can return large JSON (history pages, the search
		// mapping, enriched search rows), so they negotiate gzip; cacheAPI also
		// defaults every API response to no-store. The tiny /api/session probe
		// stays uncompressed and sets no-store itself.
		api := func(h http.HandlerFunc) http.Handler { return cacheAPI(compressAPI(h)) }
		mux.Handle("/api/history", api(reads.history))
		mux.Handle("/api/logs/index", api(reads.logIndex))
		mux.Handle("/api/logs/coverage", api(reads.logCoverage))
		mux.Handle("/api/logs/activity", api(reads.logActivity))
		// The export streams a self-contained HTML document; it negotiates gzip
		// and keeps the API's no-store default.
		mux.Handle("/api/logs/export", api(reads.logExport))
		mux.Handle("/api/logs/cleanup", api(reads.logCleanup))
		mux.Handle("/api/ads", api(reads.ads))
		mux.Handle("/api/presence", api(reads.presence))
		mux.Handle("/api/room", api(reads.room))
		mux.Handle("/api/mapping", api(reads.mapping))
		mux.Handle("/api/search", api(reads.search))
		// /api/render parses one BBCode body to HTML for on-the-fly UI checks; it
		// is uncached, so it keeps the API's no-store default.
		mux.Handle("/api/render", api(reads.render))
		mux.Handle("/api/settings", api(reads.settings))
		mux.Handle("/api/settings/global", api(reads.settingsGlobal))
		mux.Handle("/api/settings/character", api(reads.settingsCharacter))
		mux.Handle("/api/warpmarks", api(reads.warpmarks))
	}
	return &http.Server{Addr: addr, Handler: mux}, bridge
}
