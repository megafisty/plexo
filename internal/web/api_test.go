package web

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"plexo/internal/activity"
	"plexo/internal/config"
	"plexo/internal/core"
	"plexo/internal/fchat"
	"plexo/internal/model"
	"plexo/internal/render"
	"plexo/internal/store"
	"plexo/test/fixtures"
	"plexo/test/memstore"
)

// newAPIServer starts a real server (so route wiring is exercised) on a random
// loopback port and returns its base URL.
func newAPIServer(t *testing.T, manager *core.Manager, password string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv, _ := NewServer("", Options{Manager: manager, Account: core.NewAccount(), PlexoPassword: password})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String()
}

func appendHistory(t *testing.T, st *memstore.MemStore, conv model.ConvRef, n int) {
	t.Helper()
	base := time.Unix(1_700_000_000, 0)
	for i := 1; i <= n; i++ {
		if err := st.Append(context.Background(), []model.Entry{{
			ID: "e" + strconv.Itoa(i), Session: "Vix", Conv: conv, ConvSeq: uint64(i),
			Kind: "msg", Speaker: "x", Body: "[b]hi[/b]",
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

func TestAPIHistory(t *testing.T) {
	st := memstore.New()
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	appendHistory(t, st, conv, 3)
	manager := core.NewManager(context.Background(), core.Config{Store: st})
	base := newAPIServer(t, manager, "")

	res, err := http.Get(base + "/api/history?session=Vix&conv_kind=official&conv_id=Frontpage")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	raw, _ := io.ReadAll(res.Body)

	var got History
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(got.Entries))
	}
	if got.Entries[0].HTML == "" {
		t.Fatal("rendered html is missing")
	}
	// The raw body must not be on the wire, even though it is still in memory.
	var probe struct {
		Entries []map[string]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("decode probe: %v", err)
	}
	if _, ok := probe.Entries[0]["body"]; ok {
		t.Fatalf("body leaked onto the wire: %s", raw)
	}

	// A before cursor pages backward and honours the limit.
	res2, err := http.Get(base + "/api/history?session=Vix&conv_kind=official&conv_id=Frontpage&before_seq=3&limit=1")
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	defer res2.Body.Close()
	var page History
	if err := json.NewDecoder(res2.Body).Decode(&page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].ConvSeq != 2 {
		t.Fatalf("page = %+v, want one entry at seq 2", page.Entries)
	}
}

// TestAPIHistoryCompressed: a history page that outgrows the compression
// threshold is served gzip-encoded, and a client that does not negotiate gzip
// still gets the plain JSON.
func TestAPIHistoryCompressed(t *testing.T) {
	st := memstore.New()
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	appendHistory(t, st, conv, 200)
	manager := core.NewManager(context.Background(), core.Config{Store: st})
	base := newAPIServer(t, manager, "")
	url := base + "/api/history?session=Vix&conv_kind=official&conv_id=Frontpage&limit=200"

	// Setting Accept-Encoding by hand disables the transport's transparent
	// gzip, so the raw encoded body reaches the test.
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if got := res.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := res.Header.Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", got)
	}
	zr, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	var got History
	if err := json.NewDecoder(zr).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Entries) != 200 {
		t.Fatalf("entries = %d, want 200", len(got.Entries))
	}

	// An explicit identity request stays plain.
	req, err = http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Accept-Encoding", "identity")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get plain: %v", err)
	}
	defer res.Body.Close()
	if got := res.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty", got)
	}
}

// TestAPIHistoryCaching: cacheable reads carry a private revalidation policy
// and a validator, so a repeat request is answered with a bodyless 304; live
// endpoints and error paths stay no-store.
func TestAPIHistoryCaching(t *testing.T) {
	st := memstore.New()
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	appendHistory(t, st, conv, 3)
	manager := core.NewManager(context.Background(), core.Config{Store: st})
	base := newAPIServer(t, manager, "")
	url := base + "/api/history?session=Vix&conv_kind=official&conv_id=Frontpage"

	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := res.Header.Get("Cache-Control"); got != cachePrivateRevalidate {
		t.Fatalf("Cache-Control = %q, want %q", got, cachePrivateRevalidate)
	}
	etag := res.Header.Get("ETag")
	if etag == "" {
		t.Fatal("history response has no ETag")
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("If-None-Match", etag)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("revalidate: %v", err)
	}
	if res.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidate status = %d, want 304", res.StatusCode)
	}
	if b, _ := io.ReadAll(res.Body); len(b) != 0 {
		t.Fatalf("304 carried a body: %q", b)
	}
	res.Body.Close()

	// A live endpoint is never stored, even on success.
	res, err = http.Get(base + "/api/ads?session=Ghost")
	if err != nil {
		t.Fatalf("ads: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ads status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Cache-Control"); got != cacheNoStore {
		t.Fatalf("ads Cache-Control = %q, want %q", got, cacheNoStore)
	}
	res.Body.Close()

	// An error path is never stored either.
	res, err = http.Get(base + "/api/history")
	if err != nil {
		t.Fatalf("bad request: %v", err)
	}
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad request status = %d, want 400", res.StatusCode)
	}
	if got := res.Header.Get("Cache-Control"); got != cacheNoStore {
		t.Fatalf("error Cache-Control = %q, want %q", got, cacheNoStore)
	}
	res.Body.Close()
}

