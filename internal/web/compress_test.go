package web

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// request runs body through compressAPI with the given Accept-Encoding and
// returns the recorded response.
func compressRequest(t *testing.T, accept string, body []byte, prepare func(http.ResponseWriter)) *httptest.ResponseRecorder {
	t.Helper()
	h := compressAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if prepare != nil {
			prepare(w)
		}
		_, _ = w.Write(body)
	}))
	r := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	if accept != "" {
		r.Header.Set("Accept-Encoding", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestCompressAPILargeBody(t *testing.T) {
	body := []byte(strings.Repeat("history-entry ", 512)) // ~7 KiB
	rec := compressRequest(t, "gzip", body, nil)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", got)
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("round-tripped body differs (%d vs %d bytes)", len(got), len(body))
	}
}

func TestCompressAPISmallBodyStaysPlain(t *testing.T) {
	body := []byte(`{"entries":[]}`)
	rec := compressRequest(t, "gzip", body, nil)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("body = %q, want %q", rec.Body.String(), body)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", got)
	}
}

func TestCompressAPINoAcceptEncoding(t *testing.T) {
	body := []byte(strings.Repeat("x", 4096))
	rec := compressRequest(t, "", body, nil)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatal("body was altered without negotiation")
	}
}

func TestCompressAPIQualityZero(t *testing.T) {
	rec := compressRequest(t, "gzip;q=0", []byte(strings.Repeat("x", 4096)), nil)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty for gzip;q=0", got)
	}
}

func TestCompressAPIAlreadyEncoded(t *testing.T) {
	body := []byte(strings.Repeat("x", 4096))
	rec := compressRequest(t, "gzip", body, func(w http.ResponseWriter) {
		w.Header().Set("Content-Encoding", "br")
	})
	if got := rec.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding = %q, want br", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatal("an already-encoded body was compressed again")
	}
}

func TestCompressAPIEmptyStatus(t *testing.T) {
	h := compressAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	r := httptest.NewRequest(http.MethodDelete, "/api/settings/global", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty", got)
	}
}

func TestAcceptsGzip(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"gzip", true},
		{"x-gzip", true},
		{"*", true},
		{"deflate, gzip", true},
		{"gzip;q=0.5", true},
		{"gzip;q=0", false},
		{"br", false},
		{"deflate", false},
		{"gzip;q=0, *;q=1", true},
		{"*;q=0, gzip", true},
		{"*;q=0", false},
	}
	for _, tc := range cases {
		if got := acceptsGzip(tc.header); got != tc.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}
