# Core ↔ client streaming model

Why the core→client stream is shaped the way it is. The wire is specified in
[core-protocol.md](core-protocol.md); the broker's place in the core is in
[architecture.md](architecture.md#layout). Where this note and the code
disagree, the code wins.

> **Deployment invariant — core and client are one artifact.** The UI is
> embedded in the core binary, so in production they are the same version and
> cannot drift. The model targets a single-version wire: no compatibility shims,
> no version negotiation, and no legacy-client branches.

## Primitives

The stream carries exactly two update kinds, plus a transient error:

| Kind | Semantics | Ordering | Transport |
| --- | --- | --- | --- |
| `message` | append-only timeline entry for one `(session, conv)` | total, `conv_seq` | live event; older pages over HTTP |
| `state` | set-to record addressed by one flat key | none; latest value wins | live event; full set in the `snapshot` |
| `error` | transient per-command failure | none; never coalesced or resynced | live event |

`conv_view` materializes one conversation (its metadata, members, and a recent
`message` window). It is its own ordered kind, not a `state` record, so a view
always precedes the entries that raced its build. The window is capped at the
client's retained timeline size, so a view never ships entries the client
trims away. A repeat visit that supplies the client's `since` cursor gets a
**delta** view (only the missed entries, no members) instead of a full
materialization; a gap larger than one window falls back to the full view.

A `state` key encodes its scope, and both coalescing and delivery follow the
key rather than the event's producer:

```
account/<name>                          account-wide set (friends/bookmarks, ignores, catalog)
session/<character>                     one session's lifecycle
conv/<character>/<kind:id>              one conversation's metadata
summary/<character>/<kind:id>           one conversation's activity aggregate
typing/<character>/<kind:id>/<name>     one typist (ephemeral)
character/<name>                        one character's presence
search/<character>                      cached search result revision
invites/<character>                     pending room invitations (set-to)
ads/<character>                         live advertisement scheduler status
```

- `account/*`, `session/*`, `search/*`, `invites/*`, and `ads/*` reach every
  subscription.
- `conv/*` and `summary/*` are gated by that conversation's interest;
  `summary/*` is delivered only at `summary` interest, so a `full` subscriber
  derives activity from the `message` entry and never sees both.
- `typing/*` is delivered only at `full` interest.
- `character/*` is delivered for a watched character: an account friend, the
  session's own character, or a member of a `full` conversation. The broker
  stores each presence record once and drops the records a logout leaves
  unreachable, so the store (and a broad resync) does not grow without bound.
- Every session-scoped namespace is declared once in `model`'s state-namespace
  registry (scope + key shape), which drives the logout cleanup. A new namespace
  therefore cannot silently outlive the session that produced it.

A `notice` (an invalidation of an HTTP-pulled resource) is expressed as a
`search/<character>` state record rather than a third primitive: the record
carries only a revision and the rows are pulled over HTTP. The `version` field
once proposed for state records was dropped — on one ordered socket the key is
the whole identity, and resync re-reads the latest value.

## Subscription lifetime

The subscription is durable and separate from the socket that carries it:

- The first envelope a client sends is `subscribe`, naming a client-generated
  `id` stable for the life of one JS `Transport` (a page reload starts a new
  one). The bridge keys the broker subscription by that id.
- A socket drop **detaches** the socket but keeps the subscription and its
  interest for a short grace window (`DefaultSubscriptionGrace`). A reconnect
  with the same id re-attaches; the server's `hello{resumed:true}` tells the
  client it need not re-assert anything.
- A new id, or a reconnect after the grace expires, starts a fresh subscription
  at the default `summary`; `hello{resumed:false}` makes the client re-assert
  `full` for each session's active conversation.

This is why a transient blip does not reset interest, and why the client's
re-assertion path only runs for a genuinely new subscription.

## Resync by key

A subscription's shared state store holds the latest value per key, so a
delivery gap is not repaired by rebuilding state the client already has.
Instead the broker records the set of dirty state keys and conversations whose
stream gap cannot be reconstructed, and the next tick:

- re-sends the latest value for each dirty key, and
- re-materializes each dirty conversation (the `conv_view` path).

A full `snapshot` is only for a fresh subscription; a live gap never triggers
one. Because `state` is set-to, no event log is needed — only which keys changed.
