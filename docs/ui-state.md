# UI state model

The client keeps two stores: the **domain store** (`ui/src/store/state.ts`),
whose only write paths are `applyEnvelope`/`applySendResult`
(`store/apply.ts`), and the device-local **View** (`store/state.ts`), which
holds ephemeral UI state. Every component reads through `context.ts`; nothing
else is global.

## Principles

1. Presence is global, keyed by character name.
2. Everything else is scoped by our character; a tab owns its conversations.
3. Composite keys `kind:id`, so channel `Kira` and DM `Kira` never collide.
4. The timeline holds only content; membership/presence/status are state, never
   entries.
5. Ephemeral (typing/presence) is rewritten freely; messages append.
6. Ads are a per-session sideline (see [domain.md](domain.md)).
7. One write path into the domain store.
8. Correct under F-Chat desync: every update is idempotent.

## Domain store

```ts
type Store = {
  core: CoreState
  account: AccountState
  sessions: Record<SessionId, SessionSnapshot>       // from the core snapshot;
                                                     // session/<char> state records update one
  friends: MemberInfo[]                              // account-wide, snapshot root + account/friends
  ignores: string[]                                  // account-wide, snapshot root + account/ignores
  characters: Record<CharacterName, Character>       // global presence
  conversations: Record<SessionId, Record<ConvKey, Conversation>>
  entries: Record<SessionId, Record<ConvKey, EntryWindow | undefined>>
  pending: Record<cid, PendingSend>                  // optimistic sends
  channels: ChannelsPayload                          // core-wide catalog
  search: Record<SessionId, MemberInfo[]>          // cached FKS result set
  searchRevision: Record<SessionId, number>        //   (self-contained rows);
                                                   //   newer rev wins a fetch
  warpmarks: Record<SessionId, Warpmark[]>         // per-character marks (HTTP-pulled)
  warpmarksRev: number       // bumps on a mark-list change (kept separate
                             //   from conversationsRev/unreadRev)
  conversationsRev: number   // bumps on set/title change
  unreadRev: number          // bumps on any unread/highlight change
}
// entries[session][key] === undefined means "not loaded", not "empty".
// The revisions let views memoize built vnodes (see render.ts), so an unrelated
// redraw skips whole subtrees.

type Character = {                 // PARTIAL by design
  name: CharacterName; gender?: string; status?: string
  statusMsg?: string               // rendered HTML from the core
  admin: boolean                   // always resolved by the core, not optional
  online: boolean; presenceKnown: boolean  // false = member-list placeholder
}
type Conversation = {
  key: ConvKey; conv: ConvRef; session: SessionId
  title?: string; description?: string; mode?: string  // description is rendered HTML and sparse
  members?: CharacterName[]; ops?: CharacterName[]
  unread: boolean; highlight: boolean; lastActivity: number
  typing: Record<CharacterName, { at: number; paused: boolean }>
  materialized: boolean            // true once a conv_view has arrived
  interestAsked?: boolean          // full interest dispatched, window not yet in
}
type Entry = {
  id: string; convSeq: number; kind: string; speaker: CharacterName
  html: string; time: number; self?: boolean
  send?: 'pending' | 'sent' | 'failed'; error?: string
}
type EntryWindow = {
  items: Entry[]; oldestSeq?: number; newestSeq?: number
  hasOlder: boolean; hasNewer: boolean; liveSeq?: number; rev: number
}
type PendingSend = { session: SessionId; conv: ConvRef; entryId: string }
type CoreState    = { connection:'connecting'|'open'|'closed'
                      authRequired:boolean; authenticated:boolean }
type AccountState = { status:'missing'|'checking'|'ok'|'invalid'|'unreachable'
                      characters?: string[]; reason?:string }
```

`SessionSnapshot`, `ConvRef`, and the event payloads mirror
`internal/model`; see [core-protocol.md](core-protocol.md).

## View (device-local)

Not in `Store`; never synced to the core.

