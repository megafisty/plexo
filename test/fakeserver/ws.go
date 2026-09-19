package fakeserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"

	"github.com/coder/websocket"

	"plexo/internal/fchat"
)

// WSServer is a TLS WebSocket server that speaks the fake F-Chat protocol.
// Clients dial it through the production fchat transport, so tests exercise a
// real handshake, framing, masking, and control frames.
type WSServer struct {
	*httptest.Server

	opts     Options
	onAccept func(*Server)

	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	conns []*fchatWSConn
}

func newWSServer(opts Options, onAccept func(*Server)) *WSServer {
	ctx, cancel := context.WithCancel(context.Background())
	w := &WSServer{opts: opts, onAccept: onAccept, ctx: ctx, cancel: cancel}
	w.Server = httptest.NewTLSServer(http.HandlerFunc(w.serveHTTP))
	return w
}

// WSURL returns the wss:// endpoint clients dial.
func (w *WSServer) WSURL() string {
	u, _ := url.Parse(w.Server.URL)
	u.Scheme = "wss"
	u.Path = "/chat2"
	return u.String()
}

// Dial opens a WebSocket connection using the production F-Chat transport.
func (w *WSServer) Dial(ctx context.Context) (fchat.Conn, error) {
	return fchat.Dial(ctx, fchat.DialConfig{
		URL:        w.WSURL(),
		HTTPClient: w.Client(),
	})
}

func (w *WSServer) serveHTTP(rw http.ResponseWriter, r *http.Request) {
	// Mirror a real F-Chat server that offers permessage-deflate so the
	// production transport negotiates it during integration tests.
	ws, err := websocket.Accept(rw, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionContextTakeover,
	})
	if err != nil {
		return
	}
	conn := &fchatWSConn{ws: ws}
	w.mu.Lock()
	w.conns = append(w.conns, conn)
	w.mu.Unlock()

	srv := New(conn, w.opts)
	if w.onAccept != nil {
		w.onAccept(srv)
	}
	// Run blocks the handler goroutine for the life of the connection, which
	// keeps the request context valid.
	srv.Run(w.ctx)
}

func (w *WSServer) close() {
	w.cancel()
	w.Server.CloseClientConnections()
	w.Server.Close()
}

// fchatWSConn adapts a server-side WebSocket to fchat.Conn.
type fchatWSConn struct {
	ws        *websocket.Conn
	closeOnce sync.Once
}

func (c *fchatWSConn) Read(ctx context.Context) (fchat.Frame, error) {
	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		return fchat.Frame{}, err
	}
	if typ != websocket.MessageText {
		return fchat.Frame{}, fmt.Errorf("fakeserver: unexpected ws message type %d", typ)
	}
	return fchat.Parse(data)
}

func (c *fchatWSConn) Write(ctx context.Context, cmd fchat.Frame) error {
	return c.ws.Write(ctx, websocket.MessageText, cmd.Marshal())
}

// writeText bypasses fchat.Frame marshaling for malformed-frame tests.
func (c *fchatWSConn) writeText(b []byte) error {
	return c.ws.Write(context.Background(), websocket.MessageText, b)
}

func (c *fchatWSConn) Close() error {
	c.closeOnce.Do(func() { _ = c.ws.CloseNow() })
	return nil
}
