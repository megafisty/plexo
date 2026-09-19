# Core ↔ client protocol

Bidirectional WebSocket: commands from the browser, events from the core. HTTP
serves the app shell/assets and request/response reads (history, ads, presence).
The socket negotiates `permessage-deflate` (context takeover) when the browser
offers it; browsers that do not (e.g. Safari) simply stay uncompressed.

> **Deployment invariant — core and client are one artifact.** The browser UI is
> embedded with `go:embed` and served by the same binary as the Go core, so in
> production they are always the same version and **cannot drift**. There is no
> supported "older client": protocol compatibility shims, version negotiation,
> and legacy fallbacks are not required. A browser may briefly hold a stale
> cached asset after an upgrade, but a reload resolves it; treat the wire as
> single-version. The `hello`/`v` field is for diagnostics, not negotiation.

## Envelope

```
// server -> client
{ "t":"hello",         "d":{ "v":1, "resumed":true } }
{ "t":"snapshot",      "d":{ /* sessions + conversation summaries */ } }
{ "t":"account_state", "d":{ "status":"ok", "characters":[...] } }
{ "t":"batch",         "d":{ "events":[ ...events... ] } }
{ "t":"ack",           "cid":"u-42", "d":{ ... } }
{ "t":"err",           "cid":"u-42", "d":{ "code":"...", "msg":"..." } }

// client -> server
{ "t":"subscribe", "d":{ "id":"sub-..." } }   // mandatory first envelope
{ "t":"cmd", "cid":"u-42", "d":{ "op":"send_message", "session":"Vix", ... } }
{ "t":"cmd", "cid":"u-43", "d":{ "op":"set_interest", "session":"Vix",
                                 "conv":"official:Frontpage", "level":"full" } }
```

- **Durable subscriptions.** The first envelope a client sends **must** be
  `subscribe`, naming a client-generated `id` stable for the life of one client
  `Transport` (a page reload starts a new one). The bridge keys the broker
  subscription by that id: a socket drop detaches it and starts a short grace
  window, and a reconnect with the same id reattaches with its interest intact.
  A new id or an expired grace starts a fresh subscription at the default
  `summary`, and the server's `hello{resumed:false}` tells the client to
  re-assert interest. There is no older-client path: the UI is embedded in this
  binary and cannot drift.
- **Snapshot + keyed resync.** A fresh subscription gets a `snapshot` (sessions
  and conversation summaries only, never histories, so connect cost is
  independent of history size). Friends/bookmarks and ignores are account-wide,
  so the snapshot carries them once at the root rather than repeating them on
  every session. A resume gets no snapshot: the subscription kept
  its interest and buffered the events that arrived during the blip. A delivery
  gap — a batch the consumer could not take, or the subscription's own queue
  overflowing — is recorded as a set of dirty state keys and conversations; the
  subscription then re-sends the latest value for each dirty key from the
  broker's shared state store and re-materializes each dirty conversation. No
  full snapshot is involved.
- **Server limits.** The snapshot carries `chatMax`/`privMax` — the server's
  `chat_max`/`priv_max` byte limits, `0` until `VAR` reports them — so the
  client can count bytes and warn before a send is rejected as `too_long`.
- **Batching.** Flush on a short tick (~16–50 ms) so a burst is one frame and one
  redraw.
- **Backpressure.** Bounded per-client queues; a slow client's dropped events
  are coalesced latest-wins and resynced by key. The hub never blocks.
- **Optimistic sends.** The client renders pending messages keyed by `cid`; the
  server acks the canonical message or an error. A successful ack marks the row
  `sent` but keeps the client's cid mapping until the self copy arrives.
- **Sender's own copy.** F-Chat does not echo a message to its sender. The core
  records the sender's copy when it accepts `send_message`, assigning the
  canonical `conv_seq`/timestamp and copying the command `cid` onto the payload;
  that self `message` retires the client's optimistic entry by cid. A self
  message with no cid (our character active on another connection) is a normal
  message: it is appended, never matched against a pending row.

## Events and entries

Each batch `d` is `{ "events":[...] }`. An event is `{ kind, payload }` with
`kind`:

- `message` — an append-only stream entry for one conversation.
- `state` — a set-to record addressed by a single flat key.
- `conv_view` — a conversation materialization; ordered with the stream (a view
  always precedes the entries that raced its build) and never coalesced.
- `error` — transient, never coalesced or resynced.