// TestAPILogIndex exercises the two-sided log browser index: the own-character
// list, the global conversation list, the per-session conversation list, and the
// reverse lookup from a conversation id.
func TestAPILogIndex(t *testing.T) {
	st := memstore.New()
	epoch := time.Unix(1_700_000_000, 0)
	appendAt := func(session string, conv model.ConvRef, name string, seq int, kind, speaker, body string) {
		t.Helper()
		if err := st.Append(context.Background(), []model.Entry{{
			ID: session + "-" + string(conv.Kind) + "-" + conv.ID + "-" + strconv.Itoa(seq), Session: session, Conv: conv,
			ConvName: name, ConvSeq: uint64(seq), Kind: kind, Speaker: speaker, Body: body,
			CreatedAt: epoch.Add(time.Duration(seq) * time.Second),
		}}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	room := model.ConvRef{Kind: model.ConvRoom, ID: "ADH-abc"}
	dm := model.ConvRef{Kind: model.ConvDM, ID: "Kira"}
	appendAt("Vix", room, "The Tavern", 1, "msg", "Kira", "[b]hi[/b]")
	appendAt("Vix", dm, "", 1, "dm", "Kira", "hey")
	appendAt("Kira", room, "The Tavern", 1, "msg", "Vix", "yo")

	manager := core.NewManager(context.Background(), core.Config{Store: st})
	base := newAPIServer(t, manager, "")

	getJSON := func(url string, v any) int {
		t.Helper()
		res, err := http.Get(base + url)
		if err != nil {
			t.Fatalf("get %s: %v", url, err)
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusOK {
			if err := json.NewDecoder(res.Body).Decode(v); err != nil {
				t.Fatalf("decode %s: %v", url, err)
			}
		}
		return res.StatusCode
	}

	var chars logCharactersResponse
	if got := getJSON("/api/logs/index", &chars); got != http.StatusOK {
		t.Fatalf("characters status = %d", got)
	}
	if len(chars.Characters) != 2 || chars.Characters[0] != "Kira" || chars.Characters[1] != "Vix" {
		t.Fatalf("characters = %v", chars.Characters)
	}

	var convs logConvsResponse
	if got := getJSON("/api/logs/index?session=Vix", &convs); got != http.StatusOK {
		t.Fatalf("convs status = %d", got)
	}
	if len(convs.Conversations) != 2 {
		t.Fatalf("conversations = %+v", convs.Conversations)
	}
	if c := convs.Conversations[0]; c.Kind != model.ConvDM || c.ID != "Kira" || c.Name != "Kira" {
		t.Fatalf("conversation[0] = %+v", c)
	}
	if c := convs.Conversations[1]; c.Kind != model.ConvRoom || c.ID != "ADH-abc" || c.Name != "The Tavern" {
		t.Fatalf("conversation[1] = %+v", c)
	}

	// Reverse from a DM partner by id, case-insensitively.
	var byDM logSessionsResponse
	if got := getJSON("/api/logs/index?conv_kind=dm&conv_id=kira", &byDM); got != http.StatusOK {
		t.Fatalf("dm reverse status = %d", got)
	}
	if len(byDM.Characters) != 1 {
		t.Fatalf("dm reverse = %+v", byDM.Characters)
	}
	if c := byDM.Characters[0]; c.Session != "Vix" || c.Kind != model.ConvDM || c.ID != "Kira" || c.Name != "Kira" {
		t.Fatalf("dm reverse character = %+v", c)
	}

	// Reverse from a room by its opaque id, not its title; both sessions
	// witnessed the room.
	var byRoom logSessionsResponse
	if got := getJSON("/api/logs/index?conv_kind=room&conv_id=adh-ABC", &byRoom); got != http.StatusOK {
		t.Fatalf("room reverse status = %d", got)
	}
	if len(byRoom.Characters) != 2 {
		t.Fatalf("room reverse = %+v", byRoom.Characters)
	}
	for _, c := range byRoom.Characters {
		if c.Kind != model.ConvRoom || c.ID != "ADH-abc" || c.Name != "The Tavern" {
			t.Fatalf("room reverse character = %+v", c)
		}
	}

	// The global conversation list dedupes by kind and case-folded id.
	var all logConvsResponse
	if got := getJSON("/api/logs/index?conversations=1", &all); got != http.StatusOK {
		t.Fatalf("all conversations status = %d", got)
	}
	if len(all.Conversations) != 2 {
		t.Fatalf("all conversations = %+v", all.Conversations)
	}

	// Bad shapes are rejected.
	if res, _ := http.Get(base + "/api/logs/index?session=Vix&conv_kind=dm&conv_id=Kira"); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("both ends status = %d, want 400", res.StatusCode)
	}
	if res, _ := http.Get(base + "/api/logs/index?conv_kind=nope&conv_id=X"); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad kind status = %d, want 400", res.StatusCode)
	}
}

// TestAPILogCoverageAndExport: coverage reports the span, and the export streams
// a self-contained, inline HTML artifact with rendered bodies and a valid
// range. Missing or inverted bounds and non-browsable kinds are rejected.
func TestAPILogCoverageAndExport(t *testing.T) {
	renderer, err := render.New()
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	st := memstore.New()
	epoch := time.Unix(1_700_000_000, 0)
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	if err := st.Append(context.Background(), []model.Entry{
		{ID: "e1", Session: "Vix", Conv: conv, ConvSeq: 1, Kind: "msg", Speaker: "Kira", Body: "[b]hi[/b]", CreatedAt: epoch},
		{ID: "e2", Session: "Vix", Conv: conv, ConvSeq: 2, Kind: "msg", Speaker: "Vix", Body: "hello", CreatedAt: epoch.Add(time.Minute)},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	manager := core.NewManager(context.Background(), core.Config{Store: st, Renderer: renderer})
	base := newAPIServer(t, manager, "")

	var cov model.LogCoverage
	res, err := http.Get(base + "/api/logs/coverage?session=Vix&conv_kind=official&conv_id=Frontpage")
	if err != nil {
		t.Fatalf("coverage get: %v", err)
	}
	if err := json.NewDecoder(res.Body).Decode(&cov); err != nil {
		res.Body.Close()
		t.Fatalf("decode coverage: %v", err)
	}
	res.Body.Close()
	if cov.Count != 2 || cov.FirstMs != epoch.UnixMilli() || cov.LastMs != epoch.Add(time.Minute).UnixMilli() {
		t.Fatalf("coverage = %+v", cov)
	}
	if cov.FirstSeq != 1 || cov.LastSeq != 2 || cov.Name != "Frontpage" {
		t.Fatalf("coverage names/seqs = %+v", cov)
	}

	from := epoch.UnixMilli()
	to := epoch.Add(10 * time.Minute).UnixMilli()
	url := base + "/api/logs/export?session=Vix&conv_kind=official&conv_id=Frontpage&from=" + strconv.FormatInt(from, 10) + "&to=" + strconv.FormatInt(to, 10)
	res, err = http.Get(url)
	if err != nil {
		t.Fatalf("export get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("export status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q", ct)
	}
	disposition := res.Header.Get("Content-Disposition")
	if !strings.Contains(disposition, "inline") || !strings.Contains(disposition, "filename=") {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
	body, _ := io.ReadAll(res.Body)
	page := string(body)
	for _, want := range []string{
		"<!doctype html>", "<style>", "Frontpage", "Kira", ": <b>hi</b>",
		"2023-11-14", "22:13", "is-self", "<hr class=\"log-end\">",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("export missing %q", want)
		}
	}
	if strings.Contains(page, "[b]hi[/b]") {
		t.Errorf("export leaked raw BBCode")
	}

	// Bounds are required and ordered; broadcasts are not browsable.
	bad := []string{
		"/api/logs/export?session=Vix&conv_kind=official&conv_id=Frontpage&to=" + strconv.FormatInt(to, 10),
		"/api/logs/export?session=Vix&conv_kind=official&conv_id=Frontpage&from=" + strconv.FormatInt(to, 10) + "&to=" + strconv.FormatInt(from, 10),
		"/api/logs/export?session=Vix&conv_kind=broadcast&conv_id=global&from=" + strconv.FormatInt(from, 10) + "&to=" + strconv.FormatInt(to, 10),
		"/api/logs/export?session=Vix&conv_kind=official&conv_id=Frontpage&from=" + strconv.FormatInt(from, 10) + "&to=" + strconv.FormatInt(to, 10) + "&tz=abc",
	}
	for _, u := range bad {
		res, err := http.Get(base + u)
		if err != nil {
			t.Fatalf("get %s: %v", u, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", u, res.StatusCode)
		}
	}
}

func TestAPIHistoryErrors(t *testing.T) {
	st := memstore.New()
	manager := core.NewManager(context.Background(), core.Config{Store: st})
	base := newAPIServer(t, manager, "")

	cases := []struct {
		name string
		url  string
		want int
	}{
		{"missing session", "/api/history?conv_kind=official&conv_id=X", http.StatusBadRequest},
		{"missing conv", "/api/history?session=Vix&conv_kind=official", http.StatusBadRequest},
		{"bad kind", "/api/history?session=Vix&conv_kind=nope&conv_id=X", http.StatusBadRequest},
		{"bad cursor", "/api/history?session=Vix&conv_kind=official&conv_id=X&before_seq=abc", http.StatusBadRequest},
		{"bad limit", "/api/history?session=Vix&conv_kind=official&conv_id=X&limit=-1", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := http.Get(base + tc.url)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.want)
			}
		})
	}
}

func TestAPIAdsAndPresenceErrors(t *testing.T) {
	manager := core.NewManager(context.Background(), core.Config{Store: memstore.New()})
	base := newAPIServer(t, manager, "")

	// Ads for an unknown session is an empty list, not an error.
	res, err := http.Get(base + "/api/ads?session=Nobody")
	if err != nil {
		t.Fatalf("get ads: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ads status = %d, want 200", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("ads body = %q, want []", body)
	}
	if res, _ := http.Get(base + "/api/ads"); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("ads without session status = %d, want 400", res.StatusCode)
	}

	// Presence for an unknown session is a 404.
	if res, _ := http.Get(base + "/api/presence?session=Nobody"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("presence status = %d, want 404", res.StatusCode)
	}
	if res, _ := http.Get(base + "/api/presence"); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("presence without session status = %d, want 400", res.StatusCode)
	}
}

func TestAPIGuards(t *testing.T) {
	manager := core.NewManager(context.Background(), core.Config{Store: memstore.New()})
	a := &api{manager: manager, auth: NewSessionAuth("pw")}

	// Method guard applies before auth.
	req := httptest.NewRequest(http.MethodPost, "/api/history", nil)
	req.RemoteAddr = "192.0.2.7:5555"
	rec := httptest.NewRecorder()
	if a.guard(rec, req) {
		t.Fatal("guard accepted a non-GET request")
	}
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post status = %d, want 405", rec.Code)
	}

	// A LAN caller without the cookie is rejected.
	req = httptest.NewRequest(http.MethodGet, "/api/history", nil)
	req.RemoteAddr = "192.0.2.7:5555"
	rec = httptest.NewRecorder()
	if a.guard(rec, req) {
		t.Fatal("guard accepted an unauthenticated LAN request")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", rec.Code)
	}

	// Loopback is allowed even with a password configured.
	req = httptest.NewRequest(http.MethodGet, "/api/history", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec = httptest.NewRecorder()
	if !a.guard(rec, req) {
		t.Fatal("guard rejected a loopback request")
	}

	// The session cookie authenticates a LAN caller.
	req = httptest.NewRequest(http.MethodGet, "/api/history", nil)
	req.RemoteAddr = "192.0.2.7:5555"
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: a.auth.token})
	rec = httptest.NewRecorder()
	if !a.guard(rec, req) {
		t.Fatal("guard rejected a valid session cookie")
	}
}

