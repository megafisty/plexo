package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	"plexo/internal/model"
	"plexo/internal/store"
)

// fakeStore is an in-memory config.Store keyed by document name.
type fakeStore struct {
	docs map[string][]byte
	err  error
}

func newFakeStore() *fakeStore { return &fakeStore{docs: map[string][]byte{}} }

func (f *fakeStore) ConfigGet(_ context.Context, name string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	d, ok := f.docs[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return d, nil
}

func (f *fakeStore) ConfigPut(_ context.Context, name string, data []byte) error {
	f.docs[name] = append([]byte(nil), data...)
	return nil
}

func (f *fakeStore) ConfigDelete(_ context.Context, name string) error {
	delete(f.docs, name)
	return nil
}

// TestGlobalRoundTrip: save, read back, and reset the global document.
func TestGlobalRoundTrip(t *testing.T) {
	p := NewProvider(newFakeStore())
	ctx := context.Background()

	if _, present, err := p.Global(ctx); err != nil || present {
		t.Fatalf("fresh Global = present %v, err %v", present, err)
	}
	if err := p.SaveGlobal(ctx, Global{Password: "hunter2"}); err != nil {
		t.Fatal(err)
	}
	g, present, err := p.Global(ctx)
	if err != nil || !present {
		t.Fatalf("Global present = %v, err %v", present, err)
	}
	if g.Password != "hunter2" {
		t.Fatalf("Global = %+v", g)
	}
	if err := p.ResetGlobal(ctx); err != nil {
		t.Fatal(err)
	}
	if _, present, err := p.Global(ctx); err != nil || present {
		t.Fatalf("after reset present = %v, err %v", present, err)
	}
}

// TestCharacterRoundTripAndCasing: character documents are addressed
// case-insensitively.
func TestCharacterRoundTripAndCasing(t *testing.T) {
	p := NewProvider(newFakeStore())
	ctx := context.Background()

	if err := p.SaveCharacter(ctx, "ViX", Character{Highlights: []string{"Kira"}}); err != nil {
		t.Fatal(err)
	}
	c, present, err := p.Character(ctx, "vix")
	if err != nil || !present {
		t.Fatalf("Character present = %v, err %v", present, err)
	}
	if len(c.Highlights) != 1 || c.Highlights[0] != "Kira" {
		t.Fatalf("Character = %+v", c)
	}
	if err := p.ResetCharacter(ctx, "VIX"); err != nil {
		t.Fatal(err)
	}
	if _, present, err := p.Character(ctx, "vix"); err != nil || present {
		t.Fatalf("after reset present = %v, err %v", present, err)
	}
}

// TestCharacterNormalize: highlights and auto-join are canonicalized.
func TestCharacterNormalize(t *testing.T) {
	got := Character{
		Highlights: []string{" Kira ", "", "kira", "  ", "Secret"},
		AutoJoin: []JoinTarget{
			{Kind: model.ConvOfficial, ID: " Frontpage ", Name: " "},
			{Kind: model.ConvOfficial, ID: "frontpage"}, // duplicate by kind+id
			{Kind: model.ConvRoom, ID: "adh-abc", Name: "The Tavern"},
			{Kind: model.ConvRoom, ID: "  "}, // dropped
		},
	}.Normalize()
	if len(got.Highlights) != 2 || got.Highlights[0] != "Kira" || got.Highlights[1] != "Secret" {
		t.Fatalf("highlights = %v", got.Highlights)
	}
	if len(got.AutoJoin) != 2 {
		t.Fatalf("autoJoin = %+v", got.AutoJoin)
	}
	if got.AutoJoin[0].ID != "Frontpage" || got.AutoJoin[0].Name != "Frontpage" {
		t.Fatalf("name should default to id: %+v", got.AutoJoin[0])
	}
	if got.AutoJoin[1].ID != "adh-abc" || got.AutoJoin[1].Name != "The Tavern" {
		t.Fatalf("room entry = %+v", got.AutoJoin[1])
	}
	if (Character{}).Normalize().Highlights != nil {
		t.Fatal("nil highlights must stay nil")
	}
}

// TestAutoStatusNormalizeValidate: the automatic status is lowercased and
// trimmed, a blank status drops to nil, and only selectable statuses are
// accepted.
func TestAutoStatusNormalizeValidate(t *testing.T) {
	got := (Character{AutoStatus: &AutoStatus{Status: "  Away ", Message: "  brb [b]soon[/b] "}}).Normalize()
	if got.AutoStatus == nil || got.AutoStatus.Status != "away" || got.AutoStatus.Message != "brb [b]soon[/b]" {
		t.Fatalf("autoStatus = %+v", got.AutoStatus)
	}
	if (Character{AutoStatus: &AutoStatus{Status: "  "}}).Normalize().AutoStatus != nil {
		t.Fatal("a blank auto status must normalize to nil")
	}
	if err := (Character{AutoStatus: &AutoStatus{Status: "dnd", Message: "busy"}}).Validate(); err != nil {
		t.Fatalf("valid auto status rejected: %v", err)
	}
	for name, c := range map[string]Character{
		"crown":            {AutoStatus: &AutoStatus{Status: "crown"}},
		"unknown":          {AutoStatus: &AutoStatus{Status: "bogus"}},
		"message too long": {AutoStatus: &AutoStatus{Status: "online", Message: strings.Repeat("x", MaxStatusMsgLen+1)}},
	} {
		if err := c.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: Validate err = %v, want ErrInvalid", name, err)
		}
	}
}

