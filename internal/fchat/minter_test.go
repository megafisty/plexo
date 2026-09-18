package fchat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func staticPasswords(pw string) Passwords {
	return func(account string) (string, bool) {
		if account == "acct" {
			return pw, true
		}
		return "", false
	}
}

func TestMinterMintsAndCaches(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.Form.Get("account") != "acct" || r.Form.Get("password") != "hunter2" {
			t.Errorf("form = %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ticket":"fct_abc","error":""}`))
	}))
	t.Cleanup(srv.Close)

	m := NewTicketMinter(staticPasswords("hunter2"))
	m.URL = srv.URL
	m.Client = srv.Client()

	for i := 0; i < 2; i++ {
		ticket, err := m.Ticket(context.Background(), "acct")
		if err != nil {
			t.Fatalf("ticket %d: %v", i, err)
		}
		if ticket.Value != "fct_abc" {
			t.Fatalf("ticket %d = %q", i, ticket.Value)
		}
		if ticket.MintedAt.IsZero() {
			t.Fatalf("ticket %d has zero MintedAt", i)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 (second call should hit the cache)", got)
	}
}

func TestMinterReremintsAfterTTL(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		_, _ = w.Write([]byte(`{"ticket":"fct_` + string(rune('a'+n-1)) + `","error":""}`))
	}))
	t.Cleanup(srv.Close)

	now := time.Now()
	m := NewTicketMinter(staticPasswords("pw"))
	m.URL = srv.URL
	m.Client = srv.Client()
	m.Now = func() time.Time { return now }
	m.TTL = time.Minute

	if _, err := m.Ticket(context.Background(), "acct"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := m.Ticket(context.Background(), "acct"); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 after TTL", got)
	}
}

func TestMinterInvalidCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ticket":"","error":"Invalid account or password"}`))
	}))
	t.Cleanup(srv.Close)

	m := NewTicketMinter(staticPasswords("wrong"))
	m.URL = srv.URL
	m.Client = srv.Client()

	_, err := m.Ticket(context.Background(), "acct")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestMinterNoCredentials(t *testing.T) {
	m := NewTicketMinter(staticPasswords("pw"))
	_, err := m.Ticket(context.Background(), "unknown")
	if !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("err = %v, want ErrNoCredentials", err)
	}
}

func TestMinterHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	m := NewTicketMinter(staticPasswords("pw"))
	m.URL = srv.URL
	m.Client = srv.Client()

	if _, err := m.Ticket(context.Background(), "acct"); err == nil {
		t.Fatal("expected an error for HTTP 502")
	}
}

func TestMinterInvalidate(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"ticket":"fct_x","error":""}`))
	}))
	t.Cleanup(srv.Close)

	m := NewTicketMinter(staticPasswords("pw"))
	m.URL = srv.URL
	m.Client = srv.Client()

	if _, err := m.Ticket(context.Background(), "acct"); err != nil {
		t.Fatal(err)
	}
	m.Invalidate("acct")
	if _, err := m.Ticket(context.Background(), "acct"); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 after Invalidate", got)
	}
}
