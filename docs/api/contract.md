# WebUI API contract

**Status: phases 1–4 implemented; the Swagger UI (phase 5) remains
proposed.** The HTTP surface — all `GET` routes, the mutations
(`PATCH`/`PUT`/`DELETE`/`POST`), the `Host`/`Origin` security envelope,
and problem+json error mapping — is wired in
`infrastructure/delivery/http`, and the WebSocket surface — upgrade
validation, `turn.send`/`turn.edit` turns, `turn.cancel`/`turn.watch`
turn control, bounded journals with replay, and the full stream/terminal
lifecycle — is wired in `infrastructure/delivery/websocket`; both are
served by `nusallm serve` (loopback `127.0.0.1` only). This document,
the OpenAPI specification in [`openapi.yaml`](./openapi.yaml), and the
WebSocket spec describe the interface the delivery layer must satisfy;
where the current engine cannot yet supply a behavior, the
[implementation
work](#implementation-work-required) section says so explicitly.

## What this is

A single-seat, single-machine web UI for the conversation engine — one
intended user on the loopback host. (Anything on the host can reach the
socket; see [trust model](#deployment-and-trust-model).) The
engine already exposes everything a UI needs through the application
layer — `application/conversation` for turns and room lifecycle,
`application/gateway` for provider management, `application/setting` for
the JSON config — so the WebUI is a transport, not a product change. The
contract has two halves:

- **HTTP** (Gin, OpenAPI-described): resource reads and management
  writes — conversations, messages, gateways, settings, tools, health.
- **One multiplexed WebSocket**: the live channel — streaming turn
  events and the commands that drive turns (send, edit, cancel,
  reattach). There is deliberately **no client-facing SSE**: provider
  streaming stays inside `infrastructure/service` adapters, and turn
  progress reaches the browser only as WS frames. One socket carries
  every conversation's events; events are attributed by
  `conversationId`/`turnId`.

## Scope and non-goals

**In scope** — one intended user on the loopback host:

- Conversation list/detail/history reads, rename, delete, on-demand
  compaction.
- Sending and editing messages, canceling turns, reattaching to a turn
  after a reconnect — all over the one WebSocket.
- Gateway (provider route) management: list, inspect, upsert, delete —
  with write-only credential handling.
- Settings read and leaf-level write through `application/setting`.
- Tool catalog read.
- A locally-served Swagger UI rendering `openapi.yaml`.

**Out of scope, explicitly:**

- **Multi-user, remote access, or any listen address other than
  `127.0.0.1`.** There is no remotely-configurable listen address and no
  authentication or token layer in this contract — see
  [trust model](#deployment-and-trust-model). If remote access is ever
  wanted, that is a new contract, not a flag on this one.
- **Client-facing SSE.** Not offered, ever, on this transport.
- **Attachments in `turn.send`.** The initial send is text-only. No
  browser-supplied `file://` URIs and no `data:` URIs cross the API — a
  browser that could hand the engine a local file path would turn every
  local process into a file exfiltration path. Attachment upload and
  server-held secure references are future work with their own contract.
- **Tool approval prompts, settings-change events, and room-wide
  broadcast events.** Not in the initial frame set.
- **API versioning.** Per the root breaking-changes policy there is one
  contract at a time — no `/v1` path segments, no shims, no deprecated
  fields. Breaking changes land as contract revisions of these
  documents.

**Server-owned hidden field — provider `optionalContent`.** The API
never serializes `optionalContent` in either direction — the transport
does not expose or accept it, full stop. But `PUT /api/gateways/{id}`
is a full replace of the *readable* document, and a plain replace would
destroy each stored model's hidden provider params (e.g. a
`gemini.thinking_budget` written by the CLI). The contract therefore
requires the application layer to **preserve** it: on upsert, each model
in the replacement catalog that shares an `id` with a stored model keeps
that stored model's `optionalContent`; a model dropped from the catalog
loses it with the record. This preservation rule does not exist today —
`application/gateway` upsert must read the existing snapshot and carry
matching models' hidden field forward. It is application-layer work (not
a transport merge — the wire never carries the field), and it must be
atomic against the stored record: gateway writes are not serialized
through a room worker, so a read-modify-write race could lose a
concurrent update. Exposing the field on the API remains future work.

## Deployment and trust model

- The server binds **`127.0.0.1` only** — loopback, one instance. The
  port comes from `server.port` (default `10998`) and can be changed in
  Settings; the change applies after restart. `--port` remains a
  one-process CLI override.
- `server.hosts` is the list of hostname/IP aliases accepted in `Host`
  and `Origin` headers; defaults are `127.0.0.1` and `localhost`.
  Entries omit the port, which is taken from `server.port`. The socket
  still binds only to `127.0.0.1`, so configured hostnames must resolve
  to that loopback listener.
- The trust boundary is the machine: **any local process or OS user on
  the host can reach the socket.** "Single-user" here means one
  *intended* user — this contract provides no access control and does
  not pretend otherwise. There is **no token or auth** — on loopback a
  bearer token would add ceremony, not isolation. Instead the contract
  relies on strict origin checks to keep *other websites* out:

  - **`Host` validation.** Every request must carry a `Host` header
    matching one configured `server.hosts` entry with the configured
    port (for example, `localhost:10998`). Anything else is rejected
    `403`.
  - **`Origin` on unsafe HTTP.** `POST`, `PUT`, `PATCH`, `DELETE`
    requests must carry an `Origin` header matching the same configured
    host and port, such as `http://localhost:10998`. Missing or
    mismatched origins are rejected `403`. Non-browser callers (curl,
    scripts) must supply `Origin` explicitly; there is no "CLI
    exemption" path.
  - **`Origin` on the WebSocket handshake.** The upgrade request must
    carry an `Origin` matching one configured host and port; a missing
    or foreign origin is rejected before the socket opens. This is the
    standard WebSocket CSRF defense — browsers send `Origin` on every
    WS handshake.
  - **Safe methods** (`GET`, `HEAD`) do not require `Origin` — they are
    reads, and the responses are ordinary JSON that a cross-origin page
    could not read anyway under CORS rules (the server sends no CORS
    allow-headers).

  These checks are implementation work — Gin middleware — and are not
  optional plumbing; they are the security model.

- Swagger UI is served by the same server on loopback only, alongside
  the JSON/YAML form of the spec. It documents the HTTP surface; it does
  **not** define WebSocket frames — [`websocket.md`](./websocket.md) is
  normative for those.

## Projection rules — what the server never serializes

Every response body is a sanitized projection. The guarantee is scoped
honestly: it covers **server-held fields**, not message text. The
following never leave the server on any endpoint or frame:

- **API key material and raw key references.** Gateway responses expose
  `keySet` and a `keyState` enum (`unset` | `env` | `vaulted` |
  `broken`), never the stored `apiKey`, never the raw `"env:NAME"` /
  `"secret:<key>"` reference string. `apiKey` is write-only.
- **Internal error causes.** `apperror.Cause` (upstream bodies, stack
  details) stays server-side; the wire sees `code` + `detail` only.
- **Provider `optionalContent`.** Adapter-namespaced replay artifacts
  (`gemini.thought_signature`, `deepseek.reasoning_content`, …) are
  internal state — omitted from message and model read projections and
  not accepted on write. On gateway `PUT` the application preserves it
  per surviving model `id` — see the
  [hidden-field rule](#scope-and-non-goals).
- **Normalized message parts.** Assistant history exposes ordered
  `parts`: answer text, raw or summarized reasoning text, and tool-call
  positions. Live reasoning crosses in order as `turn.reasoning` chunks.
  Provider replay artifacts in `optionalContent` remain internal.
- **Attachment references, not bytes.** Message and tool-output
  attachments project `mediaType`, `modality`, `uri`, and optional
  `name` — the stored reference (including `file://` local paths)
  serializes to the loopback owner by design, and a stored name can
  carry path-shaped data, so clients render both as escaped text.
  Attachment bytes are never served over the API, and the `sha256`
  content pin stays internal.
- **What intentionally crosses: tool I/O.** This is a single-user
  loopback UI for its owner — tool `input` (exact argument string) and
  `output` (live chunks over WS; the persisted evidence envelope over
  HTTP) are part of the contract so the owner sees what the agent saw.
  They are untrusted data — can contain paths, commands, secrets — and
  must render as escaped text, never markup. Any loopback process can
  reach the API; SOP/Host checks guard browsers, they are not OS-user
  auth.
- **Server internals.** No config file location, no `storage.dir`, no
  app metadata (see [settings](#settings-semantics)).

What the contract **cannot** guarantee — and does not promise: text
fields are untrusted data. `Message.content`, `turn.delta` text, tool
catalog `description`/`parameters`, and similar fields are produced by
the model, the user, or tool execution and can contain arbitrary
strings — credentials, paths, anything. The guarantee above is about
fields the server *holds*; it is not a content filter. Clients must
render every text field as escaped, sanitized content, never as markup.

## HTTP surface

Full schemas, typed errors, and examples live in
[`openapi.yaml`](./openapi.yaml). Summary:

| Method & path | Purpose |
|---|---|
| `GET /api/health` | Liveness — `200 {"status":"ok"}`. |
| `GET /api/conversations?offset&limit` | List projections, `updatedAt` desc (best-effort). `limit` default 30, max 100. |
| `GET /api/conversations/{id}` | One projection. |
| `GET /api/conversations/{id}/messages?start&limit` | Ranged history; `start >= 0` absolute, negative counts back from the end. `limit` max 100. |
| `PATCH /api/conversations/{id}` | Rename — body `{title}`. |
| `DELETE /api/conversations/{id}` | Delete the room and its storage; queued jobs on it die `410`. |
| `POST /api/conversations/{id}/compactions` | Queue a manual compaction on the room's worker; `200` with the updated conversation when it finishes. |
| `GET /api/gateways?offset&limit` · `GET /api/gateways/{id}` | Sanitized gateway projections (paged list, sorted by id), including broken records. |
| `GET /api/gateways/{id}/available-models` | Fetch all model IDs advertised by the configured provider endpoint; returns `{ "data": ["model-id", ...] }` without changing the gateway. The UI selects IDs and saves the merged catalog through `PUT`. |
| `GET /api/gateways/{id}/providers?model={modelId}` | Upstream backends an aggregator route can serve the model through — `{ "data": [{ "name", "slug" }] }`, empty for direct providers. `model` is required (`400` without it). The picker echoes `slug` back as `payload.provider` on `turn.send`. |
| `PUT /api/gateways/{id}` | Full-replace upsert — the path id is authoritative (a body `id` is `400`); `apiKey` is tri-state write-only. |
| `DELETE /api/gateways/{id}` | Remove gateway + vaulted key; `404` when absent. |
| `GET /api/settings` | Effective settings document (projected — see below). |
| `PATCH /api/settings` | One leaf write — body `{key, value}`. |
| `GET /api/tools` | Registered tool catalog: `name`, `description`, `parameters`, `enabled`, `permission`. |
| `GET /api/fs/browse?path=` | Directory-only listing for the workspace picker — `{ "path", "parent", "roots": [{"name","path"}], "entries": [{"name","path","hidden"}] }`. Empty `path` defaults to the owner's home; `parent` ascends toward the filesystem root and `roots` jumps to `/` (or present drive letters on Windows). `path` must be absolute (`400`); missing directories are `404`, unreadable `403`. Listings cap at 1000 directories; file contents are never served. |
| `GET /api/context?conversationId&route&modelId` | Composer-footer stats — `{ "estimate", "total" }`: estimated live input tokens and the bound model's input allowance. `conversationId` is optional — a fresh room still resolves `total` when `route`+`modelId` name a known model. |
| `GET /api/ws` | WebSocket upgrade — `101`; frames defined in [`websocket.md`](./websocket.md). |

There is intentionally **no `POST /api/conversations`** and **no
`PATCH /api/gateways/{id}`**: sending a message *is* conversation
creation (`turn.send` upserts the room — a null or absent
`conversationId` gets a server-minted `c_<base32>` id), and gateway
mutation *is* full replacement via `PUT`. Inventing soft-partial
endpoints would only fork the semantics.

### Pagination, honestly

`GET /api/conversations` is backed by the repository's `List` — it reads
**all** conversation infos and the API slices the page afterward. The
order is `updatedAt` descending, but the list is **not a snapshot**: a
write between two page requests shifts offsets, so a client paging
through a live list can see duplicates or gaps. That is accepted for a
single-seat UI; cursor pagination is a contract change if it ever
matters.

`GET .../messages` is backed by `LoadAt(start, end)` over the global
logical history (sealed segments first, then the live tail). The
response carries `effectiveStart`, `nextStart`, `total`, and `hasMore`
so the client can page in either direction without caching semantics —
but the same caveat applies: indexes move when a turn appends or an edit
rewrites history. After any `turn.completed`/`turn.failed` frame a
client should re-fetch rather than merge.

`GET /api/gateways` uses the same offset/limit envelope — the transport
slices the application's `InspectList` result (sorted by id). Gateway
writes are rare, so offset drift is mostly theoretical, but the same
best-effort rule applies. `GET /api/tools` is a small static catalog
and stays unpaginated.

### Settings semantics

`GET /api/settings` returns the **effective persisted** configuration —
the JSON file as stored on disk, merged over `default` struct tags;
what a restart would load, not a snapshot of the running process. It is
projected to leaves that are safe to show a browser: `server.*`,
`conversation.*`, `service.*`, and `log.*`. `server.hosts` is a JSON
array of host aliases without ports. It excludes server internals (`App`
metadata is never marshaled) and local paths (`storage.dir` — a
filesystem location, not a UI concern).

`PATCH /api/settings` takes `{key, value}` where `key` is a dotted path
to a **leaf inside the projected document** (e.g.
`conversation.max_turn_steps`, `server.port`, or `server.hosts`) and
`value` is a JSON literal —
`null` is rejected. Writes go through `application/setting.Set`, which
validates the whole resulting document before writing it atomically;
keys outside the projected surface (`storage.dir`, section paths,
unknown keys) are `400`.

Timing nuance, documented because it is real behavior and not a promise
to fix: config writes take effect **on next process start** for most
sections, but `application/conversation` re-reads the conversation
section on every job post (`reloadConfig`), so `conversation.*` changes
apply to the *next submitted job* while `server.*`, `service.*`,
`log.*`, and `storage.dir` wait for restart. The response to `PATCH` does
not imply the running process has already applied the value.

### Error model

All 4xx/5xx error responses are `application/problem+json` (RFC 9457
shape) with a stable machine `code`, the HTTP `status`, a
human-readable `detail` safe for display, and nothing else — no
`Cause`, no stack, no internal identifiers. (The `101` WS upgrade is a
successful non-2xx and has no body.)

| `status` | `code` | Meaning |
|---|---|---|
| 400 | `invalid_argument` | Malformed id, unknown enum, negative token count, non-user edit target, nothing-to-compact, unknown setting key, `null` value, … |
| 403 | `forbidden_origin` | `Host`/`Origin` check failed. |
| 404 | `not_found` | Conversation, gateway, model-in-catalog, message id, or running turn does not exist. |
| 408 | `interrupted` | The request or turn was canceled (client cancel, shutdown race). |
| 410 | `gone` | The job was queued on a room that was deleted underneath it. |
| 413 | `input_too_large` | Request payload beyond the server's input bound. |
| 429 | `queue_full` | The room's mailbox is full — retry later. |
| 500 | `internal` | Engine or deployment fault (adapter missing, tool loop limit, persistence failure). |
| 502 | `upstream_error` | The provider call failed at the wire. |
| 503 | `unavailable` | The application is shutting down. |
| 504 | `upstream_timeout` | The provider did not answer inside its deadline. |

Upstream provider statuses pass through `apperror` and surface mapped to
this table — a provider `413` stays `413`, a provider `429` keeps status
`429` but reports `code: "upstream_error"` (a rate-limited provider, not
a full room queue), provider `401/403` collapses to `502 upstream_error`
with a dead-credential detail (the credential itself is never echoed),
and everything else upstream becomes `502`/`504`. The mapping exists so
a UI can route on a closed set, never on provider error text.

Success codes: `200` for reads and updates (`PATCH` rename, `PATCH`
settings, `PUT` gateway — upsert is idempotent so there is no `201`),
`204` for deletes, `101` for the WS upgrade. Conversation detail includes
`canCompact`; it is true when the live tail is longer than the configured
keep-tail, including after a checkpoint when that tail still exceeds it.

## WebSocket surface

`GET /api/ws` upgrades to a single multiplexed socket carrying all turn
traffic. [`websocket.md`](./websocket.md) is the normative spec; the
shape in brief:

- Client commands: `turn.send`, `turn.edit`, `turn.cancel`, `turn.watch`
  — each carrying a client-chosen `requestId` that correlates the
  command with its replies. A malformed command (bad JSON, unknown
  `type`, invalid fields) is rejected with an `error` frame before any
  turn exists — it never produces `turn.opened` or `turn.failed`.
- Server stream: `turn.opened` means the command was accepted and an
  asynchronous submission scheduled — journaled and written before any
  other turn frame; it is **not** a worker-acceptance, persistence, or
  provider-start signal. Then `turn.delta` / `turn.phase` /
  `turn.reasoning` / `turn.tool_call` / `turn.tool_input` /
  `turn.tool_output` (ordered live argument/output chunks — progress,
  not completion; raw and summarized reasoning chunks in arrival order)
  / `turn.step_finished`, and exactly one terminal
  `turn.completed` or `turn.failed` per turn. Every turn frame carries
  `conversationId`, `turnId`, and a per-turn strictly increasing `seq`.
- Every terminal frame carries `resyncRequired: true` — stream frames
  never contain persisted message ids or tool results, so clients
  re-read via HTTP after every terminal frame.
- `turn.cancel` targets one exact `turnId` — including while it sits in
  the room's queue — and never interrupts an unrelated job. The reply is
  `turn.cancel_ack` (accepted), and the terminal `turn.failed` with
  `status: 408` still follows.
- `turn.watch` reattaches to a live turn from `afterSeq` against a
  bounded server journal; gaps, unknown turns, and expired journals
  answer `turn.resync_required`, after which the client re-reads via
  HTTP. Missing deltas are never fabricated.
- Disconnecting the socket never aborts a turn — sends run under a
  server-scoped context, and `userMessagePersisted` tells the truth
  about whether the user line committed.

## Implementation work required

This contract needs pieces the codebase does not have today. Flagging
them honestly — none of these exist yet:

- ~~**The HTTP delivery itself.**~~ Done in phase 1: Gin wiring in
  `infrastructure/delivery/http`, `nusallm serve` as the `cmd` entry,
  the loopback bind, and the `Host`/`Origin` middleware described above.
- **The WS hub.** One connection registry fanning domain `TurnEvent`s
  out to subscribers by `conversationId`/`turnId`. `ConversationApp.Send`
  and `Edit` block until the job finishes — the hub runs each accepted
  turn in a goroutine around the blocking call: journal and emit
  `turn.opened` first, invoke the app, then map the return to the
  terminal frame. No submit-without-await application seam is needed.
- **Per-turn cancellation lives in the hub.** Each accepted turn gets a
  server-scoped `context.WithCancel` keyed by `turnId`, and
  `turn.cancel` invokes it. The existing job contract already makes
  this work for both queued and running jobs — a queued send canceled
  via ctx returns `408` having touched nothing, a running one
  interrupts — so no application-layer change is required. The
  room-wide `ConversationApp.Cancel` must NOT back `turn.cancel`: it
  interrupts whichever job happens to be running, not the addressed
  turn.
- **A bounded per-turn event journal** for `turn.watch` replay —
  `turn.opened` is recorded as `seq` 0 before any event can be emitted,
  and each socket's outbound writes are serialized through a single
  bounded-buffer writer. Retention is a deployment constant, and expiry
  is a normal outcome answered by `turn.resync_required`.
- **Server-side turn ids.** Minted `t_<base32>` via the domain ID rule
  at accept time, before scheduling.
- ~~**Projections.**~~ Done for reads in phase 1: message→DTO
  projection (owner-visible tool call records, attachment
  uri/name references, `optionalContent` dropped), the gateway `keyState`
  derivation, and the settings leaf-projection.
- ~~**`optionalContent` preservation.**~~ Done in phase 2:
  `application/gateway.Upsert` inspects the stored record and carries
  each surviving model's hidden `optionalContent` forward under a mutex
  that serializes same-process inspect→save. **Caveat:** the mutex is
  process-local — a concurrent `nusallm gateway set` CLI process writing
  the same store can still interleave; close cross-process atomicity
  only if multi-writer operation becomes real. The wire is untouched by
  this rule.
- **Non-blocking fan-out.** A slow or dead subscriber must never stall
  the provider read loop — bounded per-subscriber buffers with
  drop-and-disconnect on overflow, since the domain's
  `TurnEventHandler` applies synchronous backpressure by design.
- **Swagger UI serving** of `openapi.yaml`, loopback-only.

## Build phases and acceptance matrix

Suggested order — each phase is shippable and testable on its own:

1. **Phase 1 — HTTP skeleton + reads. ✅ Done.** Gin wiring, `Host`
   check, `Origin` check on unsafe methods, `GET` endpoints (health,
   conversations, messages, gateways, settings, tools), problem+json
   error mapping.
2. **Phase 2 — HTTP writes. ✅ Done.** `PATCH` rename, `DELETE`
   conversation, `POST` compactions, `PUT`/`DELETE` gateway, `PATCH`
   settings — strict one-document JSON bodies, `Content-Type`
   `application/json` required, 1 MiB body cap, path ids validated
   against the domain identity rule.
3. **Phase 3 — WebSocket. ✅ Done.** Upgrade + `Origin` check,
   `turn.send`, `turn.opened` (seq 0), stream frames,
   `turn.completed`/`turn.failed` with the persistence receipt, hub
   fan-out with bounded admission (`error`/`queue_full`), ping/pong,
   frame limits.
4. **Phase 4 — Turn control. ✅ Done.** `turn.edit` (regenerate from a
   user message; terminal never claims `userMessagePersisted`),
   `turn.cancel` (exact per-turn ctx cancel — never the room-wide
   interrupt — with `turn.cancel_ack` before cancellation), `turn.watch`
   journal replay under the hub lock with `turn.resync_required` on
   gaps/unknown/expired, live attach on truncated-but-active turns.
5. **Phase 5 — Swagger UI + polish.**

Acceptance checks (each maps to a contract clause):

| # | Check | Passes when |
|---|---|---|
| A1 | `Host` validation | `Host: evil.example` → `403 forbidden_origin` on any path; each configured `server.hosts` alias with the configured port passes. |
| A2 | `Origin` on unsafe HTTP | `POST`/`PUT`/`PATCH`/`DELETE` with absent or unconfigured `Origin` → `403`; configured host origins pass. |
| A3 | `Origin` on WS | Handshake without/with foreign `Origin` → rejected `403` before upgrade. |
| A4 | Loopback bind | Server refuses connections on non-loopback interfaces; `server.port` and `server.hosts` apply after restart. |
| A5 | Conversation list paging | `offset`/`limit` honored, `limit` > 100 → `400`; page metadata `total`/`hasMore` correct. |
| A6 | Message ranges | `start=-50` returns the last ≤50; `effectiveStart`/`nextStart`/`hasMore` consistent with `total`. |
| A7 | Rename/delete/compaction | `PATCH` rename `200`; `DELETE` `204` then `404`; compaction `200` with updated projection on success, `400` nothing-to-compact (including repeat), `429` full mailbox. |
| A8 | Gateway tri-state | `PUT` with absent `apiKey` keeps stored ref; `""` clears; literal vaults; `keyState`/`keySet` correct; no key material or raw ref in any response. |
| A9 | Settings | `GET` exposes `server`, `conversation`, `service`, and `log` leaves without `storage`/`App`/paths; `PATCH` leaf `200`, invalid values → `400`; writes atomic and schema-ordered. |
| A10 | Tools | Catalog exposes name/description/parameters/enabled/permission; nothing internal leaks. |
| A11 | WS lifecycle | An accepted `turn.send` → exactly one `turn.opened` (`seq` 0, journaled and written before any other turn frame) → ordered stream → exactly one terminal frame; `seq` strictly increasing per turn. |
| A12 | `opened` honesty | `turn.opened` = accepted + scheduled only: a full mailbox still ends the turn `turn.failed` `429` *after* `opened`, and a malformed command (bad JSON, unknown `type`, invalid fields) gets an `error` frame with no `turnId`/`seq` — never a `turn.failed` for a turn that never existed. |
| A13 | Cancel | `turn.cancel` on a running turn → `cancel_ack` then `turn.failed` `408`; on a queued turn same; on an unknown `turnId` → `error` frame `not_found`. |
| A14 | Disconnect | Closing the socket mid-turn does not abort it; `turn.watch` after reconnect replays or `resync_required`, and persisted state via HTTP is authoritative. |
| A15 | Edit preemption | `turn.edit` during a running turn: running turn ends `turn.failed` `408`, edit completes; both terminal frames carry `resyncRequired: true` and the client re-reads via HTTP. |
| A16 | Delete race | `DELETE` a conversation with queued turns → queued turns end `turn.failed` `410`. |
| A17 | Failed-send receipt | Provider failure after the user append → `turn.failed` with `userMessagePersisted: true`. |
| A18 | Projection redaction | Inject a sentinel string into each server-held field — stored `apiKey` material, a raw `env:`/`secret:` ref, an error cause, a model's `optionalContent`, and an attachment `sha256` pin — and assert it appears in no projected *metadata field* (`keyState`, `code`/`detail`, tool call refs, `parameters`, …). Provider reasoning is owner-visible content in ordered `Message.parts` and `turn.reasoning` frames; the check targets projected fields only, not all traffic. |
| A19 | Problem+json | Every error body parses as problem+json with a `code` from the catalog table. |
| A20 | Slow subscriber | A stalled WS client does not delay the provider read loop or other subscribers; overflow ends that subscriber's stream, not the turn. |
| A21 | Gateway list paging | `GET /api/gateways` honors `offset`/`limit` with the same `data`/`page` envelope as conversations; `limit` > 100 → `400`; sorted by id including `broken` records. |
| A22 | `optionalContent` preservation | Seed a stored model's `optionalContent` out-of-band, `PUT` a replacement catalog keeping that model `id` → the stored record still carries it; drop the model `id` → it is gone. No `optionalContent` field exists anywhere on the API surface. |