```ts
type View = {
  phase: 'boot'|'core-auth'|'credentials'|'chatspace'
  focused: boolean                 // the browser tab itself has focus
  tabs: Tab[]; activeTab: string | null
  readonly activeSession: string | null   // derived from tabs, never assigned
  tabCounter: number                       // source of stable tab ids
  activeConv: Record<SessionId, ConvKey>
  invitesClosed: Record<SessionId, boolean> // user hid the invites conversation
  pendingConv: Record<SessionId, ConvKey>  // [session] link awaiting JCH
  msgPinned: Record<string, boolean>      // "session/convKey"; absent = pinned
  drafts: Record<string, string>          // "session/convKey"
  soundEnabled: boolean            // device-local (This Device)
  composerEnterNewline: boolean    // device-local send-key preference
  modal: Modal | null              // single modal slot (join/status/search/ads/logs/warpmark/command)
  popout: 'friends'|'warpmarks'|null // single top-bar popout slot
  settingsOpen: boolean            // Config view swap, not an overlay slot
  searchSelection: Record<SessionId, Record<string, Array<string | number>>>
  characterMenu: { session; name; x; y; character? } | null  // context-menu slot
  toasts: Toast[]
  coreAuthError: string | null; credentialsError: string | null
  submitting: boolean
}
```

Overlays are three single-value slots rather than a flag per dialog:
`view.modal` (one modal; the warpmark prompt carries its payload as a
`"warpmark"` variant, and the command palette names its shell as a
`"command"` variant), `view.popout` (one top-bar popout), and
`view.characterMenu` (the roster context menu). A slot holding one value makes
"only one of each is ever displayed" structural instead of a hand-kept close
list at every call site. `openModal`/`toggleModal`/`openCommand`/`togglePopout`
clear the other overlays a backdrop would hide; `dialogOpen` gates global
shortcuts on `modal`/`characterMenu` only, since a popout leaves navigation
live. Global shortcuts additionally require the active tab to be bound to a
live session (`shortcuts.ts`), so they stay inert on the character picker.

