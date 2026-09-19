# F-Chat protocol

Reference: `docs/fchat/`; `API.html` is authoritative.

## Transport and framing

- WebSocket `wss://chat.f-list.net/chat2` (Hybi frames).
- `permessage-deflate` (context takeover) is offered at dial; the connection
  stays uncompressed if F-Chat does not negotiate it.
- Every command is `XXX {json}`: exactly three case-sensitive uppercase chars,
  a space, then JSON. No payload ⇒ **no trailing space**. <3 chars disconnects.

## Authentication

- `POST https://www.f-list.net/json/getApiTicket.php` with form-encoded
  `account`+`password` (HTTPS) returns a **ticket**.
- Tickets last **30 minutes**; minting a new one invalidates the previous one for
  that account.
- WS login: `IDN {"method":"ticket","account":...,"ticket":...,"character":...,
  "cname":...,"cversion":...}`.
- A session is READY only after `IDN` and its own `NLN`.
- Every IDN failure is fatal: close and reopen the socket; IDN cannot be re-sent.
- No re-auth or ticket check on an established connection.

## Tickets

Needed for `IDN` and REST calls (`account`+`ticket`); live sessions do not.

- Mint lazily; no background refresh (it would invalidate other clients'
  tickets for no gain).
- Cache `{ticket, mintedAt}` per account as a best-effort hint; any other client
  minting can invalidate ours.
- One shared ticket per account, one mutex around minting.
- `IDENT_FAILED` ⇒ the cached ticket was invalidated: mint once and retry, or
  surface (see [sessions.md](sessions.md)). Credentials never reach clients.

## Character field mapping (`mapping-list`)

`POST https://www.f-list.net/json/api/mapping-list.php` (no authentication)
returns the lookup tables behind character profile fields: `kinks`,
`kink_groups`, `infotags`, `infotag_groups`, and `listitems` (the values of a
`list`-type infotag, e.g. `gender` and `orientation`). Every property is a
string. The core fetches it once at startup and caches it in memory
(`internal/fchat`); a committed curl capture at
`test/fixtures/mapping-list.json` is used offline for dev/testing. At load the
core reduces the raw tables to the search shape the UI consumes (one field per
FKS filter, see [core-protocol.md](core-protocol.md)) before caching, so
`/api/mapping` never serves the raw lookup tables. It is not part of the chat
protocol and is never fetched per session.

## Login failures

| IDN failure | Meaning | Auto-retry |
| --- | --- | --- |
| `IDENT_FAILED` | bad login/ticket | no |
| `TOO_MANY_FROM_IP` | per-IP cap | no |
| `BANNED_FROM_SERVER` | banned | no |
| `LOGGED_IN_AGAIN` | character taken over | no |
| `UNKNOWN_AUTH_METHOD` | wrong login method | no |
| `SERVER_FULL` | server full | no |
| `NO_LOGIN_SLOTS` | login busy | no |

## Error command

`ERR {"code": int, "message": str}` — the older wiki calls the field `number`;
accept both. The server may close immediately after. Codes that must **never**
auto-reconnect: `2, 4, 9, 30, 31, 33, 39, 40, 62`. fserv sends generic
human-readable messages (`"Identification failed."`), not the symbolic code
names, so classification must key on the numeric code.

## Pitfalls (requirements)

- Unknown server command → discard; unknown property → ignore.
- Property of the wrong type → disconnect, **no** auto-reconnect.
- Messages from unknown characters → safe defaults.
- Messages for channels we are not in → ignore.
- Unsolicited `JCH` for our own character → honor as a real join.
- The server may trail-drop when a connection's queue fills, producing
  missing/duplicate/out-of-order events. **Reconcile idempotently; do not
  disconnect.** Updates are "set to", not increments.

## Channels and rooms

- `CHA` lists official channels; `ORS` lists open rooms.
- `JCH` carries `channel` (name or hash) plus `title`; `ICH` the user list and
  mode; `CDS` the description.
- Official channels use their name; rooms use their hash ID.
- Channel-scoped state (`JCH`/`ICH`/`CDS`/`COL`/`COA`/`COR`/`RMO`/`MSG`) is
  applied only while the session is joined to, or joining, the channel. A `JCH`
  for our own character is a real join; `OpJoin` marks the conversation joining
  before the reply is written, so an `ICH`/`COL` that trails it is kept. An
  `ICH` naming us is also accepted as evidence of the join; the channel stays
  non-live until self `JCH` confirms. State for a channel we are not in is
  ignored, never applied and then hidden.

### Room management

