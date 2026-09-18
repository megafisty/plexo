# AGENTS.md

Plexo is a Go core that proxies, aggregates, and persists multiple F-Chat
character sessions, plus an embedded Mithril web client for LAN use. The core
speaks F-Chat; the browser speaks only to the core.

## Working agreement

Two actions require an explicit approval or an imperative statement from the
user before the agent does them:

- **Implementing code changes.** A question or feasibility discussion is not
  authorization to edit files; wait for an explicit go-ahead.
- **Running the UI test harness** (`test/uibrowse` plus `scripts/chrome-devtools.sh`
  and the chrome-devtools MCP) to verify UI. Build and unit checks do not need
  approval.

## Where to look

Design docs are split by topic under `docs/`. Read only what the task needs.

| Task                                           | Read                    |
| ---                                            | ---                     |
| Overview, goals, status                        | `docs/overview.md`      |
| Package layout, concurrency, ops, security     | `docs/architecture.md`  |
| Domain model, SQLite schema, persistence rules | `docs/domain.md`        |
| Chatlog activity model (scopes, bursts, thresholds) | `docs/activity.md` |
| Settings storage, scopes, API, limits          | `docs/settings.md`      |
| Upstream F-Chat wire protocol, tickets         | `docs/fchat.md`         |
| Downstream core↔client WebSocket protocol      | `docs/core-protocol.md` |
| Core↔client streaming model rationale          | `docs/streaming.md`     |
| Session lifecycle, reconnect/disconnect policy | `docs/sessions.md`      |
| BBCode parsing/sanitization                    | `docs/rendering.md`     |
| Client state model, performance                | `docs/ui-state.md`      |
| Client component architecture                  | `docs/ui-components.md` |
| Warpmarks / message permalinks                 | `docs/warpmarks.md`     |
| UI CSS composition, build, tokens              | `docs/ui-css.md`        |
| Open future work                               | `TODO.md`               |

`docs/fchat/API.html` is the authoritative F-Chat reference.
`README.md` is human-edited and should never be touched by agents.

## Commands

```sh
go build ./...
go test ./...
go test -race ./...   # required for anything touching hub, sessions, or fan-out
go vet ./...
gofmt -l .

./ui/build.sh         # tsc -> ui/app/*.js; sass -> ui/base.css, ui/themes.css
                      # all output is gitignored; run before go build
                      # tsc also typechecks the generated boundary types
go generate ./cmd/tsgen   # regenerate ui/src/transport/{types.gen,enums}.ts
                          # (go test ./cmd/tsgen fails if either is stale)
./ui/test.sh          # headless store/transport unit tests (tsc + node:test)
go run ./cmd/plexo    # dev harness; 'r' or SIGHUP reloads the BBCode parser

test/uibrowse         # UI against the fake F-Chat (http://127.0.0.1:8091)
scripts/chrome-devtools.sh start   # disposable headless Chromium, CDP on :9222
                                   # for the chrome-devtools MCP; stop | status
```

Do scratch work under `/tmp/plexo-piwork`. The browser launcher keeps its
temp profile there too; never point `--remote-debugging-port` at a daily
browser profile.

## Conventions

- Go, no CGO (SQLite via `modernc.org/sqlite`).
- Only `internal/fchat` knows F-Chat protocol details.
- Sessions are actors: no shared mutable state; communicate over channels.
- State updates are idempotent ("set to", not "increment").
- Frontend: TypeScript compiled by `tsc` alone (no bundler). Sources in
  `ui/src/`, generated output in `ui/app/`, vendored Mithril in `ui/vendor/`,
  served with `go:embed`. CSS is compiled by `sass` (Dart Sass) from
  `ui/base.scss` + `ui/themes.scss` to flat `ui/base.css` + `ui/themes.css`
  (no `@layer`/`@import`; QtWebKit target). Compiled output is gitignored, so
  run `./ui/build.sh` before `go build` on a fresh checkout.
- Structured logging via `slog`; never log credentials, tickets, or message
  bodies at info level.
- Frontend helpers: a pure function shared by two or more feature folders lives
  in `ui/src/lib/`; one used by a single component stays local. Feature folders
  never import a sibling folder just to reach a helper. Placement table:
  `docs/ui-components.md` ("Helper placement").
- F-Chat credentials and tickets never cross to the client.

## Invariants

- `conv_seq` is assigned exactly once, in the session actor, before an entry is
  both persisted and published. History pages and live events share one cursor.
- Sessions own their state; mutate only from the actor goroutine.
- Persist only durable, shared content. Never persist presence, membership,
  unread/read cursors, typing, or LRP ads.
- Classify every disconnect through `session.classify`; never auto-reconnect on
  a documented non-retryable condition.

## Testing

- Extend `test/fakeserver` (fake F-Chat) and `test/fakeui` (subscriber) instead
  of adding network-dependent tests.
- Use `test/memstore.New()` in tests; exercise `store.OpenSQLite` against a temp
  file.
- Client-side store logic is covered by `./ui/test.sh`: plain `.mjs` tests under
  `ui/test/` run against the compiled `ui/app` with browser stubs, no network.
  Extend those for pure reducer/window/interest behavior instead of driving the
  manual `test/uibrowse` harness.
