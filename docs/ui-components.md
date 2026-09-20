# UI component architecture

The client is a Mithril app under `ui/src/`, compiled by `tsc` alone to
committed ES modules (`ui/app/`) and served with `go:embed`. State model:
[ui-state.md](ui-state.md).

## Assets

`ui/index.html` plus TypeScript and vendored Mithril (`ui/vendor/`). Rebuild
with `./ui/build.sh`. Styling composition, source-vs-generated files, and the
cascade are documented in [ui-css.md](ui-css.md).

History/ads/presence/logs are plain HTTP fetches in `ui/src/api.ts`; only live
events and commands share the WebSocket.

## Layers

| Layer | Reads Store? | Mutates View? | Dispatches? | Purpose |
| --- | --- | --- | --- | --- |
| App / shell | yes | yes | yes | Gates, layout, dialog host |
| Container | yes | yes | yes | Subscribe/derive view models, handlers |
| Presentational | **no** | **no** | **no** | Pure `attrs → vnode` |
| Primitive | no | no | no | Button, badge, field, modal, scroll, combobox |

A presentational or primitive component never imports `store/`, `context/`, or
`transport/`; it takes data as attrs.

## Dataflow

```
F-Chat ─ core ─ WS ─ transport/ws.ts ── applyEnvelope(Store) ──
                    scheduler.redraw()  ◀── mark dirty ───┘
                            │
              containers re-read Store/View ─▶ presentational (pure)
                            ▲
   intent ── View ──────────┤
          └─ dispatch(Command) ── transport/ws.ts ── core
```

Two non-overlapping mutation paths: `Store` (only `applyEnvelope`/
`applySendResult` and the optimistic send in `commands.ts`) and `View` (UI
local). `dispatch(Command)` is the only way to reach the core.

## Rules

1. One-way flow: props down, intents up; no child mutates a parent or the Store.
2. Only containers touch the store/transport.
3. One write path per store (events → `applyEnvelope`; local UI → `View`).
4. One redraw scheduler (`render.ts`); never `m.redraw.sync()`.
5. `MessageRow` and the roster leaves skip via `render.pure` (attrs equal);
   content is immutable `m.trust(entry.html)`.
6. Key every list item (`entry.id`, `conv.key`, `tab.id`).
7. Memoize expensive subtrees rather than recomputing every redraw, keyed on
   store revisions (`conversationsRev`, `unreadRev`) or an entry window's `rev`
   (`render.ts`; `ConversationSidebar`, `ChannelRoster`). Use only
   `render.memo`/`render.pure`: no raw `onbeforeupdate` and no ad-hoc vnode
   caches on component state.
8. No ambient globals, except `context.ts` (the sanctioned provider) and the
   root singletons `transport/ws.ts` and `sound.ts`.
9. Siblings never import each other's state; coordinate via the store or parent.
10. One feature, one module. A module owns one cohesive responsibility and is
    named for it; it may export several components that render the same feature.
    State the responsibility in the module's header comment and delimit merged
    pieces with section banners. Split a component into its own module only when
    it is reused across features.
11. Bound the mounted set (see [ui-state.md](ui-state.md)).

## Onboarding gates

A boot probe (`boot()`, in `main.ts`) checks `GET /api/session` (persistent
cookie) and opens the WS, followed by two gates. Both gates are skipped from
core state, so loopback and already-authenticated deployments go straight to
the chatspace.

The client stays on the boot spinner while the core connection is established
and does not reveal a gate until the core's first `account_state` arrives: a
warm core that already holds credentials goes straight to the chatspace rather
than flashing the credentials gate. `applyAccount` is the sole driver — `ok`
enters the chatspace, `missing`/`invalid`/`unreachable` reveal the credentials
gate — and an 8s timeout falls back to the gate if the core never answers.

- `CoreLoginGate` (shared Plexo password) is skipped when `authRequired:false`
  or the cookie is valid; the password is never stored client-side.
- `CredentialsGate` collects F-Chat credentials and is skipped when
  `account_state.status === 'ok'`. Credentials travel browser → core once,
  never back, and never as a ticket.

## Chatspace tree