func TestAPISettings(t *testing.T) {
	st := memstore.New()
	manager := core.NewManager(context.Background(), core.Config{Store: st, Settings: config.NewProvider(st)})
	base := newAPIServer(t, manager, "")

	get := func(url string) core.SettingsView {
		t.Helper()
		res, err := http.Get(base + url)
		if err != nil {
			t.Fatalf("get %s: %v", url, err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("get %s status = %d, want 200", url, res.StatusCode)
		}
		var v core.SettingsView
		if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
		return v
	}
	put := func(path, body string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPut, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("put %s: %v", path, err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("put %s status = %d, want 204", path, res.StatusCode)
		}
	}
	del := func(path string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodDelete, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("delete %s: %v", path, err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("delete %s status = %d, want 204", path, res.StatusCode)
		}
	}

	if v := get("/api/settings?session=Vix"); v.HasGlobal || v.HasCharacter {
		t.Fatalf("fresh view = %+v, want both absent", v)
	}

	put("/api/settings/global", `{"password":"hunter2"}`)
	put("/api/settings/character?session=vix", `{"highlights":[" Chart ","chart"],"autoJoin":[{"kind":"official","id":" Frontpage ","name":""}]}`)

	v := get("/api/settings?session=Vix")
	if !v.HasGlobal || v.Global.Password != "hunter2" {
		t.Fatalf("global = %+v (has %v)", v.Global, v.HasGlobal)
	}
	if !v.HasCharacter {
		t.Fatal("character document missing")
	}
	if len(v.Character.Highlights) != 1 || v.Character.Highlights[0] != "Chart" {
		t.Fatalf("highlights = %v", v.Character.Highlights)
	}
	if len(v.Character.AutoJoin) != 1 || v.Character.AutoJoin[0].Name != "Frontpage" {
		t.Fatalf("autoJoin = %+v", v.Character.AutoJoin)
	}

	// A GET without a session reads only the global scope.
	if gv := get("/api/settings"); !gv.HasGlobal || gv.HasCharacter {
		t.Fatalf("global-only view = %+v", gv)
	}

	del("/api/settings/character?session=Vix")
	if v := get("/api/settings?session=Vix"); v.HasCharacter || !v.HasGlobal {
		t.Fatalf("after character reset = %+v", v)
	}
	del("/api/settings/global")
	if v := get("/api/settings?session=Vix"); v.HasGlobal {
		t.Fatalf("after global reset = %+v", v)
	}
}

func TestAPISettingsErrors(t *testing.T) {
	// A manager without a settings provider reports 503.
	plain := core.NewManager(context.Background(), core.Config{Store: memstore.New()})
	base := newAPIServer(t, plain, "")
	res, err := http.Get(base + "/api/settings?session=Vix")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no-provider status = %d, want 503", res.StatusCode)
	}

	st := memstore.New()
	manager := core.NewManager(context.Background(), core.Config{Store: st, Settings: config.NewProvider(st)})
	base = newAPIServer(t, manager, "")

	putStatus := func(path, body string) (int, http.Header) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPut, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("put %s: %v", path, err)
		}
		defer res.Body.Close()
		return res.StatusCode, res.Header.Clone()
	}

	if got, _ := putStatus("/api/settings/global", `{"password":"`+strings.Repeat("x", config.MaxPasswordLen+1)+`"}`); got != http.StatusBadRequest {
		t.Fatalf("over-limit password status = %d, want 400", got)
	}
	if got, _ := putStatus("/api/settings/character?session=Vix", `{`); got != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", got)
	}
	if got, _ := putStatus("/api/settings/character", `{}`); got != http.StatusBadRequest {
		t.Fatalf("missing session status = %d, want 400", got)
	}

	req, err := http.NewRequest(http.MethodPost, base+"/api/settings/global", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("post status = %d, want 405", res.StatusCode)
	}
	if got := res.Header.Get("Allow"); got != "PUT, DELETE" {
		t.Fatalf("Allow = %q, want %q", got, "PUT, DELETE")
	}
}

