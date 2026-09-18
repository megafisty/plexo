// Package export renders a self-contained chatlog artifact from persisted
// history. It is deliberately separate from the live render path: it reads the
// store directly, renders through the uncached entrypoint (an export can be
// arbitrarily large and usually reads cold bodies, so it must never evict the
// live BBCode cache), and streams one standalone HTML document to an io.Writer.
//
// The artifact is a frozen document, not the live client: it inlines a
// lightweight stylesheet and references remote eicons/avatars rather than
// embedding them.
package export

import (
	"context"
	_ "embed"
	"html"
	"io"
	"strconv"
	"strings"
	"time"

	"plexo/internal/model"
	"plexo/internal/store"
)

//go:embed export.css
var stylesheet string

// pageLimit is how many entries are read per store round trip while streaming.
const pageLimit = 500

// Options selects one conversation and inclusive date range for an export.
type Options struct {
	Session string
	Conv    model.ConvRef
	// FromMs and ToMs are inclusive epoch-millisecond bounds.
	FromMs int64
	ToMs   int64
	// TzOffsetMin is the display timezone as minutes east of UTC.
	TzOffsetMin int
	Now         time.Time
	// Name is the display label for the conversation. When empty it is resolved
	// from the store (a room's recorded title, otherwise the id).
	Name string
}

// flusher lets a streaming export push each page to the browser as it is read.
// It is satisfied by http.ResponseWriter (and the gzip middleware's wrapper)
// when the endpoint runs over HTTP.
type flusher interface{ Flush() }

// WriteHTML streams a standalone HTML chatlog. The entry count is computed
// before the first row so the header is exact. A mid-stream store or write
// failure ends the document with an HTML comment and is returned; the HTTP
// status is already committed by then, so the caller cannot replace it.
func WriteHTML(ctx context.Context, w io.Writer, st store.Store, r model.Renderer, opts Options) error {
	loc := time.FixedZone("", opts.TzOffsetMin*60)
	name := opts.Name
	if name == "" {
		ext, err := st.LogCoverage(ctx, opts.Session, opts.Conv)
		if err != nil {
			return err
		}
		name = displayName(opts.Conv, ext.Name)
	}
	count, err := st.LogRangeCount(ctx, opts.Session, opts.Conv, opts.FromMs, opts.ToMs)
	if err != nil {
		return err
	}

	x := &writer{w: w}
	x.write(pageHead(opts, name, count, loc))
	if count > 0 {
		start, err := st.LogRangeStart(ctx, opts.Session, opts.Conv, opts.FromMs)
		if err != nil {
			return err
		}
		if err := stream(ctx, x, w, st, r, opts, start, loc); err != nil {
			// The status and part of the body are already sent. Leave the
			// failure visible in the artifact, then report it.
			x.write("<!-- export ended early: " + html.EscapeString(err.Error()) + " -->\n")
		}
	} else {
		x.write("<p class=\"log-empty\">No messages in this range.</p>\n")
	}
	x.write(pageFoot)
	return x.err
}

