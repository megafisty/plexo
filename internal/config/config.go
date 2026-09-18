// Package config resolves and stores Plexo's configuration. Each scope is one
// JSON document in the store, keyed by name: the global (account-wide) document
// lives under a reserved key and each character has its own document under its
// lowercased name.
package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"plexo/internal/model"
	"plexo/internal/store"
)

// GlobalKey is the reserved storage key holding the account-wide configuration.
// All keys beginning with '!' are reserved; character documents use the
// lowercased character name and may not begin with '!'.
const GlobalKey = "!global"

// CredentialsKey is the reserved storage key holding the F-Chat account and
// password. It is deliberately separate from GlobalKey: it never appears in
// the settings view, and the settings UI never round-trips it.
const CredentialsKey = "!credentials"

// Limits bound user-supplied lists so a settings write cannot grow a document
// without bound. They are exported for the web layer's own messaging.
const (
	MaxHighlights   = 100
	MaxHighlightLen = 128
	MaxAutoJoin     = 50
	MaxJoinIDLen    = 128
	MaxJoinNameLen  = 128
	MaxPasswordLen  = 256

	MaxStatusMsgLen = 512

	MaxCredentialAccountLen  = 256
	MaxCredentialPasswordLen = 256
)

// ErrInvalid marks client input rejected by settings validation: an over-limit
// field, an unknown auto-join kind, or a bad character name. The web layer maps
// it to 400; every other config error is server-side.
var ErrInvalid = errors.New("config: invalid settings")

// Global is the account-wide configuration, stored under GlobalKey. It holds
// only settings that are not tied to one character.
type Global struct {
	// Password is the shared password that guards browser access. Empty
	// disables the gate. The settings API returns it in cleartext: Plexo
	// assumes a trusted LAN where the operator owns every machine.
	Password string `json:"password,omitempty"`
}

// Credentials is the F-Chat account and password, stored under CredentialsKey.
// It is written only after F-List accepts a mint, and it is stored
// unencrypted: anyone with direct access to the database can read it. It is
// never returned by the settings API.
type Credentials struct {
	Account  string `json:"account"`
	Password string `json:"password"`
}

// Normalize returns a canonical copy of c. The receiver is not mutated. The
// password is not trimmed; only the account name is.
func (c Credentials) Normalize() Credentials {
	c.Account = strings.TrimSpace(c.Account)
	return c
}

// Validate rejects an empty or oversized account or password.
func (c Credentials) Validate() error {
	if c.Account == "" {
		return fmt.Errorf("%w: F-Chat account is required", ErrInvalid)
	}
	if len(c.Account) > MaxCredentialAccountLen {
		return fmt.Errorf("%w: F-Chat account exceeds %d characters", ErrInvalid, MaxCredentialAccountLen)
	}
	if c.Password == "" {
		return fmt.Errorf("%w: F-Chat password is required", ErrInvalid)
	}
	if len(c.Password) > MaxCredentialPasswordLen {
		return fmt.Errorf("%w: F-Chat password exceeds %d characters", ErrInvalid, MaxCredentialPasswordLen)
	}
	return nil
}

// Character is one character's configuration, stored under its lowercased name.
type Character struct {
	// Highlights is the set of substrings that elevate an incoming channel
	// message to a highlight. Matching is case-insensitive.
	Highlights []string `json:"highlights,omitempty"`
	// AutoJoin lists channels and rooms to try to join after login.
	AutoJoin []JoinTarget `json:"autoJoin,omitempty"`
	// AutoStatus, when non-nil, is applied after login and on every reconnect.
	AutoStatus *AutoStatus `json:"autoStatus,omitempty"`
}

// AutoStatus is a status the core applies after login: an STA carrying the raw
// message is sent once the session becomes ready, and again on every reconnect,
// so the character returns with the same status. Message is BBCode and travels
// unchanged. A nil *AutoStatus means the character has no automatic status.
type AutoStatus struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// selectableStatuses is the set of statuses a character may set. It mirrors the
// client's STATUS_OPTIONS; "crown" is moderator-granted and deliberately
// excluded, so an automatic status can never emit it.
var selectableStatuses = map[string]struct{}{
	"online":  {},
	"looking": {},
	"away":    {},
	"busy":    {},
	"dnd":     {},
	"idle":    {},
}

// Normalize returns a canonical copy: the status is trimmed and lowercased and
// the message is trimmed. A blank status normalizes to the zero value, which
// Character.Normalize drops so "no automatic status" round-trips.
func (a AutoStatus) Normalize() AutoStatus {
	a.Status = strings.ToLower(strings.TrimSpace(a.Status))
	a.Message = strings.TrimSpace(a.Message)
	return a
}

