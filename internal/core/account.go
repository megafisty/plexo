package core

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"plexo/internal/config"
	"plexo/internal/fchat"
	"plexo/internal/model"
)

// CredentialStore persists the F-Chat account and password so the core can
// restore them after a restart. Implementations store the password in plain
// text; it never leaves the core.
type CredentialStore interface {
	LoadCredentials(ctx context.Context) (config.Credentials, bool, error)
	SaveCredentials(ctx context.Context, c config.Credentials) error
	DeleteCredentials(ctx context.Context) error
}

// credentials holds the single F-Chat account's password. The ticket minter
// reads it on each mint; it never leaves the core.
type credentials struct {
	mu       sync.RWMutex
	account  string
	password string
}

func (c *credentials) get(account string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.account == "" || !strings.EqualFold(c.account, account) {
		return "", false
	}
	return c.password, true
}

// Account owns the F-Chat account: its credentials, its ticket minter, and the
// browser-facing account state. It is the only place the core stores an F-Chat
// password. Passwords and tickets are never logged or returned to clients.
type Account struct {
	creds  *credentials
	minter *fchat.TicketMinter
	store  CredentialStore

	// persisted reports whether a credential document is stored. It is an
	// atomic so the account state can reflect it without taking the store lock.
	persisted atomic.Bool

	mu    sync.Mutex
	state model.AccountState
	subs  map[int]chan model.AccountState
	next  int
}

// AccountOption configures the account. It exists mainly so tests and the dev
// harness can point at a fake endpoint or credential store.
type AccountOption func(*Account)

// WithTicketURL overrides the ticket endpoint.
func WithTicketURL(url string) AccountOption {
	return func(a *Account) { a.minter.URL = url }
}

// WithTicketClient overrides the ticket HTTP client.
func WithTicketClient(c *http.Client) AccountOption {
	return func(a *Account) { a.minter.Client = c }
}

// WithCredentialStore persists validated credentials so they survive a restart.
func WithCredentialStore(s CredentialStore) AccountOption {
	return func(a *Account) { a.store = s }
}

