# Chat activity calculations

How the log export decides *where the interesting stretches were* in a
conversation, so the user can pick an export range by looking at a chart instead
of guessing dates. The implementation lives in `internal/activity` (pure math),
`internal/store` (the two read queries), and `internal/web` (the
`/api/logs/activity` endpoint). This document is the reference for the model and
its thresholds; the endpoint shape is listed in
[core-protocol.md](core-protocol.md).

## Why it exists

The conversations worth exporting are bursty. A public channel has roughly
constant traffic, so a histogram of it is a flat line and says nothing. DMs and
small private rooms instead have long silences — the two ends are simply not
online at the same time — punctuated by sessions where messages are exchanged
regularly. During an actual game or roleplay the exchange is dense and the
messages are long, sometimes with short side-channel chatter interspersed.

So the meaningful unit is the **session**, not the calendar bucket, and the
signal that separates a roleplay from greetings is **length as well as rate**.

## Scope: whose activity is being measured

The same pipeline runs over a series of points `{atMs, bodyLen}`. What changes is
**whose point process** feeds it:

- **Conversation scope** — every speaker's entries. A burst means two or a few
  people were co-present and interacting. This is the DM / small-room model,
  where the timeline *is* the interaction.
- **Self scope** — only `speaker == session_char`. A burst means *we* were
  posting heavily. This is the channel / large-room model, where the room's own
  timeline is too busy and too diffuse to mean anything, but our participation
  still clusters.

A large public room is the same `kind` as a two-person topical room, so kind
alone cannot decide. Participant count does (see below).

## The scope switch

Resolved once per conversation when the activity view is first opened, then
carried through the drilldown. Kind gives a free shortcut; participation decides
the ambiguous rooms.

```
kind == dm        -> conversation
kind == official  -> self
kind == room      -> participation (below)
```

### Participation

We sample the **most recent active window** of the conversation (the last `D`
days before its last message, capped at `M` rows) and count messages per
speaker. Two measures come out of the same sample:

- **Active participants** — speakers with at least `x` messages in the window.
  This is the user-facing idea: a spectator who greeted once is not a
  participant. `x ≈ 5`.
- **Effective participants** (`N_eff`, inverse Simpson / participation entropy):

  ```
  N_eff = (Σ c_i)² / Σ c_i²
  ```

  This is the smoother version and needs no magic cutoff: frequent speakers
  dominate the sum of squares, so a handful of one-line greeters barely move it.
  Two roleplayers exchanging 200 messages each plus eight spectators with two
  each give `N_eff ≈ 2.2`; ten people with twenty each give `N_eff = 10`.

A room is conversation scope when it looks like a small interaction:

```
active <= K  AND  N_eff <= K'        (K, K' ≈ 7)
```

Otherwise it is self scope. A tiny sample falls back to the conversation's
`entry_count` (a large log is treated as self) so a dormant room is not scanned
whole. The chosen scope and the participation estimate are returned to the
client, which may override with `scope=conversation|self`.

The window matters: counting over all history would let every drive-by visitor
accumulate past `x`, so the estimate would inflate without bound. Anchoring it
to the last `D` days of activity keeps it about *who is around now*.

### Why not other signals

- **`entry_count`** (already in coverage) is free but wrong for a two-person
  room used for years: hundreds of thousands of entries, yet conversation scope
  is exactly right.
- **`selfShare`** is cheap but fails when we *lurk* in a small room: our share is
  near zero, yet the room's roleplay is still in our received log and worth
  exporting.
- **Distinct speakers** (raw) is inflated by spectators, which is the whole
  reason for the `x` floor.

## Points and buckets

