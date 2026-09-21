package session

import (
	"context"
	"fmt"
	"testing"
	"time"

	"plexo/internal/fchat"
	"plexo/internal/model"
)

// adTestCampaign builds a one-body campaign targeting one channel.
func adTestCampaign(channel, body string) *model.AdCampaign {
	return &model.AdCampaign{
		Enabled:  true,
		Ads:      []model.AdBody{{Name: "intro", Body: body}},
		Channels: []model.AdChannel{{Kind: model.ConvOfficial, ID: channel, Ads: []string{"intro"}}},
	}
}

// newAdTestSession creates a session with a live writer queue and a context so
// queued frames do not race a closed done channel.
func newAdTestSession(t *testing.T, campaign *model.AdCampaign) *Session {
	t.Helper()
	s := New(Config{Character: "Vix", Ads: campaign})
	s.out = make(chan outbound, 8)
	s.st.phase = "ready"
	s.mu.Lock()
	s.ctx = context.Background()
	s.mu.Unlock()
	t.Cleanup(s.adStop)
	return s
}

// adJoin sends a self JCH for a channel with the given mode.
func adJoin(t *testing.T, s *Session, channel, mode string) {
	t.Helper()
	body := fmt.Sprintf(`{"channel":%q,"character":{"identity":"Vix"},"mode":%q}`, channel, mode)
	if err := s.handle(jsonFrame("JCH", body)); err != nil {
		t.Fatalf("JCH %s: %v", channel, err)
	}
}

// adNextFrame returns the next written frame, or false when none is queued.
func adNextFrame(s *Session) (fchat.Frame, bool) {
	select {
	case ob := <-s.out:
		return ob.wire, true
	default:
		return fchat.Frame{}, false
	}
}

// TestAdSchedulerPostsAndCoolsDown: a confirmed, ads-allowed channel is posted
// to once, and the per-channel cooldown suppresses an immediate second post.
func TestAdSchedulerPostsAndCoolsDown(t *testing.T) {
	s := newAdTestSession(t, adTestCampaign("Frontpage", "looking for rp"))
	adJoin(t, s, "Frontpage", "both")

	s.adTick()
	f, ok := adNextFrame(s)
	if !ok {
		t.Fatal("no LRP queued")
	}
	if f.Code != "LRP" {
		t.Fatalf("frame = %s, want LRP", f.Code)
	}
	p, err := fchat.Decode[fchat.ChannelMsg](f)
	if err != nil {
		t.Fatalf("decode LRP: %v", err)
	}
	if p.Channel != "Frontpage" || p.Message != "looking for rp" {
		t.Fatalf("LRP payload = %+v", p)
	}

	s.adTick()
	if f, ok := adNextFrame(s); ok {
		t.Fatalf("second post inside the cooldown: %s", f.Code)
	}
}

// TestAdSchedulerPadsCooldown: a post sets the next eligible time to the
// server's lfrp_flood plus the fixed send/receive slack, so a bare cooldown
// cannot fire one one-way latency before the server's.
func TestAdSchedulerPadsCooldown(t *testing.T) {
	s := newAdTestSession(t, adTestCampaign("Frontpage", "ad"))
	adJoin(t, s, "Frontpage", "both")
	mark := time.Now()
	s.adTick()
	got := s.adSched.targets[0].nextEligible.Sub(mark)
	want := s.st.vars.LfrpFlood + adPostSlack
	if got < want || got > want+time.Second {
		t.Fatalf("cooldown = %s, want [%s, %s]", got, want, want+time.Second)
	}
}

// TestAdSchedulerAvailableCandidates: the core offers joined ad-allowing
// channels the campaign does not yet cover, sorted, and only those.
func TestAdSchedulerAvailableCandidates(t *testing.T) {
	s := newAdTestSession(t, nil)
	adJoin(t, s, "Zeta", "both")
	adJoin(t, s, "Alpha", "both")
	adJoin(t, s, "ChatOnly", "chat")

	got := s.adsAvailableLocked()
	if len(got) != 2 || got[0].ID != "Alpha" || got[1].ID != "Zeta" {
		t.Fatalf("available = %+v, want sorted [Alpha Zeta]", got)
	}
	if got[0].Kind != model.ConvOfficial {
		t.Fatalf("kind = %v, want official", got[0].Kind)
	}

	// A channel already in the campaign is not suggested again.
	s.adSched.refresh(adTestCampaign("Alpha", "ad"))
	if got := s.adsAvailableLocked(); len(got) != 1 || got[0].ID != "Zeta" {
		t.Fatalf("available after campaign = %+v, want [Zeta]", got)
	}
}

