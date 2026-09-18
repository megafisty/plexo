package fchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultTicketURL is the F-List API ticket endpoint.
const DefaultTicketURL = "https://www.f-list.net/json/getApiTicket.php"

// DefaultTicketTTL is how long a minted ticket is reused. Tickets are valid
// for 30 minutes, so refresh a little early.
const DefaultTicketTTL = 25 * time.Minute

// ErrInvalidCredentials indicates the account/password pair was rejected. It
// is non-retryable: retrying the same credentials will not help.
var ErrInvalidCredentials = errors.New("fchat: invalid credentials")

// ErrNoCredentials indicates no password is known for the account. It is a
// configuration error, not a transient failure.
var ErrNoCredentials = errors.New("fchat: no credentials for account")

// Passwords resolves an account's password. It is consulted on each mint and
// must never log the password.
type Passwords func(account string) (string, bool)

// TicketMinter fetches and caches per-account F-Chat API tickets. Minting is
// serialized because a fresh ticket invalidates the previous one for that
// account.
type TicketMinter struct {
	Client    *http.Client
	URL       string
	Passwords Passwords
	TTL       time.Duration
	Now       func() time.Time

	mu    sync.Mutex
	cache map[string]Ticket
}

// NewTicketMinter returns a TicketMinter with defaults applied.
func NewTicketMinter(passwords Passwords) *TicketMinter {
	return &TicketMinter{
		Passwords: passwords,
		cache:     map[string]Ticket{},
	}
}

// Ticket returns a cached ticket for the account, minting a fresh one when the
// cache is empty or stale.
func (m *TicketMinter) Ticket(ctx context.Context, account string) (Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	if t, ok := m.cache[account]; ok && now.Sub(t.MintedAt) < m.ttl() {
		return t, nil
	}

	password, ok := m.passwords()(account)
	if !ok || password == "" {
		return Ticket{}, fmt.Errorf("%w: %s", ErrNoCredentials, account)
	}
	t, err := m.mint(ctx, account, password)
	if err != nil {
		return Ticket{}, err
	}
	t.MintedAt = now
	m.cache[account] = t
	return t, nil
}

// Invalidate drops a cached ticket, e.g. after an IDENT_FAILED.
func (m *TicketMinter) Invalidate(account string) {
	m.mu.Lock()
	delete(m.cache, account)
	m.mu.Unlock()
}

type ticketResponse struct {
	Ticket     string   `json:"ticket"`
	Error      string   `json:"error"`
	Characters []string `json:"characters"`
}

func (m *TicketMinter) mint(ctx context.Context, account, password string) (Ticket, error) {
	form := url.Values{
		"account":    {account},
		"password":   {password},
		"no_friends": {"true"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.url(), strings.NewReader(form.Encode()))
	if err != nil {
		return Ticket{}, fmt.Errorf("fchat: ticket request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.client().Do(req)
	if err != nil {
		return Ticket{}, fmt.Errorf("fchat: ticket request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Ticket{}, fmt.Errorf("fchat: read ticket response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Ticket{}, fmt.Errorf("fchat: ticket endpoint: HTTP %d", resp.StatusCode)
	}

	var out ticketResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return Ticket{}, fmt.Errorf("fchat: decode ticket response: %w", err)
	}
	if out.Error != "" {
		return Ticket{}, fmt.Errorf("%w: %s", ErrInvalidCredentials, out.Error)
	}
	if out.Ticket == "" {
		return Ticket{}, errors.New("fchat: ticket endpoint returned no ticket")
	}
	return Ticket{Value: out.Ticket, Characters: out.Characters}, nil
}

func (m *TicketMinter) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return http.DefaultClient
}

func (m *TicketMinter) url() string {
	if m.URL != "" {
		return m.URL
	}
	return DefaultTicketURL
}

func (m *TicketMinter) ttl() time.Duration {
	if m.TTL > 0 {
		return m.TTL
	}
	return DefaultTicketTTL
}

func (m *TicketMinter) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *TicketMinter) passwords() Passwords {
	if m.Passwords != nil {
		return m.Passwords
	}
	return func(string) (string, bool) { return "", false }
}