- Client→server verbs: `CCR` creates a closed, invite-only private room (title
  capped at 64 escaped bytes) and force-joins us — its hash id arrives with the
  self `JCH`; `KIC` destroys a room; `CDS` sets the description (capped by the
  `cds_max` variable); `COA`/`COR` add/remove a room op; `CKU` kicks; `CBU`
  bans; `CUB` unbans or clears a timeout; `CTU` times out (length in minutes);
  `CSO` transfers ownership; `RST` opens or closes a room; `RMO` sets the
  chat/ads/both message mode; `CIU` invites. The server is the authority on
  rights; a rejected action returns `ERR` and leaves the session up.
- Rights are split: `COA`/`COR`/`CSO`/`RMO`/`RST`/`KIC` require the **owner**
  (or admin/global), while `CDS`/`CKU`/`CBU`/`CTU` accept any op. `CSO` and
  `CIU` require the target to be **online**; `CIU` also requires the caller to
  be in the room and, for a private room, to be an op or the owner.
- `COL` is the full op list (set-to) and its **first entry is the channel
  owner** — which may be the empty string. The rest are mods. `COA`/`COR` are
  the incremental mod changes and `CSO` the owner change; a `CSO` is normally
  followed by a fresh `COL`.
- `CBU`/`CKU`/`CTU` are broadcast to the whole room, so every participant can
  track the ban/timeout set. `CUB` and `CBL` answer only the caller (as `SYS`),
  so an unban performed by someone else is not observable and the core applies
  a local unban optimistically when it sends `CUB`.
- `RST` (publish/close) changes the room type between `CT_PRIVATE` and
  `CT_PUBPRIVATE`, but answers only the requester (a `SYS`) and broadcasts
  nothing. Only `CT_PUBPRIVATE` rooms appear in `ORS`; a private room is hidden
  and addressed by an unguessable `ADH-` hash.
- `CIU` grants access and notifies the invitee (`{sender,title,name}`, `name`
  being the room id); it never force-joins them, and the invitee must still
  `JCH`. Invitations are one-shot (no query), removed when the invitee is
  banned, kicked, or timed out, and persist otherwise for the room's lifetime.

## Server variables (`VAR`)

- `chat_max`/`priv_max`/`lfrp_max` (max lengths; the session stores these and
  enforces them before sending). Other variables exist but are ignored:
  `msg_flood`/`lfrp_flood` (server-side pacing; Plexo does not throttle
  client-side), `permissions`, `icon_blacklist`.
- Exceeding flood limits returns `ERR`. Session-ending ERRs tear the session
  down; every other ERR is a per-command failure surfaced as an `error` event
  while the session stays up.

## Login hydration burst

After login: `HLO`, `IDN`, own `NLN`, `CON`, batched `LIS` (online roster as
positional arrays `["Name","Gender","Status","StatusMsg"]`), `ADL`, `FRL`,
`CHA`, `ORS`, `VAR`. `CON` marks the `LIS` batches complete; this is the
per-session snapshot mirrored to clients. `ADL` is the full global-moderator
list and replaces the set; `AOP`/`DOP` (`{"character"}`) are the incremental
add/remove updates.

Other: `MSG`, `PRI` (has both `character` and `recipient`), `LRP`, `RLL`, `TPN`,
`STA`, `NLN`/`FLN` (`FLN` acts as a global `LCH`), `SYS`, `BRO`, `JCH`/`LCH`,
`ICH`, `CDS`, `COL`/`COA`/`COR`, `CBU`/`CKU`/`CTU`. `COL` is the full channel-op
list and replaces the set; `COA`/`COR` (`{"channel","character"}`) are the
incremental add/remove updates.

`TPN` (`{"character","status"}`) is private-message-only — the server never
signals channel typing — and `status` is `typing`, `paused` (text entered but no
keystrokes in flight), or `clear`. There is no `clear` after a send: a delivered
`PRI` implies the sender stopped typing. Outbound, the client names the DM
recipient in `character`; the server fills in the sender when delivering.

## Character search (`FKS`)

```
<< FKS {"kinks":[int], "genders":[str], "orientations":[str],
        "languages":[str], "furryprefs":[str], "roles":[str]}
>> FKS {"characters":[str], "kinks":[int]}
```

`kinks` is required and always an array (send `[]`, never null); the enum
filters carry the mapping value strings (see [core-protocol.md](core-protocol.md)
and the mapping section above), and `characters` are character names. Search
`ERR`s: `18` no results (treated as an empty result, not an error), `50`
throttle (5 s between searches), `72` too many results. The reply is enriched
with session presence, cached on the session as the latest result set, and
announced; the core keeps no query state.

## Keepalive

Argument-less `PIN` in, argument-less `PIN` out. Three unanswered pings (30s
apart) disconnect; multiple pings within ten seconds also disconnect. This is
the only keepalive; WebSocket ping frames are not used.
