// Package activity derives an activity model from a conversation's message
// timeline: counts per local day for the overview, and a drilldown that
// segments a bounded range into sessions and measures their intensity.
//
// The model exists because the conversations worth exporting are bursty. A
// channel has roughly constant traffic, so an activity view says nothing. DMs
// and small private rooms instead have long silences (the two ends are simply
// not online at the same time) punctuated by sessions where long messages are
// exchanged regularly. The interesting unit is therefore the session, not the
// calendar bucket, so this package segments by inter-message gaps.
//
// The pipeline is scope-agnostic. It runs over a series of Points; Scope selects
// whose point process that is: every speaker's (conversation scope, the
// DM/small-room model) or only our own (self scope, the channel/large-room
// model). A roleplay core is a run of "long" messages; the definition of long
// adapts to the conversation's own length distribution, so a roleplay written in
// short posts is still recognized. Cores are widened by a looser gap so the
// light chat leading into or out of them is not sliced off, and intensity is
// measured over active exchange time only, so silences never dilute it.
//
// Nothing here touches a store: the caller reduces entries to Points, which the
// store can produce from the covering timeline index (and, for volume, from
// length(body)). Keeping it pure makes the thresholds easy to test and tune.
// See docs/activity.md for the model and its thresholds.
package activity

import "sort"

// Scope selects whose point process the activity model analyzes.
type Scope string

const (
	// ScopeConversation analyzes every speaker: a burst means a few people
	// were co-present and interacting. The DM and small-room model.
	ScopeConversation Scope = "conversation"
	// ScopeSelf analyzes only our own posts: a burst means we were taking
	// part. The channel and large-room model.
	ScopeSelf Scope = "self"
)

// Point is one message reduced to what the activity model needs: when it
// happened and how large its body is. Points are ordered by conv_seq, which the
// store keeps time-monotonic in practice, so AtMs is non-decreasing.
type Point struct {
	AtMs    int64 `json:"atMs"`
	BodyLen int   `json:"bodyLen"`
}

// Config holds the thresholds the segmentation is tuned by. The zero value is
// not useful; call DefaultConfig or ConfigForScope.
type Config struct {
	// GapTightMs is the largest gap between consecutive messages still counted
	// as the same exchange. It groups casual chat and caps what counts as
	// active time.
	GapTightMs int64 `json:"gapTightMs"`
	// SlackGapMs is the larger gap tolerated when widening a roleplay core: a
	// neighbouring message is absorbed, preamble and postamble included, when
	// its gap to the edge is within this bound.
	SlackGapMs int64 `json:"slackGapMs"`
	// MaxExtendMs caps how far, in time, widening may reach beyond the core on
	// either side, so a run of casual chatter cannot absorb indefinitely.
	MaxExtendMs int64 `json:"maxExtendMs"`
	// CoreGapMs is the largest gap between consecutive *long* messages that
	// keeps them in one roleplay core. Short side-channel lines never break a
	// run, so this is what lets a slow roleplay be recognized.
	CoreGapMs int64 `json:"coreGapMs"`
	// CoreMinLong is the fewest long messages a run needs to be a core.
	CoreMinLong int `json:"coreMinLong"`
	// LongFloor and LongCeil clamp the adaptive long-message threshold.
	LongFloor int `json:"longFloor"`
	LongCeil  int `json:"longCeil"`
	// LongFactor scales the conversation's median message length into the
	// threshold: long = clamp(median*LongFactor, LongFloor, LongCeil). It scales
	// *down*: the median of a roleplay-heavy log is itself long, so a message is
	// "long" when it is above the conversation's typical chat, and the clamp
	// keeps the cutoff in a usable band for both terse and verbose styles.
	LongFactor float64 `json:"longFactor"`
}

