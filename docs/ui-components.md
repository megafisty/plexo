# UI component architecture

The client is a Mithril app under `ui/src/`, typechecked by `tsc` and bundled
into a single `ui/app/main.js` by the project-local Bun, served with `go:embed`.
State model:
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
| Primitive | no | no | no | Avatar, field, dialog, select, palette, popout |

A presentational or primitive component never imports `store/`, `context/`, or
`transport/`; it takes data as attrs.

## Dataflow

```
F-Chat ─ core ─ WS ─ transport/ws.ts ── applyEnvelope(Store) ──
                    request()           ◀── mark dirty ───┘
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
   `render.memo`/`render.pure` for skip decisions: no ad-hoc vnode caches on
   component state, and no raw `onbeforeupdate` except a controlled input's
   external re-seed (`Palette`).
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
│   FriendsMenu (bookmarks -> CharacterMenu); Search / Ads (session-bound
│   dialogs); Logs; WarpmarksMenu (active character's marks in a large
│   scrollable popout with a naive text filter -> warp pane); ConfigButton;
│   connection indicator + brand ("Disconnected — refresh")
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
│   └── ChannelRoster (channel/room active only)
├── CharacterPicker (content of an unconnected tab)
├── modal slot (one): JoinChannelDialog; StatusDialog; SearchDialog (FKS
│   builder + results); AdsDialog (Search tab over the session's buffered ads
│   + dummy Post tab); LogsDialog (Export Chatlogs picker + Cleanup tools);
│   WarpmarkDialog (label prompt opened from a message timestamp);
│   RoomAdminDialog (room management, opened from a room header's Manage
│   button when the session can manage: mod/owner or a global moderator);
│   CommandPalette (command shells for the active session: the main menu on
│   Ctrl/Cmd+P, the conversation jump on Ctrl/Cmd+J, and the character picker
│   on Ctrl/Cmd+K)
├── popout slot (one): FriendsPopout or WarpmarksPopout, rendered by its top-bar
│   button while `View.popout` names it
├── CharacterMenu (roster/friends context, its own slot)
├── SettingsView (when ConfigButton is active): DialogTabs ordered Global
│   (GlobalSettingsCard -> password); This Device (ThisDeviceCard -> sound,
│   send key, wide-screen column); then one tab per session
│   (CharacterSettingsCard ×N -> Highlights; AutoJoinList (remove X /
│   replace-with-joined))
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

Room member moderation is one shared model. `lib/moderation.ts` resolves the
capability set (`op`/`deop`/`kick`/`ban`/`unban`/`timeout`) from the room
context (kind, role, global-admin status, member/op sets, and `RoomInfo.owner`
when known), and its `roomOps` factory turns a capability into a `room_admin`
command. `CharacterMenu` renders the member actions it authorizes and reports
the result as a toast; the Ctrl/Cmd-K character picker adds a **Moderator
Actions** subcommand to its per-character action list when it was opened on a
channel member list and the snapshot authorizes any verb, listing the same
op/deop/kick/ban rows there; `RoomAdminDialog` routes its moderator/ban lists through
the same interface and renders the result inline; `ConversationHeader` uses
`canManageRoom` for the Manage button. The dialog's `set_owner`/`invite` and
other room-shape actions stay dialog-local.

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
                  #   labels), list.ts (bounded filtered lists), conversations.ts
                  #   (kind predicates + active-conversation lookup), dom.ts,
                  #   debounce.ts, moderation.ts (room capabilities + triggers)
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
(the channel member column), `settings/editor.ts` (the tabbed view and its three
cards). `composer/` is the reusable editor: `composer.ts` (the generic,
store-free controlled component, wrapped in `render.pure`) plus `autosize.ts`,
whose measurement model has its own deep doc comment, and `previewfield.ts`
(`PreviewField`: a dialog-bound Composer with a Preview/Edit toggle that renders
the draft through the core, shared by the status dialog and the room-description
editor). The chat container is
`conversations/editor.ts` (`MessageEditor`), which wires the Composer to the
active conversation's draft, typing signal, and send path; dialogs wire the same
Composer to their own state. A feature may split into
same-folder siblings when one file grows unwieldy; `logs/` is the example
(`logs.ts` dialog shell, `picker.ts`, `activity.ts`, `cleanup.ts`, `export.ts`,
plus shared `labels.ts`/`shared.ts`). Same-folder imports are unrestricted.
`presence/rosterWindow.ts` and `messages/timelineScroll.ts` are same-folder
siblings holding `ChannelRoster`'s window math/measurement and `MessageList`'s
pin/defer/anchor decisions, split out so those gates can be unit-tested.
`presence/` keeps `status.ts` (the shared status metadata, an import-free leaf
used by `character.ts`) apart from `statusDialog.ts` (the modal, which renders
`FeaturedCharacter`), so the metadata can be imported without a cycle.

`commands/` is the command-palette home. Each shell is an invisible container
that owns one palette's data and behavior, plus every decision outside
presentation and selection (closing, drilling, toggling), and renders only the
shared `Palette` primitive. A shell is named by `CommandId`, opened through the
palette slot (`openCommand`), and registered in `COMMANDS`
(`conversations.ts`); the shell owns the committed query and decides whether a
selection closes the palette or swaps its item set. `commands.ts` holds the
main-menu shell, `list.ts` binds the palette's generic list types to the
`CommandContext` the shells build, `conversations.ts` the conversation jump,
`characters.ts` the character picker, and `format.ts` the composer's two BBCode
palettes: `format-marks` (superscript, subscript, strikethrough, underline) and
`format-advanced` (Colors, URL, Link Character, plus the direct Spoiler leaf).

The `Palette` primitive is always driven by a `PaletteList`, not by loose rows:
the shell hands it the current list and a context, and the palette materializes
that list once and filters and displays the frozen snapshot itself. It
rematerializes only when the list id changes (a shell drilling or toggling), so
live store changes never reach an open palette. A palette row is
`{ id, title, description?, filterable, next?, previous?, value? }`: title and
description are plain strings or prebuilt Mithril content (a component as
`m(Component, attrs)`), `filterable` is the plain text the palette matches the
committed query against (usually the title, but set independently when a row
should also answer to an id or code), `next` is the further `PaletteList` the
row opens (shown with a right chevron; the palette reports it through
`onSubcommand` so the shell swaps its `current` list), and `value` is the
row's payload (a leaf's precomputed result, or what a child list's
`transformPrevious` turns into its context item). A leaf goes
to the list's own `onSelect`; the shell's `onSelect` runs afterward so it can
close. The palette filters on `filterable` alone and caps the rendered rows at
`maxVisible` (default 100), noting the hidden remainder, so a broad query over a
large catalog (the public room list) cannot build thousands of DOM nodes; the
matcher counts all matches but materializes only the prefix it renders. It shows
the list's `emptyText` when the list produced no rows and `noMatchesText`
(default `"No matches"`) when rows exist but the filter excluded them. A shell
may pass `previousItem` for a subcommand: the palette renders it above the input
and hands it to the subcommand list's `list`/`onSelect`, so the list reads its
target from there instead of the shell stashing it in its own state. When a row
carries `next`, the shell builds that item with the child list's
`transformPrevious` from the row (the row's `value` is the payload); with no
`transformPrevious` the row itself becomes the context. The main menu uses it
for the status list and for a contact's actions.

Conversation jump (`Ctrl/Cmd+J`), the character picker (`Ctrl/Cmd+K`), and the
main command menu (`Ctrl/Cmd+P`) are the three global-chord shells. The two
composer palettes are the exception: the composer opens them, because it is the
only place that can hand over the closure which wraps the live selection. That
`FormatApply` rides on the palette slot's `CommandPalette` and reaches the list
through `CommandContext.format`, so the shells never touch a textarea. The
`CommandPalette` also carries the selected text, which the URL palette branches
on. The palette slot renders above the modal slot, so a dialog's own composer
(status message, room description) can open the same format palettes without
the dialog being replaced; `openCommand` refuses a non-format palette while a
dialog is open, since the global chords are gated behind an open overlay. The
palette's Escape listener runs in the capture phase and stops the event, so
Escape closes the topmost palette and leaves the dialog under it open.
Ctrl/Cmd-S opens the flat marks palette; Ctrl/Cmd-D (the color mnemonic) and
Ctrl/Cmd-U (the url mnemonic) open the advanced one. Its color, URL, and
character-link toolbar buttons open that same palette pre-loaded to the matching
sub-list (the `CommandPalette` carries a `start` list id); the spoiler button
wraps directly, with no palette. The other three shells are
opened from `shortcuts.ts`, so every global chord stays in one place. All five
drive the palette from `CommandList`s: the conversation jump and the marks
palette are single leaf lists, while the advanced format palette, the main menu,
and the character picker own a `current` list and swap it. The palette debounces
the input before reporting a query, so the shell is not re-rendered per
keystroke. The advanced palette drills into the color list, the Make Link
(URL) list, and the Link Character tree (My Characters / Characters in Chat /
Exact Name → As Icon / As Link). The URL and exact-name lists run the palette in
free-text mode: every row stays pinned and the typed input is delivered on the
chosen item instead of filtering. Which way round the URL input goes depends on
the selection: with a URL selected the input is the link text (Set Link Text /
Just URL), otherwise the input is the URL and the selection is the link text
(Set URL). Pasting a bare http(s) URL with nothing selected opens this URL list
directly, passing the pasted URL as the selection so the input is its link text;
a paste onto selected text (or a clipboard that is not a single bare URL) is left
to the browser. A drilled row may carry a `value` that the next list's
`transformPrevious` turns into its header; the character rows put the name on
`value`, so `CharacterStyleList.transformPrevious` shows `Link: <name>` and the
style step reads the name back from that previous item's `value`. Entering Link Character with text
already selected skips the source picker: the selection is treated as the exact
name, so the exact-name step opens pre-filled with it (both from the toolbar
button and from the root row). The
main menu drills into the status list, into online friends & bookmarks, and into
the channel/room join picker; the character picker drills into a character's
action list and its Moderator Actions sub-list. The character picker's two roots
(channel members and seen characters) are each `CommandList`s too; Ctrl/Cmd-K
swaps between them without clearing the typed filter. Neither the character
picker's action lists nor its root lists are rebuilt after they materialize, so
a presence or role change while the picker is open is not tracked.

### Helper placement

A helper's home follows what it imports and who uses it:

| Helper | Home | Examples |
| --- | --- | --- |
| App singleton / global wiring | `ui/src/*.ts` (root) | `mithril` `render` `context` `sound` `api` `shortcuts` |
| Reads `Store`/`View`/`Dispatch` | `ui/src/store/*.ts` | `window` `unread` `typing` `persist` `commands` |
| Pure, no app deps, two or more feature folders | `ui/src/lib/*.ts` | `characters` `order` `format` `dom` `debounce` `conversations` |
| Capability model + dependency-injected triggers, two or more feature folders | `ui/src/lib/*.ts` | `moderation` (takes `Store`/`AppActions` as arguments, imports no context hook) |
| Mithril primitive | `components/primitives/` | `form` `Avatar` `dialog` `select` `palette` `popout` |
| One feature only | the feature's module | several components may share it |

`lib/` modules are topic-named, never a catch-all `utils.ts`. A feature folder
must not import a sibling feature folder just to reach a pure helper; move it to
`lib/` instead (e.g. `genderClass` and `profileURL` live in `lib/characters.ts`,
not in `presence/`).