// TestCharacterValidate: the caps and kind check are enforced.
func TestCharacterValidate(t *testing.T) {
	tooMany := make([]string, MaxHighlights+1)
	for i := range tooMany {
		tooMany[i] = "x"
	}
	cases := map[string]Character{
		"too many highlights": {Highlights: tooMany},
		"highlight too long":  {Highlights: []string{strings.Repeat("x", MaxHighlightLen+1)}},
		"too many joins":      {AutoJoin: make([]JoinTarget, MaxAutoJoin+1)},
		"bad kind":            {AutoJoin: []JoinTarget{{Kind: model.ConvDM, ID: "x"}}},
		"empty id":            {AutoJoin: []JoinTarget{{Kind: model.ConvOfficial}}},
		"id too long":         {AutoJoin: []JoinTarget{{Kind: model.ConvOfficial, ID: strings.Repeat("x", MaxJoinIDLen+1)}}},
		"name too long":       {AutoJoin: []JoinTarget{{Kind: model.ConvRoom, ID: "x", Name: strings.Repeat("x", MaxJoinNameLen+1)}}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := c.Validate(); err == nil {
				t.Fatalf("Validate(%+v) succeeded, want error", c)
			}
		})
	}
	if err := (Character{Highlights: []string{"ok"}, AutoJoin: []JoinTarget{{Kind: model.ConvRoom, ID: "adh-x"}}}).Validate(); err != nil {
		t.Fatalf("valid character rejected: %v", err)
	}
}

// TestGlobalValidate: the password cap is enforced.
func TestGlobalValidate(t *testing.T) {
	if err := (Global{Password: strings.Repeat("x", MaxPasswordLen+1)}).Validate(); err == nil {
		t.Fatal("expected password-too-long error")
	}
	if err := (Global{Password: "ok"}).Validate(); err != nil {
		t.Fatalf("valid global rejected: %v", err)
	}
}

// TestReservedAndEmptyCharacter: character documents must use a real name;
// empty and '!'-prefixed names are rejected so they cannot collide with the
// reserved global key.
func TestReservedAndEmptyCharacter(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"", "   ", "!evil"} {
		if err := NewProvider(newFakeStore()).SaveCharacter(ctx, name, Character{}); err == nil {
			t.Fatalf("SaveCharacter(%q) succeeded, want error", name)
		}
		if err := NewProvider(newFakeStore()).ResetCharacter(ctx, name); err == nil {
			t.Fatalf("ResetCharacter(%q) succeeded, want error", name)
		}
	}
	// A read of an empty/reserved name simply has no document.
	if _, present, err := NewProvider(newFakeStore()).Character(ctx, ""); err != nil || present {
		t.Fatalf("Character(\"\") = present %v, err %v", present, err)
	}
}

// TestMalformedDocument: a present but broken document fails loudly rather than
// silently discarding settings.
func TestMalformedDocument(t *testing.T) {
	f := newFakeStore()
	f.docs[GlobalKey] = []byte("{")
	if _, _, err := NewProvider(f).Global(context.Background()); err == nil {
		t.Fatal("expected parse error")
	}
}

