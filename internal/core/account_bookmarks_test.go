package core_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"plexo/internal/core"
	"plexo/test/fixtures"
)

// TestAccountFriendBookmarksREST exercises the Account -> AccountAPI wiring
// against loopback servers (no F-List traffic): credential validation mints the
// ticket, the fetch and mutation calls reuse it, and the mutation form carries
// the target name.
func TestAccountFriendBookmarksREST(t *testing.T) {
	ticketSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ticket":"tkt-1","characters":["Vix"],"error":""}`))
	}))
	defer ticketSrv.Close()

	var addForm url.Values
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.PostForm.Get("ticket"); got != "tkt-1" {
			t.Errorf("ticket = %q, want tkt-1", got)
		}
		switch r.URL.Path {
		case "/friend-bookmark-lists.php":
			_, _ = w.Write(fixtures.FriendBookmarkLists())
		case "/bookmark-add.php":
			addForm = r.PostForm
			_, _ = w.Write([]byte(`{"error":""}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiSrv.Close()

	acct := core.NewAccount(core.WithTicketURL(ticketSrv.URL), core.WithAPIURL(apiSrv.URL+"/"))
	if _, err := acct.SetCredentials(context.Background(), "acct", "pw", false); err != nil {
		t.Fatalf("SetCredentials: %v", err)
	}

	friends, bookmarks, err := acct.FetchFriendBookmarkLists(context.Background())
	if err != nil {
		t.Fatalf("FetchFriendBookmarkLists: %v", err)
	}
	if len(friends) != 1 || friends[0] != "Chat Test Character" {
		t.Fatalf("friends = %v", friends)
	}
	if len(bookmarks) != 2 {
		t.Fatalf("bookmarks = %v", bookmarks)
	}

	if err := acct.AddBookmark(context.Background(), "Carol"); err != nil {
		t.Fatalf("AddBookmark: %v", err)
	}
	if addForm.Get("name") != "Carol" || addForm.Get("account") != "acct" {
		t.Fatalf("add form = %v", addForm)
	}
}
