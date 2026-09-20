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

Everyday use works end to end. The core covers the F-Chat wire
(`internal/fchat`), per-character session actors with persistence and fan-out
(`internal/session`, `internal/store`, `internal/broker`, `internal/core`), the
web client (`internal/web`, `ui/`), BBCode rendering (`internal/render`),
chatlog activity and export (`internal/activity`, `internal/export`), and
database-backed settings (`internal/config`).

The client covers onboarding, the chatspace (tabs, sidebar, timeline paging,
composer), presence and roster search, friends and ignores, warpmarks, the ad
browser, the chatlog browser/export/cleanup, the settings editor, unread and
highlight handling, and the attention sound.

Open future work — per-session reconnect control, ad posting, richer chatlog
export formats, performance and security items — is consolidated in
[TODO.md](../TODO.md).
