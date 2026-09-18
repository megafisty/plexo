package core_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"plexo/internal/config"
	"plexo/internal/core"
	"plexo/internal/model"
	"plexo/test/memstore"
)

// TestSettingsUnavailable: a manager built without a provider reports a clear
// error instead of panicking or silently succeeding.
func TestSettingsUnavailable(t *testing.T) {
	m := core.NewManager(context.Background(), core.Config{Store: memstore.New()})
	if _, err := m.Settings(context.Background(), "Vix"); !errors.Is(err, core.ErrSettingsUnavailable) {
		t.Fatalf("Settings err = %v, want ErrSettingsUnavailable", err)
	}
	if err := m.SetGlobalSettings(context.Background(), config.Global{}); !errors.Is(err, core.ErrSettingsUnavailable) {
		t.Fatalf("SetGlobalSettings err = %v, want ErrSettingsUnavailable", err)
	}
	if err := m.SetCharacterSettings(context.Background(), "Vix", config.Character{}); !errors.Is(err, core.ErrSettingsUnavailable) {
		t.Fatalf("SetCharacterSettings err = %v, want ErrSettingsUnavailable", err)
	}
}

// TestSettingsScopesAndValidation drives the settings API without a live
// session: both scopes, normalization, reset, name validation, and write
// validation.
func TestSettingsScopesAndValidation(t *testing.T) {
	h := newHarness(t, model.InterestFull)
	ctx := context.Background()

	view, err := h.mgr.Settings(ctx, "Vix")
	if err != nil {
		t.Fatal(err)
	}
	if view.HasGlobal || view.HasCharacter {
		t.Fatalf("fresh view = %+v, want both absent", view)
	}

	if err := h.mgr.SetGlobalSettings(ctx, config.Global{Password: "hunter2"}); err != nil {
		t.Fatal(err)
	}
	view, err = h.mgr.Settings(ctx, "Vix")
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasGlobal || view.Global.Password != "hunter2" {
		t.Fatalf("global = %+v (has %v)", view.Global, view.HasGlobal)
	}
	if view.HasCharacter {
		t.Fatal("character document should still be absent")
	}

	// Character settings are normalized, and the name defaults to the id.
	err = h.mgr.SetCharacterSettings(ctx, "Vix", config.Character{
		Highlights: []string{" Chart ", "chart", ""},
		AutoJoin: []config.JoinTarget{
			{Kind: model.ConvOfficial, ID: " Frontpage ", Name: " "},
			{Kind: model.ConvRoom, ID: " adh-1 ", Name: " The Tavern "},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err = h.mgr.Settings(ctx, "vix")
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasCharacter {
		t.Fatal("character document should be present")
	}
	if len(view.Character.Highlights) != 1 || view.Character.Highlights[0] != "Chart" {
		t.Fatalf("highlights = %v", view.Character.Highlights)
	}
	if len(view.Character.AutoJoin) != 2 {
		t.Fatalf("autoJoin = %+v", view.Character.AutoJoin)
	}
	if view.Character.AutoJoin[0].Name != "Frontpage" {
		t.Fatalf("official name should default to id: %+v", view.Character.AutoJoin[0])
	}
	if view.Character.AutoJoin[1].ID != "adh-1" || view.Character.AutoJoin[1].Name != "The Tavern" {
		t.Fatalf("room entry = %+v", view.Character.AutoJoin[1])
	}

	// Reset reverts each scope independently.
	if err := h.mgr.ResetCharacterSettings(ctx, "Vix"); err != nil {
		t.Fatal(err)
	}
	view, _ = h.mgr.Settings(ctx, "Vix")
	if view.HasCharacter || !view.HasGlobal {
		t.Fatalf("after character reset = %+v", view)
	}
	if err := h.mgr.ResetGlobalSettings(ctx); err != nil {
		t.Fatal(err)
	}
	view, _ = h.mgr.Settings(ctx, "Vix")
	if view.HasGlobal {
		t.Fatalf("after global reset = %+v", view)
	}

	// Invalid names and over-limit writes are rejected without touching state.
	for _, name := range []string{"", "!evil"} {
		if err := h.mgr.SetCharacterSettings(ctx, name, config.Character{}); err == nil {
			t.Fatalf("SetCharacterSettings(%q) succeeded, want error", name)
		}
	}
	if err := h.mgr.SetCharacterSettings(ctx, "Vix", config.Character{
		Highlights: []string{strings.Repeat("x", config.MaxHighlightLen+1)},
	}); err == nil {
		t.Fatal("expected validation error")
	}
	if err := h.mgr.SetGlobalSettings(ctx, config.Global{
		Password: strings.Repeat("x", config.MaxPasswordLen+1),
	}); err == nil {
		t.Fatal("expected password validation error")
	}
	view, _ = h.mgr.Settings(ctx, "Vix")
	if view.HasCharacter || view.HasGlobal {
		t.Fatalf("invalid writes mutated state: %+v", view)
	}
}

// TestResetGlobalWipesCredentials: resetting the global scope also deletes any
// stored F-Chat credentials.
func TestResetGlobalWipesCredentials(t *testing.T) {
	st := memstore.New()
	provider := config.NewProvider(st)
	ctx := context.Background()
	if err := provider.SaveCredentials(ctx, config.Credentials{Account: "acct", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	m := core.NewManager(ctx, core.Config{Store: st, Settings: provider})
	if err := m.ResetGlobalSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := provider.LoadCredentials(ctx); err != nil || ok {
		t.Fatalf("LoadCredentials after reset = ok %v, err %v; want absent", ok, err)
	}
}

// TestSettingsApplyToLiveSession: a character write through the settings API
// reaches the running session's highlighter without a reconnect.
func TestSettingsApplyToLiveSession(t *testing.T) {
	h := newHarness(t, model.InterestFull)
	ctx := context.Background()
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	h.sendChannel(t, "a secret here")
	if h.waitHighlight(t, "a secret here") {
		t.Fatal("message highlighted before any settings were written")
	}

	if err := h.mgr.SetCharacterSettings(ctx, char, config.Character{Highlights: []string{"secret"}}); err != nil {
		t.Fatal(err)
	}
	h.sendChannel(t, "another secret here")
	if !h.waitHighlight(t, "another secret here") {
		t.Fatal("settings write did not reach the live session")
	}

	// Resetting removes the character document and its highlights in place.
	if err := h.mgr.ResetCharacterSettings(ctx, char); err != nil {
		t.Fatal(err)
	}
	h.sendChannel(t, "yet another secret here")
	if h.waitHighlight(t, "yet another secret here") {
		t.Fatal("reset did not revert the live session")
	}
}
