package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// Cache policies. The API surface defaults to no-store; only read endpoints
// whose content is worth reusing opt into a revalidated private cache.
const (
	// cacheNoStore forbids storing the response. It is the default for every
	// API response, so errors and live state can never be replayed.
	cacheNoStore = "no-store"
	// cachePrivateRevalidate lets the browser's private cache keep the response
	// but forces revalidation on every reuse through the ETag.
	cachePrivateRevalidate = "private, no-cache"
)

// cacheAPI installs the conservative default across the API surface. Handlers
// that emit cacheable reads override it with writeJSONCached; live endpoints and
// every error path (guard rejections, 503s, unknown sessions) keep it.
func cacheAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", cacheNoStore)
		next.ServeHTTP(w, r)
	})
}

// writeJSONCached writes v as JSON with a weak ETag over the encoded body and
// the given Cache-Control policy. A matching If-None-Match yields a 304, so a
// revalidated page costs no body.
//
// The validator covers the rendered body, not the underlying rows: a BBCode
// renderer reload or an offline history clear produces a new ETag for the same
// URL, which is exactly what makes "no-cache" safe across server restarts.
func writeJSONCached(w http.ResponseWriter, r *http.Request, cacheControl string, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	etag := weakETag(body)
	h := w.Header()
	h.Set("Cache-Control", cacheControl)
	h.Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// weakETag returns a weak validator for an encoded response body.
func weakETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `W/"` + hex.EncodeToString(sum[:16]) + `"`
}

// etagMatches reports whether an If-None-Match header selects etag, using the
// weak comparison required for GET revalidation.
func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || weakEqual(part, etag) {
			return true
		}
	}
	return false
}

// weakEqual compares two entity-tags ignoring the weak prefix.
func weakEqual(a, b string) bool {
	return strings.TrimPrefix(a, "W/") == strings.TrimPrefix(b, "W/")
}
