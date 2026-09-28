# WebSocket protocol — turn transport

**Status: implemented.** All four commands are live: `turn.send` and
`turn.edit` open server-scoped turns on the hub's own context;
`turn.cancel` cancels an exact live turn by `turnId` (never the
room-wide worker interrupt); `turn.watch` replays the bounded journal
under the hub lock and attaches for live frames, answering
`turn.resync_required` when the journal can't satisfy the window. The
OpenAPI spec in [`openapi.yaml`](./openapi.yaml) covers only the HTTP
upgrade — it does not define frames.

## Connection

- **Endpoint:** `GET /api/ws`, upgraded to
  `ws://127.0.0.1:<port>/api/ws` or another configured `server.hosts`
  alias. The default port is `10998`; default aliases are `127.0.0.1`
  and `localhost`.
- **Security:** the handshake must satisfy the same rules as unsafe HTTP
  methods — `Host` and `Origin` must match a configured host alias with
  the configured port. Violations are rejected `403` *before* the socket
  opens; there is no in-band auth and no token.
- **One user, many tabs:** the server accepts multiple concurrent
  sockets (the browser may hold several tabs). Turn events are delivered
  to the socket that owns the turn plus any socket that has attached via
  `turn.watch` — there are no room-wide broadcasts in the initial
  contract. A second tab does not passively observe another tab's turn;
  it sees persisted state through HTTP reads.
- **Disconnects are not cancels.** Turn commands run under a
  server-scoped cancellable context, not the socket's — closing the
  socket never aborts a running or queued turn. Recovery is
  `turn.watch` + HTTP re-read.

## Frame model

All messages are **JSON text frames**; binary frames are not used.
Ping/pong uses the protocol's own control frames, not JSON.

Common envelope fields:

