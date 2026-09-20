# Settings

Configuration is stored in the database (see [domain.md](domain.md#configuration)),
not a file, so it is transactional with history and needs no deploy-time path
wiring. The core exposes a typed settings API; the web layer is a thin
request/response wrapper over it.

## Scopes and shape

The core stores two scopes, each one JSON document in the `configs` table. They
are **disjoint**: no field exists in both, so there is no merge/effective
document. Browser-local preferences live outside the core — see
[This Device](#this-device).

**Global** (key `!global`) — settings not tied to a character:

```json
{
  "password": "hunter2"
}
```

- `password` — the shared password guarding browser access. Empty disables the
  gate. It is returned in cleartext by the settings API: Plexo assumes a trusted
  LAN where the operator owns every machine.

**Character** (key = lowercased character name) — per-character settings:

```json
{
  "highlights": ["Kira", "secret"],
  "autoJoin": [
    { "kind": "official", "id": "Frontpage", "name": "Frontpage" },
    { "kind": "room", "id": "adh-abc123", "name": "The Tavern" }
  ],
  "autoStatus": { "status": "away", "message": "brb [b]soon[/b]" }
}
```

- `highlights` — substrings that elevate an incoming channel message. Matching
  is case-insensitive.
- `autoJoin` — channels and rooms to try to join after login. `id` is the
  joinable identifier sent to F-Chat (official channel name or room hash);
  `name` is the human-readable label the UI shows before any ORS/JCH data
  arrives. `kind` (`official`/`room`) is explicit because it cannot be reliably
  inferred from an ID and the UI needs it to group and style entries. The core
  fires a best-effort `JCH` for each entry once a session becomes ready
  (including on reconnect); a rejected join is discarded, not retried or
  surfaced.
- `autoStatus` — an optional status the core re-emits as `STA` once a session
  becomes ready and on every reconnect, so the character returns with the same
  status. `status` is one of the selectable statuses (`online`, `looking`,
  `away`, `busy`, `dnd`, `idle`) and never the moderator-granted `crown`;
  `message` is raw BBCode and travels to F-Chat unchanged. An absent field, or
  an empty `status`, means the character has no automatic status.

### Reserved keys

All keys beginning with `!` are reserved. A character name must be non-empty and
may not begin with `!`, so a character document can never collide with the
global key. Character keys are folded to lowercase on read and write, so lookups
are case-insensitive and match F-List character identity.

## This Device

Per-browser preferences are **not** core settings: they live only in the
browser's `localStorage`, as one JSON document (key `plexo:device`), so they are
never synced across devices and need no API.

```json
{ "soundEnabled": true, "composerEnterNewline": false, "limitMessageWidth": false }
```

- `soundEnabled` — play the attention sound for elevated traffic. Defaults to
  **true**; see [Notification sound](#notification-sound).
- `composerEnterNewline` — when true, Enter inserts a newline and
  Ctrl/Cmd+Enter sends. Defaults to false.
- `limitMessageWidth` — when true, the conversation's message column is capped
  and centered so long lines stay readable on wide screens. Defaults to false.
  The client toggles a class on the message pane; the layout is browser-local
  like the rest of this document.

The document is versionless: unknown keys are preserved, and a missing or
corrupt document falls back to the defaults. It is written through
`ui/src/store/persist.ts` by the **This Device** card in the Config editor and by
the composer's send-key toggle.

## Core API

All methods live on `core.Manager` and are safe for concurrent use. They take a
`context.Context` so the persistence call is cancellable.

```go
type SettingsView struct {
    Global       config.Global    `json:"global"`
    Character    config.Character `json:"character"`
    HasGlobal    bool             `json:"hasGlobal"`
    HasCharacter bool             `json:"hasCharacter"`
}

func (m *Manager) Settings(ctx context.Context, character string) (SettingsView, error)
func (m *Manager) SetGlobalSettings(ctx context.Context, g config.Global) error
func (m *Manager) SetCharacterSettings(ctx context.Context, character string, c config.Character) error
func (m *Manager) ResetGlobalSettings(ctx context.Context) error
func (m *Manager) ResetCharacterSettings(ctx context.Context) error
```

- `Settings` returns both documents and whether each exists. It does **not**
  require the character to be logged in; an empty character simply has no
  character document.
- `HasGlobal`/`HasCharacter` distinguish "no document" from a stored document
  whose fields are all empty.
- `Set*` accept a **whole document** (not a patch): the UI reads a scope, edits,
  and writes it back. Writes are normalized and validated before persisting.
- `Reset*` deletes the scope document, reverting it to defaults (a fully
  disabled global config, or an empty character config). Resetting a missing
  document is a no-op.

New fields are added to `config.Global`/`config.Character` and flow through the
view and the `Set*` methods without an API change.

## Write semantics

1. **Normalize** — character highlights are trimmed, empty entries dropped, and
   case-insensitive duplicates removed (first spelling wins); auto-join entries
   are trimmed, empty IDs dropped, `name` defaults to `id`, and duplicates are
   removed by (kind, id); the automatic status is trimmed and lowercased and a
   blank status drops the field. Global is unchanged.
2. **Validate** — enforce the documented caps and reject an unknown auto-join
   kind, a non-selectable automatic status, or an empty/reserved character name.
3. **Persist** — upsert the scope document.
4. **Apply** — re-resolve every running session's character config and hand the
   whole document to the session, which owns it and applies each field in place
   (highlights swap immediately; auto-join and auto-status take effect on the
   next ready transition), without a reconnect. The shared password is applied by the web
   gate at startup; the settings UI writes it but does not rebuild the running
   gate, so a password change takes effect on the next core restart.

A rejected write (normalize/validate failure, or a store error) leaves the
stored document **untouched** and returns the error. Reads of a malformed
document are a hard error too, so a bad row fails loudly rather than silently
discarding settings.

### Limits

| Field | Cap |
| --- | --- |
| highlights | 100 entries, 128 chars each |
| autoJoin | 50 entries, 128 chars per id and per name |
| autoStatus.status | one of online/looking/away/busy/dnd/idle |
| autoStatus.message | 512 chars |
| password | 256 chars |
| F-Chat account | 256 chars |
| F-Chat password | 256 chars |

### Character presence is not enforced

`SetCharacterSettings`/`ResetCharacterSettings` accept any valid character name,
logged in or not, so settings can be prepared ahead of a login. "Only editable
while connected" is a **UI guardrail**; the core intentionally does not check
the session registry.

### Errors

- `core.ErrSettingsUnavailable` — the manager was built without a settings
  provider. The web layer should surface this as `503`.
- `config` validation errors — client input (including an empty or
  `!`-prefixed character name); map to `400`.
- Store errors — server-side; map to `500`.

## Command-line password override

The shared password lives in the global document. If `--password` (or
`PLEXO_ACCESS_PASSWORD`) is non-empty at startup, it **overwrites** the stored
password before the core starts, so the database stays the single source of
truth at runtime and the flag acts as a set/reset. An empty flag leaves the
stored password (or its absence) alone.

## F-Chat credentials

The F-Chat account and password are **not** part of either settings scope. They
live in their own reserved document, `!credentials`, so the settings API can
never return them and a whole-document settings write can never clobber them.

```json
{ "account": "example", "password": "hunter2" }
```

Credentials are written only after F-List **accepts a mint**. The gate's
"Remember on this server" checkbox sets the `remember` flag on
`set_credentials`; a validated pair is then persisted and a rejected pair is
never stored. A later `set_credentials` without `remember` deletes any stored
pair, so the explicit choice is honored. The document is stored **unencrypted**,
and the gate says so:

> Stored unencrypted in the core's database to remember across core restarts.
> Anyone with direct access to that database can read them.

At startup the core loads the document, seeds `checking`, and revalidates in the
background: a pair F-List rejects is purged, while a transient failure keeps both
the stored and in-memory pair so a later restart can retry. `account_state`
reports `persisted` (a boolean only; never the values), which the Config
editor's Global card uses to show a **Forget credentials** button. Deleting via
that button (`purge_credentials`) removes the stored document but leaves running
sessions and the in-memory pair alone, so the credentials gate returns on the
**next core restart**.

`ResetGlobalSettings` (the Config card's "Reset to defaults") deletes both
`!global` and `!credentials`; `--reset-config` clears the whole `configs` table.

## Web mapping

| Method | Path | Body | Result |
| --- | --- | --- | --- |
| `GET` | `/api/settings?session=<char>` | — | `SettingsView` (`session` optional → global only) |
| `PUT` | `/api/settings/global` | `config.Global` | `204` |
| `PUT` | `/api/settings/character?session=<char>` | `config.Character` | `204` |
| `DELETE` | `/api/settings/global` | — | `204` |
| `DELETE` | `/api/settings/character?session=<char>` | — | `204` |

All routes sit behind the existing session cookie (`api.guard`). `Session` is
the character name as the client knows it; the core folds case. The UI guardrail
is that `PUT`/`DELETE /api/settings/character` is only offered while a session
for that character is present in the store.

After a successful write the UI re-fetches `GET /api/settings` so the
core-normalized form is shown. No settings event is published over the
WebSocket; settings are not live state.

## Web client

The top-bar **Config** button swaps the chat workspace for `SettingsView`
(`ui/src/components/settings/`). It renders `ThisDeviceCard` (browser-local,
saves immediately), `GlobalSettingsCard`, and one `CharacterSettingsCard` per
character with a session in the store (the UI guardrail above). The core-backed
cards load their own document over HTTP, edit a local draft, and PUT the whole
document on Save. `AutoJoinList` is managed: an X
removes an entry, and "Replace with joined" snapshots the character's current
channel/room conversations. Switching a session tab or closing the editor
returns to the chat workspace. The character card also shows any saved
`autoStatus` and offers a **Clear** button; the status dialog is the place to
set it. There, **Set status** changes only the live status, while **Save for
login** stores the dialog's current status and message as the automatic status
and **Clear** removes it, so a temporary status never clobbers the saved one.

## Notification sound

When the device's `soundEnabled` preference is on (the default), the client
plays the F-Chat attention sound for an **elevated** entry: an incoming **direct
message**, or a channel message the core flagged **`highlight`**. The asset
(`ui/sound/attention.mp3`, downloaded from
`https://static.f-list.net/sound/attention.mp3`, ≈4 KB) is embedded by
`ui/embed.go` and served like the other UI assets at `./sound/attention.mp3`.

### Why the chime lives in both apply paths

The broker gates activity by interest, and a conversation receives **exactly
one** signal per entry: a `full` conversation gets the `message` entry, a
background one the `summary` record only. The chime is therefore handled once in
each path (`applyMessage`, `applySummary`), both using the same predicate (a
self-authored copy is excluded via the payload's `self`, set in
`session.recordEntry`):

```ts
if (view.soundEnabled && p.self !== true &&
    (p.highlight === true || p.conv.kind === "dm")) {
	playAttention();
}
```

### Client pieces

- `ui/src/sound.ts` wraps one lazily-created `HTMLAudioElement`, plays at most
  once per second (collapsing a burst into a single chime), and never throws.
- `main.ts` primes the element on the first `pointerdown`/`keydown`, because
  browsers block programmatic audio until a user gesture.
- `View.soundEnabled` is seeded from the [This Device](#this-device) document at
  startup and updated by the card/composer toggles; the event path reads it
  without a settings fetch.
