# Rendered content (BBCode)

Message bodies are raw BBCode in storage. Parsing and sanitization happen in the
core, so clients receive a ready-to-use HTML fragment and one parse is shared by
all clients. `model.RenderedEntry` (embedded `Entry` + `html`) is delivered in
message events, `ConvView.Window`, and `GET /api/history`; storage keeps the raw
`Entry`, and the wire omits `body`.

- **Delivery types only.** `model.Entry` stays raw; delivery carries
  `model.RenderedEntry{ Entry; HTML string }` (`MarshalJSON` drops `Body`), used
  by `MessagePayload`, `ConvView.Window`, and history. In-process code still
  reads `Entry.Body`.
- **Status and descriptions** are BBCode too (`MemberInfo.StatusMsg`,
  `ConvStatePayload`/`ConvView.Description`). Session state keeps the raw source;
  delivery paths render at the boundary (roster emission, snapshot, presence
  search, `ConvView`), not at ingestion, so the `LIS` burst is parsed only for
  what is actually delivered.
- **Parser.** `internal/render` walks the body once and appends HTML straight
  into a buffer (no intermediate tree). The tag set is `table.json`: `tags` maps
  a name to a `valid` template plus optional `param_check`, `content_check`,
  `param_transform`, `content_transform`, `void`. Templates use `{content}`
  (already-rendered HTML, inserted verbatim) and `{param}` (HTML-escaped).
- **Recovery.** Malformed input never becomes an error marker and never swallows
  a subtree. An unknown tag, a stray or mismatched close, a deep open past
  `maxDepth`, or a known tag whose `param_check`/`content_check` fails is
  emitted as its HTML-escaped raw source, and parsing resumes right after it, so
  a hand-typed mistake stays readable and later text and tags still render. For a
  known tag with a failed guard this means only the opening tag is shown
  literally: its content is still parsed (the content guard runs on the already-
  rendered content but never discards it) and the matching close, when present,
  is re-emitted as source. Bad params and bad content therefore recover
  identically, and neither method re-parses the content. A valid open whose close
  never arrives is rolled back to where the tag began and replaced with the raw
  source through where the scan stopped; its already-wrong nesting is not
  half-rendered.
- **`[noparse]`.** The one tag whose content is not parsed: source up to the
  first `[/noparse]` is emitted HTML-escaped, so typed BBCode displays literally
  (`[noparse][b]x[/b][/noparse]` renders as
  `<code class="bc-noparse">[b]x[/b]</code>`). The behavior is hardcoded in the
  parser, keyed on the tag name rather than a table field, so no other tag can
  skip parsing. The table entry supplies only the `<code>` wrapper and its
  params are ignored; an unclosed `[noparse]` follows the normal rollback rule,
  and its entry may set no guard, transform, or `void`.
- **Cache.** In-memory, by body, shared by messages/status/descriptions; cleared
  on restart or reload. One-offs that must not pollute it — the chatlog export
  and the `POST /api/render` preview — use the renderer's uncached entrypoints
  (`Render`/`RenderMessage`/`RenderEntry`-`Uncached`) instead. Those snapshot the
  table under the lock and parse without touching the map, so an export cannot
  evict the live cache and a reload mid-export cannot mix table versions. The
  live paths are unchanged and still cache.
- **Hot reload.** `table.json` and `entry_table.json` are embedded, but the
  on-disk `internal/render/table.json` / `internal/render/entry_table.json` win
  when present. `SIGHUP` recompiles both, swaps, and clears the cache; a
  load error keeps the previous table.

## Chat message prefix

F-Chat has no server-side emote command, but clients conventionally prefix an
action with `"/me "`. Plexo's client renders the speaker inline before the
body, so message bodies are normalized at render time: a leading `/me` token
(one followed by whitespace or nothing) is dropped, keeping its separating
whitespace so the action flows after the name, and every other message is
prefixed with `": "` so it reads `Name: text`. `render.Renderer.RenderMessage`
applies the transform and delegates to the normal parse, so the transformed body
is the cache key (an emote and a plain message share one parse, and a status body
with the same source cannot collide with the message form). Plain message bodies
go through `RenderMessage`/`model.RenderMessageHTML`; statuses and descriptions
keep `Render`/`RenderHTML`. Storage keeps the raw body, so the transform is
re-applied whenever a parser reload re-renders it.

