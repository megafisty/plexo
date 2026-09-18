# Session lifecycle and reconnect policy

```
idle ─▶ connecting ─▶ live
             │           │
             ───────────┴──▶ disconnected { reason, severity, autoRetry }
```

An auto-retry re-enters `connecting` with `autoRetry:true` once before
surfacing `disconnected`.

- Only an **unexpected network drop** is auto-retried, exactly **once**; the
  retry reuses a valid cached ticket, otherwise mints one. Failure →
  `disconnected` with `autoRetry:false`.
- Every other failure is surfaced, never auto-retried.
- Per-command `ERR`s are not disconnects: they surface as `error` events and the
  session stays up. Only connection-fatal ERRs produce a `disconnected` state.
- An `ident_failed` disconnect invalidates the cached ticket, so a reconnect
  mints a fresh one instead of replaying the rejected ticket.
- **All** disconnected states are user-retryable: the core's `reconnect`
  command runs the full connect flow (ticket reuse → mint → stored credentials).
  The per-session reconnect control is not built yet ([TODO.md](../TODO.md)).
  Losing the core↔UI socket is a different failure; its recovery is a page
  reload, not a command.
- On core restart all sessions log out (tickets are lost); reconnect is per
  character, no bulk reconnect. Stored F-Chat credentials, if the user opted to
  remember them, are restored and revalidated at startup (see
  [settings.md](settings.md#f-chat-credentials)).

## Disconnect taxonomy

| reason | autoRetry | severity | notes |
| --- | --- | --- | --- |
| `network` | yes (once) | normal | unexpected socket close |
| `no_login_slots` | no | normal | login busy |
| `ident_failed` | no | normal | ticket expired/invalidated |
| `taken_over` | no | severe | `LOGGED_IN_AGAIN` / `ERR 31` |
| `banned` | no | severe | `ERR 9` |
| `kicked` | no | severe | `ERR 40` |
| `timed_out` | no | severe | `ERR 39` |
| `too_many_from_ip` | no | severe | |
| `server_full` | no | severe | `ERR 2` |
| `unknown_auth_method` | no | severe | `ERR 33` |
| `protocol_mismatch` | no | severe | wrong property type |
| `auth_failed` | no | severe | invalid/missing credentials |

Severity classifies the disconnect for presentation; the client currently shows
only the tab's state dot. A user-initiated reconnect is allowed even on a severe
condition and attempts a fresh handshake, which may pick up a protocol update.

## Architectural risk

All characters and devices share one LAN host and thus one public IP.
`TOO_MANY_FROM_IP` is the most likely failure this architecture will trip and is
not auto-retryable; validate early and surface clearly.
