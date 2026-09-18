package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"plexo/internal/core"
	"plexo/internal/fchat"
	"plexo/internal/model"
	"plexo/internal/session"
	"plexo/test/fakeserver"
	"plexo/test/fchatpipe"
	"plexo/test/memstore"
)

func testTickets(context.Context, string) (fchat.Ticket, error) {
	return fchat.Ticket{Value: "tkt", MintedAt: time.Now()}, nil
}

// postJSON issues a JSON POST and returns the status code.
func postJSON(t *testing.T, url, body string) int {
	t.Helper()
	res, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

// TestAPISearch exercises the search endpoints: POST is accepted on a live
// session, 404 unknown, 400 malformed, 409 before ready; GET returns the
// session's cached result set.
func TestAPISearch(t *testing.T) {
	fac := fakeserver.NewWSFactory(fakeserver.Options{Character: "Vix", SearchResults: []string{"Some Guy"}})
	t.Cleanup(fac.Close)
	manager := core.NewManager(context.Background(), core.Config{
		Store:   memstore.New(),
		Tickets: fchat.TicketFunc(testTickets),
		Dial:    func(string) session.Dialer { return session.Dialer(fac.Dial) },
	})
	base := newAPIServer(t, manager, "")
	if err := manager.Login("acct", "Vix"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Logout("Vix") })

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := manager.Snapshot()
		if len(snap.Sessions) == 1 && snap.Sessions[0].State == "live" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session never became live: %+v", snap)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got := postJSON(t, base+"/api/search?session=Vix", `{"kinks":[523],"genders":["Male"]}`); got != http.StatusAccepted {
		t.Fatalf("search status = %d, want 202", got)
	}
	// The relay caches the reply on the session; GET pulls it. The server's FKS
	// answer is asynchronous, so poll briefly until the cache is populated.
	var payload model.SearchPayload
	deadline = time.Now().Add(2 * time.Second)
	for {
		res, err := http.Get(base + "/api/search?session=Vix")
		if err != nil {
			t.Fatalf("get search: %v", err)
		}
		if res.StatusCode != http.StatusOK {
			res.Body.Close()
			t.Fatalf("GET /api/search status = %d, want 200", res.StatusCode)
		}
		payload = model.SearchPayload{}
		decodeErr := json.NewDecoder(res.Body).Decode(&payload)
		res.Body.Close()
		if decodeErr != nil {
			t.Fatalf("decode search: %v", decodeErr)
		}
		if len(payload.Characters) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("search cache never populated: %+v", payload)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if payload.Revision == 0 {
		t.Fatal("cached search revision = 0, want > 0")
	}
	if payload.Characters[0].Name != "Some Guy" {
		t.Fatalf("cached characters = %+v, want Some Guy", payload.Characters)
	}
	if res, _ := http.Get(base + "/api/search?session=Nobody"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session GET status = %d, want 404", res.StatusCode)
	}
	if got := postJSON(t, base+"/api/search?session=Nobody", `{"kinks":[]}`); got != http.StatusNotFound {
		t.Fatalf("unknown session status = %d, want 404", got)
	}
	if got := postJSON(t, base+"/api/search?session=Vix", `not json`); got != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", got)
	}
	if got := postJSON(t, base+"/api/search", `{"kinks":[]}`); got != http.StatusBadRequest {
		t.Fatalf("missing session status = %d, want 400", got)
	}

	// A session that never reaches ready rejects the trigger with 409.
	connecting := core.NewManager(context.Background(), core.Config{
		Store:   memstore.New(),
		Tickets: fchat.TicketFunc(testTickets),
		Dial: func(string) session.Dialer {
			return session.Dialer(func(ctx context.Context) (fchat.Conn, error) {
				client, _ := fchatpipe.Pipe()
				return client, nil
			})
		},
	})
	connectBase := newAPIServer(t, connecting, "")
	if err := connecting.Login("acct", "Nix"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connecting.Logout("Nix") })
	if got := postJSON(t, connectBase+"/api/search?session=Nix", `{"kinks":[]}`); got != http.StatusConflict {
		t.Fatalf("not-ready status = %d, want 409", got)
	}
}