## Structured entries (RLL)

An RLL frame (dice roll / bottle spin) is persisted with its **whole server
payload** in `timeline_entries.data` (a nullable JSON column) and kind `rll`,
and rendered from that payload rather than from the server's `message` text. The
renderer is a second, kind-keyed template table (`entry_table.json`) compiled
into the same `internal/render.Renderer`; the client still receives only
`kind` + `html` and never parses.

- **Table shape.** Each entry kind maps to `variant_switch` (payload field whose
  value selects a variant; empty, missing, or unknown → `default`), `preprocess`
  (source field → `"dest:step|step"`), and `variants` (name → `html/template`
  source). A pipeline runs its steps left to right as string functions and stores
  the result under `dest` (`lower` folds to lowercase, reusing the BBCode table's
  transform; `bbcode` delegates to the BBCode renderer). A pipeline ending in
  `bbcode` is inserted as trusted HTML; every other value, and every field not
  named in `preprocess`, is escaped by context. A missing source field is
  skipped, so one kind's rules cover variants that lack a field; a missing
  output field or an unknown step is a load error; a failed step aborts the entry
  to escaped text. A kind with no table entry, or a non-structured kind, falls
  back to the chat-message path.
- **Delivery.** `model.RenderedEntry` carries the template output; `Entry.Data`
  is persistence-only (`json:"-"`), so the wire and the client model are
  unchanged.
- **Dispatch.** `model.Renderer` has `RenderEntry(kind, body, data)`;
  `Session.recordEntry` and `Manager.renderWindow` call it via
  `model.RenderEntryHTML`, so live events and history render identically.
- **Routing.** A DM roll carries `recipient` instead of `channel`; the partner
  is `recipient == self ? character : recipient`, the same rule TPN uses, so both
  parties' rolls land in one `dm:` conversation. Self is kept (there is no
  outbound RLL command; the server echo is the only copy).

## Styling split

The core emits semantics; the UI owns presentation (`bc-*` classes, `bc-eicon`
sized in CSS, named-color classes vs validated inline hex). The client renders
with `m.trust(entry.html)` in an immutable component; no client re-parse. The
composer's BBCode bar likewise only wraps the selection in raw tag text — it
never parses or previews. Roll entries are presentation-distinct: `.msg.kind-rll`
drops the chat bubble and lays the speaker, the `.roll` pill, and the time out
as a centered system line, since a roll is an event rather than speech.

`[spoiler]` and `[session]` are the tags that need interaction. A
`[spoiler]` wraps its content in `.bc-spoiler` / `.bc-spoiler-body`, hidden by
CSS until the chatspace's single delegated click handler (`clickHandlers`, see
[ui-components.md](ui-components.md)) toggles `.is-revealed` on the element. A
`[session=Title]adh-...[/session]` renders a `.bc-session` span carrying
`data-conv-kind` and `data-conv-id`; the same handler focuses the room, joining
it first if absent and opening the pane only once the server's `JCH` confirms
the join (see [ui-components.md](ui-components.md)). Both
toggles are direct DOM/state changes with no re-render of the message, so the
trusted HTML is never re-parsed.

## Trust

F-Chat transmits message bodies, status messages, and channel descriptions
HTML-escaped (`&`, `<`, `>`, via fserv's `UnicodeTools::escapeHTML`). The
renderer reverses exactly that escaping once before parsing (`decodeWireEntities`,
single-level), then re-escapes literal text and `{param}` on output; `{content}`
is already-safe. This keeps a typed `>` as `>` instead of `&gt;`. Guards a table
may name (`color`, `url`, `slug`) are built-in Go predicates; an unknown
reference is a load error. A table may also name value transforms
(`param_transform`/`content_transform`, today only `lower`); they rewrite the
value at substitution time, after the guard has run on the original. This is
how the slug tags (`eicon`, `icon`) fold user-typed uppercase names, since
F-List image URLs are lowercase and case-sensitive. The table is F-Chat-specific
and curated against the live server — the reason it is hot-reloadable.
