package core

import (
	"context"

	"plexo/internal/model"
)

// AdsCampaign returns one character's advertisement campaign and its live
// scheduler status. The character need not be logged in; a `Running` false in
// the view means the status is not meaningful. A missing campaign is an empty
// (nil) one, not an error.
func (m *Manager) AdsCampaign(ctx context.Context, character string) (model.AdsCampaignView, error) {
	p, err := m.settingsProvider()
	if err != nil {
		return model.AdsCampaignView{}, err
	}
	campaign, _, err := p.Ads(ctx, character)
	if err != nil {
		return model.AdsCampaignView{}, err
	}
	view := model.AdsCampaignView{Campaign: campaign}
	if s, ok := m.Session(character); ok {
		view.Status = s.AdsStatus()
		view.Available = s.AdsAvailable()
		view.Running = true
	}
	return view, nil
}

// SetAdsCampaign replaces one character's advertisement campaign. The write is
// normalized and validated; a rejection leaves the stored document untouched.
// A running session for the character updates in place without a reconnect.
func (m *Manager) SetAdsCampaign(ctx context.Context, character string, campaign *model.AdCampaign) (model.AdsCampaignView, error) {
	p, err := m.settingsProvider()
	if err != nil {
		return model.AdsCampaignView{}, err
	}
	saved, err := p.SaveAds(ctx, character, campaign)
	if err != nil {
		return model.AdsCampaignView{}, err
	}
	view := model.AdsCampaignView{Campaign: saved}
	if s, ok := m.Session(character); ok {
		s.SetAdsCampaign(saved)
		view.Status = s.AdsStatus()
		view.Available = s.AdsAvailable()
		view.Running = true
	}
	return view, nil
}

// ResetAdsCampaign deletes one character's advertisement campaign, reverting it
// to disabled. The character need not be logged in. A running session's
// scheduler is cleared in place.
func (m *Manager) ResetAdsCampaign(ctx context.Context, character string) error {
	p, err := m.settingsProvider()
	if err != nil {
		return err
	}
	if err := p.ResetAds(ctx, character); err != nil {
		return err
	}
	if s, ok := m.Session(character); ok {
		s.SetAdsCampaign(nil)
	}
	return nil
}