The reporting session is not repeated on the event wrapper: `message` entries
carry it, `conv_view` carries it, and a `state` key encodes it as its first path
segment past the namespace (`conv/`, `summary/`, `typing/`, `search/`,
`invites/`) or in its
whole `rest` (`session/<character>`). `error` is the one payload that names its
session explicitly, so the client can clear a pending conversation open.

A `state` payload is `{ "key":"...", "value":{...}, "removed":true }`. `removed`
marks a key that no longer exists; for `session/<character>` the client drops the
whole session subtree. The key encodes the scope, and is both the coalescing
identity and the unit of resync:

| key | value | delivered to |
| --- | --- | --- |
| `account/friends` `account/ignores` `account/catalog` | set-to payloads | every subscriber |
| `session/<character>` | `SessionStatePayload` | every subscriber; a removal drops the session |
| `invites/<character>` | `InvitesPayload` | every subscriber; pending room invitations, set-to |
| `conv/<character>/<kind:id>` | `ConvStatePayload` | interest ≥ summary; a removal means left/gone |
| `summary/<character>/<kind:id>` | `SummaryPayload` | interest == summary only |
| `typing/<character>/<kind:id>/<name>` | `TypingPayload` | interest == full |
| `character/<name>` | `PresencePayload` | the character is watched |
| `search/<character>` | `SearchNotice` | every subscriber |

Account-wide sets are stored once and de-duplicated by the broker: a second
session reporting the same FRL/IGN is not re-fanned. Friend de-duplication is by
**name set**, so a report that only refreshes inline presence is not forwarded;
friend presence streams as `character/<name>` records (the snapshot still
carries it inline for hydration).

A conversation record is upserted on the client. The core emits it only for a
conversation the character is in, so there is no out-of-order resurrection to
guard against, and a removal deletes the conversation and its loaded window.
Conversation metadata carries `role`, the reporting session's room-scoped
authority (`none`/`mod`/`owner`; absent for DMs and broadcasts), so the client
can offer management affordances without a fetch. Global-moderator status is
*not* part of `role`: it is a property of the character and arrives as
presence `admin`, since it is independent of the room. The description is
**sparse**, not set-to: it is present only when it changed, an explicit empty
string clears it, and an omitted field means the client keeps its copy. This
keeps a room's (possibly large) description off every roster and mode update;
a fresh client gets it when the conversation materializes (`conv_view`).

Timeline entries (`message`, `conv_view.window`, `history.entries`) carry a
sanitized `html` fragment; the raw `body` is stored but never delivered. Times
are epoch milliseconds, so the client parses a number rather than a date string.

```json
{ "id":"...", "session":"Vix", "conv":{"kind":"official","id":"Frontpage"},
  "convSeq":42, "kind":"msg", "speaker":"Kira",
  "html":"<b>hi</b>", "createdAtMs":1730000000000, "receivedAtMs":1730000000000 }
```

Ephemeral `typing` carries `{ conv, character, on, paused? }`. F-Chat's `TPN`
is DM-only, so `conv` is always the typist's DM; `paused` marks text waiting to
be sent while the typist is not actively typing. Outbound, `send_typing` uses
the same `status` values: the composer emits `typing` on the first keystroke, a
single `paused` after 5 s of idle, and `clear` when the box empties. There is
no keep-alive re-send (matching Horizon); clients retire the indicator on the
explicit status, a delivered message, `FLN`, or, client-side, on releasing the
conversation. Because nothing refreshes it, the receiving client keeps a
5-minute TTL as a last-resort safety net for a lost `clear`; it is longer than
any plausible unbroken stretch of typing, so the bar cannot vanish mid-post.

## Demand-driven delivery

Persistence is unconditional; **live delivery follows interest**, so a weak
client only receives what it renders.

- Per-subscriber interest per conversation: `none | summary | full`;
  `set_interest` is last-write-wins.
- `summary` is delivered as a `summary/<character>/<kind:id>` state record
  carrying title/`lastActivity` and the `highlight`/`self` flags, no bodies
  (`self` lets a background conversation tell the user's own copy from incoming
  traffic, since it never sees the `message` entry). A `full` subscriber does
  **not** also receive it: the `message` entry carries the same flags and drives
  unread and the attention sound, so the summary is delivered only at the
  summary tier.
- `full` receives `conv/<...>` metadata, `typing/<...>` records, and rendered
  `message` entries; enabling it triggers a `conv_view` materialization. A
  re-assert that supplies the client's `since` cursor instead sends a **delta**
  view: only the entries after that `conv_seq`, no `members` (conversation
  metadata streams at summary interest too), which the client merges into its
  retained window. A gap larger than one window falls back to a full view, so
  the client can rebuild coherently.
