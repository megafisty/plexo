package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"plexo/internal/config"
	"plexo/internal/core"
	"plexo/internal/model"
	"plexo/test/memstore"
)

func readEnvelope(t *testing.T, ctx context.Context, c *websocket.Conn) Envelope {
	t.Helper()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env
}

// TestBridgeSetCredentials drives the browser credential path: send
// set_credentials, then observe account_state reach ok with the character list.
func TestBridgeSetCredentials(t *testing.T) {
	ticket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ticket":"fct_test","error":"","characters":["Vix","Second"]}`))
	}))
	defer ticket.Close()

	account := core.NewAccount(core.WithTicketURL(ticket.URL), core.WithTicketClient(ticket.Client()))
	manager := core.NewManager(context.Background(), core.Config{Store: memstore.New()})

	mux := http.NewServeMux()
	mux.Handle("/ws", NewBridge(manager, account, NewSessionAuth(""), nil).Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	subscribeT(t, ctx, c, "cred-1")

	// The handshake must arrive before anything else.
	if env := readEnvelope(t, ctx, c); env.Type != THello {
		t.Fatalf("first envelope = %q, want %q", env.Type, THello)
	}

	// Credentials are parsed from the raw envelope, not model.Command.
	creds := struct {
		Op       string `json:"op"`
		Account  string `json:"account"`
		Password string `json:"password"`
	}{Op: string(model.OpSetCredentials), Account: "acct", Password: "pw"}
	payload, _ := json.Marshal(creds)
	out, _ := json.Marshal(Envelope{Type: TCmd, CID: "u-1", Data: payload})
	if err := c.Write(ctx, websocket.MessageText, out); err != nil {
		t.Fatalf("write cmd: %v", err)
	}

	var acked bool
	var state model.AccountState
	for !(acked && state.Status == model.AccountOK) {
		env := readEnvelope(t, ctx, c)
		switch env.Type {
		case TAck, TErr:
			var res model.Result
			_ = json.Unmarshal(env.Data, &res)
			if res.CID == "u-1" {
				acked = res.Accepted
			}
		case TAccountState:
			_ = json.Unmarshal(env.Data, &state)
		}
	}
	if strings.Join(state.Characters, ",") != "Vix,Second" {
		t.Fatalf("characters = %v", state.Characters)
	}
}