// DefaultConfig returns conversation-scope thresholds. See ConfigForScope.
func DefaultConfig() Config {
	return Config{
		GapTightMs:  15 * 60 * 1000,
		SlackGapMs:  60 * 60 * 1000,
		MaxExtendMs: 2 * 60 * 60 * 1000,
		CoreGapMs:   60 * 60 * 1000,
		CoreMinLong: 4,
		LongFloor:   120,
		LongCeil:    300,
		LongFactor:  0.8,
	}
}

// ConfigForScope returns the thresholds tuned for a scope. Self-scope posts are
// sparser and interleaved by other people, so the gaps are wider and fewer long
// messages are required to call a core.
func ConfigForScope(scope Scope) Config {
	cfg := DefaultConfig()
	if scope == ScopeSelf {
		cfg.GapTightMs = 30 * 60 * 1000
		cfg.SlackGapMs = 120 * 60 * 1000
		cfg.MaxExtendMs = 3 * 60 * 60 * 1000
		cfg.CoreGapMs = 120 * 60 * 1000
		cfg.CoreMinLong = 3
	}
	return cfg
}

// Session is one contiguous stretch of conversation: a roleplay core with its
// widened edges, or a run of casual chat. StartMs and EndMs are the first and
// last message times (not the widened bounds), so they map directly onto an
// export range. ActiveMs sums only the exchange gaps, so a session's rate is
// not diluted by pauses. RP reports whether the session grew from a roleplay
// core; sessions that did not are still returned so the drilldown can show
// casual chat too.
type Session struct {
	StartMs   int64 `json:"startMs"`
	EndMs     int64 `json:"endMs"`
	Count     int   `json:"count"`
	Volume    int64 `json:"volume"`
	LongCount int   `json:"longCount"`
	ActiveMs  int64 `json:"activeMs"`
	RP        bool  `json:"rp"`
}

// DurationMs is the wall-clock span from the first to the last message.
func (s Session) DurationMs() int64 { return s.EndMs - s.StartMs }

// MeanLen is the average body length in bytes.
func (s Session) MeanLen() float64 {
	if s.Count == 0 {
		return 0
	}
	return float64(s.Volume) / float64(s.Count)
}

// RatePerMin is the message rate over active exchange time only.
func (s Session) RatePerMin() float64 {
	if s.ActiveMs <= 0 {
		return 0
	}
	return float64(s.Count) / (float64(s.ActiveMs) / 60000)
}

// Segment splits an ordered timeline into sessions. Every message lands in
// exactly one session, and RP is set for any session that grew from a roleplay
// core.
func Segment(points []Point, cfg Config) []Session {
	if len(points) == 0 {
		return nil
	}
	long := longThreshold(points, cfg)
	cores := coreRuns(points, long, cfg)

	// Widen each core, then merge the ones that touch.
	ranges := make([]span, 0, len(cores))
	for _, c := range cores {
		ranges = append(ranges, widen(points, c, cfg))
	}
	ranges = mergeSpans(ranges)

	rangeAt := make(map[int]span, len(ranges))
	for _, r := range ranges {
		rangeAt[r.l] = r
	}

	out := make([]Session, 0, len(ranges)+1)
	i := 0
	for i < len(points) {
		if r, ok := rangeAt[i]; ok {
			s := summarize(points, r.l, r.r, long, cfg)
			s.RP = true
			out = append(out, s)
			i = r.r + 1
			continue
		}
		// Casual chat: a run of messages within the tight gap, ending at the
		// next widened core.
		j := i
		for j+1 < len(points) && points[j+1].AtMs-points[j].AtMs <= cfg.GapTightMs {
			if _, isCore := rangeAt[j+1]; isCore {
				break
			}
			j++
		}
		out = append(out, summarize(points, i, j, long, cfg))
		i = j + 1
	}
	return out
}