// TestInvalidErrorsAreTyped: rejected client input is wrapped in ErrInvalid so
// the web layer can map it to 400, while a malformed stored document is not.
func TestInvalidErrorsAreTyped(t *testing.T) {
	if err := (Global{Password: strings.Repeat("x", MaxPasswordLen+1)}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("global validation err = %v, want ErrInvalid", err)
	}
	if err := (Character{AutoJoin: []JoinTarget{{Kind: model.ConvDM, ID: "x"}}}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("character validation err = %v, want ErrInvalid", err)
	}
	if _, err := characterKey("!bad"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("characterKey err = %v, want ErrInvalid", err)
	}
	f := newFakeStore()
	f.docs[GlobalKey] = []byte("{")
	if _, _, err := NewProvider(f).Global(context.Background()); errors.Is(err, ErrInvalid) {
		t.Fatal("malformed stored document must not be classified as invalid input")
	}
}

// TestNilProvider: a manager without a provider must not panic.
func TestNilProvider(t *testing.T) {
	var p *Provider
	if _, present, err := p.Global(context.Background()); err != nil || present {
		t.Fatalf("nil Global = present %v, err %v", present, err)
	}
	if _, present, err := p.Character(context.Background(), "Vix"); err != nil || present {
		t.Fatalf("nil Character = present %v, err %v", present, err)
	}
}

// TestWriteNoStore: writes without a store fail rather than panicking.
func TestWriteNoStore(t *testing.T) {
	p := NewProvider(nil)
	if err := p.SaveGlobal(context.Background(), Global{}); err == nil {
		t.Fatal("expected error")
	}
	if err := p.ResetGlobal(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// TestSaveRejectsInvalid: an over-limit write must not touch the store.
func TestSaveRejectsInvalid(t *testing.T) {
	f := newFakeStore()
	err := NewProvider(f).SaveCharacter(context.Background(), "Vix", Character{
		Highlights: []string{strings.Repeat("x", MaxHighlightLen+1)},
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if len(f.docs) != 0 {
		t.Fatalf("store mutated on invalid write: %v", f.docs)
	}
}

// TestReadError: a store read error propagates.
func TestReadError(t *testing.T) {
	f := &fakeStore{docs: map[string][]byte{}, err: errors.New("boom")}
	if _, _, err := NewProvider(f).Global(context.Background()); err == nil {
		t.Fatal("expected read error")
	}
}

// TestCredentialsRoundTrip: save, read back, and delete the F-Chat credential
// document, which lives under its own reserved key.
func TestCredentialsRoundTrip(t *testing.T) {
	f := newFakeStore()
	p := NewProvider(f)
	ctx := context.Background()

	if _, present, err := p.LoadCredentials(ctx); err != nil || present {
		t.Fatalf("fresh LoadCredentials = present %v, err %v", present, err)
	}
	if err := p.SaveCredentials(ctx, Credentials{Account: " acct ", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	// The account is normalized (trimmed); the password is not.
	c, present, err := p.LoadCredentials(ctx)
	if err != nil || !present {
		t.Fatalf("LoadCredentials present = %v, err %v", present, err)
	}
	if c.Account != "acct" || c.Password != "pw" {
		t.Fatalf("Credentials = %+v", c)
	}
	if _, ok := f.docs[CredentialsKey]; !ok {
		t.Fatalf("not stored under %q", CredentialsKey)
	}
	if err := p.DeleteCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	if _, present, err := p.LoadCredentials(ctx); err != nil || present {
		t.Fatalf("after delete present = %v, err %v", present, err)
	}
}

// TestCredentialsRejectsInvalid: empty or oversized fields are rejected and the
// store is untouched.
func TestCredentialsRejectsInvalid(t *testing.T) {
	cases := []Credentials{
		{Account: "", Password: "pw"},
		{Account: "acct", Password: ""},
		{Account: strings.Repeat("x", MaxCredentialAccountLen+1), Password: "pw"},
		{Account: "acct", Password: strings.Repeat("x", MaxCredentialPasswordLen+1)},
	}
	for _, c := range cases {
		f := newFakeStore()
		if err := NewProvider(f).SaveCredentials(context.Background(), c); err == nil {
			t.Fatalf("SaveCredentials(%+v) succeeded, want error", c)
		}
		if len(f.docs) != 0 {
			t.Fatalf("store mutated on invalid write: %v", f.docs)
		}
	}
}
