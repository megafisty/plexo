package activity

import (
	"testing"
	"time"
)

// base is an arbitrary UTC anchor; only relative times matter.
var base = time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC)

// ts is base plus hour:min as epoch milliseconds.
func ts(hour, min int) int64 {
	return base.Add(time.Duration(hour)*time.Hour + time.Duration(min)*time.Minute).UnixMilli()
}

// burst appends n messages starting at startMs, everyMs apart.
func burst(pts []Point, startMs, everyMs int64, n, bodyLen int) []Point {
	for i := 0; i < n; i++ {
		pts = append(pts, Point{AtMs: startMs + int64(i)*everyMs, BodyLen: bodyLen})
	}
	return pts
}

// TestSporadicChatHasNoRP: pairs of greetings days apart are chat, not roleplay.
func TestSporadicChatHasNoRP(t *testing.T) {
	var pts []Point
	for _, day := range []int{0, 3, 7} {
		start := base.AddDate(0, 0, day).Add(21 * time.Hour).UnixMilli()
		pts = append(pts, Point{AtMs: start, BodyLen: 60}, Point{AtMs: start + 5*60000, BodyLen: 40})
	}
	sessions := Segment(pts, DefaultConfig())
	if len(sessions) != 3 {
		t.Fatalf("sessions = %d, want 3", len(sessions))
	}
	for i, s := range sessions {
		if s.RP {
			t.Errorf("session %d marked RP; sporadic chat must not be", i)
		}
	}
}

// TestFastRoleplayIsOneSession: a dense evening of paragraph posts is one RP
// session.
func TestFastRoleplayIsOneSession(t *testing.T) {
	pts := burst(nil, ts(20, 0), 2*60000, 40, 500)
	sessions := Segment(pts, DefaultConfig())
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	s := sessions[0]
	if !s.RP || s.Count != 40 || s.LongCount != 40 {
		t.Fatalf("session = %+v", s)
	}
}

// TestSlowRoleplayIsOneSession: long posts every 30 minutes still form one core,
// because only the gaps between long messages matter.
func TestSlowRoleplayIsOneSession(t *testing.T) {
	pts := burst(nil, ts(20, 0), 30*60000, 5, 500) // 20:00..22:00
	sessions := Segment(pts, DefaultConfig())
	if len(sessions) != 1 || !sessions[0].RP {
		t.Fatalf("slow roleplay sessions = %+v", sessions)
	}
	if sessions[0].Count != 5 {
		t.Errorf("count = %d, want 5", sessions[0].Count)
	}
}

// TestShortSidechannelDoesNotBreakCore: a long post every few minutes separated
// by short lines stays one run.
func TestShortSidechannelDoesNotBreakCore(t *testing.T) {
	var pts []Point
	for i := 0; i < 5; i++ {
		at := ts(20, 0) + int64(i)*10*60000
		pts = append(pts, Point{AtMs: at, BodyLen: 500})
		pts = append(pts, Point{AtMs: at + 60000, BodyLen: 30})
	}
	sessions := Segment(pts, DefaultConfig())
	if len(sessions) != 1 || !sessions[0].RP {
		t.Fatalf("sessions = %+v", sessions)
	}
}

// TestLightChatIsAbsorbedIntoRP: short preamble chatter within the slack gap is
// pulled into the session, and the pause between it and the core is not counted
// as active time.
func TestLightChatIsAbsorbedIntoRP(t *testing.T) {
	pts := burst(nil, ts(19, 0), 8*60000, 3, 60) // 19:00..19:16, short
	pts = burst(pts, ts(20, 0), 3*60000, 6, 500) // gap 44m (<= slack 60m)
	sessions := Segment(pts, DefaultConfig())
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1 (preamble absorbed)", len(sessions))
	}
	s := sessions[0]
	if !s.RP || s.StartMs != ts(19, 0) || s.Count != 9 {
		t.Fatalf("session = %+v", s)
	}
	// Active: 2 preamble gaps x 8m + 5 core gaps x 3m = 31m. The 44m pause
	// between pieces contributes nothing.
	if want := int64(31 * 60000); s.ActiveMs != want {
		t.Errorf("activeMs = %d, want %d", s.ActiveMs, want)
	}
}