- The overview is **per-local-day counts** over the conversation's whole
  persisted span, aligned to the display timezone and zero-filled. It is
  computed in SQL over the timeline index; see [Indexing](#persistence-and-indexing).
- The drilldown reads the points themselves (when the message happened, and its
  body length) for a bounded range, then segments and measures them in Go.

## Detecting roleplay (conversation scope)

A roleplay core is a **run of long messages**, not a rise in raw count:

1. **"Long" adapts to the pair's style.** The threshold is
   `clamp(median_length * LongFactor, LongFloor, LongCeil)`. The clamp is what
   keeps it from misbehaving on a roleplay-heavy log, where the median is itself
   long: it never rises so far that ordinary roleplay posts stop counting.
2. **Long runs.** Walk the long messages in order and split the run wherever the
   gap between consecutive long messages exceeds `CoreGapMs` (~60 min). A run
   with at least `CoreMinLong` (~4) long messages is a core. Gaps between long
   messages, not between all messages, so short side-channel lines do not break
   the run. This is what lets a slow roleplay (a long post every ~30 min) be
   recognized, which a fixed-density sliding window would miss.
3. **Widen.** Each core is expanded outward while the gap to the next message
   stays within `SlackGapMs` (~60 min), capped by `MaxExtendMs` (~2 h), so the
   light chat that led into or trailed out of the session is not sliced off.
   Widened cores that touch are merged.
4. **Everything else is chat.** Points not in a core are grouped into chat
   sessions by the tight gap (`GapTightMs`, ~15 min) so the drilldown can show
   casual exchanges too.

`activeMs` sums only the gaps at or below `GapTightMs`, so silences and the
pauses between merged pieces never dilute the rate.

## Self scope: rate anomaly first, sessions second

Self posts are sparse and interleaved by other people, so pure gap-segmentation
is a convenience rather than the source of truth. The primary output is the one
the user named: **posting more than usual**.

- **Primary: anomaly over day buckets.** With self day counts `c_d`, take a
  robust baseline over the nonzero days — median `b` and scaled MAD
  `s = 1.4826 · MAD` — and flag a day when `c_d` reaches `max(3, b + 2s)`
  (`b + 1` when the MAD is zero, so a flat baseline still yields a peak). Fewer
  than three active days yields no peak. The client derives this from the
  returned buckets, so the server stores no baseline.
- **Secondary: sessions for clickable ranges.** The same segmentation runs on
  self points with scope-tuned parameters (larger gaps, fewer required long
  messages), so a flagged day can be narrowed to a range. Our own long posts
  during a roleplay are still the RP signal.

Because self gaps can be broken by stretches where the room talks without us,
the anomaly ranking is authoritative and the session blocks are a convenience.

## Intensity

Reported per session and for a selected range:

| Field | Meaning |
| --- | --- |
| `count` | messages |
| `volume` | summed body length in bytes |
| `longCount` | messages at or above the adaptive long threshold |
| `activeMs` | summed exchange gaps (each capped at the tight gap) |
| `ratePerMin` | `count / active minutes` — silences excluded |
| `rp` | the session grew from a roleplay core |

The UI renders rate and long-share as a text readout, since bar height alone
cannot carry count, volume, and length at once.

## Persistence and indexing

- **Conversation scope uses the existing cursor index.**
  `idx_entries_cursor(session_char, conv_kind, conv_id, conv_seq, created_at)`
  covers the day-count `GROUP BY` (no table access) and locates the bounded
  drilldown.
- **Self scope uses a partial covering index:**

  ```sql
  CREATE INDEX IF NOT EXISTS idx_entries_self
    ON timeline_entries(session_char, conv_kind, conv_id, created_at, length(body))
    WHERE speaker = session_char;
  ```

  Own posts are stored with `speaker` exactly equal to `session_char`, so the
  predicate is exact. The index covers self day counts *and* the self drilldown
  (body-free), and its size tracks our posts, not the channel. A query must
  repeat `AND speaker = session_char` to use it, so a test asserts the plan
  (`EXPLAIN QUERY PLAN`) does not fall back to a full scan.
- **The participation sample** reads `speaker` unindexed but only `M` rows from
  the most recent end, which the cursor index orders directly.
- Indexes make scans *covering*, not O(1). Conversation day counts on a
  multi-million-entry channel are still O(n) in index entries, which is exactly
  why self scope does **not** also fetch the conversation-wide counts.
- A day-level aggregate (`{count, self_count, sum_len, long_count}` maintained on
  append like `log_conversations`) is the escape hatch only if a self-history
  ever grows too large to scan; the partial index is expected to suffice.

This deliberately reintroduces a small, partial successor to the dropped
`idx_entries_speaker`.

## API

`GET /api/logs/activity` carries a `scope=auto|conversation|self` parameter
(default `auto`) and resolves it server-side; the response echoes the resolved
scope and the participation estimate. `from`/`to` are 31-day-capped; absent, the
overview is returned. Full request/response shapes are in
[core-protocol.md](core-protocol.md).

## Thresholds

All in `activity.Config`, split by scope via `activity.ConfigForScope`. The
tests pin the behaviour each one owns.

| Name | Conversation | Self | Role |
| --- | --- | --- | --- |
| `GapTightMs` | 15 min | 30 min | chat grouping, active-time cap |
| `SlackGapMs` | 60 min | 120 min | widen a core over light chat |
| `MaxExtendMs` | 2 h | 3 h | cap on widening |
| `CoreGapMs` | 60 min | 120 min | max gap between long messages in a run |
| `CoreMinLong` | 4 | 3 | long messages per core |
| `LongFloor` / `LongCeil` / `LongFactor` | 120 / 300 / 0.8 | same | adaptive "long" clamp |

Scope switch: `active <= 7` and `N_eff <= 7`, sampled over the last 7 days
capped at 5000 rows, with `x = 5`; a sample too small to decide falls back to
conversation scope unless the log exceeds 20000 entries, which is treated as a
large room (self scope).

## Testing

- `internal/activity` — segmentation over synthetic timelines (sporadic chat,
  fast and slow roleplay, absorbed preamble, widening cap, adaptive long), the
  participation math, and the scope decision.
- `internal/store` — the SQL day counts match the pure oracle across timezones,
  and the self queries use the partial index.
- `internal/web` — both scopes end to end, auto resolution, and the rejected
  request shapes.