- `conv_view` is one composite (`meta`, `members`, `ops`, recent `window`,
  `cursor{asOfSeq, oldestSeq, hasOlder}`, `delta`). The full view carries the
  room `ops` (the same set a live `conv/<...>` record carries) so a fresh client
  seeds the moderator marks without waiting for the next metadata event; a
  delta view omits `ops` and the client keeps the set it already holds. The core
  buffers live full events while materialization is in flight, then emits the
  view followed by the buffered events — the client never sees torn state and
  buffers nothing.
- **Presence is scoped**: `character/<name>` records are delivered for a `full`
  conversation's members and the session's own character, plus account-wide
  friends/bookmarks that are watched globally (so late subscribers get them;
  `LIS` is authoritative and not streamed row by row). `GET /api/presence`
  queries the full online roster.
  Presence/member payloads carry `admin` (`ADL`/`AOP`/`DOP`, set-to, always
  present); conversation payloads carry `ops` the same way, so an empty list
  clears the marks.
- **Friends/bookmarks** are account-wide and never client-managed. `FRL` (the
  documented union) is captured per session connection and published as
  `account/friends` (`{ friends: [MemberInfo] }`) and once at the snapshot root;
  realtime
  bridge `RTB` frames (`trackadd`/`trackrem`, `friendadd`/`friendremove`) update
  the union and re-publish. The broker stores the set once and de-duplicates it,
  so a second session reporting the same list is not re-sent, and the client
  applies one record to every session. De-duplication is by **name set**, so a
  report that only refreshes inline presence is not forwarded; friend presence
  streams as `character/<name>` records (the snapshot still carries it inline
  for hydration).
- **Ignore list** is account-wide from `IGN` (`init` plus `add`/`delete`),
  published as `account/ignores` and once at the snapshot root; clients change
  it via `set_ignore`. Like friends, it is stored once and de-duplicated.