// NewAccount returns an account with no credentials.
func NewAccount(opts ...AccountOption) *Account {
	c := &credentials{}
	a := &Account{
		creds:  c,
		minter: fchat.NewTicketMinter(c.get),
		state:  model.AccountState{Status: model.AccountMissing},
		subs:   map[int]chan model.AccountState{},
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Tickets returns the ticket manager backed by this account's credentials.
func (a *Account) Tickets() fchat.TicketManager { return a.minter }

// Name returns the stored account name, or "".
func (a *Account) Name() string {
	a.creds.mu.RLock()
	defer a.creds.mu.RUnlock()
	return a.creds.account
}

// State returns a copy of the current account state.
func (a *Account) State() model.AccountState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.copyLocked()
}

func (a *Account) copyLocked() model.AccountState {
	st := a.state
	if st.Characters != nil {
		st.Characters = append([]string(nil), st.Characters...)
	}
	st.Persisted = a.persisted.Load()
	return st
}

// SetCredentials validates the credentials by minting a ticket, then remembers
// them. Only a validated pair is retained. When remember is true the pair is
// also persisted (unencrypted) so the core can restore it after a restart; a
// rejected pair is never retained or stored. A transient failure is returned as
// an error and leaves the in-memory credentials in place so a later mint can
// retry.
func (a *Account) SetCredentials(ctx context.Context, account, password string, remember bool) (model.AccountState, error) {
	a.remember(account, password)

	t, err := a.validate(ctx, account)
	if err != nil {
		return a.failValidation(ctx, err)
	}

	if remember && a.store != nil {
		if serr := a.store.SaveCredentials(ctx, config.Credentials{Account: account, Password: password}); serr != nil {
			// The pair is valid and usable, but the request to remember it could
			// not be honored. Surface the failure without discarding it.
			a.persisted.Store(false)
			a.setState(okState(t))
			return a.State(), serr
		}
		a.persisted.Store(true)
	} else if a.store != nil {
		// Remembering was not requested; drop any stored pair so the explicit
		// choice is honored on the next restart.
		_ = a.store.DeleteCredentials(ctx)
		a.persisted.Store(false)
	}
	a.setState(okState(t))
	return a.State(), nil
}

// Restore loads persisted credentials and revalidates them in the background.
// It returns after seeding the checking state, so the first client to connect
// sees validation in flight rather than the gate. A definitive rejection purges
// the stored pair; a transient failure keeps it for a later restart.
func (a *Account) Restore(ctx context.Context) {
	if a.store == nil {
		return
	}
	creds, ok, err := a.store.LoadCredentials(ctx)
	if err != nil || !ok || creds.Account == "" || creds.Password == "" {
		return
	}
	a.persisted.Store(true)
	a.remember(creds.Account, creds.Password)
	a.minter.Invalidate(creds.Account)
	a.setState(model.AccountState{Status: model.AccountChecking})
	go a.revalidate(ctx, creds.Account)
}

// PurgeStoredCredentials deletes any persisted credentials, leaving the
// in-memory pair and running sessions untouched. The gate returns after the next
// core restart.
func (a *Account) PurgeStoredCredentials(ctx context.Context) error {
	if a.store != nil {
		if err := a.store.DeleteCredentials(ctx); err != nil {
			return err
		}
	}
	if a.persisted.Swap(false) {
		a.setState(a.State())
	}
	return nil
}

// revalidate finishes a background restore by minting a ticket for the loaded
// account so the character list is fresh and a revoked pair is detected.
func (a *Account) revalidate(ctx context.Context, account string) {
	t, err := a.minter.Ticket(ctx, account)
	if err != nil {
		a.failValidation(ctx, err)
		return
	}
	a.setState(okState(t))
}

// remember stores the account and password in memory. It does not touch the
// ticket cache or the published state.
func (a *Account) remember(account, password string) {
	a.creds.mu.Lock()
	a.creds.account = account
	a.creds.password = password
	a.creds.mu.Unlock()
}

// validate invalidates the cached ticket, publishes the checking state, and
// mints a fresh ticket.
func (a *Account) validate(ctx context.Context, account string) (fchat.Ticket, error) {
	a.minter.Invalidate(account)
	a.setState(model.AccountState{Status: model.AccountChecking})
	return a.minter.Ticket(ctx, account)
}

// failValidation records a failed mint. A rejected pair is cleared from memory
// and any stored copy is purged; a transient failure is surfaced and leaves
// both in place so a later mint can retry.
func (a *Account) failValidation(ctx context.Context, err error) (model.AccountState, error) {
	if errors.Is(err, fchat.ErrInvalidCredentials) {
		a.clearCredentials()
		a.persisted.Store(false)
		_ = a.deleteStored(ctx)
		a.setState(model.AccountState{
			Status: model.AccountInvalid,
			Reason: "F-List rejected the account or password.",
		})
		return a.State(), nil
	}
	a.setState(model.AccountState{
		Status: model.AccountUnreachable,
		Reason: "Could not reach F-List. Check your connection and try again.",
	})
	return a.State(), err
}

// deleteStored removes any persisted credentials. A failure is reported to the
// caller; callers that cannot act on it treat it as best-effort.
func (a *Account) deleteStored(ctx context.Context) error {
	if a.store == nil {
		return nil
	}
	return a.store.DeleteCredentials(ctx)
}

// okState builds the published state for validated credentials.
func okState(t fchat.Ticket) model.AccountState {
	return model.AccountState{
		Status:     model.AccountOK,
		Characters: append([]string(nil), t.Characters...),
	}
}

// ClearCredentials forgets the account, the password, and the cached ticket in
// memory. It does not delete any stored credentials; use
// PurgeStoredCredentials for that, or the stored pair will return on the next
// restart.
func (a *Account) ClearCredentials() {
	account := a.Name()
	a.clearCredentials()
	a.minter.Invalidate(account)
	a.setState(model.AccountState{Status: model.AccountMissing})
}

// clearCredentials forgets the stored account and password. It does not touch
// the cached ticket or the published state; callers own those decisions.
func (a *Account) clearCredentials() {
	a.creds.mu.Lock()
	a.creds.account = ""
	a.creds.password = ""
	a.creds.mu.Unlock()
}

func (a *Account) setState(st model.AccountState) {
	// Sends are non-blocking, so hold the lock across them: a subscriber's
	// cancel (which closes its channel) must not interleave between the
	// snapshot and the send, or the send panics on a closed channel.
	a.mu.Lock()
	defer a.mu.Unlock()
	st.Persisted = a.persisted.Load()
	a.state = st
	for _, ch := range a.subs {
		sendLatest(ch, st)
	}
}

// Subscribe returns a latest-wins channel seeded with the current state and a
// cancel function that closes it.
func (a *Account) Subscribe() (<-chan model.AccountState, func()) {
	ch := make(chan model.AccountState, 1)

	// Seed the channel while still holding the lock so a concurrent setState
	// cannot deliver a newer value and then have it overwritten by this one.
	a.mu.Lock()
	id := a.next
	a.next++
	a.subs[id] = ch
	sendLatest(ch, a.copyLocked())
	a.mu.Unlock()

	return ch, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if _, ok := a.subs[id]; ok {
			delete(a.subs, id)
			close(ch)
		}
	}
}

// sendLatest delivers st, replacing any undelivered value so a slow subscriber
// only ever sees the newest state.
func sendLatest(ch chan model.AccountState, st model.AccountState) {
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- st:
	default:
	}
}
