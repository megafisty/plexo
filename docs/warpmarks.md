# Warpmarks

> **Status:** wired. The `warpmarks` table, the `warp` conversation kind and
> HTTP endpoints, the read-only warp pane, and the delegated timestamp click are
> implemented. This note is the reference for the model, the virtual
> conversation, and the invariants.

A **warpmark** is a private, per-character annotation on one stored timeline
entry: `(session_char, entry_id, label)`. Clicking a message's timestamp marks
it; opening a mark activates a **virtual, read-only conversation** whose window
ends at the marked message, so the mark lands at the bottom of an ordinary
pinned timeline.

Warpmarks subsume per-entry permalinks: the `warp:<entry_id>` identity is the
permalink, and the same resolver serves both. There is no separate permalink
feature.

## Why it exists

F-Chat has no server-side logs, bookmarks, or permalinks, and Plexo keeps
history indefinitely ([domain.md](domain.md)). The only way back to an
interesting message was scrolling or the chatlog export. A warpmark is the
smallest durable pointer into that history: a label the user recognizes, bound
to a message id the core can always resolve, and a way to read it in its
conversation.

Warpmarks are **not** F-List bookmarks (the server-side friend/bookmark list).
The name is deliberately distinct; do not overload "bookmark".

## Model

A warpmark is one row in `warpmarks`; the schema and its lifetime (`ON DELETE
CASCADE`) rules are in [domain.md](domain.md#warpmarks). The feature-level rules:

- **One mark per message, label replaced on re-mark.** The
  `(session_char, entry_id)` tuple is the identity; re-marking edits.
- **Labels are optional.** An empty label renders as `speaker · clock`. Cap at
  128 chars, matching the limits in [settings.md](settings.md).
- **The entry's context is snapshotted at mark time** (`conv_*`, `speaker`), so
  the list is self-contained; `conv_seq` and the rendered snippet are resolved
  from `timeline_entries` on read.
- Marks are **private annotations**, the one deliberate exception to the
  persist-shared-content rule in [domain.md](domain.md).

## Creating a mark

`MessageRow` already emits `data-entry={entry.id}` (the DB id) and stays pure;
no per-row JavaScript is added. The timestamp span carries the same id, so the
click delegated once at the chatspace root (`clickHandlers`) reads it from the
exact clicked element instead of an ancestor lookup.

- Left-click on `span.msg-time`. The check is exact (the clicked element must
  be the timestamp), so a click on the nested send-state mark is not a hit.
- An optimistic entry is never markable: a pending send's id is
  `pending-<cid>` and its `convSeq` is a sentinel, not a DB id. The handler
  ignores any row whose entry has `send` set, and the affordance is hidden on
  pending/failed rows.
- No hit sets `redraw = false` (the rows are memoized; a stray click must not
  repaint a window).
- The label is prompted in a modal built on `components/primitives/dialog.ts`
  (the `View.modal` slot's `"warpmark"` variant); an existing mark opens in
  edit/delete mode.
- Create/delete toasts via `pushToast`.

## The warp list

A **popout** in the top bar, beside Config, built like the friends popout
(`FriendsMenu`): a **Warp** button, a full-screen transparent overlay that
dismisses on outside click, and an absolutely positioned card below the
button. It is larger than the friends list and scrolls: a header, a filter box,
a hint line, then the list. The list covers the **active session's** marks,
newest first, with label, conversation title, speaker, time, and a **rendered
snippet**. Because it is per active session, its "connect the character"
precondition is met by construction: the character's tab is already open.

The filter is deliberately naive: a case-folded substring match over the
mark's label, speaker, and conversation title/id, applied client-side on every
keystroke. There is no server query and no index.

The snippet is rendered with `model.RenderEntryUncachedHTML` (the export path in
[rendering.md](rendering.md)): a handful of one-off renders must not evict the
live BBCode cache. Message bodies never cross to the client
([rendering.md](rendering.md)), so the snippet is server-rendered like any
history page.

The list is pulled from the core on popout mount (`loadWarpmarks`), so opening
the popout or switching the active tab refetches it. `store.warpmarksRev` is a
separate revision for the list; it must never fold into `conversationsRev` or
`unreadRev`, which drive sidebar and unread memoization
([ui-components.md](ui-components.md)).

## The virtual conversation

Opening a mark activates a virtual conversation, keyed `warp:<entry_id>`. The id
is **self-describing**: the core resolves it by looking up the entry, so there is
no registration, no per-click server state, no snapshot entry, and nothing to
garbage-collect.

### Kind

`model.ConvWarp = "warp"` is a new, read-only `ConvKind`, added to the web
layer's `validConvKind` (whose only caller is `/api/history`, so warp cannot
leak into the log/export/activity endpoints, which use `validLogConvKind`). The
session rejects it for every command. Reusing `room` with a non-`ADH-` id is not
viable: `convRefForChannel` classifies any non-`ADH-` channel id as `official`,
and join/send/roster would treat a synthetic id as a real channel.

### Anchor

The marked message is the **bottom** of the window: the seed page is
`History(before_seq = anchor + 1, limit)`. No tail look-ahead.

Because the window ends at the mark, the existing machinery is correct rather
than fought:

- The pane pins to the bottom, so the mark is visible on load — no pinning
  override.
- `hasNewer` is false and `liveSeq` never advances, so there is no silent
  auto-refill and no way to be stranded mid-history.
- `hasOlder` + `before_seq` paging is `loadOlder` exactly as it already works.

### Read path

The alias is resolved in the manager, **above the store**, so the store only
ever sees real conversations:

- `warp` + no cursor → resolve the entry, then a tail page ending at its
  `conv_seq`.
- `warp` + `before_seq` → a normal older page in the resolved conversation.
- `warp` + `after_seq` → rejected; a warp window has no newer side.

A useful consequence: `parseConvKey` and `loadOlder` are already generic, so
scrolling up in a warp pane needs no new client code once the server accepts the
`warp` kind. Only the seed and the activation branch are new.

### Client activation

`activateConv` on a warp key creates an ephemeral
`store.conversations[session]["warp:<entry_id>"]` marked `readOnly: true`, sets
the active key, and issues one history fetch. It must **not** dispatch
`set_interest`, `set_tracked`, or `join`.

Read-only-ness is one capability on `Conversation`, checked once, rather than
scattered `kind === "warp"` branches:

- The pane hides `Composer` and `TypingBar`; the header action becomes **Open
  conversation** (with a secondary **Close** to dismiss the ephemeral pane).
- The "ensure materialization" `set_interest` guard (`store/interest.ts`,
  `ensureActiveInterest`) excludes warp.
- The roster is already skipped (it selects on `official`/`room`).

### Sidebar

Warp conversations get their own section, separate from channels and DMs, via
`splitConversations`/`orderedConversations`, and are **included in Ctrl+Tab
cycling**: a pane that is visible is reachable by cycle. Each mark produces one
pseudo-conversation (two marks in one channel are two panes); dedupe by
`entry_id`. Lifecycle is ephemeral: removed on dismiss, on session close, and
when its mark is deleted, and never carried in `SessionSnapshot.Conversations`.

### Open conversation

The warp pane's header offers **Open conversation**, which activates the real
conversation (join/track/live stream). This is the only path that touches the
live machinery; the warp pane itself never does.

## Invariants

- **Write-path exclusion.** `OpJoin`, `OpLeave`, `OpSendMessage`, and
  `OpSendLRP` reject a `warp` conversation in `internal/session/commands.go`, and
  `recordEntry` is never called with a warp ref. The F-Chat server can never
  emit a `warp:` id (`convRefForChannel`/`PRI` routing), so no inbound frame can
  reach one either.
- **Persistence and log exclusion.** A warp *conversation* is never written to
  `timeline_entries` or `log_conversations`, and never appears in the export or
  activity endpoints. (Its *marks* are ordinary `warpmarks` rows and are
  deleted with the entries they annotate.)
- **Offline / tab requirement.** A warp conversation is hosted by a session
  view, so opening a mark requires that character to have a connected session.
  The mark list itself is per character and can be read/edited for any valid
  character name over HTTP, matching the settings rule that character presence
  is not enforced. Browsing a mark while the character is offline is deferred.
- **Multi-client.** Marks are HTTP-only (no live event); the list refetches when
  the popout opens. This keeps the WS event set from growing one kind per
  feature.

## HTTP endpoints

The endpoint shapes — `GET`/`POST`/`DELETE /api/warpmarks` and the
warp-addressed `/api/history` alias — are listed in
[core-protocol.md](core-protocol.md#http-read-endpoints). All are guarded by the
session cookie; the list and mutations read and write the store directly, so
they need no live session for the character.