`phase` starts at `boot` (the spinner). It leaves `boot` only on the core's
`account_state` verdict: `ok` mounts the chatspace, `missing`/`invalid`/
`unreachable` reveal the credentials gate. The client therefore never flashes a
gate at a core that already holds credentials; an 8s timeout covers a core that
never answers. See [ui-components.md](ui-components.md#onboarding-gates).

Drafts are mirrored to `localStorage` (`store/persist.ts`, keyed
`plexo:draft:<session>/<ConvKey>`, writes throttled) so a reload never loses a
long post; they are message content and never leave the device. The device
preferences live in one JSON document under `plexo:device`
(`store/persist.ts`; see [settings.md](settings.md#this-device)) and seed
`View` at startup. Tab ids are stable for the tab's lifetime; `session` is
null until login binds the tab to a character.

The character-search dialog is a client-side shell over the core's
session-cached FKS results. Its query-builder selections live in
`View.searchSelection` keyed by session, and its last result set lives in
`Store.search`, pulled over HTTP (`GET /api/search`) on the `search` notice,
on dialog open, and via the "Recall last search" button. Results are
self-contained `MemberInfo` rows (the core enriches them from its LIS-hydrated
roster when the reply arrives, and never re-enriches a cached set) and are
deliberately **not** merged into the character registry, so transient search
hits never pollute it. The result set is shared across clients through the core
cache and survives a page reload for as long as the session stays connected;
the builder selections are per-client and are not persisted, and a reconnect
clears the cache because its presence came from the lost connection's roster.

## Open DMs

Which DMs the sidebar shows is a **per-session core** concern, not a client
one. The core materializes every DM it sees (and keeps its history), but that
set grows without bound; a snapshot that listed all of them would flood a fresh
client with stale conversations. Instead each session tracks the DMs the user
wants visible, and the snapshot includes only those. Channels/rooms stay
governed by membership and broadcasts always appear.

- A DM becomes tracked when the user **opens** it (roster menu, or
  `activateConv` dispatching `set_tracked`) or when **any message** to or from
  the partner is recorded. Tracking is idempotent.
- A DM becomes untracked when the user **closes** it (the header's Close
  action, dispatching `set_tracked:false`). Closing hides it but keeps its core
  state and history; the next message reopens it. Interest is downgraded to
  `summary` so bodies stop streaming.
- `set_tracked` is `LayerSession`/`ScopeConversation` and the session emits a
  conversation event (`tracked` to add, `gone` to remove) so every connected
  client stays in sync. The client needs no local tracked flag: the sidebar
  simply lists the DMs the core has sent it.

The tracked set is deliberately **in-memory**: a core restart resets every DM
to untracked, and the client rebuilds its list from messages and explicit
opens. Persisting it across core restarts is not implemented. A UI reload, by
contrast, is fully covered because the core survives it.

## Room invitations

F-Chat delivers a room invitation (`CIU`) exactly once and offers no query, so
the core captures it into a session-scoped set-to list at
`invites/<character>` and retires it when the invitee accepts (the self `JCH`)
or sends `dismiss_invite`; see [core-protocol.md](core-protocol.md).

The client mirrors that list onto `SessionSnapshot.invites` and presents it as
a **client-only virtual conversation** — not a core `Conversation`, with no
`conv_seq`, window, or history. It is reachable from the sidebar's own
"Invites" section only while the session has pending invitations. Accept joins
the room through the normal path; Dismiss dispatches `dismiss_invite`.

That conversation is closeable without dismissing anything: the header's Close
sets `View.invitesClosed`, and a room key that was not present in the previous
list reopens it. It also closes on its own when the list empties (the last
invitation is accepted or dismissed), which resets the flag. Invitations are
never persisted client-side and carry no unread or attention state.

## Unread and highlight

Unread is **client-owned** and focus-derived; the core tracks no read state at
all. A conversation carries a single unread boolean, set when a
`summary`/`message` arrives while it is not the active conversation in the
active session with the browser tab focused (`isConvFocused`,
`store/unread.ts`). Opening that pane — or the tab regaining focus with it
active — clears it.

`highlight` is a real core signal carried on the `summary`/`message` payloads:
the core matches incoming **channel** messages (official/room, never DMs or
broadcasts) case-insensitively against the character's `highlights` list. An
empty list disables it. The client ORs the flag into the conversation and
clears it with unread.

Severity is derived, not stored (`convSeverity`): `elevated` = an unread DM or
any highlight; `unread` = any other unread. Only `elevated` raises the global
marker (the `💬` document title and the severe sidebar badge). Unread and
highlight are never persisted and never cross back to the core.

## Load and mutation

- **Entry windows**: one contiguous slice, capped at `WINDOW` (~120) entries
  (`store/window.ts`), cursor-based on `convSeq` (`oldestSeq`/`newestSeq`,
  `hasOlder`/`hasNewer`, `liveSeq` = live-edge high-water). The cap trims the
  edge *opposite* the viewport: pinned to the bottom drops the oldest and
  raises `hasOlder`; scrolled up drops the newest, so a busy channel cannot
  evict the history being read. `loadOlder`/`loadNewer` merge a history page
  and re-trim; `MessageList` owns scroll/pinning and re-fills newer silently
  when the view returns to the bottom.
- **Interest/materialization**: switching away downgrades interest to `summary`
  so the core stops streaming bodies, but keeps the window (bounded per session
  by `windowLru`). Switching back sends the retained window's `newestSeq` as
  `since`, and the core returns a delta view with only the entries that arrived
  while away; merge it into the retained window, preserving older history and
  the cursor. A conversation with no retained window (a first visit, or one
  evicted by the LRU) asks for a full newest window. The core buffers live full
  events while in flight; the client buffers nothing. A conversation selected by
  the snapshot's auto-select never passed through `activateConv`, so the session
  view calls `ensureActiveInterest` (`store/interest.ts`) during render; the
  conversation's `interestAsked` flag makes that a no-op after the first request
  until the window materializes or the conversation is released, so a
  redraw-heavy pane does not re-ask the core every frame.
- **Reconnect**: interest lives on the core's broker subscription, keyed by the
  Transport's subscribe id, so a socket blip reattaches with interest intact and
  the client does nothing. Only a fresh subscription (`hello{resumed:false}`,
  from a page reload or an expired grace window) makes the client re-assert
  `full` for each session's active conversation (`resubscribeActive`,
  `store/interest.ts`). A resume delivers no snapshot and no re-materialization,
  only the batches buffered across the blip.
- **State records**: every non-stream update is one `state` event
  `{key, value?, removed?}` applied through `applyState` (`store/apply.ts`),
  which dispatches on the key's namespace (`account`, `session`, `conv`,
  `summary`, `typing`, `character`, `search`). Values are set-to, so a replayed
  or resynced record is idempotent; a `removed` record deletes the key (and, for
  `session/`, the whole session subtree). A dropped delivery is recovered by the
  core re-sending the latest value per key, so the client needs no gap
  bookkeeping.

- **Optimistic sends**: `sendDraft` creates an entry with a stable id and
  `send:'pending'`. A successful ack marks it `sent` but deliberately keeps the
  `Pending[cid]` mapping; the canonical self echo (which carries the command
  `cid`) uses it to retire exactly that row. A failure drops the mapping (no
  echo is coming). The `cid` is the only correlation: a cid-less self message is
  one our character produced on another connection, with no optimistic row to
  retire, so it is appended normally rather than matched by speaker. A window
  replaced without the echo (a dropped batch or a broker resync) prunes the
  stale mapping.
- **Single write path**: only `applyEnvelope` (events/snapshot/account) and
  `applySendResult` (send acks), plus the optimistic entry created in
  `commands.ts`, mutate the store. Components only read it.
- **Async HTTP reads** (`store/commands.ts`, `store/search.ts`) re-check the
  target after every await before writing: a session can be logged out while a
  history/mark/search fetch is in flight, and a late write would resurrect a
  window or mark list for a session that is gone (`sessionAlive`). The HTTP
  layer itself (`api.ts`) never rejects -- a transport error, a timeout, or a
  malformed body becomes the function's null/[]/false contract, so a floating
  `.then()` cannot strand a loading state.
- **Awaited commands** (`join`, `login`, `set_credentials`, `purge_credentials`)
  go through `transport/broker.ts`: each has a timeout, is failed when the
  socket closes, and is flushed on logout, so no dialog can spin on a lost ack.
  Fire-and-forget sends still use `dispatch` directly.

### Mutation contract

The skip mechanisms in `render.ts` decide "nothing changed" by identity:
`memo` keys on revisions, `pure` on record references. Both are only sound if
every change is observable that way, so the store follows one rule:

- **Records are replaced wholesale.** A change to a `Character`
  (`applyPresence`) or an `Entry` produces a new object, so reference equality
  is a complete check. `Entry` fields are `readonly`. An identical set-to
  payload (a resync, a repeated presence refresh) is a no-op that keeps the
  existing reference, so re-sent records do not churn dependent views.
- **Containers are revisioned.** `EntryWindow.rev` (and the store-level
  `conversationsRev`/`unreadRev`) must bump on every content change. Mutate
  entries only through the `store/window.ts` helpers, which bump `rev`; the
  optimistic-send ack replaces the entry and bumps `rev` rather than writing a
  field in place.

An in-place write that does not replace the record or bump a revision is
invisible to every view that memoizes on it — a stale-UI bug. When adding a
mutation, make exactly one of the two happen and say which.

## Partial presence

The full global roster is never received (`LIS` can be thousands). `characters`
holds only materialized characters. Unknown member-list/DM names resolve to a
placeholder (`presenceKnown:false`) — never treat absence as invalid. Never
render the whole map; render the active conversation's members and explicit
search/friends views. A few thousand records are fine to hold, not to render.

## Performance

Mithril re-diffs every mounted view per redraw, so bound **mounted work**.

- Only the active tab renders content; only the active conversation renders its
  timeline. Evict least-recently-viewed timelines.
- Bounded/chunked log with a hard cap (no virtualization); stable `key` =
  entry id; `content-visibility: auto` + `contain-intrinsic-size` where
  supported.
- One redraw scheduler: at most one `m.redraw()` per frame (`render.ts`);
  never `m.redraw.sync()`. Coalesce typing/presence.
- `m.trust(entry.html)` in an immutable component; no client re-parse; flat
  markup; CSS-only animation.
- Cache derived lists; never `.sort()` per frame. `ConversationSidebar` and
  `ChannelRoster` memoize against the store revisions, and a leaf presentational
  component skips its subtree via `render.pure` (`render.ts`).
- Budgets: ≤ ~1,000 vnodes in the message area, ≤ ~1,500 for the active tab;
  validate under 4–6× CPU throttle.

## Deliberately excluded

Profiles/kinks/infotags/images (linked out, not fetched); friends/bookmarks
management (the list is server-owned); server vars beyond `chat_max`/`priv_max`;
client-side BBCode parsing; a cross-session DM inbox; full-list virtualization.