// TestAdSchedulerSkipsUnjoinedAndChatOnly: a target is skipped until a self JCH
// confirms it, and a chat-only channel is skipped even when joined.
func TestAdSchedulerSkipsUnjoinedAndChatOnly(t *testing.T) {
	s := newAdTestSession(t, adTestCampaign("Frontpage", "ad"))
	if s.adSched.enabled != true {
		t.Fatal("campaign not enabled")
	}

	s.adTick()
	if f, ok := adNextFrame(s); ok {
		t.Fatalf("posted before joining: %s", f.Code)
	}

	adJoin(t, s, "Frontpage", "chat")
	s.adTick()
	if f, ok := adNextFrame(s); ok {
		t.Fatalf("posted to a chat-only channel: %s", f.Code)
	}
	if st := s.adsStatusLocked(); st.Targets[0].State != "disabled" || st.Targets[0].Reason != "chat-only" {
		t.Fatalf("status = %+v, want disabled/chat-only", st.Targets[0])
	}

	// A later JCH can flip the mode back; the scheduler must notice.
	adJoin(t, s, "Frontpage", "both")
	s.adTick()
	if f, ok := adNextFrame(s); !ok || f.Code != "LRP" {
		t.Fatalf("mode change did not re-enable posting: ok=%v", ok)
	}
}

// TestAdSchedulerResetsOnDisconnect: a reconnect clears channel confirmation,
// so a stale membership cannot cause a post into a channel the new connection
// has not joined.
func TestAdSchedulerResetsOnDisconnect(t *testing.T) {
	s := newAdTestSession(t, adTestCampaign("Frontpage", "ad"))
	adJoin(t, s, "Frontpage", "both")
	s.resetAds()
	s.adTick()
	if f, ok := adNextFrame(s); ok {
		t.Fatalf("posted after disconnect without a fresh JCH: %s", f.Code)
	}
	adJoin(t, s, "Frontpage", "both")
	s.adTick()
	if f, ok := adNextFrame(s); !ok || f.Code != "LRP" {
		t.Fatalf("post after rejoin: ok=%v", ok)
	}
}

// TestAdSchedulerAppliesServerErrors: a throttle backs the target off, and a
// not-in-channel rejection clears the confirmation.
func TestAdSchedulerAppliesServerErrors(t *testing.T) {
	s := newAdTestSession(t, adTestCampaign("Frontpage", "ad"))
	adJoin(t, s, "Frontpage", "both")
	s.adTick()
	if _, ok := adNextFrame(s); !ok {
		t.Fatal("no initial post")
	}

	if err := s.handle(jsonFrame("ERR", `{"number":5,"message":"slow down"}`)); err != nil {
		t.Fatalf("ERR: %v", err)
	}
	if s.adSched.awaiting != nil {
		t.Fatal("awaiting target not cleared after ERR")
	}
	s.adTick()
	if f, ok := adNextFrame(s); ok {
		t.Fatalf("posted after message throttle: %s", f.Code)
	}

	// Force the target due again and reject it with a membership error.
	s.adSched.targets[0].nextEligible = s.now().Add(-time.Second)
	s.adSched.lastSend = s.now().Add(-time.Second)
	s.adTick()
	if _, ok := adNextFrame(s); !ok {
		t.Fatal("no post for membership error test")
	}
	if err := s.handle(jsonFrame("ERR", `{"number":45,"message":"not in channel"}`)); err != nil {
		t.Fatalf("ERR: %v", err)
	}
	if s.adSched.confirmed[convKey(s.adSched.targets[0].ref)] {
		t.Fatal("not-in-channel error left the channel confirmed")
	}
}

// TestAdSchedulerRebuildPreservesCursor: editing an unrelated campaign field
// must not reset the rotation cursor of a surviving target.
func TestAdSchedulerRebuildPreservesCursor(t *testing.T) {
	s := newAdTestSession(t, adTestCampaign("Frontpage", "ad"))
	s.adSched.targets[0].cursor = 1
	s.adSched.refresh(&model.AdCampaign{
		Enabled:  true,
		Ads:      []model.AdBody{{Name: "intro", Body: "one"}, {Name: "alt", Body: "two"}},
		Channels: []model.AdChannel{{Kind: model.ConvOfficial, ID: "Frontpage", Ads: []string{"intro", "alt"}}},
	})
	if got := s.adSched.targets[0].cursor; got != 1 {
		t.Fatalf("cursor = %d, want 1", got)
	}
	if got := s.adSched.targets[0].ads; len(got) != 2 {
		t.Fatalf("bodies = %v", got)
	}
}