```
Chatspace
├── TopBar: SessionTabs (SessionTab ×N -> state dot + close; "+" add);
│   FriendsMenu (bookmarks -> CharacterMenu); WarpmarksMenu (active character's
│   marks in a large scrollable popout with a naive text filter -> warp pane);
│   ConfigButton; connection indicator + brand ("Disconnected — refresh")
├── SessionView (active character tab; empty-state if none)
│   ├── ConversationSidebar: channels/rooms block; DMs block; Warps block
│   │   (memoized; the visible set is core-tracked); join button -> JoinChannelDialog
│   ├── ConversationPane: ConversationHeader (title, description dialog,
│   │   leave channel / close DM, or Open conversation for a read-only warp
│   │   pane); MessageList (LoadOlder, auto-refill newer, day separators,
│   │   immutable MessageRow); TypingBubble; MessageEditor (the generic Composer
│   │   wired to the conversation: auto-grow input, BBCode bar
│   │   b/i (Ctrl/Cmd+B/I)/s/sub/sup/color/url, byte counter, send-key toggle,
│   │   send) — TypingBubble and MessageEditor are omitted for a read-only pane
│   └── ChannelRoster (channel/room active) | RosterPanel (presence search)
├── CharacterPicker (content of an unconnected tab)
├── modal slot (one): JoinChannelDialog; StatusDialog; SearchDialog (FKS
│   builder + results); AdsDialog (Search tab over the session's buffered ads
│   + dummy Post tab); LogsDialog (Export Chatlogs picker + Cleanup tools);
│   WarpmarkDialog (label prompt opened from a message timestamp);
│   RoomAdminDialog (room management, opened from a room header's Manage
│   button when the session is mod/owner);
│   CommandPalette (command shells for the active session: the main menu on
│   Ctrl/Cmd+P and the conversation jump on Ctrl/Cmd+J)
├── popout slot (one): FriendsPopout or WarpmarksPopout, rendered by its top-bar
│   button while `View.popout` names it
├── CharacterMenu (roster/friends context, its own slot)
├── SettingsView (when ConfigButton is active): ThisDeviceCard (sounds,
│   multiline); GlobalSettingsCard (password); CharacterSettingsCard ×N ->
│   Highlights; AutoJoinList (remove X / replace-with-joined)
└── toast host
```

A disconnected session is shown by its tab's state dot (a per-session reconnect
control on the tab is open work — [TODO.md](../TODO.md)). Recovering the core↔UI
socket itself is a page reload — the top bar shows `Disconnected — refresh` —
not a reconnect command.

A **warp pane** is a virtual, read-only conversation keyed `warp:<entry id>`.
It is client-local and ephemeral (never in a snapshot) and is seeded over HTTP
with a window ending at the marked message, so `MessageList`'s bottom pinning
is correct and no `set_interest`, `ConvView`, join, or send is involved. The
`readOnly` capability on `Conversation` gates its chrome: `TypingBubble` and
`MessageEditor` are omitted and the header action becomes "Open conversation". The
Pointer events are delegated once at the chatspace root (`clickHandlers`): a
`[spoiler]` toggles open, a `[session]` link focuses the room it names, joining
it first if needed and opening the pane only once the server's `JCH` confirms
the join, a message timestamp opens `WarpmarkDialog`, and a character row
activates on click or opens `CharacterMenu` on right click. See
[warpmarks.md](warpmarks.md) and [rendering.md](rendering.md).

## State ownership

| Data | Owner | Mutator |
| --- | --- | --- |
| messages, conversations, members, presence, summaries | `Store` | `applyEnvelope` |
| pending sends | `Store.pending` | `applyEnvelope` + `applySendResult` + `commands.ts` |
| warpmarks, warp panes | `Store.warpmarks`, `Store.conversations` | `commands.ts` (HTTP list is pulled, panes are ephemeral) |
| core session + account | `Store.core`, `Store.account` | `applyEnvelope` |
| search result sets, conversation catalog (`Store.search`, `Store.channels`) | `Store` | `applyEnvelope` |
| tabs, active panes, drafts, scroll, banners, dialogs | `View` | UI-local actions |

## Module layout

