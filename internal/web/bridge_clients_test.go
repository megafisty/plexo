package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"plexo/internal/core"
	"plexo/test/memstore"
)

// TestBridgeTracksClients: the bridge must expose the connected browser
// sockets (and only those) for the dev console's client pane.
func TestBridgeTracksClients(t *testing.T) {
	manager := core.NewManager(context.Background(), core.Config{Store: memstore.New()})
	bridge := NewBridge(manager, core.NewAccount(), NewSessionAuth(""), nil)
	mux := http.NewServeMux()
	mux.Handle("/ws", bridge.Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if got := waitClients(bridge, 1); len(got) != 1 {
		t.Fatalf("clients = %v, want 1", got)
	}
	c.CloseNow()
	if got := waitClients(bridge, 0); len(got) != 0 {
		t.Fatalf("clients after close = %v, want 0", got)
	}
}

// waitClients waits for the tracked client count to reach want, returning the
// last observed list when it times out.
func waitClients(b *Bridge, want int) []string {
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := b.Clients()
		if len(got) == want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}