// sendAccountCmd writes a command and returns whether the matching result was
// accepted. Account-state envelopes interleaved in between are skipped; callers
// poll the account directly for state.
func sendAccountCmd(ctx context.Context, c *websocket.Conn, cid string, payload any) (bool, error) {
	out, _ := json.Marshal(Envelope{Type: TCmd, CID: cid, Data: mustJSON(payload)})
	if err := c.Write(ctx, websocket.MessageText, out); err != nil {
		return false, err
	}
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return false, err
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		if env.Type != TAck && env.Type != TErr {
			continue
		}
		var res model.Result
		_ = json.Unmarshal(env.Data, &res)
		if res.CID != cid {
			continue
		}
		return res.Accepted, nil
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// subscribeT sends the mandatory first envelope naming a durable subscription.
func subscribeT(t *testing.T, ctx context.Context, c *websocket.Conn, id string) {
	t.Helper()
	out, _ := json.Marshal(Envelope{Type: TSubscribe, Data: mustJSON(Subscribe{ID: id})})
	if err := c.Write(ctx, websocket.MessageText, out); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
}

func waitPersisted(a *core.Account, want bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if a.State().Persisted == want {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// TestBridgeRememberAndPurgeCredentials drives persisting a validated pair and
// then deleting it through purge_credentials, leaving the live account alone.
func TestBridgeRememberAndPurgeCredentials(t *testing.T) {
	ticket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ticket":"fct_test","error":"","characters":["Vix"]}`))
	}))
	defer ticket.Close()

	st := memstore.New()
	provider := config.NewProvider(st)
	account := core.NewAccount(core.WithTicketURL(ticket.URL), core.WithTicketClient(ticket.Client()), core.WithCredentialStore(provider))
	manager := core.NewManager(context.Background(), core.Config{Store: st, Settings: provider, Credentials: account})

	mux := http.NewServeMux()
	mux.Handle("/ws", NewBridge(manager, account, NewSessionAuth(""), nil).Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	subscribeT(t, ctx, c, "cred-2")
	if env := readEnvelope(t, ctx, c); env.Type != THello {
		t.Fatalf("first envelope = %q", env.Type)
	}

	remember := struct {
		Op       string `json:"op"`
		Account  string `json:"account"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}{Op: string(model.OpSetCredentials), Account: "acct", Password: "pw", Remember: true}
	ok, err := sendAccountCmd(ctx, c, "u-1", remember)
	if err != nil {
		t.Fatalf("set_credentials: %v", err)
	}
	if !ok {
		t.Fatal("set_credentials was rejected")
	}
	if !waitPersisted(account, true) {
		t.Fatal("account not marked persisted after remember")
	}
	if _, stored, _ := provider.LoadCredentials(context.Background()); !stored {
		t.Fatal("credentials were not stored")
	}

	purge := struct {
		Op string `json:"op"`
	}{Op: string(model.OpPurgeCredentials)}
	ok, err = sendAccountCmd(ctx, c, "u-2", purge)
	if err != nil {
		t.Fatalf("purge_credentials: %v", err)
	}
	if !ok {
		t.Fatal("purge_credentials was rejected")
	}
	if !waitPersisted(account, false) {
		t.Fatal("account still marked persisted after purge")
	}
	if _, stored, _ := provider.LoadCredentials(context.Background()); stored {
		t.Fatal("stored credentials were not deleted")
	}
}

// TestBridgeNegotiatesCompression verifies the browser socket negotiates
// permessage-deflate when the client offers it.
func TestBridgeNegotiatesCompression(t *testing.T) {
	account := core.NewAccount()
	manager := core.NewManager(context.Background(), core.Config{Store: memstore.New()})

	mux := http.NewServeMux()
	mux.Handle("/ws", NewBridge(manager, account, NewSessionAuth(""), nil).Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	c, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	if got := resp.Header.Get("Sec-WebSocket-Extensions"); !strings.Contains(got, "permessage-deflate") {
		t.Fatalf("negotiated %q, want permessage-deflate", got)
	}
}

func TestSessionAuthCookie(t *testing.T) {
	auth := NewSessionAuth("secret")

	probe := func(cookies []*http.Cookie) map[string]bool {
		req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		auth.Handle(rec, req)
		var out map[string]bool
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode probe: %v", err)
		}
		return out
	}

	if got := probe(nil); !got["authRequired"] || got["authenticated"] {
		t.Fatalf("unauthenticated probe = %v", got)
	}

	// Wrong password is rejected and sets no cookie.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(`{"password":"nope"}`))
	auth.Handle(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d, want 401", rec.Code)
	}

	// Correct password sets a persistent HttpOnly cookie.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(`{"password":"secret"}`))
	auth.Handle(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("login status = %d, want 204", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].MaxAge <= 0 {
		t.Fatalf("cookie = %+v", cookies)
	}

	if got := probe(cookies); !got["authenticated"] {
		t.Fatalf("authenticated probe = %v", got)
	}

	// The cookie value must never be the password itself.
	if cookies[0].Value == "secret" {
		t.Fatal("cookie leaked the password")
	}
}

func TestSessionAuthDisabledAllows(t *testing.T) {
	auth := NewSessionAuth("")
	if !auth.Authenticated(httptest.NewRequest(http.MethodGet, "/ws", nil)) {
		t.Fatal("auth should not be required when no password is set")
	}
}

// TestSessionAuthLoopbackSkips: a same-machine caller never sees the gate,
// even with a password configured; LAN callers still do.
func TestSessionAuthLoopbackSkips(t *testing.T) {
	auth := NewSessionAuth("secret")
	for _, addr := range []string{"127.0.0.1:5555", "[::1]:5555"} {
		req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
		req.RemoteAddr = addr
		if !auth.Authenticated(req) {
			t.Fatalf("loopback %s must be authenticated", addr)
		}
		rec := httptest.NewRecorder()
		auth.Handle(rec, req)
		var out map[string]bool
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode probe: %v", err)
		}
		if out["authRequired"] {
			t.Fatalf("loopback %s probe = %v, want authRequired false", addr, out)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.RemoteAddr = "192.0.2.7:5555"
	if auth.Authenticated(req) {
		t.Fatal("non-loopback must not be authenticated without a cookie")
	}
}
