package session

import (
	"sort"
	"strings"
	"time"

	"plexo/internal/fchat"
	"plexo/internal/model"
)

// adMinDelay floors the timer reset so an already-due target cannot spin the
// actor in a tight arm/fire loop.
const adMinDelay = 10 * time.Millisecond

// adAwaitWindow bounds how long a posted advertisement is attributed to its
// target for error handling. A server reply arrives well within this; a later
// unrelated ERR must not be misapplied to the scheduler.
const adAwaitWindow = 30 * time.Second

// adPostSlack pads every server cooldown the scheduler sets. It anchors a
// cooldown at the moment it *sends* a post, while the server anchors it at the
// moment it *receives* it, so a bare lfrp_flood fires one one-way latency early
// and would be rejected every cycle. The slack covers that skew plus jitter; at
// the default lfrp_flood (ten minutes) it costs under two percent.
const adPostSlack = 5 * time.Second

// adTarget is one campaign channel's runtime state. It survives a campaign
// rebuild (matched by conversation key) so a cursor or backoff is not lost when
// an unrelated field is edited.
type adTarget struct {
	ref  model.ConvRef
	name string
	ads  []model.AdBody // resolved bodies, in rotation order

	cursor int

	// nextEligible is the earliest time the channel may be posted to: the last
	// attempt plus lfrp_flood and adPostSlack, adjusted by server throttles.
	nextEligible time.Time
	// disabled is set when the server rejects the channel as chat-only, until a
	// rejoin re-confirms it.
	disabled bool
	reason   string

	lastBody string
	lastErr  string
}

// adScheduler is the per-session advertisement scheduler. It is entirely owned
// by the actor goroutine: the timer's channel is read only from the actor's
// select, and no field is touched from another goroutine.
type adScheduler struct {
	enabled  bool
	targets  []*adTarget
	byKey    map[string]*adTarget
	lastSend time.Time
	timer    *time.Timer

	// confirmed records conversations joined on the current connection. Actor
	// state alone is not enough: channel membership persists across a reconnect
	// while the server's does not, so the scheduler must not post into a channel
	// it has not seen a self JCH for this connection.
	confirmed map[string]bool

	// awaiting is the target of the most recent automatic post, used to attribute
	// a following ERR to the scheduler instead of surfacing it as a user error.
	awaiting *adTarget
	awaitAt  time.Time
}

func newAdScheduler() *adScheduler {
	t := time.NewTimer(time.Hour)
	t.Stop()
	return &adScheduler{
		timer:     t,
		byKey:     map[string]*adTarget{},
		confirmed: map[string]bool{},
	}
}

// refresh rebuilds the target list from campaign, preserving runtime state for
// conversations that remain targets. A nil or disabled campaign still keeps the
// targets so their status remains observable; enabled gates posting.
func (a *adScheduler) refresh(c *model.AdCampaign) {
	a.enabled = c != nil && c.Enabled
	byName := map[string]model.AdBody{}
	if c != nil {
		for _, b := range c.Ads {
			byName[strings.ToLower(b.Name)] = b
		}
	}
	old := a.byKey
	a.targets = a.targets[:0]
	a.byKey = map[string]*adTarget{}
	if c == nil {
		return
	}
	for _, ch := range c.Channels {
		ref := convRefForChannel(ch.ID)
		key := convKey(ref)
		t := old[key]
		if t == nil {
			t = &adTarget{}
		}
		t.ref = ref
		t.name = ch.Name
		var entries []model.AdBody
		for _, r := range ch.Ads {
			if b, ok := byName[strings.ToLower(r)]; ok {
				entries = append(entries, b)
			}
		}
		t.ads = entries
		if len(entries) > 0 {
			t.cursor %= len(entries)
		} else {
			t.cursor = 0
		}
		a.targets = append(a.targets, t)
		a.byKey[key] = t
	}
}

// adEligible reports whether t can be posted to at all right now, and the
// reason it cannot. It ignores the time gates (per-channel cooldown and the
// shared message spacing); those are applied when choosing a due time.
func (s *Session) adEligible(t *adTarget) (bool, string) {
	if !s.adSched.enabled {
		return false, "disabled"
	}
	if len(t.ads) == 0 {
		return false, "no-ad"
	}
	key := convKey(t.ref)
	if !s.adSched.confirmed[key] {
		return false, "not-joined"
	}
	if t.disabled {
		return false, t.reason
	}
	cs, ok := s.st.convs[key]
	if !ok || cs.membership != memJoined {
		return false, "not-joined"
	}
	if strings.EqualFold(cs.mode, "chat") {
		return false, "chat-only"
	}
	if s.st.vars.LfrpMax > 0 && len(t.ads[t.cursor%len(t.ads)].Body) > s.st.vars.LfrpMax {
		return false, "too-long"
	}
	return true, ""
}

