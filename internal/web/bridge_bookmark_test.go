package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"plexo/internal/core"
	"plexo/internal/model"
	"plexo/test/memstore"
)

// TestBridgeSetBookmark drives the account-layer op: add/remove reach the REST
// endpoint with the target name, and an unknown action is rejected before any
// call is made.
func TestBridgeSetBookmark(t *testing.T) {
	ticket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ticket":"fct_test","error":"","characters":["Vix"]}`))
	}))
	defer ticket.Close()

	var mu sync.Mutex
	var added, removed string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/bookmark-add.php":
			mu.Lock()
			added = r.PostForm.Get("name")
			mu.Unlock()
			_, _ = w.Write([]byte(`{"error":""}`))
		case "/bookmark-remove.php":
			mu.Lock()
			removed = r.PostForm.Get("name")
			mu.Unlock()
			_, _ = w.Write([]byte(`{"error":""}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	account := core.NewAccount(
		core.WithTicketURL(ticket.URL), core.WithTicketClient(ticket.Client()),
		core.WithAPIURL(api.URL+"/"), core.WithAPIClient(api.Client()),
	)
	if _, err := account.SetCredentials(context.Background(), "acct", "pw", false); err != nil {
		t.Fatalf("SetCredentials: %v", err)
	}
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
	subscribeT(t, ctx, c, "bm-1")
	if env := readEnvelope(t, ctx, c); env.Type != THello {
		t.Fatalf("first envelope = %q, want %q", env.Type, THello)
	}

	ok, err := sendAccountCmd(ctx, c, "add-1", model.Command{CID: "add-1", Op: model.OpSetBookmark, Character: "Carol", Action: "add"})
	if err != nil || !ok {
		t.Fatalf("set_bookmark add: ok=%v err=%v", ok, err)
	}
	if _, bookmarks := manager.FriendBookmarks(); !slices.Contains(bookmarks, "Carol") {
		t.Fatalf("manager split missing Carol after add: %v", bookmarks)
	}
	ok, err = sendAccountCmd(ctx, c, "rm-1", model.Command{CID: "rm-1", Op: model.OpSetBookmark, Character: "Carol", Action: "remove"})
	if err != nil || !ok {
		t.Fatalf("set_bookmark remove: ok=%v err=%v", ok, err)
	}
	if _, bookmarks := manager.FriendBookmarks(); slices.Contains(bookmarks, "Carol") {
		t.Fatalf("manager split still has Carol after remove: %v", bookmarks)
	}
	ok, err = sendAccountCmd(ctx, c, "bad-1", model.Command{CID: "bad-1", Op: model.OpSetBookmark, Character: "Carol", Action: "bogus"})
	if err != nil {
		t.Fatalf("set_bookmark bogus: %v", err)
	}
	if ok {
		t.Fatal("bogus action accepted")
	}

	mu.Lock()
	defer mu.Unlock()
	if added != "Carol" || removed != "Carol" {
		t.Fatalf("api names: added=%q removed=%q", added, removed)
	}
}
