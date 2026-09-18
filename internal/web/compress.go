package web

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// minCompressSize is the smallest response body worth gzipping. Below it the
// gzip header and trailer cost more than they save, and most API replies (acks,
// small errors, a settings document) stay tiny.
const minCompressSize = 1024

// gzipPool reuses gzip writers across requests. gzip.NewWriterLevel allocates a
// sizeable deflate state that is wasteful to rebuild for every response.
var gzipPool = sync.Pool{
	New: func() any {
		zw, err := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		if err != nil {
			panic(err) // BestSpeed is always a valid level
		}
		return zw
	},
}

// compressAPI wraps an API handler so its response is gzipped when the client
// accepts it and the body is large enough to be worth it. It is applied to the
// HTTP read endpoints only: history pages and the search mapping are the large
// payloads, and they travel over HTTP precisely so they do not burden the
// event socket.
//
// The decision is deferred until the first kilobyte of body is known, so short
// replies stay uncompressed with an exact Content-Length and no gzip framing.
func compressAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The chosen representation depends on Accept-Encoding, so caches must
		// key on it whether or not this particular response is compressed.
		w.Header().Add("Vary", "Accept-Encoding")
		if r.Header.Get("Range") != "" || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{w: w}
		defer cw.finish()
		next.ServeHTTP(cw, r)
	})
}

// compressWriter buffers the first minCompressSize bytes before choosing a
// representation. Once the body outgrows the buffer it switches to a pooled
// gzip stream and passes the buffered prefix through it.
type compressWriter struct {
	w http.ResponseWriter

	status     int
	headerSent bool
	gz         *gzip.Writer
	buf        bytes.Buffer
}

// Header implements http.ResponseWriter.
func (c *compressWriter) Header() http.Header { return c.w.Header() }

// WriteHeader implements http.ResponseWriter. The status is recorded rather
// than forwarded so Content-Encoding can still be added at commit time.
func (c *compressWriter) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

// Write implements http.ResponseWriter.
func (c *compressWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if c.gz != nil {
		return c.gz.Write(p)
	}
	if c.buf.Len()+len(p) <= minCompressSize {
		c.buf.Write(p)
		return len(p), nil
	}
	if !c.compressible() {
		c.sendHeader()
		if err := c.flushBuffer(); err != nil {
			return 0, err
		}
		return c.w.Write(p)
	}
	if err := c.startGzip(); err != nil {
		return 0, err
	}
	return c.gz.Write(p)
}

// Flush implements http.Flusher so a streaming handler can push each chunk to
// the client as it is produced. Before the gzip threshold is crossed there is
// nothing committed to flush, so it deliberately does nothing: flushing the
// underlying writer there would commit the headers before the representation is
// chosen, and the buffered prefix (or whole body) is sent by finish.
func (c *compressWriter) Flush() {
	if c.gz != nil {
		_ = c.gz.Flush()
	}
	if !c.headerSent {
		return
	}
	if f, ok := c.w.(http.Flusher); ok {
		f.Flush()
	}
}

// finish commits the response. The middleware defers it so a body that never
// reached the threshold is sent verbatim.
func (c *compressWriter) finish() {
	if c.gz != nil {
		_ = c.gz.Close()
		gzipPool.Put(c.gz)
		c.gz = nil
		return
	}
	if c.headerSent {
		return
	}
	c.sendHeader()
	if bodyAllowed(c.status) {
		_ = c.flushBuffer()
	}
}

// compressible reports whether the recorded response may carry gzip framing.
// A handler that already set Content-Encoding owns the representation, and
// bodyless statuses must not gain one.
func (c *compressWriter) compressible() bool {
	return c.w.Header().Get("Content-Encoding") == "" && bodyAllowed(c.status)
}

// startGzip commits the compressed representation and runs the buffered prefix
// through the gzip stream.
func (c *compressWriter) startGzip() error {
	h := c.w.Header()
	h.Set("Content-Encoding", "gzip")
	// The compressed length is unknown until the stream closes; a stale
	// Content-Length would truncate the response.
	h.Del("Content-Length")
	zw := gzipPool.Get().(*gzip.Writer)
	zw.Reset(c.w)
	c.gz = zw
	c.sendHeader()
	return c.flushBuffer()
}

// flushBuffer writes and clears any buffered body bytes. While gzip is active
// it routes them through the stream; otherwise they go to the wire directly.
func (c *compressWriter) flushBuffer() error {
	if c.buf.Len() == 0 {
		return nil
	}
	var (
		n   int
		err error
	)
	if c.gz != nil {
		n, err = c.gz.Write(c.buf.Bytes())
	} else {
		n, err = c.w.Write(c.buf.Bytes())
	}
	c.buf.Next(n)
	return err
}

// sendHeader forwards the recorded status exactly once.
func (c *compressWriter) sendHeader() {
	if c.headerSent {
		return
	}
	c.headerSent = true
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.w.WriteHeader(c.status)
}

// bodyAllowed reports whether a status may carry a response body.
func bodyAllowed(status int) bool {
	return status >= 200 && status != http.StatusNoContent && status != http.StatusNotModified
}

// acceptsGzip reports whether an Accept-Encoding header offers gzip with a
// non-zero weight. A wildcard counts, and an explicit "gzip;q=0" is honored.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		token, params, _ := strings.Cut(part, ";")
		token = strings.TrimSpace(token)
		if token != "gzip" && token != "x-gzip" && token != "*" {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			p = strings.TrimSpace(p)
			if v, ok := strings.CutPrefix(p, "q="); ok {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		if q > 0 {
			return true
		}
	}
	return false
}