// TestFartherChatStaysSeparate: chat hours later is its own (non-RP) session.
func TestFartherChatStaysSeparate(t *testing.T) {
	pts := burst(nil, ts(20, 0), 2*60000, 20, 500) // 20:00..20:38
	pts = append(pts, Point{AtMs: ts(23, 0), BodyLen: 50}, Point{AtMs: ts(23, 4), BodyLen: 50})
	sessions := Segment(pts, DefaultConfig())
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sessions))
	}
	if !sessions[0].RP || sessions[1].RP {
		t.Errorf("RP flags = %v, %v; want true, false", sessions[0].RP, sessions[1].RP)
	}
}

// TestNearbyRoleplayCoresMerge: a short pause inside one evening does not split
// the session, because it stays within CoreGapMs and the widened edges touch.
func TestNearbyRoleplayCoresMerge(t *testing.T) {
	pts := burst(nil, ts(20, 0), 2*60000, 12, 500) // 20:00..20:22
	pts = burst(pts, ts(21, 30), 2*60000, 12, 500) // gap 68m (> core 60m)
	sessions := Segment(pts, DefaultConfig())
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1 merged", len(sessions))
	}
	if sessions[0].Count != 24 {
		t.Errorf("count = %d, want 24", sessions[0].Count)
	}
}

// TestWideningIsCapped: a chain of casual chatter cannot drag the session out
// past MaxExtendMs.
func TestWideningIsCapped(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxExtendMs = 30 * 60000
	var pts []Point
	for m := 0; m <= 100; m += 20 { // 17:00..18:40, short, 20m apart
		pts = append(pts, Point{AtMs: ts(17, m), BodyLen: 50})
	}
	pts = burst(pts, ts(19, 0), 3*60000, 6, 500)
	sessions := Segment(pts, cfg)

	var rp *Session
	chat := 0
	for i := range sessions {
		if sessions[i].RP {
			rp = &sessions[i]
		} else {
			chat++
		}
	}
	if rp == nil || rp.StartMs != ts(18, 40) {
		t.Fatalf("RP session = %+v (want start 18:40)", rp)
	}
	if chat != 5 {
		t.Errorf("chat sessions = %d, want 5", chat)
	}
}

// TestAdaptiveLongThreshold: the long cutoff follows the conversation's style,
// so a roleplay written in shorter-than-usual posts is still recognized, while
// plain chatter is not.
func TestAdaptiveLongThreshold(t *testing.T) {
	terse := burst(nil, ts(20, 0), 10*60000, 6, 180)
	if sessions := Segment(terse, DefaultConfig()); len(sessions) != 1 || !sessions[0].RP {
		t.Errorf("180-byte roleplay = %+v, want one RP session", sessions)
	}
	chat := burst(nil, ts(21, 0), 10*60000, 6, 60)
	if sessions := Segment(chat, DefaultConfig()); len(sessions) != 1 || sessions[0].RP {
		t.Errorf("60-byte chatter = %+v, want one non-RP session", sessions)
	}
}

// TestSummarizeRange: the selected-range readout counts only messages inside it.
func TestSummarizeRange(t *testing.T) {
	pts := burst(nil, ts(20, 0), 2*60000, 11, 500) // 20:00..20:20
	s := Summarize(pts, ts(20, 0), ts(20, 10), DefaultConfig())
	if s.Count != 6 {
		t.Errorf("count = %d, want 6", s.Count)
	}
	if empty := Summarize(pts, ts(22, 0), ts(23, 0), DefaultConfig()); empty.Count != 0 {
		t.Errorf("empty range count = %d, want 0", empty.Count)
	}
}