// TestAPIMapping: the endpoint serves the cached mapping data and is 503 until
// the background load lands. It is offline: the source decodes the committed
// fixture. A manager built without a source never fetches and reports 503.
func TestAPIMapping(t *testing.T) {
	manager := core.NewManager(context.Background(), core.Config{
		Store: memstore.New(),
		Mapping: core.MappingFunc(func(context.Context) (model.MappingList, error) {
			return fchat.DecodeMappingList(fixtures.MappingList())
		}),
	})
	base := newAPIServer(t, manager, "")

	var list model.SearchMapping
	deadline := time.Now().Add(2 * time.Second)
	for {
		res, err := http.Get(base + "/api/mapping")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if res.StatusCode == http.StatusOK {
			if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
				res.Body.Close()
				t.Fatalf("decode: %v", err)
			}
			res.Body.Close()
			break
		}
		status := res.StatusCode
		res.Body.Close()
		if time.Now().After(deadline) {
			t.Fatalf("mapping never became available: status %d", status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(list.Kinks.Entries) == 0 || len(list.Genders.Entries) == 0 {
		t.Fatalf("mapping payload is incomplete: %+v", list)
	}

	// With no source configured the cache is empty, so the endpoint is 503.
	plain := core.NewManager(context.Background(), core.Config{Store: memstore.New()})
	res, err := http.Get(newAPIServer(t, plain, "") + "/api/mapping")
	if err != nil {
		t.Fatalf("get plain: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", res.StatusCode)
	}
}

// TestAPILogActivity: the day overview aligns to the timezone and zero-fills
// empty days, and the drilldown recovers the roleplay session from the dense
// day. Bad ranges, kinds, and gaps are rejected.
func TestAPILogActivity(t *testing.T) {
	st := memstore.New()
	conv := model.ConvRef{Kind: model.ConvDM, ID: "Kira"}
	dayA := time.Date(2024, 3, 1, 20, 0, 0, 0, time.UTC)
	dayB := time.Date(2024, 3, 3, 12, 0, 0, 0, time.UTC)
	var entries []model.Entry
	seq := 0
	add := func(at time.Time, body int) {
		seq++
		entries = append(entries, model.Entry{
			ID: "e" + strconv.Itoa(seq), Session: "Vix", Conv: conv, ConvSeq: uint64(seq),
			Kind: "dm", Speaker: "Kira", Body: strings.Repeat("x", body), CreatedAt: at,
		})
	}
	for i := 0; i < 30; i++ {
		add(dayA.Add(time.Duration(i)*2*time.Minute), 500)
	}
	add(dayB, 40)
	add(dayB.Add(time.Minute), 40)
	if err := st.Append(context.Background(), entries); err != nil {
		t.Fatalf("append: %v", err)
	}
	manager := core.NewManager(context.Background(), core.Config{Store: st})
	base := newAPIServer(t, manager, "")

	var overview LogActivityOverview
	res, err := http.Get(base + "/api/logs/activity?session=Vix&conv_kind=dm&conv_id=Kira")
	if err != nil {
		t.Fatalf("overview get: %v", err)
	}
	if err := json.NewDecoder(res.Body).Decode(&overview); err != nil {
		res.Body.Close()
		t.Fatalf("decode overview: %v", err)
	}
	res.Body.Close()
	if overview.UnitMs != activity.DayMs || overview.Total != 32 || len(overview.Buckets) != 3 {
		t.Fatalf("overview = %+v", overview)
	}
	if overview.Scope != activity.ScopeConversation {
		t.Errorf("scope = %q, want conversation", overview.Scope)
	}
	if overview.Buckets[0].StartMs != time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Errorf("bucket 0 start = %d", overview.Buckets[0].StartMs)
	}
	for i, want := range []int64{30, 0, 2} {
		if overview.Buckets[i].Count != want {
			t.Errorf("bucket %d count = %d, want %d", i, overview.Buckets[i].Count, want)
		}
	}

	dayStart := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	from := dayStart
	to := dayStart + activity.DayMs - 1
	var detail LogActivityDetail
	res, err = http.Get(base + "/api/logs/activity?session=Vix&conv_kind=dm&conv_id=Kira&from=" +
		strconv.FormatInt(from, 10) + "&to=" + strconv.FormatInt(to, 10))
	if err != nil {
		t.Fatalf("detail get: %v", err)
	}
	if err := json.NewDecoder(res.Body).Decode(&detail); err != nil {
		res.Body.Close()
		t.Fatalf("decode detail: %v", err)
	}
	res.Body.Close()
	if len(detail.Sessions) != 1 || !detail.Sessions[0].RP || detail.Sessions[0].Count != 30 {
		t.Fatalf("detail sessions = %+v", detail.Sessions)
	}
	if detail.Summary.Count != 30 || detail.Summary.LongCount != 30 {
		t.Fatalf("detail summary = %+v", detail.Summary)
	}

	// An empty conversation is a valid, empty overview.
	var empty LogActivityOverview
	res, err = http.Get(base + "/api/logs/activity?session=Vix&conv_kind=dm&conv_id=Nobody")
	if err != nil {
		t.Fatalf("empty get: %v", err)
	}
	if err := json.NewDecoder(res.Body).Decode(&empty); err != nil {
		res.Body.Close()
		t.Fatalf("decode empty: %v", err)
	}
	res.Body.Close()
	if empty.Total != 0 || len(empty.Buckets) != 0 {
		t.Fatalf("empty overview = %+v", empty)
	}

	bad := []string{
		"/api/logs/activity?session=Vix&conv_kind=dm&conv_id=Kira&from=" + strconv.FormatInt(from, 10),
		"/api/logs/activity?session=Vix&conv_kind=dm&conv_id=Kira&from=" + strconv.FormatInt(to, 10) + "&to=" + strconv.FormatInt(from, 10),
		"/api/logs/activity?session=Vix&conv_kind=broadcast&conv_id=global",
		"/api/logs/activity?session=Vix&conv_kind=dm&conv_id=Kira&tz=abc",
		"/api/logs/activity?session=Vix&conv_kind=dm&conv_id=Kira&from=" + strconv.FormatInt(from, 10) + "&to=" + strconv.FormatInt(to, 10) + "&gap_min=0",
		"/api/logs/activity?session=Vix&conv_kind=dm&conv_id=Kira&from=" + strconv.FormatInt(from, 10) + "&to=" + strconv.FormatInt(from+32*activity.DayMs, 10),
	}
	for _, u := range bad {
		res, err := http.Get(base + u)
		if err != nil {
			t.Fatalf("get %s: %v", u, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", u, res.StatusCode)
		}
	}
}

// TestAPILogActivityScope: auto resolution picks conversation scope for a small
// room interaction and self scope for a crowd or an official channel, explicit
// scope overrides, and the self overview counts only our own posts.
func TestAPILogActivityScope(t *testing.T) {
	st := memstore.New()
	base := time.Date(2024, 4, 1, 20, 0, 0, 0, time.UTC)
	seq := 0
	add := func(session, speaker string, conv model.ConvRef, at time.Time, body int) {
		seq++
		if err := st.Append(context.Background(), []model.Entry{{
			ID: "s" + strconv.Itoa(seq), Session: session, Conv: conv, ConvSeq: uint64(seq),
			Kind: "msg", Speaker: speaker, Body: strings.Repeat("x", body), CreatedAt: at,
		}}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	// A two-person room interaction.
	small := model.ConvRef{Kind: model.ConvRoom, ID: "ADH-small"}
	for i := 0; i < 12; i++ {
		m := time.Duration(2*i) * time.Minute
		add("Vix", "Vix", small, base.Add(m), 500)
		add("Vix", "Kira", small, base.Add(m+time.Minute), 500)
	}
	// A crowded room: ten people, ten messages each.
	crowd := model.ConvRef{Kind: model.ConvRoom, ID: "ADH-crowd"}
	for i := 0; i < 10; i++ {
		for p := 0; p < 10; p++ {
			name := "C" + strconv.Itoa(p)
			if p == 0 {
				name = "Vix"
			}
			add("Vix", name, crowd, base.Add(time.Duration(i*10+p)*time.Minute), 60)
		}
	}
	// An official channel: always self scope.
	chanConv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	add("Vix", "Vix", chanConv, base, 80)
	add("Vix", "Other", chanConv, base.Add(time.Minute), 80)

	manager := core.NewManager(context.Background(), core.Config{Store: st})
	baseURL := newAPIServer(t, manager, "")

	get := func(url string) LogActivityOverview {
		t.Helper()
		res, err := http.Get(baseURL + url)
		if err != nil {
			t.Fatalf("get %s: %v", url, err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", url, res.StatusCode)
		}
		var ov LogActivityOverview
		if err := json.NewDecoder(res.Body).Decode(&ov); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
		return ov
	}

	smallOv := get("/api/logs/activity?session=Vix&conv_kind=room&conv_id=ADH-small")
	if smallOv.Scope != activity.ScopeConversation || smallOv.Total != 24 {
		t.Fatalf("small room = scope %q total %d", smallOv.Scope, smallOv.Total)
	}
	if smallOv.Participation == nil || smallOv.Participation.Active != 2 {
		t.Fatalf("small room participation = %+v", smallOv.Participation)
	}

	crowdOv := get("/api/logs/activity?session=Vix&conv_kind=room&conv_id=ADH-crowd")
	if crowdOv.Scope != activity.ScopeSelf || crowdOv.Total != 10 {
		t.Fatalf("crowd = scope %q total %d (want self, only Vix's 10)", crowdOv.Scope, crowdOv.Total)
	}

	override := get("/api/logs/activity?session=Vix&conv_kind=room&conv_id=ADH-small&scope=self")
	if override.Scope != activity.ScopeSelf || override.Total != 12 {
		t.Fatalf("self override = scope %q total %d (want self, Vix's 12)", override.Scope, override.Total)
	}

	chanOv := get("/api/logs/activity?session=Vix&conv_kind=official&conv_id=Frontpage")
	if chanOv.Scope != activity.ScopeSelf || chanOv.Total != 1 {
		t.Fatalf("official = scope %q total %d (want self, Vix's 1)", chanOv.Scope, chanOv.Total)
	}

	res, err := http.Get(baseURL + "/api/logs/activity?session=Vix&conv_kind=room&conv_id=ADH-small&scope=bogus")
	if err != nil {
		t.Fatalf("bogus scope get: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("bogus scope status = %d, want 400", res.StatusCode)
	}
}

func TestAPIWarpmarks(t *testing.T) {
	st := memstore.New()
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	appendHistory(t, st, conv, 3)
	manager := core.NewManager(context.Background(), core.Config{Store: st})
	base := newAPIServer(t, manager, "")

	post := func(body string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, base+"/api/warpmarks", strings.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	// Create snapshots the entry and lists it back with a rendered snippet.
	if code := post(`{"session":"Vix","entryId":"e2","label":"the good bit"}`); code != http.StatusNoContent {
		t.Fatalf("create status = %d, want 204", code)
	}
	res, err := http.Get(base + "/api/warpmarks?session=Vix")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var list warpmarksResponse
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	res.Body.Close()
	if len(list.Warpmarks) != 1 {
		t.Fatalf("warpmarks = %+v", list.Warpmarks)
	}
	got := list.Warpmarks[0]
	if got.EntryID != "e2" || got.Label != "the good bit" || got.ConvSeq != 2 ||
		got.HTML == "" || got.Conv != conv || got.Missing {
		t.Fatalf("warpmark = %+v", got)
	}

	// The warp alias resolves the entry and seeds a tail ending at it.
	res, err = http.Get(base + "/api/history?session=Vix&conv_kind=warp&conv_id=e2")
	if err != nil {
		t.Fatalf("warp history: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("warp history status = %d, want 200", res.StatusCode)
	}
	var hist History
	if err := json.NewDecoder(res.Body).Decode(&hist); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	res.Body.Close()
	if len(hist.Entries) != 2 || hist.Entries[len(hist.Entries)-1].ID != "e2" {
		t.Fatalf("warp window = %+v", hist.Entries)
	}

	// Delete is idempotent and empties the list.
	del := func() int {
		t.Helper()
		req, err := http.NewRequest(http.MethodDelete, base+"/api/warpmarks?session=Vix&entryId=e2", nil)
		if err != nil {
			t.Fatalf("new delete: %v", err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := del(); code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", code)
	}
	if code := del(); code != http.StatusNoContent {
		t.Fatalf("delete again status = %d, want 204", code)
	}
	res, _ = http.Get(base + "/api/warpmarks?session=Vix")
	list = warpmarksResponse{}
	_ = json.NewDecoder(res.Body).Decode(&list)
	res.Body.Close()
	if len(list.Warpmarks) != 0 {
		t.Fatalf("warpmarks after delete = %+v", list.Warpmarks)
	}

	// Errors: unknown entry is 404, an over-long label is 400, an empty
	// session is 400, a warp alias has no newer side (400), and a warp alias
	// cannot resolve another session's entry (404).
	if code := post(`{"session":"Vix","entryId":"nope","label":"x"}`); code != http.StatusNotFound {
		t.Fatalf("unknown entry status = %d, want 404", code)
	}
	long, _ := json.Marshal(map[string]string{
		"session": "Vix", "entryId": "e1", "label": strings.Repeat("a", 129),
	})
	if code := post(string(long)); code != http.StatusBadRequest {
		t.Fatalf("long label status = %d, want 400", code)
	}
	for _, url := range []string{
		"/api/warpmarks",
		"/api/history?session=Vix&conv_kind=warp&conv_id=e2&after_seq=1",
		"/api/history?session=Someone&conv_kind=warp&conv_id=e2",
	} {
		res, err := http.Get(base + url)
		if err != nil {
			t.Fatalf("get %s: %v", url, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusNotFound {
			t.Fatalf("get %s status = %d, want 400/404", url, res.StatusCode)
		}
	}
	// A DELETE without an entry id is rejected input.
	req, err := http.NewRequest(http.MethodDelete, base+"/api/warpmarks?session=Vix&entryId=", nil)
	if err != nil {
		t.Fatalf("new delete: %v", err)
	}
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("empty delete: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty delete status = %d, want 400", res.StatusCode)
	}
}

// TestAPILogCleanup exercises the cleanup write endpoint end to end: a preview
// reports the impact without deleting, apply removes the rows, and invalid
// input or the wrong method is rejected.
func TestAPILogCleanup(t *testing.T) {
	st := memstore.New()
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	old := time.Now().AddDate(0, 0, -100)
	for i := 1; i <= 4; i++ {
		if err := st.Append(context.Background(), []model.Entry{{
			ID: "e" + strconv.Itoa(i), Session: "Vix", Conv: conv, ConvSeq: uint64(i),
			Kind: "msg", Speaker: "Other", Body: "hi", CreatedAt: old.Add(time.Duration(i) * time.Second),
		}}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	manager := core.NewManager(context.Background(), core.Config{Store: st})
	base := newAPIServer(t, manager, "")

	post := func(body string) *http.Response {
		t.Helper()
		res, err := http.Post(base+"/api/logs/cleanup", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		return res
	}

	// Preview: 4 old entries in one conversation, nothing deleted.
	res := post(`{"op":"age","days":30,"apply":false}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d, want 200", res.StatusCode)
	}
	var preview model.CleanupResult
	if err := json.NewDecoder(res.Body).Decode(&preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if preview.Entries != 4 || preview.Conversations != 1 {
		t.Fatalf("preview = %+v", preview)
	}
	if count := countCoverage(t, st, conv); count != 4 {
		t.Fatalf("preview deleted entries: count = %d", count)
	}

	// Apply removes them.
	res = post(`{"op":"age","days":30,"apply":true}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d, want 200", res.StatusCode)
	}
	var applied model.CleanupResult
	if err := json.NewDecoder(res.Body).Decode(&applied); err != nil {
		t.Fatalf("decode applied: %v", err)
	}
	if applied.Entries != 4 {
		t.Fatalf("applied = %+v", applied)
	}
	if count := countCoverage(t, st, conv); count != 0 {
		t.Fatalf("apply left entries: count = %d", count)
	}

	// Rejections: an unknown op and a wildcard day count.
	for _, body := range []string{
		`{"op":"bogus","days":30}`,
		`{"op":"age","days":0}`,
		`{"op":"conversation","days":30}`,
		`{"op":"conversation","session":"Vix","kind":"official","id":"Frontpage","days":-1}`,
		`{"op":"dms","days":7,"maxEntries":0}`,
	} {
		res := post(body)
		if res.StatusCode != http.StatusBadRequest {
			res.Body.Close()
			t.Fatalf("body %s: status = %d, want 400", body, res.StatusCode)
		}
		res.Body.Close()
	}

	// A cleanup is a write; GET is not allowed.
	getRes, err := http.Get(base + "/api/logs/cleanup")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer getRes.Body.Close()
	if getRes.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", getRes.StatusCode)
	}
}

func countCoverage(t *testing.T, st store.Store, conv model.ConvRef) int64 {
	t.Helper()
	ext, err := st.LogCoverage(context.Background(), "Vix", conv)
	if err != nil {
		t.Fatalf("LogCoverage: %v", err)
	}
	return ext.Count
}