// stream pages the range in conv_seq order and stops at the first entry past
// ToMs. created_at is assigned from the local clock on receipt, so it is
// monotonic with conv_seq in practice and the early stop is exact.
func stream(ctx context.Context, x *writer, w io.Writer, st store.Store, r model.Renderer, opts Options, start uint64, loc *time.Location) error {
	if start == 0 {
		return nil
	}
	after := start - 1
	lastDay := ""
	for {
		if x.err != nil {
			return x.err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := st.LogRange(ctx, store.LogRangeQuery{
			Session:  opts.Session,
			Conv:     opts.Conv,
			AfterSeq: after,
			Limit:    pageLimit,
		})
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
		for _, e := range entries {
			if e.CreatedAt.UnixMilli() > opts.ToMs {
				return nil
			}
			writeRow(x, r, e, loc, &lastDay)
			after = e.ConvSeq
		}
		if f, ok := w.(flusher); ok {
			f.Flush()
		}
		if len(entries) < pageLimit {
			return nil
		}
	}
}

// writeRow emits one entry, preceded by a day separator when the local date
// changes.
func writeRow(x *writer, r model.Renderer, e model.Entry, loc *time.Location, lastDay *string) {
	t := e.CreatedAt.In(loc)
	if day := t.Format("2006-01-02"); day != *lastDay {
		x.write("<div class=\"log-day\"><span>" + html.EscapeString(day) + "</span></div>\n")
		*lastDay = day
	}
	body := model.RenderEntryUncachedHTML(r, e.Kind, e.Body, e.Data)
	cls := "msg kind-" + html.EscapeString(e.Kind)
	if strings.EqualFold(e.Speaker, e.Session) {
		cls += " is-self"
	}
	x.write("<div class=\"" + cls + "\">")
	x.write("<div class=\"msg-body\">")
	x.write("<span class=\"msg-speaker\">" + html.EscapeString(e.Speaker) + "</span>")
	x.write(body)
	x.write("</div>")
	x.write("<div class=\"msg-meta\"><span class=\"msg-time\">" + t.Format("15:04") + "</span></div>")
	x.write("</div>\n")
}

// pageHead builds the document head and the artifact header. count is the exact
// number of entries in the requested range.
func pageHead(opts Options, name string, count int64, loc *time.Location) string {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n")
	b.WriteString("<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>" + html.EscapeString(opts.Session+" — "+name) + "</title>\n")
	b.WriteString("<style>" + stylesheet + "</style>\n")
	b.WriteString("</head>\n<body>\n")
	b.WriteString("<header class=\"log-header\">\n")
	b.WriteString("<h1>" + html.EscapeString(name) + "</h1>\n")
	b.WriteString("<p class=\"log-sub\">" + html.EscapeString(string(opts.Conv.Kind)) + " · " + html.EscapeString(opts.Session) + "</p>\n")
	from := time.UnixMilli(opts.FromMs).In(loc).Format("2006-01-02")
	to := time.UnixMilli(opts.ToMs).In(loc).Format("2006-01-02")
	b.WriteString("<p class=\"log-range\">" + from + " – " + to + " · " + strconv.FormatInt(count, 10) + " messages</p>\n")
	b.WriteString("<p class=\"log-generated\">Exported " + opts.Now.In(loc).Format("2006-01-02 15:04") + "</p>\n")
	b.WriteString("</header>\n<main class=\"log\">\n")
	return b.String()
}

const pageFoot = "</main>\n<hr class=\"log-end\">\n</body>\n</html>\n"

// Filename returns a safe base name for the artifact, e.g.
// plexo-Vix-The-Tavern-20240101-20240131.html. Components are reduced to
// [A-Za-z0-9._-]; the conversation name is length-capped.
func Filename(opts Options, name string) string {
	loc := time.FixedZone("", opts.TzOffsetMin*60)
	from := time.UnixMilli(opts.FromMs).In(loc).Format("20060102")
	to := time.UnixMilli(opts.ToMs).In(loc).Format("20060102")
	return strings.Join([]string{"plexo", sanitize(opts.Session), sanitize(name), from, to}, "-") + ".html"
}

// displayName picks the readable label for a conversation: a room's recorded
// title, otherwise the id (already readable for channels and DMs).
func displayName(conv model.ConvRef, name string) string {
	if conv.Kind == model.ConvRoom && name != "" {
		return name
	}
	return conv.ID
}

// sanitize reduces s to a safe filename component.
func sanitize(s string) string {
	var b strings.Builder
	dashed := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.':
			b.WriteRune(r)
			dashed = false
		default:
			if !dashed && b.Len() > 0 {
				b.WriteByte('-')
				dashed = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 64 {
		out = out[:64]
	}
	if out == "" {
		out = "log"
	}
	return out
}

// writer accumulates the first write error so a broken client ends the stream
// instead of writing into a failed response.
type writer struct {
	w   io.Writer
	err error
}

func (x *writer) write(s string) {
	if x.err != nil {
		return
	}
	_, x.err = io.WriteString(x.w, s)
}
