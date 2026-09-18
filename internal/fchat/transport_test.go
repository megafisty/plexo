package fchat_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/coder/websocket"

	"plexo/internal/fchat"
)

// wsServer starts a TLS WebSocket echo harness and returns its base URL.
func wsServer(t *testing.T, opts *websocket.AcceptOptions, serve func(ctx context.Context, ws *websocket.Conn)) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if opts == nil {
			opts = &websocket.AcceptOptions{InsecureSkipVerify: true}
		}
		ws, err := websocket.Accept(w, r, opts)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		serve(r.Context(), ws)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func wsURL(srv *httptest.Server) string {
	u, _ := url.Parse(srv.URL)
	u.Scheme = "wss"
	u.Path = "/chat2"
	return u.String()
}

func dialTest(t *testing.T, srv *httptest.Server, cfg fchat.DialConfig) fchat.Conn {
	t.Helper()
	cfg.URL = wsURL(srv)
	cfg.HTTPClient = srv.Client()
	conn, err := fchat.Dial(context.Background(), cfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestDialRoundTrip(t *testing.T) {
	got := make(chan fchat.Frame, 1)
	srv := wsServer(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			t.Errorf("server read type = %v, want text", typ)
		}
		frame, err := fchat.Parse(data)
		if err != nil {
			t.Errorf("server parse: %v", err)
			return
		}
		got <- frame
		_ = ws.Write(ctx, websocket.MessageText, []byte(`PRI {"character":"Other","message":"hello"}`))
		<-ctx.Done()
	})

	conn := dialTest(t, srv, fchat.DialConfig{})
	want := fchat.Frame{Code: "PRI", Data: []byte(`{"recipient":"Other","message":"hi"}`)}
	if err := conn.Write(context.Background(), want); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case f := <-got:
		if f.Code != want.Code || string(f.Data) != string(want.Data) {
			t.Fatalf("server got %+v, want %+v", f, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the frame")
	}

	reply, err := conn.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if reply.Code != "PRI" || string(reply.Data) != `{"character":"Other","message":"hello"}` {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestReadNonTextIsProtocolError(t *testing.T) {
	srv := wsServer(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		_ = ws.Write(ctx, websocket.MessageBinary, []byte(`PRI {}`))
		<-ctx.Done()
	})
	conn := dialTest(t, srv, fchat.DialConfig{})
	if _, err := conn.Read(context.Background()); !errors.Is(err, fchat.ErrProtocol) {
		t.Fatalf("err = %v, want ErrProtocol", err)
	}
}

func TestReadMalformedFrame(t *testing.T) {
	srv := wsServer(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		_ = ws.Write(ctx, websocket.MessageText, []byte("abc not a command"))
		<-ctx.Done()
	})
	conn := dialTest(t, srv, fchat.DialConfig{})
	if _, err := conn.Read(context.Background()); !errors.Is(err, fchat.ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
}

func TestReadOversizedMessage(t *testing.T) {
	big := make([]byte, 256)
	for i := range big {
		big[i] = 'a'
	}
	big = append(big, []byte(`MSG {}`)...)
	srv := wsServer(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		_ = ws.Write(ctx, websocket.MessageText, big)
		<-ctx.Done()
	})
	conn := dialTest(t, srv, fchat.DialConfig{MaxMessageBytes: 32})
	if _, err := conn.Read(context.Background()); !errors.Is(err, fchat.ErrProtocol) {
		t.Fatalf("err = %v, want ErrProtocol", err)
	}
}

func TestDialHandshakeFailure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	_, err := fchat.Dial(context.Background(), fchat.DialConfig{
		URL:        wsURL(srv),
		HTTPClient: srv.Client(),
	})
	var he *fchat.HandshakeError
	if !errors.As(err, &he) {
		t.Fatalf("err = %v, want *HandshakeError", err)
	}
	if he.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", he.StatusCode)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	srv := wsServer(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				return
			}
		}
	})
	conn := dialTest(t, srv, fchat.DialConfig{})
	if err := conn.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := conn.Read(context.Background()); err == nil {
		t.Fatal("read after close returned nil error")
	}
}