// TestDayCountsAlignsToTimezone: local-day bucketing shifts with the offset.
func TestDayCountsAlignsToTimezone(t *testing.T) {
	p1 := base.Add(23*time.Hour + 30*time.Minute).UnixMilli()     // Jan 10 23:30 UTC
	p2 := base.AddDate(0, 0, 1).Add(30 * time.Minute).UnixMilli() // Jan 11 00:30 UTC
	pts := []Point{{AtMs: p1, BodyLen: 10}, {AtMs: p2, BodyLen: 10}}

	utc := DayCounts(pts, p1, p2, 0)
	if len(utc) != 2 || utc[0].Count != 1 || utc[1].Count != 1 {
		t.Fatalf("utc buckets = %+v, want two days of one", utc)
	}
	west := DayCounts(pts, p1, p2, -60) // UTC-1: both land on Jan 10
	if len(west) != 1 || west[0].Count != 2 {
		t.Fatalf("utc-1 buckets = %+v, want one day of two", west)
	}
}

// TestMeasureParticipation: active counts speakers over the message floor, and
// effective participants barely move for low-frequency greeters.
func TestMeasureParticipation(t *testing.T) {
	cfg := DefaultScopeConfig()
	// Two core participants with 200 each and eight greeters with two each.
	var speakers []string
	for i := 0; i < 200; i++ {
		speakers = append(speakers, "Vix", "Kira")
	}
	for i := 0; i < 8; i++ {
		name := "G" + string(rune('a'+i))
		speakers = append(speakers, name, name)
	}
	p := MeasureParticipation(speakers, cfg)
	if p.Active != 2 {
		t.Errorf("active = %d, want 2", p.Active)
	}
	if p.Effective < 2 || p.Effective > 3 {
		t.Errorf("effective = %.2f, want ~2.2", p.Effective)
	}
	// A crowd of ten equal speakers is a crowd.
	var crowd []string
	for i := 0; i < 10; i++ {
		name := "C" + string(rune('a'+i))
		for j := 0; j < 20; j++ {
			crowd = append(crowd, name)
		}
	}
	c := MeasureParticipation(crowd, cfg)
	if c.Active != 10 || c.Effective < 9.5 {
		t.Errorf("crowd = %+v, want ~10 active/effective", c)
	}
}

// TestChooseScope: a small sample cannot decide; a small interaction is
// conversation scope; a crowd is self scope.
func TestChooseScope(t *testing.T) {
	cfg := DefaultScopeConfig()
	if scope, ok := ChooseScope(Participation{Sampled: 5}, cfg); ok {
		t.Errorf("tiny sample decided %q, want undecided", scope)
	}
	small := Participation{Sampled: 400, Active: 2, Effective: 2.2}
	if scope, ok := ChooseScope(small, cfg); !ok || scope != ScopeConversation {
		t.Errorf("small interaction = %q, %v", scope, ok)
	}
	crowd := Participation{Sampled: 400, Active: 10, Effective: 10}
	if scope, ok := ChooseScope(crowd, cfg); !ok || scope != ScopeSelf {
		t.Errorf("crowd = %q, %v", scope, ok)
	}
}

// TestVisualize prints an ASCII strip so the segmentation can be eyeballed with
// `go test -run TestVisualize -v ./internal/activity`. It asserts nothing.
func TestVisualize(t *testing.T) {
	var pts []Point
	pts = append(pts, Point{AtMs: ts(19, 0), BodyLen: 70})
	pts = append(pts, Point{AtMs: ts(19, 12), BodyLen: 55})
	pts = burst(pts, ts(20, 0), 3*60000, 30, 620)
	pts = append(pts, Point{AtMs: ts(21, 45), BodyLen: 40})
	pts = burst(pts, ts(23, 0), 4*60000, 12, 80)

	for _, s := range Segment(pts, DefaultConfig()) {
		tag := "chat"
		if s.RP {
			tag = "RP  "
		}
		t.Logf("%s %s..%s n=%d vol=%d mean=%.0f rate=%.1f/min",
			tag,
			time.UnixMilli(s.StartMs).UTC().Format("15:04"),
			time.UnixMilli(s.EndMs).UTC().Format("15:04"),
			s.Count, s.Volume, s.MeanLen(), s.RatePerMin())
	}
}