// adNextDue returns the earliest time any eligible target may be posted to. The
// shared message spacing is folded in, so a channel that is off cooldown but
// behind a recent message waits for the connection-wide gate.
func (s *Session) adNextDue() (time.Time, bool) {
	gate := s.adSched.lastSend.Add(s.st.vars.MsgFlood)
	var best time.Time
	found := false
	for _, t := range s.adSched.targets {
		if ok, _ := s.adEligible(t); !ok {
			continue
		}
		due := t.nextEligible
		if gate.After(due) {
			due = gate
		}
		if !found || due.Before(best) {
			best, found = due, true
		}
	}
	return best, found
}

// adPick selects the eligible target with the earliest due time if it is
// already due, along with the next body to post.
func (s *Session) adPick(now time.Time) (*adTarget, string, bool) {
	gate := s.adSched.lastSend.Add(s.st.vars.MsgFlood)
	var best *adTarget
	var bestDue time.Time
	for _, t := range s.adSched.targets {
		if ok, _ := s.adEligible(t); !ok {
			continue
		}
		due := t.nextEligible
		if gate.After(due) {
			due = gate
		}
		if best == nil || due.Before(bestDue) {
			best, bestDue = t, due
		}
	}
	if best == nil || bestDue.After(now) {
		return nil, "", false
	}
	return best, best.ads[best.cursor%len(best.ads)].Body, true
}

// adArm resets the timer to the next due target, or stops it when nothing can
// be posted. Only the actor calls this.
func (s *Session) adArm() {
	if s.out == nil {
		s.adStop()
		return
	}
	due, ok := s.adNextDue()
	if !ok {
		s.adStop()
		return
	}
	d := time.Until(due)
	if d < adMinDelay {
		d = adMinDelay
	}
	s.adSched.timer.Reset(d)
}

func (s *Session) adStop() { s.adSched.timer.Stop() }

// adTick posts at most one advertisement. One per wake is forced by the
// server's per-connection message spacing, which is shared by MSG/PRI/LRP/RLL.
func (s *Session) adTick() {
	if s.out == nil {
		s.adStop()
		return
	}
	now := s.now()
	t, body, ok := s.adPick(now)
	if !ok {
		s.adStop()
		return
	}
	if err := s.queue("LRP", fchat.ChannelMsg{Channel: t.ref.ID, Message: body}); err != nil {
		s.adStop()
		return
	}
	s.adSched.lastSend = now
	s.adSched.awaiting = t
	s.adSched.awaitAt = now
	t.lastBody = body
	t.lastErr = ""
	t.cursor++
	t.nextEligible = now.Add(s.st.vars.LfrpFlood + adPostSlack)
	s.emitAdsStatus()
	s.adArm()
}

// adConfirmed marks a conversation joined on this connection. It clears any
// sticky chat-only disable and any local backoff, because the server has no
// per-channel cooldown for a character that just joined.
func (s *Session) adConfirmed(ref model.ConvRef) {
	key := convKey(ref)
	s.adSched.confirmed[key] = true
	if t := s.adSched.byKey[key]; t != nil {
		t.disabled = false
		t.reason = ""
		t.nextEligible = time.Time{}
	}
	s.emitAdsStatus()
	s.adArm()
}

// adUnconfirmed marks a conversation left on this connection. The server erases
// its per-channel cooldown on part, so the local backoff is cleared too.
func (s *Session) adUnconfirmed(ref model.ConvRef) {
	key := convKey(ref)
	delete(s.adSched.confirmed, key)
	if t := s.adSched.byKey[key]; t != nil {
		t.disabled = false
		t.reason = ""
		t.nextEligible = time.Time{}
		if s.adSched.awaiting == t {
			s.adSched.awaiting = nil
		}
	}
	s.emitAdsStatus()
	s.adArm()
}

// adTouch re-evaluates the schedule after a mode or membership change that
// could make a skipped target eligible (or vice versa).
func (s *Session) adTouch() {
	s.emitAdsStatus()
	s.adArm()
}

// adApplyErr folds a server rejection into the scheduler when it belongs to the
// most recent automatic post, and reports whether it consumed the error so it
// is not surfaced as a user-facing error event.
func (s *Session) adApplyErr(code int) bool {
	t := s.adSched.awaiting
	if t == nil {
		return false
	}
	now := s.now()
	if now.Sub(s.adSched.awaitAt) > adAwaitWindow {
		s.adSched.awaiting = nil
		return false
	}
	switch code {
	case 5: // throttle message: the shared 0.5s connection gate
		t.lastErr = "message throttled"
		t.nextEligible = now.Add(s.st.vars.MsgFlood)
	case 56: // throttle ad: the per-channel cooldown
		t.lastErr = "ad throttled"
		t.nextEligible = now.Add(s.st.vars.LfrpFlood + adPostSlack)
	case 15: // message too long
		t.lastErr = "message too long"
		t.nextEligible = now.Add(s.st.vars.LfrpFlood + adPostSlack)
	case 45: // not in channel
		t.lastErr = "not in channel"
		delete(s.adSched.confirmed, convKey(t.ref))
		t.nextEligible = now.Add(s.st.vars.LfrpFlood + adPostSlack)
	case 59: // chat-only
		t.disabled = true
		t.reason = "chat-only"
		t.lastErr = "channel only allows chat"
	default:
		return false
	}
	s.adSched.awaiting = nil
	s.log().Debug("ad post rejected", "character", s.cfg.Character, "channel", t.ref.ID, "code", code)
	s.emitAdsStatus()
	s.adArm()
	return true
}