```
ui/src/
  main.ts         # m.mount + provideApp (composition root); boot gate
  app.ts          # gate selection + Chatspace shell + document title
  context.ts      # useStore/useView/useDispatch (only sanctioned global)
  render.ts       # request(): the single m.redraw entry point;
                  #   memo(): build-once / reuse-while-keys-unchanged vnode cache;
                  #   pure(): skip a leaf view() while attrs are unchanged
  mithril.ts sound.ts api.ts shortcuts.ts   # root singletons + global shortcuts
  lib/            # characters.ts (genderClass/profileURL/openProfile), order.ts
                  #   (collation + conversation order), format.ts (clock/bytes/
                  #   labels), list.ts (bounded filtered lists), dom.ts, debounce.ts
  transport/      # ws.ts (connect/backoff/envelope), broker.ts (bounded command
                  #   promises: ack/timeout/abort), protocol.ts (types + OPS)
  store/          # state.ts (Store + View) apply.ts commands.ts window.ts
                  #   unread.ts typing.ts persist.ts (device prefs + drafts)
                  #   search.ts interest.ts (reconnect resubscribe)
  components/     # primitives/ auth/ chatspace/ conversations/ messages/
                  #   composer/ presence/ search/ settings/ ads/ logs/
                  #   warpmarks/ commands/
```

Each `components/` feature folder is one module per cohesive feature, e.g.
`presence/character.ts` (the character-rendering leaves), `presence/roster.ts`
(channel + presence-search column), `settings/editor.ts` (the view and its three
cards). `composer/` is the reusable editor: `composer.ts` (the generic,
store-free controlled component, wrapped in `render.pure`) plus `autosize.ts`,
whose measurement model has its own deep doc comment. The chat container is
`conversations/editor.ts` (`MessageEditor`), which wires the Composer to the
active conversation's draft, typing signal, and send path; dialogs wire the same
Composer to their own state. A feature may split into
same-folder siblings when one file grows unwieldy; `logs/` is the example
(`logs.ts` dialog shell, `picker.ts`, `activity.ts`, `cleanup.ts`, `export.ts`,
plus shared `labels.ts`/`shared.ts`). Same-folder imports are unrestricted.
`presence/rosterWindow.ts` and `messages/timelineScroll.ts` are same-folder
siblings holding `ChannelRoster`'s window math/measurement and `MessageList`'s
pin/defer/anchor decisions, split out so those gates can be unit-tested.

`commands/commands.ts` is the command-palette home: each shell is an invisible
container that owns one palette's data and action and renders only the shared
`Palette` primitive, so the visual component is reused across unrelated
commands. A shell is named by `CommandId`, opened through the modal slot
(`openCommand`), and registered in `COMMANDS`; the shell (not the palette) owns
the committed query and decides whether a selection closes the palette or
swaps its item set (subcommands). A palette row is
`{ id, title, description?, filterable, subcommand?, value? }`: title and
description are plain strings or prebuilt Mithril content (a component as
`m(Component, attrs)`), `filterable` is the plain text the palette matches the
committed query against (usually the title, but set independently when a row
should also answer to an id or code), `subcommand` adds a right chevron for a
row that opens a further palette, and `onSelect` receives the whole row: a shell
reads an optional `value` off it as a precomputed result shape, or any other
field it put on the item. The palette filters its row set itself, on
`filterable` alone; a shell passes every row and never filters. It caps the
rendered rows at `maxVisible` (default 100) and notes the hidden remainder, so a
broad query over a large catalog (the public room list) cannot build thousands
of DOM nodes; the matcher counts all matches but materializes only the prefix it
renders. A shell may
pass `previousItem` to show the row it drilled in from above the input; the
main menu uses it for the status list and for a contact's actions.
Conversation jump (`Ctrl/Cmd+J`) and the main command menu (`Ctrl/Cmd+P`) are
the two shells. The palette debounces the input before reporting a query, so
the shell is not re-rendered per keystroke. A shell may swap its item set for a
subcommand; the main menu drills into the status list, into online friends
& bookmarks, and into the channel/room join picker this way.

### Helper placement

A helper's home follows what it imports and who uses it:

| Helper | Home | Examples |
| --- | --- | --- |
| App singleton / global wiring | `ui/src/*.ts` (root) | `mithril` `render` `context` `sound` `api` `shortcuts` |
| Reads `Store`/`View`/`Dispatch` | `ui/src/store/*.ts` | `window` `unread` `typing` `persist` `commands` |
| Pure, no app deps, two or more feature folders | `ui/src/lib/*.ts` | `characters` `order` `format` `dom` `debounce` |
| Mithril primitive | `components/primitives/` | `form` `Avatar` `dialog` `select` `palette` |
| One feature only | the feature's module | several components may share it |

`lib/` modules are topic-named, never a catch-all `utils.ts`. A feature folder
must not import a sibling feature folder just to reach a pure helper; move it to
`lib/` instead (e.g. `genderClass` and `profileURL` live in `lib/characters.ts`,
not in `presence/`).
