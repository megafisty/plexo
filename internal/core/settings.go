package core

import (
	"context"
	"errors"

	"plexo/internal/config"
)

// ErrSettingsUnavailable is returned when the manager was built without a
// settings provider (and therefore has no configuration store).
var ErrSettingsUnavailable = errors.New("core: settings unavailable")

// SettingsView is the configuration visible to a settings UI: the raw global
// and character documents plus whether each exists. The two scopes are
// disjoint, so there is no merged/effective document.
type SettingsView struct {
	Global       config.Global    `json:"global"`
	Character    config.Character `json:"character"`
	HasGlobal    bool             `json:"hasGlobal"`
	HasCharacter bool             `json:"hasCharacter"`
}

// settingsProvider returns the configuration store, or ErrSettingsUnavailable
// when the manager was built without one. It is the single guard behind every
// settings read/write method.
func (m *Manager) settingsProvider() (*config.Provider, error) {
	if m.cfg.Settings == nil {
		return nil, ErrSettingsUnavailable
	}
	return m.cfg.Settings, nil
}

// Settings returns the global configuration and one character's configuration.
// It does not require the character to be logged in; an empty character simply
// has no character document.
func (m *Manager) Settings(ctx context.Context, character string) (SettingsView, error) {
	p, err := m.settingsProvider()
	if err != nil {
		return SettingsView{}, err
	}
	global, hasGlobal, err := p.Global(ctx)
	if err != nil {
		return SettingsView{}, err
	}
	characterCfg, hasCharacter, err := p.Character(ctx, character)
	if err != nil {
		return SettingsView{}, err
	}
	return SettingsView{
		Global:       global,
		Character:    characterCfg,
		HasGlobal:    hasGlobal,
		HasCharacter: hasCharacter,
	}, nil
}

// SetGlobalSettings replaces the account-wide configuration. The write is
// normalized and validated; a rejection leaves the stored document untouched.
func (m *Manager) SetGlobalSettings(ctx context.Context, g config.Global) error {
	p, err := m.settingsProvider()
	if err != nil {
		return err
	}
	if err := p.SaveGlobal(ctx, g); err != nil {
		return err
	}
	return m.ReloadConfig(ctx)
}

// SetCharacterSettings replaces one character's configuration. The character
// need not be logged in: requiring a live session is a UI guardrail, not a core
// rule, so settings can be prepared ahead of a login. A running session for the
// character updates in place.
func (m *Manager) SetCharacterSettings(ctx context.Context, character string, c config.Character) error {
	p, err := m.settingsProvider()
	if err != nil {
		return err
	}
	if err := p.SaveCharacter(ctx, character, c); err != nil {
		return err
	}
	return m.ReloadConfig(ctx)
}

// ResetGlobalSettings deletes the account-wide configuration including any
// stored F-Chat credentials, reverting to defaults.
func (m *Manager) ResetGlobalSettings(ctx context.Context) error {
	p, err := m.settingsProvider()
	if err != nil {
		return err
	}
	if err := p.ResetGlobal(ctx); err != nil {
		return err
	}
	if m.cfg.Credentials != nil {
		if err := m.cfg.Credentials.PurgeStoredCredentials(ctx); err != nil {
			return err
		}
	} else if err := p.DeleteCredentials(ctx); err != nil {
		return err
	}
	return m.ReloadConfig(ctx)
}

// ResetCharacterSettings deletes one character's configuration, reverting it to
// defaults. The character need not be logged in.
func (m *Manager) ResetCharacterSettings(ctx context.Context, character string) error {
	p, err := m.settingsProvider()
	if err != nil {
		return err
	}
	if err := p.ResetCharacter(ctx, character); err != nil {
		return err
	}
	return m.ReloadConfig(ctx)
}
