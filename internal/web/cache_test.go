package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSONCachedETagAndRevalidate(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request) {
		writeJSONCached(w, r, cachePrivateRevalidate, map[string]string{"a": "b"})
	}

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != cachePrivateRevalidate {
		t.Fatalf("Cache-Control = %q, want %q", got, cachePrivateRevalidate)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag is missing")
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("body is not JSON: %q", rec.Body.String())
	}

	// Revalidating with the same validator yields a bodyless 304.
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("304 carried a body: %q", rec.Body.String())
	}

	// A stale validator falls through to a full 200.
	req = httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("If-None-Match", `W/"deadbeef"`)
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("stale validator: status %d, body %d bytes", rec.Code, rec.Body.Len())
	}
}

func TestWriteJSONCachedETagIsContentStable(t *testing.T) {
	encode := func() string {
		rec := httptest.NewRecorder()
		writeJSONCached(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil),
			cachePrivateRevalidate, map[string]int{"n": 1})
		return rec.Header().Get("ETag")
	}
	if a, b := encode(), encode(); a != b {
		t.Fatalf("ETag is not stable: %q vs %q", a, b)
	}
}

func TestETagMatches(t *testing.T) {
	const tag = `W/"abc"`
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{tag, true},
		{`"abc"`, true}, // weak comparison ignores the W/ prefix
		{`"other"`, false},
		{`"other", ` + tag, true},
		{"*", true},
		{`"W/\"abc\" extra"`, false},
	}
	for _, tc := range cases {
		if got := etagMatches(tc.header, tag); got != tc.want {
			t.Errorf("etagMatches(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestCacheAPIDefaultNoStore(t *testing.T) {
	h := cacheAPI(compressAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != cacheNoStore {
		t.Fatalf("Cache-Control = %q, want %q", got, cacheNoStore)
	}
	if got := rec.Header().Get("ETag"); got != "" {
		t.Fatalf("error response carried an ETag: %q", got)
	}
}

func TestCacheAPICacheableOverridesDefault(t *testing.T) {
	h := cacheAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONCached(w, r, cachePrivateRevalidate, []int{1, 2, 3})
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))

	if got := rec.Header().Get("Cache-Control"); got != cachePrivateRevalidate {
		t.Fatalf("Cache-Control = %q, want %q", got, cachePrivateRevalidate)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("1")) {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}