// Summarize measures the messages in the inclusive range [fromMs, toMs] as a
// single span, for the "N messages, X active, Y bytes" readout over a chosen
// export range. It returns a zero Session when the range holds no messages.
func Summarize(points []Point, fromMs, toMs int64, cfg Config) Session {
	first, last := -1, -1
	for i, p := range points {
		if p.AtMs < fromMs || p.AtMs > toMs {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	if first < 0 {
		return Session{}
	}
	return summarize(points, first, last, longThreshold(points, cfg), cfg)
}

// Bucket is one local day's message count for the overview.
type Bucket struct {
	StartMs int64 `json:"startMs"`
	Count   int64 `json:"count"`
}

// DayMs is one local day in milliseconds, the overview bucket unit. It is the
// fixed offset the client and exporter bucket by, not a calendar span.
const DayMs int64 = 24 * 60 * 60 * 1000

// DayCounts returns one count per local day spanning [fromMs, toMs] inclusive,
// aligned to tzOffsetMin minutes east of UTC. Empty days are zero-filled so the
// result is directly renderable. The real endpoint computes this in SQL from the
// covering index; this is the reference implementation and the test oracle.
func DayCounts(points []Point, fromMs, toMs int64, tzOffsetMin int) []Bucket {
	off := int64(tzOffsetMin) * 60000
	startDay := FloorDiv(fromMs+off, DayMs)
	endDay := FloorDiv(toMs+off, DayMs)
	n := int(endDay - startDay + 1)
	if n <= 0 {
		return nil
	}
	counts := make([]int64, n)
	for _, p := range points {
		d := FloorDiv(p.AtMs+off, DayMs)
		if d >= startDay && d <= endDay {
			counts[d-startDay]++
		}
	}
	out := make([]Bucket, n)
	for i := range counts {
		out[i] = Bucket{StartMs: (startDay+int64(i))*DayMs - off, Count: counts[i]}
	}
	return out
}

// Participation summarizes who is actually taking part in a conversation, from a
// sample of recent speakers. Active counts the speakers with at least
// ScopeConfig.ActiveMin messages; Effective is the inverse Simpson index
// (participation entropy), which low-frequency spectators barely move.
type Participation struct {
	Sampled   int     `json:"sampled"`
	Active    int     `json:"active"`
	Effective float64 `json:"effective"`
}

// ScopeConfig tunes the conversation-versus-self decision.
type ScopeConfig struct {
	// ActiveMin is how many messages a speaker needs to count as active.
	ActiveMin int
	// ActiveMax and EffectiveMax are the participation bounds at or below which
	// a conversation is treated as a small interaction (conversation scope).
	ActiveMax    int
	EffectiveMax float64
	// MinSample is the smallest sample that may decide; below it the caller
	// falls back to a size heuristic.
	MinSample int
}

// DefaultScopeConfig returns the switch thresholds. See docs/activity.md.
func DefaultScopeConfig() ScopeConfig {
	return ScopeConfig{
		ActiveMin:    5,
		ActiveMax:    7,
		EffectiveMax: 7,
		MinSample:    20,
	}
}

// MeasureParticipation reduces a sample of speaker names to the participation
// statistics. Names are compared case-sensitively, matching how entries store
// them.
func MeasureParticipation(speakers []string, cfg ScopeConfig) Participation {
	counts := make(map[string]int, len(speakers))
	for _, s := range speakers {
		counts[s]++
	}
	p := Participation{Sampled: len(speakers)}
	var sum, sumSq float64
	for _, c := range counts {
		if c >= cfg.ActiveMin {
			p.Active++
		}
		f := float64(c)
		sum += f
		sumSq += f * f
	}
	if sumSq > 0 {
		p.Effective = sum * sum / sumSq
	}
	return p
}

// ChooseScope decides conversation scope when the sample looks like a small
// interaction. The bool is false when the sample is too small to decide and the
// caller should fall back to a size heuristic.
func ChooseScope(p Participation, cfg ScopeConfig) (Scope, bool) {
	if p.Sampled < cfg.MinSample {
		return ScopeConversation, false
	}
	if p.Active <= cfg.ActiveMax && p.Effective <= cfg.EffectiveMax {
		return ScopeConversation, true
	}
	return ScopeSelf, true
}

// span is an inclusive index range over the point slice.
type span struct{ l, r int }

// longThreshold is the adaptive cutoff for a "long" message: the conversation's
// median length scaled by LongFactor, clamped so a roleplay-heavy log does not
// push it out of reach of ordinary posts.
func longThreshold(points []Point, cfg Config) int {
	lens := make([]int, len(points))
	for i, p := range points {
		lens[i] = p.BodyLen
	}
	sort.Ints(lens)
	med := lens[len(lens)/2]
	th := int(float64(med) * cfg.LongFactor)
	if th < cfg.LongFloor {
		th = cfg.LongFloor
	}
	if cfg.LongCeil > 0 && th > cfg.LongCeil {
		th = cfg.LongCeil
	}
	return th
}

// coreRuns finds roleplay cores: maximal runs of long messages whose
// consecutive gaps are within CoreGapMs, kept when the run has at least
// CoreMinLong long messages. Short messages between long ones do not break a
// run; the returned span covers the whole index range, short messages included.
func coreRuns(points []Point, long int, cfg Config) []span {
	var out []span
	start, last, count := -1, -1, 0
	for i, p := range points {
		if p.BodyLen < long {
			continue
		}
		if start < 0 {
			start, last, count = i, i, 1
			continue
		}
		if p.AtMs-points[last].AtMs > cfg.CoreGapMs {
			if count >= cfg.CoreMinLong {
				out = append(out, span{start, last})
			}
			start, count = i, 1
		} else {
			count++
		}
		last = i
	}
	if start >= 0 && count >= cfg.CoreMinLong {
		out = append(out, span{start, last})
	}
	return out
}

// widen expands a core over neighbouring messages while their gap to the edge is
// within SlackGapMs, stopping before the extension passes MaxExtendMs beyond the
// core.
func widen(points []Point, sp span, cfg Config) span {
	a, b := sp.l, sp.r
	coreStart := points[sp.l].AtMs
	coreEnd := points[sp.r].AtMs
	for a > 0 {
		if points[a].AtMs-points[a-1].AtMs > cfg.SlackGapMs {
			break
		}
		if coreStart-points[a-1].AtMs > cfg.MaxExtendMs {
			break
		}
		a--
	}
	for b < len(points)-1 {
		if points[b+1].AtMs-points[b].AtMs > cfg.SlackGapMs {
			break
		}
		if points[b+1].AtMs-coreEnd > cfg.MaxExtendMs {
			break
		}
		b++
	}
	return span{a, b}
}

// mergeSpans merges overlapping or adjacent index ranges. Input is ordered by l.
func mergeSpans(in []span) []span {
	if len(in) == 0 {
		return nil
	}
	out := []span{in[0]}
	for _, sp := range in[1:] {
		last := &out[len(out)-1]
		if sp.l <= last.r+1 {
			if sp.r > last.r {
				last.r = sp.r
			}
			continue
		}
		out = append(out, sp)
	}
	return out
}

// summarize measures the inclusive point range [start, end]. ActiveMs counts
// only gaps that are within GapTightMs, so pauses between merged pieces
// contribute no active time.
func summarize(points []Point, start, end, long int, cfg Config) Session {
	s := Session{StartMs: points[start].AtMs, EndMs: points[end].AtMs}
	for i := start; i <= end; i++ {
		s.Count++
		s.Volume += int64(points[i].BodyLen)
		if points[i].BodyLen >= long {
			s.LongCount++
		}
		if i > start {
			if gap := points[i].AtMs - points[i-1].AtMs; gap <= cfg.GapTightMs {
				s.ActiveMs += gap
			}
		}
	}
	return s
}

// FloorDiv divides a by b, rounding toward negative infinity, so local-day
// indices stay contiguous when the tz offset shifts a boundary before the
// epoch. The store's day bucketing uses it too.
func FloorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}