// Validate rejects a status the character may not set and an over-long message.
// It assumes a normalized value.
func (a AutoStatus) Validate() error {
	if _, ok := selectableStatuses[a.Status]; !ok {
		return fmt.Errorf("%w: invalid automatic status %q", ErrInvalid, a.Status)
	}
	if len(a.Message) > MaxStatusMsgLen {
		return fmt.Errorf("%w: automatic status message exceeds %d characters", ErrInvalid, MaxStatusMsgLen)
	}
	return nil
}

// JoinTarget is one auto-join entry. ID is the joinable identifier sent to
// F-Chat (an official channel name or a room hash); Name is the human-readable
// label the UI can show before any ORS/JCH data arrives (for official channels
// it defaults to ID).
type JoinTarget struct {
	Kind model.ConvKind `json:"kind"`
	ID   string         `json:"id"`
	Name string         `json:"name,omitempty"`
}

// Normalize returns a canonical copy of g. The receiver is not mutated.
func (g Global) Normalize() Global { return g }

// Validate rejects a password beyond the documented cap.
func (g Global) Validate() error {
	if len(g.Password) > MaxPasswordLen {
		return fmt.Errorf("%w: password exceeds %d characters", ErrInvalid, MaxPasswordLen)
	}
	return nil
}

// Normalize returns a canonical copy of c: highlight entries are trimmed, empty
// entries dropped, and case-insensitive duplicates removed (first spelling
// wins); auto-join entries are trimmed, empty IDs dropped, names defaulted to
// the ID, and duplicates removed by (kind, ID); the automatic status is trimmed
// and lowercased, and a blank status drops the field. The receiver is not
// mutated.
func (c Character) Normalize() Character {
	var out Character
	if c.Highlights != nil {
		out.Highlights = make([]string, 0, len(c.Highlights))
		seen := make(map[string]struct{}, len(c.Highlights))
		for _, h := range c.Highlights {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			key := strings.ToLower(h)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out.Highlights = append(out.Highlights, h)
		}
	}
	if c.AutoJoin != nil {
		out.AutoJoin = make([]JoinTarget, 0, len(c.AutoJoin))
		seen := make(map[string]struct{}, len(c.AutoJoin))
		for _, j := range c.AutoJoin {
			id := strings.TrimSpace(j.ID)
			if id == "" {
				continue
			}
			name := strings.TrimSpace(j.Name)
			if name == "" {
				name = id
			}
			key := strings.ToLower(string(j.Kind)) + "\x00" + strings.ToLower(id)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out.AutoJoin = append(out.AutoJoin, JoinTarget{Kind: j.Kind, ID: id, Name: name})
		}
	}
	if c.AutoStatus != nil {
		a := c.AutoStatus.Normalize()
		if a.Status != "" {
			out.AutoStatus = &a
		}
	}
	return out
}