- **Character search** is per-session. `POST /api/search` queues an `FKS` on
  the session's connection; the server's `FKS` reply is enriched with the
  presence the session already holds (name, gender, status, rendered status
  message, online), cached on the session as its latest result set, and
  announced as a `search/<character>` state record. Enrichment is read-only and does not seed
  roster entries, and a cached row's presence is never re-enriched: the result
  set is a point-in-time match against the online roster. The event is only a
  notice (`revision`); clients pull the rows with `GET /api/search` (see below),
  so a large enriched payload is not fanned out over the event socket. `ERR 18`
  ("no results") is a successful empty search and caches an empty set; `ERR 50`
  (throttle) and `72` (too many) stay errors. A disconnect clears the cache (its
  presence came from that connection's roster) and announces the change; nothing
  is persisted, and rapid searches coalesce latest-wins.
- **Channel catalog** is core-wide, never persisted, delivered to every
  subscriber. After the first session goes live — re-checked on every `PIN` —
  the core requests `CHA` (once per process) and `ORS` (refreshed when >30 min
  old) and publishes one `account/catalog` record; the snapshot carries it as
  `catalog`.
- **Release**: dropping a timeline downgrades interest to `summary`; the client
  keeps the window (bounded, least-recently-used) so re-selecting the
  conversation asks for a delta over the missed entries rather than a fresh
  newest-window materialization.
- **Reconnect**: interest lives on the durable subscription keyed by the
  client's subscribe id, so a socket blip reattaches with interest intact and
  the client does nothing. Only a fresh subscription `hello{resumed:false}`
  makes the client re-assert `full` for each session's active conversation
  (`resubscribeActive`) — a page reload or an expired grace window. A transient
  blip never silently stalls live delivery.

## Command catalog

Authoritative op list: `internal/model/commands.go` (`model.Commands()`), which
also assigns each op its `layer` and `scope`; the core rejects any other `op`.
`layer` handles it, `scope` is what it acts on, and `fields` is the command's
payload contract (enforced by the handler, not by the catalog).

| op | layer | scope | fields | purpose |
| --- | --- | --- | --- | --- |
| `set_credentials` | account | global | `account`, `password` | Validate credentials and optionally persist them (`remember`); emits `account_state` |
| `clear_credentials` | account | global | — | Forget in-memory credentials + cached ticket; stored credentials are left for the next restart |
| `purge_credentials` | account | global | — | Delete stored credentials; running sessions keep their in-memory pair until restart |
| `list_characters` | account | global | — | Re-emit `account_state` |
| `login` | manager | session | `character` | Start a session |
| `logout` | manager | session | `session` or `character` | Stop and remove a session |
| `reconnect` | manager | session | `session` | Re-run the connect flow |
| `send_message` | session | conversation | `session`, `conv`, `body` | Send a channel or private message |
| `send_lrp` | session | session | `session`, `body` | Emit an LRP advertisement |
| `send_typing` | session | conversation | `session`, `conv`, `status` | Signal `typing`/`paused`/`clear` for a DM |
| `join` | session | conversation | `session`, `conv` | Join a channel or room |
| `leave` | session | conversation | `session`, `conv` | Leave a channel or room |
| `set_status` | session | session | `session`, `status` | Change status |
| `set_ignore` | session | session | `session`, `action` | Block, unblock, or list |
| `set_tracked` | session | conversation | `session`, `conv`, `tracked` | Show or hide a DM in the client's conversation list |
| `room_admin` | session | conversation | `session`, `room.action` | Create a room or administer one: describe, add/remove mod, kick, ban, unban, destroy, mode, visibility, set_owner, invite, timeout; `room` carries the action and its parameters |
| `dismiss_invite` | session | conversation | `session`, `conv` | Drop one pending room invitation so it is not offered again |
| `set_interest` | broker | conversation | `session`, `conv`, `level`, `since?` | Set live delivery interest; `since` asks for a delta re-entry |

`layer: session` commands route to the session actor; every other layer is
handled by the core or the subscription. Results are an `ack` or `err` keyed by
`cid`.

## HTTP read endpoints

Plain `GET`s guarded by the session cookie; kept off the event socket so a large
or paginated payload cannot delay live events. A response may carry a short text
rejection body. The client treats every request as fallible: it bounds each call
with a timeout, and a transport error, non-2xx, or malformed body is a normal
null/empty result rather than an exception.

```
GET /api/history?session=&conv_kind=&conv_id=&before_seq=&after_seq=&limit=
    -> { session, conv, entries:[...] }   // rendered entries, newest by default
GET    /api/warpmarks?session=           -> { warpmarks:[ {...} ] }
POST   /api/warpmarks   { session, entryId, label }   // create or replace
DELETE /api/warpmarks?session=&entryId=
GET /api/logs/index                      -> { characters:[...] }
GET /api/logs/index?conversations=1      -> { conversations:[ {kind,id,name} ] }
GET /api/logs/index?session=<char>       -> { conversations:[ {kind,id,name} ] }
GET /api/logs/index?conv_kind=&conv_id=  -> { characters:[ {session,kind,id,name} ] }
GET /api/logs/coverage?session=&conv_kind=&conv_id=
    -> { count, firstMs, lastMs, firstSeq, lastSeq, name }
GET /api/logs/activity?session=&conv_kind=&conv_id=&tz=&scope=
    -> { scope, unitMs, total, buckets:[ {startMs,count} ], participation? }
GET /api/logs/activity?session=&conv_kind=&conv_id=&tz=&scope=&from=&to=&gap_min=
    -> { scope,
         sessions:[ {startMs,endMs,count,volume,longCount,activeMs,rp} ],
         summary:{ startMs,endMs,count,volume,longCount,activeMs,rp } }
GET /api/logs/export?session=&conv_kind=&conv_id=&from=&to=&tz=
    -> text/html   // streamed, self-contained chatlog; inline; no-store
POST /api/logs/cleanup           { op, session?, kind?, id?, days?, maxEntries?, apply }
    -> { conversations, entries, warpmarks, bodyBytes, vacuumed, bytesReclaimed }
                                    // op is age | conversation | dms; apply false previews
GET /api/ads?session=            -> [ { character, channel, message, receivedAt } ]
                                    // message is rendered HTML, never raw BBCode
GET /api/presence?session=&q=&gender=&status=&limit=
    -> [ { name, gender, status, statusMsg, admin, online } ]
GET /api/room?session=&conv_kind=&conv_id=
    -> { conv, title, description, rawDescription, mode, owner, ops:[...],
         selfRole, bans:[...], cdsMax, titleMax, visibility }
                                    // on-demand room management view; never streamed
                                    // description rendered HTML, rawDescription BBCode
GET /api/mapping
    -> { <field>: { name, field, idtype, entries:[ { name, id } ] }, ... }
POST /api/render                { bbcode }   -> { html }
                                    // one-off BBCode render, never cached
```

`/api/logs/index` is the chatlog browser's two-sided navigation index, served
entirely from the `log_conversations` aggregate so it never scans the timeline.
It is resolved one end at a time: with no parameters it lists own characters
that have persisted history; with `conversations=1` it lists every conversation
across them (deduped by kind and case-folded id); with `session` it lists one
character's conversations; and with `conv_kind` and `conv_id` it resolves the
reverse direction to the own characters with history in that conversation.
`conv_id` matches case-insensitively. Each reverse result carries the session's
exact stored id, so it addresses `/api/logs/export` without re-deriving casing.
Broadcasts are never listed.

`/api/logs/coverage` reports a conversation's persisted span so the browser can
narrow the export range before committing to it. `/api/logs/export` streams a
self-contained HTML chatlog (`from`/`to` are required inclusive epoch-
millisecond bounds, `tz` is the display offset in minutes east of UTC); it is
served `inline` with a `Content-Disposition` filename and `no-store`. It renders
through an uncached path (see [rendering.md](rendering.md)) so a large artifact
never evicts the live BBCode cache. No live session is required: all three read
the store directly.

`POST /api/logs/cleanup` is the Cleanup tab's only write. `op` selects the
rule: `age` deletes every entry older than `days` (optionally narrowed to one
`session`); `conversation` deletes one `session`/`kind`/`id` entirely (`days`
absent or zero) or just its older entries; `dms` deletes whole DM conversations
inactive for `days` that hold fewer than `maxEntries` entries. `apply` false
previews, reporting the conversations, entries, body bytes, and warpmarks at
stake; true performs the deletion, rebuilds the `log_conversations` aggregate in
the same transaction, and vacuums when enough free pages accumulated, returning
`vacuumed` and `bytesReclaimed`. Warpmarks on deleted entries cascade, so the
preview's `warpmarks` count is a data-loss warning. Deleting a conversation's
history does not touch live session state.

`/api/logs/activity` is the export range's activity model, for the bursty
conversations where a plain span hides when the interesting stretches were. It
has a **scope**: `conversation` measures every speaker (DMs and small rooms),
`self` measures only our own posts (channels and large rooms). `scope` defaults to
`auto` and is resolved from the conversation kind and its recent participants;
the response echoes the resolved scope. Without `from`/`to` it returns the whole
span as zero-filled per-local-day counts (`unitMs` is one day, `tz` is the
display offset in minutes east of UTC): the overview, with the `participation`
estimate when a room was decided by it. With `from` and `to` it segments that
bounded range into sessions and reports their intensity (`gap_min` overrides the
tight gap). A roleplay core is a run of messages long enough to stand out from
the conversation's own median length, and it is widened by a looser gap so the
light chat leading into or out of it is not sliced off. `volume` is the summed
body length, `longCount` counts messages at or above the adaptive threshold, and
`activeMs` sums only the exchange gaps, so rate is not diluted by silences.
Conversation scope reads the covering timeline index; self scope reads the
partial `idx_entries_self` index, so neither touches a body except for the
bounded drilldown. Both are served from the store and need no live session.
Range is capped at 31 days; the overview is the cheap way to choose a sub-range.
The full model and its thresholds are in [activity.md](activity.md).

`/api/history` is the live-chat backfill endpoint: rendered entries, newest by
default, addressed by `conv_seq` cursors and clamped to 1000 entries. It renders
through the shared cache and is otherwise unrelated to the log export.

`conv_kind=warp` addresses a warpmark's entry (`warp:<entry id>`) as a virtual,
read-only conversation. The core resolves it to the real conversation and, with
no cursor, seeds a tail page **ending at the mark**; `before_seq` then pages
older normally and `after_seq` is rejected, because a warp window has no newer
side. Reads need no live session and the warp kind never reaches the timeline,
log, export, or activity tables. `/api/warpmarks` stores a character's private
annotations (`label` optional, capped at 128 characters); a snippet is rendered
through the uncached path, so opening the list never evicts the live cache. See
[warpmarks.md](warpmarks.md).

`statusMsg` and conversation `description` are rendered HTML, like entry `html`.
`limit` is clamped (history default 100 / max 1000; presence default 100 /
max 500). `before_seq`/`after_seq` are optional `conv_seq` cursors.

`GET /api/room` is the on-demand management view of one joined channel or room:
`owner`, the op list, the caller's `selfRole`, the observed ban list,
`visibility` (best-effort; it changes only through `RST`, which the server does
not broadcast), and the title/description/byte limits. `description` is the rendered HTML for
display and `rawDescription` the editable BBCode source, so the management pane
can prefill its editor without double-escaping. It is deliberately
**not** streamed as conversation state — only `role` rides the conversation
record — so the owner, op, and ban detail is fetched once when a management pane
opens. Bans are the session's best-effort in-memory set (from `CBU`/`CTU`
broadcasts and local unban acks), not an authoritative server read. An unknown
session answers `404` and a room the session is not in answers `409`.

