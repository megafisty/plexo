package core_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"plexo/internal/config"
	"plexo/internal/core"
	"plexo/internal/model"
)

// ticketServer returns an httptest server that responds to ticket mints.
func ticketServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("ticket request method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("content-type = %q", ct)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestAccount(t *testing.T, srv *httptest.Server) *core.Account {
	t.Helper()
	return core.NewAccount(core.WithTicketURL(srv.URL), core.WithTicketClient(srv.Client()))
}

func TestAccountStartsMissing(t *testing.T) {
	a := core.NewAccount()
	if got := a.State().Status; got != model.AccountMissing {
		t.Fatalf("status = %q, want %q", got, model.AccountMissing)
	}
	if a.Name() != "" {
		t.Fatalf("name = %q, want empty", a.Name())
	}
}

func TestAccountSetCredentialsValid(t *testing.T) {
	srv := ticketServer(t, `{"ticket":"fct_abc","error":"","characters":["Vix","Second"]}`, http.StatusOK)
	a := newTestAccount(t, srv)

	st, err := a.SetCredentials(context.Background(), "acct", "hunter2", false)
	if err != nil {
		t.Fatalf("SetCredentials: %v", err)
	}
	if st.Status != model.AccountOK {
		t.Fatalf("status = %q, want ok", st.Status)
	}
	if strings.Join(st.Characters, ",") != "Vix,Second" {
		t.Fatalf("characters = %v", st.Characters)
	}
	if got := a.State().Status; got != model.AccountOK {
		t.Fatalf("State().Status = %q, want ok", got)
	}
	// The ticket manager must now serve the cached ticket.
	tk, err := a.Tickets().Ticket(context.Background(), "acct")
	if err != nil || tk.Value != "fct_abc" {
		t.Fatalf("Ticket = %+v, %v", tk, err)
	}
}

func TestAccountSetCredentialsInvalidIsNotRetained(t *testing.T) {
	srv := ticketServer(t, `{"ticket":"","error":"Invalid account or password"}`, http.StatusOK)
	a := newTestAccount(t, srv)

	st, err := a.SetCredentials(context.Background(), "acct", "wrong", false)
	if err != nil {
		t.Fatalf("SetCredentials returned transient error: %v", err)
	}
	if st.Status != model.AccountInvalid {
		t.Fatalf("status = %q, want invalid", st.Status)
	}
	if st.Reason == "" {
		t.Error("expected a reason for invalid credentials")
	}
	if a.Name() != "" {
		t.Fatalf("name = %q, want empty after a rejected pair", a.Name())
	}
	if _, err := a.Tickets().Ticket(context.Background(), "acct"); err == nil {
		t.Fatal("expected an error minting with cleared credentials")
	}
}

func TestAccountSetCredentialsUnreachableKeepsCredentials(t *testing.T) {
	srv := ticketServer(t, "boom", http.StatusInternalServerError)
	a := newTestAccount(t, srv)

	st, err := a.SetCredentials(context.Background(), "acct", "pw", false)
	if err == nil {
		t.Fatal("expected a transient error")
	}
	if st.Status != model.AccountUnreachable {
		t.Fatalf("status = %q, want unreachable", st.Status)
	}
}

func TestAccountClearCredentials(t *testing.T) {
	srv := ticketServer(t, `{"ticket":"fct_abc","error":"","characters":["Vix"]}`, http.StatusOK)
	a := newTestAccount(t, srv)
	if _, err := a.SetCredentials(context.Background(), "acct", "pw", false); err != nil {
		t.Fatal(err)
	}
	a.ClearCredentials()
	if got := a.State().Status; got != model.AccountMissing {
		t.Fatalf("status = %q, want missing", got)
	}
	if a.Name() != "" {
		t.Fatalf("name = %q, want empty after clear", a.Name())
	}
}

func TestAccountSubscribeSeesUpdates(t *testing.T) {
	srv := ticketServer(t, `{"ticket":"fct_abc","error":"","characters":["Vix"]}`, http.StatusOK)
	a := newTestAccount(t, srv)

	ch, cancel := a.Subscribe()
	defer cancel()
	if got := waitState(t, ch); got.Status != model.AccountMissing {
		t.Fatalf("initial status = %q, want missing", got.Status)
	}
	if _, err := a.SetCredentials(context.Background(), "acct", "pw", false); err != nil {
		t.Fatal(err)
	}
	if got := waitState(t, ch); got.Status != model.AccountOK {
		t.Fatalf("status = %q, want ok", got.Status)
	}
}

func waitState(t *testing.T, ch <-chan model.AccountState) model.AccountState {
	t.Helper()
	select {
	case st := <-ch:
		return st
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for account state")
		return model.AccountState{}
	}
}

// fakeStore is an in-memory core.CredentialStore.
type fakeStore struct {
	creds config.Credentials
	ok    bool
	saves int
	dels  int
}

func (f *fakeStore) LoadCredentials(context.Context) (config.Credentials, bool, error) {
	return f.creds, f.ok, nil
}