// resetAds clears all connection-scoped scheduler state. It runs when a
// connection ends: the server's per-channel cooldowns are gone with it, and a
// fresh login must re-confirm every channel with a self JCH before posting.
func (s *Session) resetAds() {
	s.adStop()
	s.adSched.confirmed = map[string]bool{}
	s.adSched.lastSend = time.Time{}
	s.adSched.awaiting = nil
	for _, t := range s.adSched.targets {
		t.nextEligible = time.Time{}
		t.disabled = false
		t.reason = ""
		t.lastErr = ""
	}
	s.emitAdsStatus()
}

// SetAdsCampaign replaces the session's advertisement campaign. It is safe to
// call while running: the target list is rebuilt on the actor goroutine, and
// runtime state for conversations that remain targets is preserved. A stopped
// session is a no-op.
func (s *Session) SetAdsCampaign(c *model.AdCampaign) {
	ask(s, func(reply chan struct{}) {
		s.adSched.refresh(c)
		if !s.adSched.enabled && len(s.adSched.targets) == 0 {
			// Clearing the campaign removes its live status too, so a subscribed
			// client does not keep stale targets.
			s.emitStateRemoved(model.AdsKey(s.cfg.Character))
		} else {
			s.emitAdsStatus()
		}
		s.adArm()
		reply <- struct{}{}
	})
}

// AdsStatus returns the live scheduler status, or the zero value when the
// session is not running.
func (s *Session) AdsStatus() model.AdsStatus {
	r, ok := ask(s, func(reply chan model.AdsStatus) { reply <- s.adsStatusLocked() })
	if !ok {
		return model.AdsStatus{}
	}
	return r
}

// AdsAvailable returns the joined channels that currently allow ads and are not
// yet in the character's campaign. It lets the client offer them without having
// to know each channel's mode, which only reaches it at interest. Nothing is
// persisted: the caller merges the list into its draft and saves it if wanted.
func (s *Session) AdsAvailable() []model.AdChannel {
	r, ok := ask(s, func(reply chan []model.AdChannel) { reply <- s.adsAvailableLocked() })
	if !ok {
		return nil
	}
	return r
}

// adsAvailableLocked builds AdsAvailable on the actor goroutine. Channels are
// sorted so the suggestion list is stable across calls.
func (s *Session) adsAvailableLocked() []model.AdChannel {
	out := make([]model.AdChannel, 0, len(s.st.convs))
	for key, cs := range s.st.convs {
		if cs.membership != memJoined || strings.EqualFold(cs.mode, "chat") {
			continue
		}
		if _, ok := s.adSched.byKey[key]; ok {
			continue
		}
		out = append(out, model.AdChannel{Kind: cs.ref.Kind, ID: cs.ref.ID, Name: cs.title})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// adsStatusLocked builds the client-facing status. It is called on the actor
// goroutine, so it reads session state directly.
func (s *Session) adsStatusLocked() model.AdsStatus {
	running := s.out != nil && s.st.phase == "ready"
	st := model.AdsStatus{Running: running, Enabled: running && s.adSched.enabled}
	for _, t := range s.adSched.targets {
		names := make([]string, 0, len(t.ads))
		for _, a := range t.ads {
			names = append(names, a.Name)
		}
		ts := model.AdTargetStatus{
			Kind:      t.ref.Kind,
			ID:        t.ref.ID,
			Name:      t.name,
			Ads:       names,
			LastBody:  t.lastBody,
			LastError: t.lastErr,
		}
		if !running {
			ts.State = "skipped"
			ts.Reason = "offline"
		} else if ok, reason := s.adEligible(t); ok {
			ts.State = "active"
			if !t.nextEligible.IsZero() {
				due := t.nextEligible
				if g := s.adSched.lastSend.Add(s.st.vars.MsgFlood); g.After(due) {
					due = g
				}
				ts.NextEligibleAt = &due
			}
		} else {
			ts.State = "skipped"
			if t.disabled || reason == "chat-only" {
				ts.State = "disabled"
			}
			ts.Reason = reason
		}
		st.Targets = append(st.Targets, ts)
	}
	return st
}

// emitAdsStatus publishes the current scheduler status as a coalesced state
// record. It is safe on a session without a broker. Sessions with no campaign
// emit nothing, so a channel join does not publish an empty status for every
// character.
func (s *Session) emitAdsStatus() {
	if !s.adSched.enabled && len(s.adSched.targets) == 0 {
		return
	}
	s.emitState(model.AdsKey(s.cfg.Character), s.adsStatusLocked())
}