Pending room invitations are a session-scoped set-to list at
`invites/<character>` (`InvitesPayload`), seeded inline in the snapshot so a
fresh client renders them. The core has no way to query invitations — the
server sends `CIU` once — so accepting (the self `JCH`) or `dismiss_invite`
retires one. The client renders this list as a client-only virtual
conversation (see [ui-state.md](ui-state.md#room-invitations)); the core knows
nothing of that pane. Publishing a room (`room_admin` `visibility: public`) also updates
the core-wide catalog immediately, since `RST` has no server broadcast.

`POST /api/render` renders one raw `bbcode` fragment to its HTML and returns
`html`. It is the UI's on-the-fly check for a draft: it parses through the
renderer's **uncached** path (`model.RenderUncachedHTML`), so a preview can
never evict the live BBCode cache's hot entries, and it needs no live session.
An empty body renders to an empty string. It is POST-only (a fragment can exceed
a URL) and no-store.

`/api/mapping` is the core's cached, precomputed search field mapping: one
field per FKS filter (`kinks`, `genders`, `orientations`, `languages`,
`furryprefs`, `roles`), each carrying a display `name`, the FKS payload `field`
to populate, an `idtype` (`number` for kinks, `string` otherwise), and the
selectable `entries`. The client iterates the fields and renders a multi-select
for each. It is derived once at core start from F-List's raw mapping tables
(kinks and the `gender`/`orientation`/`languagepreference`/`furrypref`/`subdom`
list values), is read-only, core-wide, and never persisted; before the first load
lands it answers `503`.

`POST /api/search?session=` has an FKS filter set as its body (`kinks`,
`genders`, `orientations`, `languages`, `furryprefs`, `roles`; the same shape
`/api/mapping` describes). It queues `FKS` on that session and answers `202` on
acceptance (`404` unknown session, `400` malformed body, `409` not ready). The
reply is cached on the session and announced as the `search` event.
`GET /api/search?session=` returns that cached set (`characters` plus
`revision`), so clients pull the enriched rows over HTTP rather than the event
socket; an untriggered session answers an empty set at revision `0`. See
[fchat.md](fchat.md) for the wire command.

## Session state and errors

Session lifecycle is the `session/<character>` state record:
`{ state:"connecting"|"live"|"disconnected", reason?, severity?, autoRetry? }`.
A removal (on logout) drops the whole session on the client. `error` stays a
distinct transient event: `{ code?, message? }`.

Per-command F-Chat `ERR`s (rejected join, flood limit, unknown channel) stay
inside a batch and do **not** end the session. Only connection-fatal ERRs (ban,
takeover, kick, timeout, server full, `TOO_MANY_FROM_IP`, …) produce a
`session/<character>` disconnected state.

## Auth and account

```
GET    /api/session   -> { "authRequired": bool, "authenticated": bool }
POST   /api/session   { "password": "..." }  -> 204 + HttpOnly cookie
DELETE /api/session   -> 204
```

`POST` sets a persistent HttpOnly cookie (an HMAC of the password, not the
password). On boot the client calls `GET`; `authenticated:true` skips the gate.
Auth is disabled when no password is configured, and loopback callers are always
auto-skipped.

```
{ "t":"account_state", "d":{ "status":"missing|checking|ok|invalid|unreachable",
                             "characters":[...], "reason":"...", "persisted":true } }
{ "t":"cmd", "cid":"u-45", "d":{ "op":"set_credentials",
                                 "account":"...", "password":"...", "remember":true } }
{ "t":"cmd", "cid":"u-46", "d":{ "op":"clear_credentials" } }
{ "t":"cmd", "cid":"u-47", "d":{ "op":"purge_credentials" } }
```

Credentials travel browser → core once; they are never returned and no ticket
reaches the client. `set_credentials` triggers a mint; the resulting
`account_state` carries the character list for the session picker. When
`remember` is true a pair that mints successfully is stored unencrypted so the
core can restore it after a restart; `persisted` reports whether such a document
exists (it never carries the values). `clear_credentials` is a memory-only
forget; `purge_credentials` deletes the stored document and is what the Config
editor's "Forget credentials" button sends.
