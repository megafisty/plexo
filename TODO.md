# Plexo TODO

Open future work, consolidated from the design docs and code comments. This is a
parking lot to review periodically: promote an item into the design docs when it
gets scheduled, and delete it when it is done or dropped. Nothing here is
committed or ordered.

## Client features

- **Per-session reconnect control.** The core's `reconnect` command runs the full
  connect flow (ticket reuse → mint → stored credentials), but the UI exposes no
  control on a disconnected tab; it shows only the state dot. Groundwork:
  [docs/sessions.md](docs/sessions.md).
- **Post LRP ads.** The Ads dialog browses each session's buffered ads
  (`GET /api/ads`); its Post tab is a placeholder, so nothing sends an
  advertisement yet. Groundwork: [docs/domain.md](docs/domain.md).
- **Chatlog exporter, richer.** The browser, HTML export, and the activity
  histogram are implemented; the open work is alternate formats (plain text,
  JSON). Groundwork: `docs/core-protocol.md`.

## Performance and robustness

- **Suppress redundant state emits at the source.** A prototype broker-level
  dedup of byte-identical `state` records showed the redundancy is concentrated
  in producer-side re-emits: `emitFriendPresence` fires on every `FRL` (re-sends
  every friend's presence), and `emitConversation` fires from ~12 frame handlers
  even when client-visible metadata did not change (re-sending the full member
  list). Fix these in the session (skip the emit, or track the last emitted
  value) rather than deduplicating in the broker, which pays a marshal per
  publish and retains a second copy of every stored value.
  `internal/session/events.go`, `internal/broker/broker.go`.
- **Account-global presence.** `character/<name>` is one global store key but
  delivery is gated per reporting session, so the same character online in
  multiple sessions is emitted once per session, and a resync can drop a
  presence a subscriber watches only through another session. One record per
  character, gated by "watched in any session," would dedup multi-session
  traffic and remove that edge. `internal/broker/broker.go`.
- **Uniform epoch-millisecond times on the wire.** Entries carry
  `createdAtMs`, but `SummaryPayload`/`ConvSummary.lastActivity`, warpmarks, and
  ads still carry RFC3339 strings the client `Date.parse`s. Convert the rest so
  one parsing rule and one wire shape holds. `internal/model/model.go`.
- **Outbound rate limiting.** Respect the server's flood limits (or predict
  them) rather than only reacting to `ERR`.
- **Low-power mode negotiation.** Reduce redraws and background work when the
  client is idle.

## Persistence and schema

- **FTS5 full-text search over message bodies.** Indefinite retention is current
  policy; shape `body` so FTS5 can be added later, but do not build it yet.
  Groundwork: [docs/domain.md](docs/domain.md).
- **Day-level activity aggregate.** Escape hatch if a self-history ever grows
  too large to scan: `{count, self_count, sum_len, long_count}` maintained on
  append like `log_conversations`. The partial `idx_entries_self` is expected to
  suffice. Groundwork: [docs/activity.md](docs/activity.md).

## Core simplification

- **Make backpressure explicit and typed.** `Session.request` silently blocks
  the caller until `done`; decide per path (reject command dispatch as `busy`,
  drop-and-mark events) and surface a named error instead of
  `not_running`/`nil`/`false` depending on the wrapper.
  `internal/session/session.go`.