func (f *fakeStore) SaveCredentials(_ context.Context, c config.Credentials) error {
	f.creds, f.ok, f.saves = c, true, f.saves+1
	return nil
}

func (f *fakeStore) DeleteCredentials(context.Context) error {
	f.creds, f.ok, f.dels = config.Credentials{}, false, f.dels+1
	return nil
}

// waitStatus polls until the account reaches want.
func waitStatus(t *testing.T, a *core.Account, want string) model.AccountState {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := a.State(); st.Status == want {
			return st
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for status %q (got %q)", want, a.State().Status)
	return model.AccountState{}
}

func TestAccountSetCredentialsRemembers(t *testing.T) {
	srv := ticketServer(t, `{"ticket":"fct_abc","error":"","characters":["Vix"]}`, http.StatusOK)
	store := &fakeStore{}
	a := core.NewAccount(core.WithTicketURL(srv.URL), core.WithTicketClient(srv.Client()), core.WithCredentialStore(store))

	st, err := a.SetCredentials(context.Background(), "acct", "pw", true)
	if err != nil {
		t.Fatalf("SetCredentials: %v", err)
	}
	if !st.Persisted {
		t.Error("state.Persisted = false, want true")
	}
	if !store.ok || store.creds.Account != "acct" || store.creds.Password != "pw" {
		t.Fatalf("stored = %+v, ok=%v", store.creds, store.ok)
	}
}

func TestAccountSetCredentialsWithoutRememberPurges(t *testing.T) {
	srv := ticketServer(t, `{"ticket":"fct_abc","error":"","characters":["Vix"]}`, http.StatusOK)
	store := &fakeStore{creds: config.Credentials{Account: "old", Password: "oldpw"}, ok: true}
	a := core.NewAccount(core.WithTicketURL(srv.URL), core.WithTicketClient(srv.Client()), core.WithCredentialStore(store))

	st, err := a.SetCredentials(context.Background(), "acct", "pw", false)
	if err != nil {
		t.Fatalf("SetCredentials: %v", err)
	}
	if st.Persisted {
		t.Error("state.Persisted = true, want false")
	}
	if store.ok {
		t.Error("stored credentials were not purged")
	}
}

func TestAccountRestoreValid(t *testing.T) {
	srv := ticketServer(t, `{"ticket":"fct_abc","error":"","characters":["Vix"]}`, http.StatusOK)
	store := &fakeStore{creds: config.Credentials{Account: "acct", Password: "pw"}, ok: true}
	a := core.NewAccount(core.WithTicketURL(srv.URL), core.WithTicketClient(srv.Client()), core.WithCredentialStore(store))

	a.Restore(context.Background())
	st := waitStatus(t, a, model.AccountOK)
	if !st.Persisted {
		t.Error("Persisted = false, want true")
	}
	if a.Name() != "acct" {
		t.Fatalf("name = %q, want acct", a.Name())
	}
}

func TestAccountRestoreInvalidPurges(t *testing.T) {
	srv := ticketServer(t, `{"ticket":"","error":"Invalid account or password"}`, http.StatusOK)
	store := &fakeStore{creds: config.Credentials{Account: "acct", Password: "wrong"}, ok: true}
	a := core.NewAccount(core.WithTicketURL(srv.URL), core.WithTicketClient(srv.Client()), core.WithCredentialStore(store))

	a.Restore(context.Background())
	st := waitStatus(t, a, model.AccountInvalid)
	if st.Persisted {
		t.Error("Persisted = true after a rejected pair")
	}
	if store.ok {
		t.Error("rejected stored credentials were not purged")
	}
}

func TestAccountRestoreUnreachableKeeps(t *testing.T) {
	srv := ticketServer(t, "boom", http.StatusInternalServerError)
	store := &fakeStore{creds: config.Credentials{Account: "acct", Password: "pw"}, ok: true}
	a := core.NewAccount(core.WithTicketURL(srv.URL), core.WithTicketClient(srv.Client()), core.WithCredentialStore(store))

	a.Restore(context.Background())
	st := waitStatus(t, a, model.AccountUnreachable)
	if !st.Persisted {
		t.Error("Persisted = false, want true")
	}
	if !store.ok {
		t.Error("transient failure should keep stored credentials")
	}
}

func TestAccountPurgeStoredKeepsMemory(t *testing.T) {
	srv := ticketServer(t, `{"ticket":"fct_abc","error":"","characters":["Vix"]}`, http.StatusOK)
	store := &fakeStore{}
	a := core.NewAccount(core.WithTicketURL(srv.URL), core.WithTicketClient(srv.Client()), core.WithCredentialStore(store))
	if _, err := a.SetCredentials(context.Background(), "acct", "pw", true); err != nil {
		t.Fatal(err)
	}

	if err := a.PurgeStoredCredentials(context.Background()); err != nil {
		t.Fatalf("PurgeStoredCredentials: %v", err)
	}
	if store.ok {
		t.Error("stored credentials were not deleted")
	}
	if a.State().Persisted {
		t.Error("Persisted = true after purge")
	}
	if a.Name() != "acct" {
		t.Fatalf("name = %q, want in-memory pair kept", a.Name())
	}
}
