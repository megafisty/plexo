package fchat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"plexo/test/fixtures"
)

// countingTickets is a TicketManager + TicketInvalidator that hands out a fresh
// ticket per mint and records invalidations.
type countingTickets struct {
	mu      sync.Mutex
	mints   int
	invalid int
}

func (c *countingTickets) Ticket(context.Context, string) (Ticket, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mints++
	return Ticket{Value: "tkt-" + strconv.Itoa(c.mints), MintedAt: time.Now()}, nil
}

func (c *countingTickets) Invalidate(string) {
	c.mu.Lock()
	c.invalid++
	c.mu.Unlock()
}

func newAPITestServer(t *testing.T, handler http.HandlerFunc) (*AccountAPI, *countingTickets, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	tickets := &countingTickets{}
	return &AccountAPI{Tickets: tickets, Invalidator: tickets, BaseURL: srv.URL + "/"}, tickets, srv.Close
}

func TestFriendBookmarkLists(t *testing.T) {
	var gotForm url.Values
	api, _, closeSrv := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/friend-bookmark-lists.php" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = r.ParseForm()
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixtures.FriendBookmarkLists())
	})
	defer closeSrv()

	friends, bookmarks, err := api.FriendBookmarkLists(context.Background(), "acct")
	if err != nil {
		t.Fatalf("FriendBookmarkLists: %v", err)
	}
	if gotForm.Get("account") != "acct" || gotForm.Get("ticket") != "tkt-1" {
		t.Fatalf("form missing account/ticket: %v", gotForm)
	}
	if gotForm.Get("friendlist") != "true" || gotForm.Get("bookmarklist") != "true" {
		t.Fatalf("form missing list flags: %v", gotForm)
	}
	if len(friends) != 1 || friends[0] != "Chat Test Character" {
		t.Fatalf("friends = %v, want [Chat Test Character]", friends)
	}
	if len(bookmarks) != 2 || bookmarks[0] != "Chat Test Character" || bookmarks[1] != "Chat Test Character2" {
		t.Fatalf("bookmarks = %v", bookmarks)
	}
}

// TestDecodeFriendBookmarkLists checks the pure decoder against an inline body
// (case-insensitive dedupe and API error handling) so it does not depend on the
// fixture's exact contents.
func TestDecodeFriendBookmarkLists(t *testing.T) {
	friends, bookmarks, err := DecodeFriendBookmarkLists([]byte(`{
		"friendlist": [
			{"source":"Vix","dest":"Aiaru","last_online":1},
			{"source":"Vix","dest":"aiaru","last_online":1},
			{"source":"Vix","dest":"Bob","last_online":2}
		],
		"bookmarklist": ["Carol", "Dave"],
		"error": ""
	}`))
	if err != nil {
		t.Fatalf("DecodeFriendBookmarkLists: %v", err)
	}
	if len(friends) != 2 || friends[0] != "Aiaru" || friends[1] != "Bob" {
		t.Fatalf("friends = %v, want [Aiaru Bob] (deduped)", friends)
	}
	if len(bookmarks) != 2 || bookmarks[0] != "Carol" || bookmarks[1] != "Dave" {
		t.Fatalf("bookmarks = %v", bookmarks)
	}

	if _, _, err := DecodeFriendBookmarkLists([]byte(`{"error":"nope"}`)); err == nil || err.Error() != "nope" {
		t.Fatalf("error field not surfaced: %v", err)
	}
}

func TestAddBookmarkSendsName(t *testing.T) {
	var gotForm url.Values
	api, _, closeSrv := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bookmark-add.php" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = r.ParseForm()
		gotForm = r.PostForm
		_, _ = w.Write([]byte(`{"error":""}`))
	})
	defer closeSrv()

	if err := api.AddBookmark(context.Background(), "acct", "Carol"); err != nil {
		t.Fatalf("AddBookmark: %v", err)
	}
	if gotForm.Get("name") != "Carol" || gotForm.Get("account") != "acct" || gotForm.Get("ticket") != "tkt-1" {
		t.Fatalf("form = %v", gotForm)
	}
}

func TestBookmarkInvalidTicketRetriesOnce(t *testing.T) {
	var mu sync.Mutex
	var tickets []string
	api, ticketsMgr, closeSrv := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		tickets = append(tickets, r.PostForm.Get("ticket"))
		n := len(tickets)
		mu.Unlock()
		if n == 1 {
			_, _ = w.Write([]byte(`{"error":"Invalid ticket."}`))
			return
		}
		_, _ = w.Write([]byte(`{"error":""}`))
	})
	defer closeSrv()

	if err := api.AddBookmark(context.Background(), "acct", "Carol"); err != nil {
		t.Fatalf("AddBookmark: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), tickets...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "tkt-1" || got[1] != "tkt-2" {
		t.Fatalf("tickets used = %v, want [tkt-1 tkt-2]", got)
	}
	if ticketsMgr.invalid != 1 {
		t.Fatalf("invalidations = %d, want 1", ticketsMgr.invalid)
	}
}

func TestBookmarkAPIErrorPassesThrough(t *testing.T) {
	api, _, closeSrv := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":"You already have this character bookmarked."}`))
	})
	defer closeSrv()

	err := api.AddBookmark(context.Background(), "acct", "Carol")
	if err == nil || err.Error() != "You already have this character bookmarked." {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, errInvalidTicket) {
		t.Fatal("a non-ticket API error must not trigger the retry path")
	}
}

func TestBookmarkExpiredTicketRetries(t *testing.T) {
	var n int
	api, _, closeSrv := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			_, _ = w.Write([]byte(`{"error":"Your login ticket has expired (five minutes) or no ticket requested."}`))
			return
		}
		_, _ = w.Write([]byte(`{"error":""}`))
	})
	defer closeSrv()

	if err := api.RemoveBookmark(context.Background(), "acct", "Carol"); err != nil {
		t.Fatalf("RemoveBookmark: %v", err)
	}
}
