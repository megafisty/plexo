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
)

// DefaultAPIBaseURL is the F-List JSON API base. Bookmark endpoints hang off
// it; the ticket endpoint is separate (see TicketMinter).
const DefaultAPIBaseURL = "https://www.f-list.net/json/api/"

// The two ticket-rejection messages the API returns. The official client keys
// its single retry on exactly these strings, so we do too.
const (
	msgInvalidTicket = "Invalid ticket."
	msgExpiredTicket = "Your login ticket has expired (five minutes) or no ticket requested."
)

// errInvalidTicket signals that the cached ticket must be dropped and the call
// retried once with a fresh one.
var errInvalidTicket = errors.New("fchat: invalid ticket")

// maxAPIBody bounds a JSON API response. The friend/bookmark list is small;
// this leaves generous headroom.
const maxAPIBody = 1 << 20

// AccountAPI is the F-List account REST surface Plexo needs beyond chat: the
// friend/bookmark split and bookmark mutations. It reuses the shared, cached
// account ticket; a rejected ticket is invalidated and the call retried once,
// matching the official client. Only this package names the endpoints.
type AccountAPI struct {
	Tickets     TicketManager
	Invalidator TicketInvalidator
	Client      *http.Client
	BaseURL     string
}

// Friend is one entry of the friend-list response. The friend is Dest; Source
// is one of the account's own characters.
type Friend struct {
	Source     string `json:"source"`
	Dest       string `json:"dest"`
	LastOnline int64  `json:"last_online"`
}

type friendBookmarkLists struct {
	Friends    []Friend `json:"friendlist"`
	Bookmarks  []string `json:"bookmarklist"`
	ErrorField string   `json:"error"`
}

// FriendBookmarkLists fetches both lists in one call (the documented combined
// endpoint). Friends are the Dest names, de-duplicated case-insensitively;
// bookmarks are returned as sent.
func (a AccountAPI) FriendBookmarkLists(ctx context.Context, account string) (friends, bookmarks []string, err error) {
	form := url.Values{"friendlist": {"true"}, "bookmarklist": {"true"}}
	body, err := a.request(ctx, account, "friend-bookmark-lists.php", form)
	if err != nil {
		return nil, nil, err
	}
	return DecodeFriendBookmarkLists(body)
}

// DecodeFriendBookmarkLists parses a friend-bookmark-lists response. Friends
// are the Dest names, de-duplicated case-insensitively; a non-empty error field
// is returned as the API's message.
func DecodeFriendBookmarkLists(data []byte) (friends, bookmarks []string, err error) {
	var out friendBookmarkLists
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, nil, fmt.Errorf("fchat: decode friend/bookmark lists: %w", err)
	}
	if out.ErrorField != "" {
		return nil, nil, apiError{out.ErrorField}
	}

	seen := make(map[string]bool, len(out.Friends))
	friends = make([]string, 0, len(out.Friends))
	for _, f := range out.Friends {
		if f.Dest == "" || seen[strings.ToLower(f.Dest)] {
			continue
		}
		seen[strings.ToLower(f.Dest)] = true
		friends = append(friends, f.Dest)
	}
	return friends, out.Bookmarks, nil
}

// AddBookmark bookmarks the named character for the account.
func (a AccountAPI) AddBookmark(ctx context.Context, account, name string) error {
	return a.mutate(ctx, account, "bookmark-add.php", name)
}

// RemoveBookmark removes the named character from the account's bookmarks.
func (a AccountAPI) RemoveBookmark(ctx context.Context, account, name string) error {
	return a.mutate(ctx, account, "bookmark-remove.php", name)
}

func (a AccountAPI) mutate(ctx context.Context, account, endpoint, name string) error {
	_, err := a.request(ctx, account, endpoint, url.Values{"name": {name}})
	return err
}

// request mints or reuses the account ticket, POSTs the form, and retries once
// with a fresh ticket when the API rejects the cached one. A non-empty error
// field is returned verbatim as the API's message.
func (a AccountAPI) request(ctx context.Context, account, endpoint string, extra url.Values) ([]byte, error) {
	ticket, err := a.tickets().Ticket(ctx, account)
	if err != nil {
		return nil, err
	}
	body, err := a.post(ctx, endpoint, account, ticket.Value, extra)
	if errors.Is(err, errInvalidTicket) {
		if inv := a.invalidator(); inv != nil {
			inv.Invalidate(account)
		}
		ticket, err = a.tickets().Ticket(ctx, account)
		if err != nil {
			return nil, err
		}
		return a.post(ctx, endpoint, account, ticket.Value, extra)
	}
	return body, err
}

// post sends one form request and decodes the shared {error} envelope.
func (a AccountAPI) post(ctx context.Context, endpoint, account, ticket string, extra url.Values) ([]byte, error) {
	form := url.Values{}
	for k, vs := range extra {
		form[k] = vs
	}
	form.Set("account", account)
	form.Set("ticket", ticket)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpointURL(endpoint), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("fchat: %s request: %w", endpoint, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fchat: %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fchat: %s: HTTP %d", endpoint, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBody))
	if err != nil {
		return nil, fmt.Errorf("fchat: %s read: %w", endpoint, err)
	}

	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("fchat: %s decode: %w", endpoint, err)
	}
	switch envelope.Error {
	case "":
		return body, nil
	case msgInvalidTicket, msgExpiredTicket:
		return body, errInvalidTicket
	default:
		return body, apiError{envelope.Error}
	}
}

func (a AccountAPI) endpointURL(endpoint string) string {
	return strings.TrimSuffix(a.base(), "/") + "/" + endpoint
}

func (a AccountAPI) base() string {
	if a.BaseURL != "" {
		return a.BaseURL
	}
	return DefaultAPIBaseURL
}

func (a AccountAPI) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return http.DefaultClient
}

func (a AccountAPI) tickets() TicketManager {
	if a.Tickets != nil {
		return a.Tickets
	}
	return TicketFunc(func(context.Context, string) (Ticket, error) {
		return Ticket{}, ErrNoCredentials
	})
}

func (a AccountAPI) invalidator() TicketInvalidator {
	if a.Invalidator != nil {
		return a.Invalidator
	}
	if inv, ok := a.Tickets.(TicketInvalidator); ok {
		return inv
	}
	return nil
}

// apiError is an error reported by the F-List API. Its message is the API's
// own user-facing text (for example "You already have this character
// bookmarked.") and is surfaced unchanged to the client.
type apiError struct{ msg string }

func (e apiError) Error() string { return e.msg }