// Validate rejects a (normalized) character config that exceeds the documented
// limits, names an unknown conversation kind, or names a non-selectable
// automatic status.
func (c Character) Validate() error {
	if len(c.Highlights) > MaxHighlights {
		return fmt.Errorf("%w: too many highlights (%d, max %d)", ErrInvalid, len(c.Highlights), MaxHighlights)
	}
	for _, h := range c.Highlights {
		if len(h) > MaxHighlightLen {
			return fmt.Errorf("%w: highlight %q exceeds %d characters", ErrInvalid, h, MaxHighlightLen)
		}
	}
	if len(c.AutoJoin) > MaxAutoJoin {
		return fmt.Errorf("%w: too many auto-join entries (%d, max %d)", ErrInvalid, len(c.AutoJoin), MaxAutoJoin)
	}
	for _, j := range c.AutoJoin {
		switch j.Kind {
		case model.ConvOfficial, model.ConvRoom:
		default:
			return fmt.Errorf("%w: auto-join %q has invalid kind %q", ErrInvalid, j.ID, j.Kind)
		}
		if j.ID == "" {
			return fmt.Errorf("%w: auto-join entry has an empty id", ErrInvalid)
		}
		if len(j.ID) > MaxJoinIDLen {
			return fmt.Errorf("%w: auto-join id %q exceeds %d characters", ErrInvalid, j.ID, MaxJoinIDLen)
		}
		if len(j.Name) > MaxJoinNameLen {
			return fmt.Errorf("%w: auto-join name %q exceeds %d characters", ErrInvalid, j.Name, MaxJoinNameLen)
		}
	}
	if c.AutoStatus != nil {
		if err := c.AutoStatus.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Store is the persistence port the provider needs. The SQLite and in-memory
// stores both satisfy it. Keys are opaque to the store.
type Store interface {
	ConfigGet(ctx context.Context, name string) ([]byte, error)
	ConfigPut(ctx context.Context, name string, data []byte) error
	ConfigDelete(ctx context.Context, name string) error
}

// Provider resolves and persists configuration through a Store.
type Provider struct{ s Store }

// NewProvider wraps a Store. The store may be nil, in which case every lookup
// resolves to the zero document and writes fail.
func NewProvider(s Store) *Provider { return &Provider{s: s} }

// Global returns the account-wide configuration and whether a document exists.
// A missing document is not an error: it is the zero Global.
func (p *Provider) Global(ctx context.Context) (Global, bool, error) {
	return get[Global](p, ctx, GlobalKey)
}

// SaveGlobal normalizes and validates g, then upserts it.
func (p *Provider) SaveGlobal(ctx context.Context, g Global) error {
	g = g.Normalize()
	if err := g.Validate(); err != nil {
		return err
	}
	return put(p, ctx, GlobalKey, g)
}

// ResetGlobal deletes the account-wide document, reverting to defaults.
func (p *Provider) ResetGlobal(ctx context.Context) error {
	return del(p, ctx, GlobalKey)
}

// LoadCredentials returns the stored F-Chat credentials and whether a document
// exists. A missing document is not an error.
func (p *Provider) LoadCredentials(ctx context.Context) (Credentials, bool, error) {
	return get[Credentials](p, ctx, CredentialsKey)
}

// SaveCredentials normalizes and validates c, then upserts it. The password is
// persisted unencrypted, as documented on Credentials.
func (p *Provider) SaveCredentials(ctx context.Context, c Credentials) error {
	c = c.Normalize()
	if err := c.Validate(); err != nil {
		return err
	}
	return put(p, ctx, CredentialsKey, c)
}

// DeleteCredentials removes the stored F-Chat credentials. Deleting a missing
// document is a no-op.
func (p *Provider) DeleteCredentials(ctx context.Context) error {
	return del(p, ctx, CredentialsKey)
}

// Character returns one character's configuration and whether a document
// exists. An empty or reserved name simply has no document.
func (p *Provider) Character(ctx context.Context, character string) (Character, bool, error) {
	key, err := characterKey(character)
	if err != nil {
		return Character{}, false, nil
	}
	return get[Character](p, ctx, key)
}

// SaveCharacter normalizes and validates c, then upserts it under the
// character's lowercased name. The name must be non-empty and not reserved.
func (p *Provider) SaveCharacter(ctx context.Context, character string, c Character) error {
	key, err := characterKey(character)
	if err != nil {
		return err
	}
	c = c.Normalize()
	if err := c.Validate(); err != nil {
		return err
	}
	return put(p, ctx, key, c)
}

// ResetCharacter deletes one character's document, reverting it to defaults.
// The name must be non-empty and not reserved.
func (p *Provider) ResetCharacter(ctx context.Context, character string) error {
	key, err := characterKey(character)
	if err != nil {
		return err
	}
	return del(p, ctx, key)
}

// get loads one JSON document. A missing document is (zero, false, nil); a
// present but malformed document is an error, so a bad write fails loudly
// rather than silently discarding settings.
func get[T any](p *Provider, ctx context.Context, key string) (T, bool, error) {
	var zero T
	if p == nil || p.s == nil {
		return zero, false, nil
	}
	data, err := p.s.ConfigGet(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, fmt.Errorf("config: read %s: %w", key, err)
	}
	var v T
	if len(data) > 0 {
		if err := json.Unmarshal(data, &v); err != nil {
			return zero, false, fmt.Errorf("config: parse %s: %w", key, err)
		}
	}
	return v, true, nil
}

func put[T any](p *Provider, ctx context.Context, key string, v T) error {
	if p == nil || p.s == nil {
		return errors.New("config: no store")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("config: encode %s: %w", key, err)
	}
	if err := p.s.ConfigPut(ctx, key, data); err != nil {
		return fmt.Errorf("config: write %s: %w", key, err)
	}
	return nil
}

func del(p *Provider, ctx context.Context, key string) error {
	if p == nil || p.s == nil {
		return errors.New("config: no store")
	}
	if err := p.s.ConfigDelete(ctx, key); err != nil {
		return fmt.Errorf("config: reset %s: %w", key, err)
	}
	return nil
}

// characterKey folds a character name for storage. Character identity is
// case-insensitive, and a '!'-prefixed key is reserved, so the two can never
// collide.
func characterKey(character string) (string, error) {
	c := strings.ToLower(strings.TrimSpace(character))
	if c == "" {
		return "", fmt.Errorf("%w: character name is required", ErrInvalid)
	}
	if c[0] == '!' {
		return "", fmt.Errorf("%w: character name %q must not begin with '!'", ErrInvalid, character)
	}
	return c, nil
}
