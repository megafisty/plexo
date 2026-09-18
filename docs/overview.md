# Plexo design

Plexo is a LAN proxy, aggregator, and persistence layer for F-Chat: one core
holds several concurrent character sessions (one account), persists history
F-Chat does not, and serves a lightweight web client. The core speaks F-Chat;
the browser speaks only to the core.

| Doc | Covers |
| --- | --- |
| [architecture.md](architecture.md) | Layout, concurrency, ops, security, testing |
| [domain.md](domain.md) | Characters, conversations, SQLite persistence |
| [activity.md](activity.md) | Chatlog activity model: scopes, bursts, thresholds |
| [settings.md](settings.md) | Database-backed settings and the settings API |
| [fchat.md](fchat.md) | Upstream F-Chat wire protocol, tickets |
| [core-protocol.md](core-protocol.md) | Downstream core↔client WebSocket protocol |
| [sessions.md](sessions.md) | Session lifecycle, reconnect/disconnect policy |
| [rendering.md](rendering.md) | BBCode parsing and sanitization |
| [ui-state.md](ui-state.md) | Client state model and performance |
| [ui-components.md](ui-components.md) | Client component architecture |
| [warpmarks.md](warpmarks.md) | Warpmarks and message permalinks |
| [ui-css.md](ui-css.md) | UI stylesheet composition, build, tokens |
| [fchat/](fchat/) | Protocol reference; `API.html` is authoritative |

## Status

The core and the embedded Mithril client cover everyday use:

- **Core** — `internal/fchat` (framing, codecs, WebSocket, ticket minting,
  character field mapping), `internal/session`
  (actor, FSM/hydration, `conv_seq`, persistence, typing, highlights,
  disconnect classification), `internal/store` (SQLite, no CGO),
  `internal/broker` (interest-gated coalescing fan-out with a shared state store and keyed resync), `internal/core` +
  `internal/model` (registry, dispatch, snapshots, views), `internal/web` (HTTP
  + settings endpoints + WS bridge + session auth), `internal/render` (fused BBCode
  parser/renderer plus kind-keyed entry templates, both hot-reloadable, with a
  shared cache and an uncached path for exports), `internal/activity` (chatlog
  activity model: day buckets, session segmentation, scope switch),
  `internal/export` (streamed, self-contained chatlog HTML), `internal/config` (scoped,
  database-backed settings).
- **Client** — onboarding gates, transport, the domain store, the chatspace
  (tabs, sidebar, timeline paging, composer with a BBCode bar and DM typing
  signals), presence/roster search, friends and ignores, warpmarks and their
  read-only warp panes, the ad browser, the chatlog browser/export/cleanup, the
  settings editor, unread/highlight handling, and the attention sound.

Open future work — per-session reconnect control, ad posting, richer chatlog
export formats, performance and security items — is consolidated in
[TODO.md](../TODO.md).
