# Architecture

## Goals

- Many F-Chat character sessions from one core; per-character tabs.
- Any LAN device sees the same state without re-login.
- Persist history indefinitely (F-Chat has no server-side logs/search).
- Core does protocol, persistence, routing, fan-out; client is UI only.

## Non-goals

- Multi-user accounts (one shared Plexo password).
- Reimplementing the full official UI; FTS search in v1; federation or exposure
  beyond the LAN by default.

## Layout

Only `internal/fchat` knows F-Chat specifics.

```
F-Chat WSS─▶ internal/fchat ─▶ internal/session ─▶ internal/store
                                      │                    ▲
                                      ▼                    │
                              internal/broker ─▶ internal/core ─▶ internal/web ─▶ browser
```

- `cmd/plexo` — flags, wiring, graceful shutdown.
- `internal/fchat` — wire framing, IDN, codecs, WebSocket transport, API
  ticket minting/caching, and F-List character field mapping (kinks, infotags,
  list values).
- `internal/session` — per-character actor + FSM, hydration, disconnect classification.
- `internal/store` — SQLite persistence (no CGO).
- `internal/activity` — pure chatlog activity math: day buckets, session
  segmentation, participant/scope statistics. No store or wire knowledge.
- `internal/broker` — bounded, coalescing subscriptions: a transport-neutral
  fan-out core plus a delivery-policy layer over broker-owned membership,
  account-wide sets, and a shared latest-value-per-key state store. The state
  store is what lets a subscriber recover a dropped delivery by key instead of a
  snapshot. The stream model is in [streaming.md](streaming.md).
- `internal/core` — manager and account service: registry, dispatch,
  snapshots, views, credentials, settings.
- `internal/model` — canonical events, commands, snapshots.
- `internal/render` — fused BBCode parser/renderer and kind-keyed entry
  templates, hot-reloadable JSON tables, shared cache, plus uncached entrypoints
  for one-off exports.
- `internal/export` — standalone, self-contained chatlog HTML writer: reads the
  store directly and streams an artifact without touching the live render cache.
- `internal/web` — HTTP handlers, browser WS bridge, session auth, embedded UI.
  The bridge owns the durable, client-id-keyed subscription registry (and its
  grace window) but is otherwise a forwarder: keyed resync lives in the broker
  subscription, not here.
- `cmd/tsgen` — reflects the Go boundary types into
  `ui/src/transport/types.gen.ts` and reads the string const declarations into
  `ui/src/transport/enums.ts`; a Go test keeps both current, and
  `ui/src/transport/protocol.ts` re-exports them, so there is no hand-written
  mirror to drift. Coverage is the socket payloads plus the HTTP
  request/response shapes the client reads.
- `internal/console` — terminal status screen (UI address, live sessions,
  connected browser clients) with a keypress loop.
- `internal/tray` — system-tray front end (`fyne.io/systray`): the UI address
  and a Shut Down item.
- `internal/browser` — opens a URL in the platform's default browser.
- `ui` — TypeScript via `tsc`, committed output, vendored Mithril. Embedded
  with `go:embed` and served by the core, so in production the client and core
  are one artifact and cannot drift
  ([core-protocol.md](core-protocol.md)).
- `test/` — fakeserver (F-Chat), fakeui (subscriber), fchatpipe, memstore.

## Concurrency

- One session actor per character owns its connection and all mutable state; no
  shared state, communication over channels.
- Broker owns subscriber registries and fan-out; never blocks on a session.
- Bounded per-subscriber queues; a slow subscriber's dropped events are
  coalesced latest-wins and resynced by key, so the hub never blocks.
- `context.Context` throughout; tests run under `-race`.

## Operations

- Single static binary with the UI embedded; cross-platform, no CGO.
- Flags in `cmd/plexo`: `--http`, `--db`, `--password`, `--fchat-url`,
  `--ticket-url`, `--mapping-url`, `--mapping-file`, `--origin`,
  `--no-systray`, `--clear-history`, `--reset-config`.
- Front end: the system tray runs by default wherever a backend is available;
  on Linux that means a StatusNotifier host must own
  `org.kde.StatusNotifierWatcher` on the session bus. `--no-systray` selects
  the console TUI (including on Windows), and a platform without a usable
  backend falls back to the console. The macOS tray backend needs cgo; a
  cgo-less macOS build falls back to the console. A Windows build can pass
  `-ldflags -H=windowsgui` to suppress the console window, but such a build has
  nowhere to draw the `--no-systray` console, so reserve that flag for
  tray-only releases. SIGHUP reloads the BBCode table and every session's
  config in all front ends.
- The character field mapping data is loaded once at core start (`--mapping-url`)
  and cached in memory; it is never persisted. `--mapping-file` (default
  `test/fixtures/mapping-list.json`) loads the committed curl capture instead,
  so starting the dev harness for testing never queries F-List. It is exposed
  to the client at `GET /api/mapping`.
- Configuration lives in the database's `configs` table, not a file: `!global`
  holds the shared access password; a lowercased character name holds that
  character's settings (highlights, auto-join list). The scopes are disjoint.
  See [settings.md](settings.md) and [domain.md](domain.md#configuration).
- The resolved per-character config is handed to the session at login and
  refreshed by `Manager.ReloadConfig` (SIGHUP reloads it and the BBCode table);
  the session owns it and applies each field. A non-empty `--password`
  overwrites the stored shared password at startup. `--reset-config` clears the
  configs table; `--clear-history` empties the timeline.
- Graceful shutdown: context cancel stops sessions, the tray/console loop and
  HTTP server close, and the DB closes.

## Security and auth

- Shared Plexo password (constant-time compare, HttpOnly SameSite cookie),
  reported by `GET /api/session`; disabled when unset. Loopback callers are
  auto-skipped. Rate limiting not implemented.
- Optional persistent cookie is an HMAC of the password, not the password; the
  password is never stored client-side.
- F-Chat credentials are supplied by the browser and held in core memory; they
  are never returned to a client and no ticket leaves the core. When the user
  opts in ("Remember on this server") a validated pair is stored **unencrypted**
  in the `!credentials` config document so the core can restore it after a
  restart; anyone with direct access to the database can read it.
- Check `Origin`/`Host` on the WS upgrade; bind LAN by default.
- Credentials/tickets never leave the core. `slog`; no credentials or message
  bodies at info level.
- Outbound F-Chat messages are not client-throttled; oversends surface as
  per-command `ERR`.

## Testing

- Fixture-based framing/parse/serialize tests, including malformed input.
- `test/fakeserver` + `test/fakeui` (over `test/fchatpipe`) drive end-to-end
  tests without a network.
- Coverage: fan-out and resync, duplicate/out-of-order reconciliation,
  disconnect classification, and both stores exercised through one shared
  suite (`test/memstore` + `OpenSQLite`). Run anything touching hub, sessions,
  or fan-out under `-race`.