| Field | Present on | Meaning |
|---|---|---|
| `type` | every frame | Frame kind, e.g. `turn.send`. |
| `requestId` | every client command; echoed on that command's replies | Client-chosen opaque correlation id (recommend unique per command). |
| `conversationId` | commands and all turn frames | The room the turn belongs to. On `turn.send` it may be `null`/absent — the server mints one. |
| `turnId` | all turn frames once allocated | Server-minted `t_<base32>` id, unique per turn. |
| `seq` | turn lifecycle and stream frames | Per-turn strictly increasing integer sequence — see [sequencing](#sequencing-and-ordering). |
| `payload` | commands and most events | Frame-specific body. |

## Client → server

### `turn.send` — submit a user message

```json
{
  "type": "turn.send",
  "requestId": "req-01",
  "conversationId": null,
  "payload": {
    "route": "openrouter",
    "modelId": "openai/gpt-5",
    "content": "the cache test still flakes — look at invalidate()",
    "effort": "high",
    "provider": "DeepInfra",
    "workspace": "/home/alice/project"
  }
}
```

- `conversationId` — `null`, absent, or a client-minted **path-safe** id.
  When absent the server mints a `c_<base32>` domain id *immediately*,
  before the job is scheduled — `turn.opened` always names the room.
- `payload.route`, `payload.modelId` — optional overrides. On a brand-new
  conversation both are required (nothing is bound yet — a missing
  binding surfaces as `turn.failed` `400` when the job runs); on an
  existing one `""`/absent reuses the stored binding. A successful turn
  re-binds the overrides to the room.
- `payload.effort` — optional reasoning-effort override for the turn:
  `default`, `none`, `low`, `medium`, `high`, `xhigh`, `max`. Absent or
  `default` leaves the model's configured effort alone.
- `payload.provider` — optional upstream-backend pin for aggregator
  routes (e.g. OpenRouter's `provider.order`). `""`/absent lets the
  gateway route automatically.
- `payload.workspace` — optional absolute directory the room's tools work
  in; rejected when relative or not an existing directory. A successful
  turn re-binds it to the room — per-conversation, like route/model.
- `payload.content` — required non-empty text. **Text only**: no
  attachments, no `file://` or `data:` URIs. Attachment upload is future
  work, out of scope for this contract.
- A command that fails **syntactic validation** — unparseable JSON,
  unknown `type`, missing or ill-typed required fields, a non-path-safe
  `conversationId`, empty `content`, an unknown `effort` word, or a
  `workspace` that isn't an absolute existing directory — is rejected
  with an `error` frame
  (`code: "invalid_argument"`). No `turnId` is allocated, nothing is
  scheduled, and no `turn.opened`/`turn.failed` exist for it — it never
  became a turn.
- A syntactically valid command is **accepted**: the server allocates a
  `turnId`, emits `turn.opened`, and schedules an asynchronous
  submission. From that point the turn always ends with exactly one
  terminal `turn.completed` or `turn.failed` — including submission-time
  failures such as a full room queue (`429`) or a deleted room (`410`).

### `turn.edit` — rewrite a user message and regenerate

```json
{
  "type": "turn.edit",
  "requestId": "req-02",
  "conversationId": "c_k4j9xm2p8q1vz7w0t6nb5r3yhd",
  "payload": {
    "messageId": "m_9p2xv7kq4f1nz8w0t6jb5r3yce",
    "content": "actually, look at invalidate() in cache.go"
  }
}
```

- Same lifecycle as `turn.send` (`turn.opened` → stream → terminal).
- Only user messages are editable — a non-user `messageId` ends the turn
  `turn.failed` `400`; an id that exists nowhere in history is `404`.
- **Preempts:** the edit interrupts the room's running job and cuts ahead
  of queued sends, which then run against the rewritten history. The
  preempted turn ends `turn.failed` `408`.
- Terminal frames for an edit never claim `userMessagePersisted` (the
  edit rewrites an existing line, it does not append one), and
  `resyncRequired` is always `true` — the rewrite drops everything
  recorded after the target.

### `turn.cancel` — cancel one turn

```json
{
  "type": "turn.cancel",
  "requestId": "req-03",
  "conversationId": "c_k4j9xm2p8q1vz7w0t6nb5r3yhd",
  "turnId": "t_3kd9xm2p8q1vz7w0t6nb5r4yhf"
}
```

- Targets **one exact turn** — identified by `turnId` — including while
  it is still sitting in the room's queue. The hub cancels the
  server-scoped context it created for that turn: the existing job
  contract already aborts a queued submission before it touches anything
  and interrupts a running one, so the room-wide
  `ConversationApp.Cancel` — which interrupts *whatever happens to be
  running* on the room — is never invoked here.
- Reply: `turn.cancel_ack` — meaning **the request was accepted**, nothing
  more. The canceled turn still emits its own terminal — normally
  `turn.failed` `408`, but if the job had already committed before the
  context was observed the terminal may be `turn.completed`. An ack is
  never a guarantee of interruption.
- An unknown, finished, or foreign `turnId` answers an `error` frame
  (`code: "not_found"`), not an ack.

### `turn.watch` — attach / reattach to a turn

```json
{
  "type": "turn.watch",
  "requestId": "req-04",
  "conversationId": "c_k4j9xm2p8q1vz7w0t6nb5r3yhd",
  "turnId": "t_3kd9xm2p8q1vz7w0t6nb5r4yhf",
  "afterSeq": 12
}
```

- Attaches this socket to the turn and replays journaled frames with
  `seq > afterSeq` verbatim (original `requestId`s included). `afterSeq`
  `-1` replays the whole journal from `seq` 0. Afterwards the socket
  stays attached and receives live frames.
- If the turn is unknown, the journal has expired, the `turnId` belongs
  to a different conversation, `afterSeq` exceeds the latest `seq`, or a
  retained range has a gap at `afterSeq`, the reply is
  `turn.resync_required` — the client must then re-read via
  `GET /api/conversations/{id}` and `GET .../messages` and render
  **persisted** state. Missing deltas are never fabricated.
- On a **gap in an active turn** the socket answers
  `resync_required` and still attaches for live frames — the tab sees
  the terminal while it re-reads history; a finished turn with a gap
  answers `resync_required` and attaches nothing.
- Watching a finished turn replays whatever the journal still holds
  (possibly through its terminal frame) or answers `resync_required`.

## Server → client

### Command replies

`turn.opened` — transport acceptance of a send/edit. It means exactly
this: the command was syntactically valid, ids were allocated, and an
asynchronous submission was scheduled. It says **nothing** about the
room worker — the underlying application `Send`/`Edit` call blocks
until the job completes, so the hub cannot truthfully claim the job was
even queued when `opened` goes out. A full mailbox (`429`), a deleted
room (`410`), an unbound route (`400`), or a non-editable `messageId`
all surface later as the terminal `turn.failed`. `seq: 0` is always the
first frame of a turn, and the server must write `opened` to the socket
and the journal **before** any later frame of that turn can be emitted —
each socket's writes go through one serialized writer with a bounded
buffer.

```json
{
  "type": "turn.opened",
  "requestId": "req-01",
  "conversationId": "c_k4j9xm2p8q1vz7w0t6nb5r3yhd",
  "turnId": "t_3kd9xm2p8q1vz7w0t6nb5r4yhf",
  "seq": 0
}
```

`turn.cancel_ack` — a `turn.cancel` was accepted (no `seq`; it is a
reply to the cancel command, not part of the turn stream):

```json
{
  "type": "turn.cancel_ack",
  "requestId": "req-03",
  "conversationId": "c_k4j9xm2p8q1vz7w0t6nb5r3yhd",
  "turnId": "t_3kd9xm2p8q1vz7w0t6nb5r4yhf"
}
```

`turn.resync_required` — a `turn.watch` could not be satisfied:

```json
{
  "type": "turn.resync_required",
  "requestId": "req-04",
  "conversationId": "c_k4j9xm2p8q1vz7w0t6nb5r3yhd",
  "turnId": "t_3kd9xm2p8q1vz7w0t6nb5r4yhf"
}
```

`error` — a command rejection that belongs to no turn: unparseable JSON,
an unknown `type`, a syntactically invalid command (missing or ill-typed
required fields, a non-path-safe `conversationId`, empty `content`), or
`turn.cancel` naming an unknown or finished `turnId`. `requestId` is
echoed when it could be parsed. There is no `turnId` and no `seq` —
nothing was allocated — and no turn frames follow.

```json
{
  "type": "error",
  "requestId": "req-05",
  "code": "not_found",
  "message": "no live turn with that id"
}
```

### Stream frames

One turn's frames, in order. Every stream frame carries `conversationId`,
`turnId`, and `seq`.

`turn.delta` — incremental assistant text:

```json
{
  "type": "turn.delta",
  "conversationId": "c_k4j9xm2p8q1vz7w0t6nb5r3yhd",
  "turnId": "t_3kd9xm2p8q1vz7w0t6nb5r4yhf",
  "seq": 1,
  "payload": { "text": "Found it — " }
}
```

`turn.phase` — a coarse status marker. `phase` is currently only
`"reasoning"`: the model is producing reasoning. One marker is emitted
per reasoning stretch, no matter how many chunks the provider streams.
All provider reasoning text, including raw `reasoning_content` and
`reasoning_text` as well as provider-supplied summaries, crosses as
`turn.reasoning` chunks in the same order as other stream events:

```json
{
  "type": "turn.phase",
  "conversationId": "c_k4j9xm2p8q1vz7w0t6nb5r3yhd",
  "turnId": "t_3kd9xm2p8q1vz7w0t6nb5r4yhf",
  "seq": 2,
  "payload": { "phase": "reasoning" }
}
```

`turn.reasoning` — one bounded chunk of provider reasoning text. Every
nonempty provider chunk crosses **in order**; chunks
larger than the 32 KiB outbound bound are split UTF-8-safely, so
reassemble by concatenating `text` fields in `seq` order. The text is
untrusted display content — render it escaped, never as markup:

```json
{
  "type": "turn.reasoning",
  "conversationId": "c_k4j9xm2p8q1vz7w0t6nb5r3yhd",
  "turnId": "t_3kd9xm2p8q1vz7w0t6nb5r4yhf",
  "seq": 3,
  "payload": { "text": "I'll compare the two cache paths…" }
}
```

`turn.tool_call` — the model requested a tool. This frame carries only
call metadata, so oversized arguments can never turn the request itself
into an oversized frame:

```json
{
  "type": "turn.tool_call",
  "conversationId": "c_k4j9xm2p8q1vz7w0t6nb5r3yhd",
  "turnId": "t_3kd9xm2p8q1vz7w0t6nb5r4yhf",
  "seq": 3,
  "payload": { "callId": "call_9f2a", "name": "read_file" }
}
```

`turn.tool_input` — one bounded chunk of the exact `input` argument
string the model produced. Chunks follow the owning `turn.tool_call`
in order; concatenate `text` in `seq` order to reconstruct the argument
before rendering or copying it:

```json
{
  "type": "turn.tool_input",
  "conversationId": "c_k4j9xm2p8q1vz7w0t6nb5r3yhd",
  "turnId": "t_3kd9xm2p8q1vz7w0t6nb5r4yhf",
  "seq": 4,
  "payload": { "callId": "call_9f2a", "text": "{\"path\":\"README.md\"}" }
}
```

`turn.tool_output` — a live output chunk for the identified call. Every
nonempty provider chunk is delivered **in order**; chunks larger than
the 32 KiB outbound bound are split UTF-8-safely into multiple frames,
so a burst or a large output produces one frame per bounded chunk —
reassemble by concatenating `text` fields in `seq` order. This is
progress content, **not** proof the call finished: settlement is only
ever implied by later model activity or the terminal.

Tool input and output are arbitrary data — they can legitimately
contain credentials, local paths, or commands. They cross because this
is a single-user loopback UI for its owner; treat them as untrusted
display text, never markup. The same data persists in the call's record
and is retrievable over HTTP history (`input`/`output`/`calledAt`).
Frames carry JSON strings, not binary: malformed UTF-8 emitted by a tool
is normalized by JSON encoding and is not promised byte-exact.

```json
{
  "type": "turn.tool_output",
  "conversationId": "c_k4j9xm2p8q1vz7w0t6nb5r3yhd",
  "turnId": "t_3kd9xm2p8q1vz7w0t6nb5r4yhf",
  "seq": 4,
  "payload": { "callId": "call_9f2a", "text": "partial output chunk" }
}
```

`turn.step_finished` — one provider call ended. This maps the domain's
`TurnEventFinished`, which is **per provider call, not per turn** — a
tool-using turn emits several of these. `finishReason` is one of `stop`,
`tool_calls`, `length`, `content_filter`; `usage` is the same shape as
the HTTP `Usage` projection:

```json
{
  "type": "turn.step_finished",
  "conversationId": "c_k4j9xm2p8q1vz7w0t6nb5r3yhd",
  "turnId": "t_3kd9xm2p8q1vz7w0t6nb5r4yhf",
  "seq": 5,
  "payload": {
    "finishReason": "tool_calls",
    "usage": {
      "inputTokens": 4120,
      "cacheReadTokens": 0,
      "cacheWriteTokens": 0,
      "outputTokens": 203,
      "totalInputTokens": 4120,
      "totalTokens": 4323
    }
  }
}
```

### Terminal frames

Exactly one per turn, always the last frame.

`turn.completed` — emitted **only** after the application call returned
success and all persistence for the turn finalized. A `turn.step_finished`
is never a substitute: the domain finished event ends one provider call;
only the job's return ends the turn.

```json
{
  "type": "turn.completed",
  "requestId": "req-01",
  "conversationId": "c_k4j9xm2p8q1vz7w0t6nb5r3yhd",
  "turnId": "t_3kd9xm2p8q1vz7w0t6nb5r4yhf",
  "seq": 9,
  "payload": { "userMessagePersisted": true, "resyncRequired": true }
}
```

- `userMessagePersisted` — present on send turns, always `true` at
  `turn.completed` (a send cannot complete without committing the user
  line). Absent on edit turns.
- `resyncRequired` — **always `true`** on an accepted turn's terminal
  frame. Live frames never carry persisted message ids, tool results, or
  rewritten history, so a client MUST re-read `GET
  /api/conversations/{id}` and `GET .../messages` after every terminal
  frame rather than reconstructing state from the stream.

`turn.failed` — the turn ended unsuccessfully. This is terminal for
**every** failure mode of an accepted turn: queue rejection,
cancellation, provider errors, engine faults — a client never waits
indefinitely after `turn.opened`. (A command rejected before
allocation gets `error`, not this frame — there is no turn to fail.)

```json
{
  "type": "turn.failed",
  "requestId": "req-01",
  "conversationId": "c_k4j9xm2p8q1vz7w0t6nb5r3yhd",
  "turnId": "t_3kd9xm2p8q1vz7w0t6nb5r4yhf",
  "seq": 9,
  "payload": {
    "status": 502,
    "code": "upstream_error",
    "message": "upstream request failed",
    "userMessagePersisted": true,
    "resyncRequired": true
  }
}
```

- `status` — the same HTTP-shaped status the REST surface would return
  for the failure: `400` invalid, `404` missing, `408` canceled,
  `410` deleted-underneath, `413` provider context overflow, `429` room
  queue full (or upstream rate-limit, `code` `upstream_error`), `500`
  internal, `502`/`504` upstream, `503` shutting down.
- `code` — the same stable code strings as the HTTP problem catalog
  (`invalid_argument`, `not_found`, `interrupted`, `gone`,
  `input_too_large`, `queue_full`, `internal`, `upstream_error`,
  `unavailable`, `upstream_timeout`). Command-level `error` frames may
  additionally carry `unimplemented` for a recognized command whose
  phase has not landed yet (none today — reserved for future commands)
  and `queue_full` for turn admission, not just room mailboxes.
- `message` — display-safe only; **internal causes are never exposed**.
- `userMessagePersisted` — on send turns, whether the user line
  committed before the failure (`true` = it is in history even though
  the turn died — the failed-send receipt). On edit turns it is absent
  or `false`, never claimed.
- `resyncRequired` — always `true`, same as `turn.completed`: re-read
  persisted state over HTTP after every terminal frame.

## Sequencing and ordering

- `seq` is **per turn**, starting at `0` (`turn.opened`) and strictly
  increasing by one for every subsequent frame of that turn, terminal
  frame last. `turn.cancel_ack`, `turn.resync_required`, and `error`
  are command replies and carry no `seq`.
- Frames of one turn are totally ordered. **There is no cross-turn
  ordering guarantee** — interleaved frames of two turns are valid, and
  clients correlate by `turnId`, never by arrival position.
- `requestId` correlates a command to its `turn.opened`, terminal frame,
  `turn.cancel_ack`, or `turn.resync_required`/`error` reply — whichever
  applies. Stream frames mid-turn do not repeat it (they attribute via
  `turnId`).

## Liveness, size, and backpressure

- **Admission is bounded.** The hub admits a fixed number of concurrent
  turns; beyond it a `turn.send` is rejected before any work or id
  allocation with a pre-allocated `error`/`queue_full` reply. The
  admission cap is a deployment constant — the contract guarantees only
  that the bound exists.
- **Ping/pong** ride the WebSocket control frames: the server pings on a
  regular interval and drops a socket that misses its pong deadline;
  client pings are answered. Intervals are implementation constants,
  not negotiated.
- **Frame size** is bounded in both directions. An oversized inbound
  frame is a connection-level violation: it is rejected with an `error`
  frame when parseable, and the socket may be closed with a
  policy-violation or too-big close code — it is *not* a `turn.failed`
  `413` (that status in a terminal frame means an application/provider
  payload limit, not frame size). Byte limits are deployment constants;
  the contract guarantees only that they exist.
- **Backpressure:** the domain's `TurnEventHandler` is synchronous — a
  blocked consumer stalls the provider read loop. The hub therefore
  buffers per subscriber with a bounded queue; on overflow it terminates
  *that subscriber's delivery* (close the socket, or drop it from the
  turn's fan-out), never the turn and never other subscribers.
- **Stream frames are bounded too.** A single provider delta, tool
  argument, or tool output burst is split into multiple frames rather
  than allowed to dwarf the journal or one subscriber's queue — chunks
  are cut on UTF-8 boundaries so reassembly is byte-exact.
- **The journal is bounded.** Each turn keeps only a bounded tail of
  frames (frame count and byte caps, plus an age limit on finished
  turns) — enough for short-window `turn.watch` reattachment, never
  unbounded growth. A window the journal can't satisfy honestly is a
  `resync_required`, never fabricated deltas.

As implemented today the constants are: max 32 concurrent turns, max 64
concurrent sockets (a full hub rejects the upgrade with `503`
problem+json before the handshake), 64 KiB inbound frames, 64 queued
outbound frames per socket, a hard 1 MiB cap on any single outbound
frame (a frame that can't fit — e.g. oversized provider-originated
metadata — terminates that turn with a sanitized `turn.failed` `413`),
a journal of 512 frames / 4 MiB / 2 minutes post-terminal retention,
pings every 25 s with a 60 s pong deadline, a 10 s write deadline, and
`turn.delta`, `turn.reasoning`, `turn.tool_input`, and
`turn.tool_output` text chunks of at most 32 KiB.

## Lifecycle races, catalogued

- **`opened` before any real work.** `turn.opened` only means the
  command was accepted and scheduled — the job may still bounce off a
  full mailbox (`429`), a deleted room (`410`), an unbound route
  (`400`), or a non-editable `messageId` (`400`/`404`); every such
  outcome arrives as the terminal `turn.failed`.
- **Disconnect mid-turn.** Nothing aborts; the turn runs to its terminal
  state against the server-scoped context. Reconnect + `turn.watch`
  (or plain HTTP re-read) recovers it.
- **Cancel racing completion.** `turn.cancel` against a turn that already
  finished answers `error`/`not_found` — there is no retroactive cancel.
- **Edit preemption.** An edit preempts the running turn (`turn.failed`
  `408` on the preempted turn) and queues ahead of pending sends.
- **Delete under queued work.** `DELETE /api/conversations/{id}` retires
  the room; queued turns end `turn.failed` `410 gone`.
- **Failed send with a committed user line.** `turn.failed` may carry
  `userMessagePersisted: true` — the message is in history; render from
  `GET .../messages`, not from the frame stream.
- **Shutdown.** A graceful shutdown ends live turns `turn.failed` `503`
  before closing sockets — best-effort only: on an abrupt process exit
  (crash, `SIGKILL`) no terminal frame is guaranteed. After a restart
  the client reattaches with `turn.watch` or re-reads persisted state
  over HTTP; a turn orphaned by a crash resolves to `resync_required` or
  whatever history actually committed.

## Out of scope (initial contract)

- Tool approval prompts or policy questions to the browser — tool
  permission is decided by the domain schema, not negotiated per call.
- Settings or gateway change events — poll the HTTP endpoints.
- Room-wide presence/broadcast events — delivery is owner + watchers only.
- Attachments of any kind on `turn.send`.
- Binary frames, subprotocols, per-message compression — plain JSON text
  frames only.
