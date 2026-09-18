# Domain model and persistence

The domain is **character-centric**, never account-centric.

- **Our character** — a session; a UI tab.
- **Other character** — the only representation of another player. F-Chat never
  exposes the owning account, so there is no "person" entity or contact merging.
- **Conversation** — official channel, room, or DM.
- **Account / credential** — a login implementation detail, never surfaced.

## Account scope

A core manages **exactly one F-List account**, which may have several connected
characters (sessions). Some F-List features are **account-wide** and therefore
shared across every session — most notably the **friends and bookmarks lists**
(the server's `FRL` union) and the ignore list. Model these as account/core
state, not per-character state. Their presence is watched globally so every
subscriber sees it (see [core-protocol.md](core-protocol.md)).

## Conversation identity

Identity and display are separate; commands and queries address conversations by
`conv_id`, never by display name.

- **Official channels** — unique name; `conv_id` = case-folded name.
- **Rooms** — `conv_id` = internal hash, a mutable human-readable title (stored
  per entry as `conv_name`). A vanished-and-recreated room is a *different*
  conversation and must not merge history.
- **DMs** — the character pair (`our_char`, `other_char`).

Conversations are session-owned, in-memory state; only their persisted history
(in `timeline_entries`) and the derived `log_conversations` aggregate reach the
database.

## Timeline

Embedded **SQLite** (`modernc.org/sqlite`, no CGO), WAL, single writer; the only
history, since F-Chat has no server-side search.

```sql
timeline_entries(
  id            TEXT PK,     -- sortable: ms timestamp + random suffix
  upstream_id   TEXT,        -- dedup/reconciliation
  session_char  TEXT,
  conv_kind     TEXT,
  conv_id       TEXT,
  conv_name     TEXT,        -- readable title; room name for ADH-... rooms
  conv_seq      INTEGER,     -- monotonic per (session_char, conv_id)
  kind          TEXT,        -- 'msg'|'dm'|'rll'|'broadcast'
  speaker       TEXT,
  body          TEXT,        -- raw BBCode; re-renderable
  data          TEXT,        -- NULL, or the raw JSON payload for structured kinds
  created_at    INTEGER,     -- upstream/observed ms
  received_at   INTEGER      -- local receipt ms
)
-- index: (session_char, conv_kind, conv_id, conv_seq, created_at)
--   for history paging, the streaming log export, and range counts.
-- partial index: (session_char, conv_kind, conv_id, created_at, length(body))
--   WHERE speaker = session_char, for the self-scoped activity view; own posts
--   store speaker = session_char exactly. See docs/activity.md.
-- log_conversations is a derived aggregate, one row per (session,
-- conversation): the log browser reads only it, so navigation never scans
-- the timeline. It is maintained on append and rebuilt by ClearHistory.
log_conversations(
  session_char, conv_kind, conv_id,
  name         TEXT,     -- latest non-empty room title
  entry_count  INTEGER,
  first_ms, last_ms, first_seq, last_seq,
  PRIMARY KEY (session_char, conv_kind, conv_id)
)
-- index: (conv_kind, lower(conv_id), session_char)
--   for the reverse lookup that resolves a conversation back to its
--   own characters.
```

- **`conv_name` makes rooms human-readable.** A room's `conv_id` is an opaque
  `ADH-...` hash, so an entry also carries the room's title at receive time.
  Official channels and DMs already use a readable `conv_id` and leave it empty.
  It is persistence-only and never sent to clients (they get the title from the
  conversation metadata). The `log_conversations` aggregate keeps the latest
  non-empty title and exposes it as the room's display name.
- **Channel identity is case-insensitive.** F-Chat lowercases channel lookups,
  and several relayed frames (op list, description, mode changes) echo the
  caller's casing rather than the canonical `ADH-...` name. The session keys
  official channels and rooms without regard to case and keeps the first-seen
  spelling, so a lowercase `adh-...` frame cannot fork the real room into a
  phantom official channel.
- **Plain bodies store decoded BBCode; structured kinds store their payload.**
  `body` is the decoded BBCode the renderer consumes for text kinds, so a parser
  change can re-render history without a second copy. Structured kinds (today
  `rll`, dice/bottle results) keep the **whole raw server payload** in the
  nullable `data` column; it is persistence-only and never delivered, so the
  renderer can change without lossy re-ingestion. See
  [rendering.md](rendering.md).

- **`conv_seq` is the shared cursor**: a monotonic per-conversation sequence,
  assigned by the session actor and persisted, so history pages and live events
  share one ordering for paging, refill, and dedup. There is no per-session
  sequence.
- **Store once per session**: a message witnessed by two of our characters is
  stored twice, mirroring the official model.
- Sortable ids; keep upstream time distinct from local receipt time.
- **Retention is indefinite until the user prunes.** The Logs dialog's
  Cleanup tab deletes by age, by one conversation, or sweeps one-off DMs
  ([core-protocol.md](core-protocol.md)). A prune rebuilds `log_conversations`
  in the same transaction and cascades to warpmarks. Shape `body` so FTS5 can
  be added later; do not build it yet ([TODO.md](../TODO.md)).

## Warpmarks

A **warpmark** is a private, per-character annotation on one stored entry: a
label the user recognizes, bound to an `id` the core can always resolve. It is
not shared content; it is the one deliberate exception to the persist rule
below, stored per session like history. See [warpmarks.md](warpmarks.md).

```sql
warpmarks(
  session_char TEXT, entry_id TEXT, label TEXT,
  conv_kind TEXT, conv_id TEXT, conv_name TEXT,   -- snapshot at mark time
  speaker TEXT, created_at INTEGER,
  PRIMARY KEY (session_char, entry_id),
  FOREIGN KEY (entry_id) REFERENCES timeline_entries(id) ON DELETE CASCADE
)
-- index: (session_char, created_at) for the per-character list.
```

The row snapshots the entry's conversation, speaker, and mark time so the list
reads without a join; the entry is joined back in only to resolve the anchor
and render a snippet. The foreign key makes a mark share its entry's lifetime:
`ClearHistory`, or the per-conversation / prune-by-age / one-off-DM cleanup
in the Logs dialog, deletes the entries
and their marks together. The constraint is created with the table, so a fresh
database gets it; SQLite foreign keys are enabled on the connection, since they
are off by default. A mark whose entry is gone anyway (an out-of-band edit, or
the in-memory test store) still lists from its snapshot but cannot be opened.

## Configuration

Configuration is stored in the database (`configs`), not a file, so it is
transactional with history and needs no deploy-time path wiring. Each document
is one row keyed by name: the global (account-wide) document under the reserved
key `!global`, and a character document under the lowercased character name.
All keys beginning with `!` are reserved and a character name may not use one.
The two scopes are **disjoint** — no field exists in both — so there is no
merged document.

```sql
configs(
  name       TEXT PK,  -- '!global', or a lowercased character name
  data       TEXT,     -- JSON document
  updated_at INTEGER
)
```

The store treats `name` as an opaque key; the mapping and document types live in
`internal/config` (`{"password"}` globally, `{"highlights","autoJoin","autoStatus"}` per
character, and `{"account","password"}` under the separate reserved key
`!credentials`). The credentials document is deliberately not part of the
settings view: the typed settings API never reads or returns it, and it is
written only after F-List accepts a mint. It is stored unencrypted. Missing
documents are not errors (they mean "defaults"); a malformed
document is a hard error, so a bad write fails loudly. The shared password is
applied by the web gate at startup; character highlights are applied at login and
re-applied to live sessions by a reload, never persisted into session state.
`--reset-config` clears the table. The typed read/write API, limits, and write
semantics are in [settings.md](settings.md).

## Persistence rule

Persist **durable, shared content** other players can see: channel/room
messages, DMs, dice/bottle (`rll`) results, broadcasts.

Do **not** persist presence, membership, op lists, unread/read cursors, typing,
LRP ads, or per-connection notices. State is re-hydrated on reconnect; on an
outage intervening messages are lost, with no backfill and no marker.

## LRP ads

A **per-session sideline**, not part of any conversation. Each session keeps a
bounded in-memory buffer (200 ads, FIFO, deduped by poster). Ads are never
pushed live; the client requests them on demand, and the snapshot carries only a
per-session count. The session renders each advertisement's BBCode when it
arrives, so the ad's `message` is display HTML from then on and the client never
parses it; the raw text is not retained. A room's opaque `ADH-...` channel id is
replaced by the room's readable title when the session already knows it (the
same resolution entries apply to `ConvName`).
