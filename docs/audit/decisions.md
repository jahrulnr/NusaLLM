# Decisions — round 0 (SA), round 1 contracts

Status: ready (round 1 — ADR-0002/0003 ratified by PA, contract clauses landed
in `docs/api/websocket.md` and `infrastructure/AGENTS.md`)
Tickets: T-05 (ownership of path meaning), T-06 (retry, attach, merge
contracts), T-07 (cross-repo delta map)
Owner: SA · writes `docs/audit/decisions.md`, `docs/adr/**`,
`docs/api/websocket.md`, `docs/persistence.md` (workspace section) and
`infrastructure/AGENTS.md:19-22` — the round-1 scope the PA granted

This file records what the base *decides*, not what it should copy. Every
behavioural claim carries `file:line`; where a document and the code disagree,
both are cited and the wrong one is named. NusaShell is used as evidence that a
defect survived the fork — its shape is cited, never endorsed.

Decisions that deserve to outlive the round are promoted to ADRs:
[ADR-0002](../adr/0002-workspace-resolves-path-arguments.md) (path arguments),
[ADR-0003](../adr/0003-turn-retry-and-watch-attachment.md) (retry + watch).

| Decision | Rule in one sentence | Ticket |
| --- | --- | --- |
| D-05 | The workspace repository decides what a path argument means — syntax, placement, confinement; adapters pass the raw string and never stat/expand/join. | T-05 → T-01 |
| D-06 | A provider call is retried until it *persists*; the client drops the abandoned attempt on `retry_wait` back to the last finished step; a watch on a live turn always attaches (within a bounded replay); the record outranks the journal at attach; `turn.failed` is self-describing. | T-06 → T-02/T-04/T-09/T-10 |
| D-07 | The fork's real delta is ~25k changed lines in 241 shared files; only ~9 clusters are shared-core work the base should take. | T-07 |

---

## D-05 — Where a path argument gets its meaning

### Decision

The **workspace repository** (`repository.Workspace` and its per-driver
implementation) is the single owner of "what a path argument means". Tool
adapters and delivery routes pass the string they received, unmodified, and
consume an opened handle, a resolved reference, or an error. They do not
`stat`, `filepath.Clean`, `Join`, `IsAbs`, expand `~`, or decode `file:`/`%VAR%`.
The pure *syntax* half of the rule lives in one mechanism package
(`infrastructure/system/patharg`) that the local driver calls; it knows nothing
about workspaces.

### Context

Three layers each own a fragment of the rule today, and they disagree:

| Consumer | What it does with `path` | Evidence |
| --- | --- | --- |
| `read_file` | passes it raw into the session `os.Root`; schema says "relative to the workspace root" | `infrastructure/service/tool/file/read.go:38`, `:73` |
| `read_media` | absolute → `OpenHost`; relative → session root | `infrastructure/service/tool/file/read_media.go:53`, `:89-95` |
| `write_file` | uses it raw for `Stat`/`MkdirAll`/`WriteFile` | `infrastructure/service/tool/file/write.go:169-183` |
| port `Workspace` | documents the asymmetry: reads may opt into a host path, "writes never ride it" | `infrastructure/domain/repository/workspace.go:15-22` |
| host path | the "read tools opt in" clause exists only in the local driver | `infrastructure/repository/workspace/workspace.go:45-67` |
| `GET /api/fs/browse` | own rule: "path must be absolute", then `Clean` | `infrastructure/delivery/http/fs/fs.go:64-68` |

Verified behaviour of the current code (probe run against this tree, scratch dir
outside the repo):

```
os.Root.OpenFile("/etc/hostname")      -> openat /etc/hostname: path escapes from parent
os.Root.OpenFile("/tmp/x.txt")         -> openat /tmp/x.txt: path escapes from parent
os.Root.WriteFile("/tmp/x.txt")        -> openat /tmp/x.txt: path escapes from parent
os.Root.WriteFile("~/probe-write.txt") -> creates ./~/probe-write.txt (no error)
```

So: an absolute read is refused with a storage-flavoured message even though
`read_media` accepts the same argument, and `~/…` silently becomes a literal
`~` folder — the exact symptom NusaShell's CHANGELOG records. There is **no HTTP
preview/download route in the base today**; attachments cross as `file://`
references (`infrastructure/delivery/http/conversation/dto.go:100-113`) and only
directory browsing exists (`fs/fs.go:51`). A preview route is NusaShell's
(`../NusaShell/infrastructure/delivery/http/conversation/workspace.go:36-37`,
`localfile.go:15-35`), so on this side the rule is prospective, not a migration.

The layer rules already place the decision:

- `infrastructure/repository/AGENTS.md:28` — "`workspace/` owns workspace-scoped
  file access and path confinement."
- `infrastructure/service/AGENTS.md:20` — the workspace flow goes "through
  `repository.Workspace`"; a service adapter must not open, read, or write host
  files directly.
- `infrastructure/AGENTS.md:19-22` — `system/` is the host-OS boundary;
  mechanisms there implement, they do not decide conversation policy.
- ADR-0001 — the workspace is a per-room, per-driver binding, so `~`, `$HOME`
  and `%VAR%` are only meaningful relative to the host that owns that workspace.

### Options considered

1. **Adapter-side expansion** — NusaShell's shape: `patharg.Expand` then
   `placeOf`/`anchorOf` inside the tool package
   (`../NusaShell/infrastructure/service/tool/file/root.go:27-95`). Smallest
   diff, but (a) it stats the host from a service adapter
   (`anchorOf` calls `os.Stat`, `root.go:78-93`) — forbidden by
   `service/AGENTS.md:20`; (b) the media tools write their own second version
   (`../NusaShell/infrastructure/service/tool/media/media.go:277-300`); (c) a
   remote driver would expand against the wrong host.
2. **Port-side placement + mechanism-side syntax** (chosen).
3. **Do nothing, keep strict confinement** — rejected: `~` still creates a
   literal folder, and `read_media` already disagrees with `read_file`.
4. **Let `application/job` expand before tools run** — the use case would own
   filesystem syntax and still be wrong for a remote driver.
5. **Allow writes outside the workspace (NusaShell's `anchorOf`)** — rejected:
   contradicts the port's documented contract; `exec` is the deliberate escape
   hatch (`docs/architecture.md:541`).

### Chosen rule (enforceable sentences)

**R1 — Ownership.** A tool or route passes the raw argument to the workspace
capability. No path syntax, cleaning, joining, or existence check happens in
`infrastructure/service/tool/**` or `infrastructure/delivery/**`.

**R2 — Syntax** (pure, host-facts-injected; `file:`, `~`, `%VAR%` are the only
three shapes that decode; everything else is literal):

| Input | Result |
| --- | --- |
| `file:/abs`, `file:///abs`, `file://localhost/abs` | `/abs`; percent-decoded when decodable, literal when not |
| `file://otherhost/abs` | error `… is not a local path` (these tools open this machine) |
| `file:///C:/x` on Windows | `C:/x` (the leading slash before a drive letter is URL syntax, not a path) |
| `~`, `~/x`, `~\x` | the home of the host that owns the workspace; unknown home = error naming it |
| `~user/x`, `a/~/b`, `~notes.md` | literal — only a leading `~` followed by nothing or a separator is the home marker |
| `$HOME/x`, `${HOME}/x` | literal — a model may genuinely mean the characters |
| `%NAME%` **on Windows** | the variable's value; `%%` = `%`; unset name = error naming it; a `%` pair with no plausible name stays literal |
| `%NAME%` elsewhere | literal — a legal folder name |
| everything else | unchanged |

**R3 — Placement.** A relative path resolves in the turn's workspace directory
(`dservice.WorkspaceDir`, else the driver's fallback root). An absolute path
**inside** that root resolves relative to it. An absolute path **outside** it:
reads open the regular file by host path (`OpenHost`: must exist, must be
regular, must be absolute); writes fail with a 400-class error naming the
workspace root.

**R4 — Confinement.** Everything reached by a relative name keeps going through
`os.Root` (symlink and `..` escapes refused). A host-opened read keeps its
sha256 pin, so what send-time serves is what check-time saw
(`read_media.go:143-156`).

**R5 — Preview route.** When one is added, it accepts the raw `path` query,
asks the workspace capability for a reader plus size/mod-time, and streams it
with `X-Content-Type-Options: nosniff`, a `sandbox` CSP for non-PDF payloads,
and `Content-Disposition` decided by sniffed type. A directory or non-regular
file is a 404/400, never a stream. The handler contains no `patharg` import.

**R6 — `WorkspaceDir` is an input, not a decision.** `service.WithWorkspaceDir`
supplies *which folder* the rule works in; it never decides what the argument
means.

### Consequences

- BE (T-01) adds the resolve step to the port and its local implementation,
  plus one syntax package with a table test; `read_file`, `write_file`,
  `apply_patch`, `read_media` and the media tools lose their own path logic.
- `read_file` gains the absolute-read behaviour `read_media` already has; a
  leading `~` stops creating `./~`; a *write* to `~/x` becomes a clear error
  rather than a silent folder.
- No new endpoint, no change to `application/`, no change to stored records,
  no migration.
- Docs to touch: `docs/persistence.md` (workspace section) and
  `infrastructure/AGENTS.md:19-22` (name the path-syntax mechanism in the
  `system/` description). Both are outside SA's write scope — flagged for PA.

### Ticket(s)

T-05 (this decision, feeds T-01). T-01 owns the implementation and the
regression tests; QA's T-14 probe becomes the black-box proof.

### References

`infrastructure/service/tool/file/read.go:38,73` · `read_media.go:53,89-95,143-156`
· `write.go:169-183` · `infrastructure/domain/repository/workspace.go:15-22` ·
`infrastructure/repository/workspace/workspace.go:45-67` ·
`infrastructure/repository/AGENTS.md:28` · `infrastructure/service/AGENTS.md:20`
· `infrastructure/AGENTS.md:19-22` · `docs/architecture.md:541` ·
`infrastructure/delivery/http/fs/fs.go:64-68` ·
`infrastructure/delivery/http/conversation/dto.go:100-113` ·
`../NusaShell/infrastructure/system/patharg/expand.go:1-182` ·
`../NusaShell/infrastructure/service/tool/file/root.go:27-95` ·
`../NusaShell/infrastructure/service/tool/media/media.go:277-300` ·
`../NusaShell/infrastructure/delivery/http/conversation/localfile.go:15-35` ·
`docs/adr/0002-workspace-resolves-path-arguments.md`

---

## D-06 — Retry, the `retry_wait` reset, `turn.watch` attachment, and `turn.failed`

### Decision

Five sentences, each enforceable in code, replacing the three silences below
and incorporating the four PA amendments ratified in round 1. Full rationale in
[ADR-0003](../adr/0003-turn-retry-and-watch-attachment.md).

1. **Retry gate = persistence.** A provider call may be retried iff the failure
   is transient (`429` or `>= 500`), the turn was not cancelled, the attempt
   appended no message, the retry budget is not exhausted, and any provider
   `Retry-After` is <= 60 s. Streamed-but-uncommitted output never blocks a
   retry; an appended message always does.
2. **`retry_wait` means "discard what cannot be in the store".** On
   `turn.phase{phase:"retry_wait"}` the client drops everything the turn
   streamed after its **last `turn.step_finished`** — text, reasoning, tool rows
   without a result — back to that boundary. It never drops history rows, the
   user line, a folded steer, or a finished step's usage. A *settled* tool row
   is not the boundary: the client settles tool rows on the terminal too
   (`web/lib/turn.js:23-25`, called from the terminal branch at `:93-96`).
3. **A watch on a live turn always attaches, within a bounded replay.** Any
   unsatisfiable `turn.watch` on an active turn answers `turn.resync_required`
   and then joins the subscriber set under the same lock; a finished turn
   attaches nothing. A replay is verbatim only while the window fits the
   socket's **free queue space minus the server's headroom for live frames** —
   never "the whole queue", because the next live delta must still fit after
   the replay; a deeper window answers `resync_required` and, on a live turn,
   still attaches. The guarantee: a client that got `resync_required` for a turn active at that
   moment also receives that turn's terminal frame (unless its own socket dies
   or its queue overflows).
4. **`turn.failed` is self-describing.** `status` (>= 400), `code` (catalog
   member, and *upstream* for anything the provider originated), a non-empty
   display-safe `message`, `resyncRequired: true`, and on send turns
   `userMessagePersisted` + `steering`. It **may** carry `retryAfterMs` when the
   provider named a pause the turn refused to sit out. A failure with nothing to
   say says so; it never sends an empty message.
5. **The record is authoritative when a client attaches.** The journal supplies
   only what the record cannot hold (the running attempt) — see D-06.1.

**Not relaxed:** the `413` compact-and-retry keeps the stricter gate (nothing
streamed, nothing persisted) because a fold rewrites the history the client is
currently rendering; mid-turn history invalidation is not in the protocol.

**Ratified (round 1).** ADR-0002 and ADR-0003 are `Accepted`. The four
amendments: the discard boundary is the last `turn.step_finished` (not a
settled tool row); `turn.failed` may carry `retryAfterMs`; the attach rule is
unchanged; the replay bound is contract, for the base's own reason
(`conn.enqueue` kills a socket whose queue is full — `conn.go:53-67`,
`outboundQueueCap = 64` at `conn.go:16`). Citation errata adopted:
`write.go:74,80,87` for the raw-path use, `interruptedReply` at `turn.go:572`,
the retry gate at `turn.go:225-227`, and the steer fold at `turn.go:113-130`.

### Context

#### The retry gate today

```go
// application/job/turn.go:217
parts = nil
err = svc.Send(ctx, conv, gw, wireModel, captureEvent)
added = conv.Messages()[before:]
...
// turn.go:226-229
if !transient || attempt == len(sendRetryBackoff) ||
    len(added) > 0 || len(parts) > 0 {
    break
}
```

**State at round 1 (BE, uncommitted working tree):** the `len(parts) > 0` term
is gone — `if !transient || attempt == len(sendRetryBackoff) || len(added) > 0`
(`turn.go:230`), with the tests rewritten in `retry_test.go`. The block below
is the round-0 state this decision replaced; the line numbers describe the
committed tree.

`parts` is this attempt's streamed capture (`turn.go:173-193`), reset at
`turn.go:217`; `added` is what the adapter appended to the aggregate. The
`len(parts) > 0` term is what makes "one 429 after the first delta ends the
turn". Two tests pin it: `application/job/retry_test.go:144`
(`TestRunTurnNoRetryAfterEmittedDelta`, asserts `calls == 1`) and the 413 case
below it (`TestRunTurn413AfterEmittedDeltaPropagates`, `retry_test.go:227`). The
attempt's shown text is then persisted as an interrupted reply so
it is not lost (`turn.go:278-300`, `interruptedReply` at `turn.go:611-634`).

NusaShell removed the term and kept the rest
(`../NusaShell/application/job/turn.go:247-250`), with
`TestRunTurnRetriesAfterStreamedDeltas` and `TestRunTurnNoRetryAfterAppendedMessage`
replacing the old test (`../NusaShell/application/job/retry_test.go:188-283`).
Its CHANGELOG states the rule: "yang dinilai adalah apa yang dipersist, bukan
apa yang sudah di layar" (`../NusaShell/CHANGELOG.md`, Unreleased → Fixed).

#### What the docs say — and do not

| # | Sentence | Where | Status |
| --- | --- | --- | --- |
| S1 | "It is `retry_wait` while the harness pauses before retrying a transient provider failure (rate limit or upstream 5xx) … A `retry_wait` marker carries `retryAfterMs`" | `docs/api/websocket.md:489-494` | **true**, code agrees (`event.go:120-130`, `frames.go:182-187`) |
| S2 | Nothing about *when* a provider call may be retried | — | **missing** — no doc sentence anywhere (`rg retry docs/`) |
| S3 | Nothing about what a client must discard when it sees `retry_wait` | — | **missing** |
| S4 | "Attaches this socket to the turn … Afterwards the socket stays attached and receives live frames." | `websocket.md:297-300` | **code violates it** for `afterSeq` ahead of the journal on a live turn: `hub.go:606-611` answers and returns |
| S5 | "If … `afterSeq` exceeds the latest `seq` … the reply is `turn.resync_required`" | `websocket.md:301-306` | **incomplete**: says nothing about attaching afterwards |
| S6 | "On a **gap in an active turn** the socket answers `resync_required` and still attaches for live frames; a finished turn with a gap … attaches nothing." | `websocket.md:307-311` | **true**, code agrees (`hub.go:614-626`) |
| S7 | "`message` — display-safe only; internal causes are never exposed" | `websocket.md:717` | **incomplete**: does not require a message to exist; `terminalPayload.Message` is `omitempty` (`frames.go:262`) |
| S8 | "`status` — the same HTTP-shaped status the REST surface would return" | `websocket.md:706-712` | true; every construction site sets it (`hub.go:475-520`) |
| S9 | "`code` — the same stable code strings as the HTTP problem catalog" | `websocket.md:713-720` | true (`transport.go:42-84`) |
| S10 | "`turn.step_finished` … maps the domain's `TurnEventFinished`, which is **per provider call**" | `websocket.md:613-615` | true (`event.go:112-119`), but it does **not** say the step is persisted — the boundary R2 relies on |
| S11 | `turn.failed` "is terminal for **every** failure mode of an accepted turn" | `websocket.md:682-687` | true (`hub.go:449-520`) |

Verdicts: S4 — **the code is wrong**, the doc sentence stands; S5/S7/S10 — **the
doc is wrong (incomplete)** and gains the missing clause; S2/S3 — **the doc has
no sentence at all**, and the base's behaviour is therefore unpinned by any
contract.

The `websocket.md` line numbers in the table are the round-0 file. The clauses
written in round 1 sit at: retry gate, discard boundary and compaction note
527-555, watch attach and replay bound 303-333, persisted step 671-678,
`turn.failed` fields 748-803, replay budget constant 860-862.

The base's own tests make the current watch behaviour explicit:
`control_test.go:373-377` watches with `afterSeq: 99` and asserts only the
`resync_required` reply; `TestWatchSuffixAndResync` never asserts the socket is
left unsubscribed, so fixing the branch does not contradict a test. Covered
today: unknown turn / foreign conversation / future seq / suffix replay /
journal gap / expired journal (`control_test.go:248,295,354,394`;
`hub_test.go:TestWatchEvictsExpiredJournal`). **Not covered:** that a
`resync_required` on a live turn is followed by the terminal — the property T-04
adds.

### Options considered

| Option | Verdict |
| --- | --- |
| Keep `len(parts) > 0`; rely on `turn.resume` for recovery | Cheapest; keeps a pause the harness could absorb becoming a dead turn the user must retry by hand, and keeps a half answer in history |
| Retry and let the client keep both attempts | Rejected — the two attempts splice into one answer (T-09) |
| Retry + client reset to the last **persisted** boundary (chosen) | One rule on each side, provable with a frame-sequence test |
| Retry + server-side live shadow replayed after the retry | NusaShell's `liveDoc` (`../NusaShell/.../hub.go` turn struct), heavier: the hub carries a second copy of every turn's content |
| Watch answers only; client re-watches after resync | Works, but every client must know the dance and the doc already promises the attach |
| Require a separate "step committed" frame | Cleanest boundary, but a new frame kind for one rule; rejected for round 1 |

### Chosen rule, and what it forces

- **BE / T-02:** drop `len(parts) > 0` from the gate (`turn.go:226-229`); keep
  `len(added) > 0`; keep the 413 gate as is (`turn.go:257-263`); replace
  `TestRunTurnNoRetryAfterEmittedDelta` with the two NusaShell-shaped tests
  (retry-after-deltas, no-retry-after-append) plus an exhaustion test that
  asserts *no* assistant line is stored.
- **BE / T-04 (landed in the round-1 working tree):** the ahead-of-journal
  branch answers and subscribes when `!t.terminal` (`hub.go:608-615`), and a
  replay larger than `conn.replayBudget()` answers the snapshot and attaches
  the same way (`hub.go:642-649`, `conn.go:67-74`). Still owed: the test that
  proves the live terminal arrives after the snapshot (the working tree adds
  `watch_replay_test.go`).
- **FE / T-09 (landed in the round-1 working tree, uncommitted):** the
  projection keeps `persistedMark` — the count of live segments the store
  already holds (`web/lib/turn.js:17`) — advances it on `turn.step_finished`
  (`:86-91`) and on a folded steer (`:76-83`, because the row is committed
  before the frame), and truncates to it on `retry_wait` (`:51-60`). A tool
  row's `settled` status is **not** the boundary: `settleTools` also runs on
  the terminal frame (`:23-25`, `:93-96`).
- **FE / T-06:** render `message` first, then `status`/`code` as facts; the
  existing fallback (`web/lib/chat.js:470`, `p.message || 'The turn failed.'`)
  stays as a belt, not as the explanation.
- **Docs (done in round 1, SA):** `docs/api/websocket.md` now carries the retry
  gate and the discard boundary under `turn.phase` (527-553), the bounded replay
  and attach rule under `turn.watch` (303-333), the persisted-step guarantee
  under `turn.step_finished` (669-676), the self-describing `turn.failed` with
  `retryAfterMs` and the upstream-code rule (746-800), and the 32-frame replay
  budget in the constants block (858-859).

### Consequences

- One `429` mid-answer costs a pause instead of the answer; the store keeps at
  most one assistant line per step across retries (proved by T-02's exhaustion
  test).
- A reloaded/second tab always reaches the terminal of a turn it attached to.
- The client gains a rule it must implement exactly; over-dropping erases a
  persisted step, under-dropping splices two attempts. The invariant that makes
  it safe is server-side: a step's message is persisted before any further
  provider call starts (`turn.go:322-336`), so a *finished* step is a true
  boundary and a settled tool row is not.
- `turn.step_finished` is emitted by the adapter before `AppendMessage`
  (`event.go:112-119` runs inside `svc.Send`); the ordering is safe only
  because a failed persist ends the turn. The doc must state this, and QA should
  pin it with a test that fails the append after a finished event.

### D-06.1 — Merge live and history at attach (T-10)

**Rule — the record is authoritative.** When a socket attaches to a running
turn (`turn.watch`, or opening a room whose `activeTurnId` is set —
`docs/api/websocket.md:43-47`), every message the store holds is drawn from
history, and the journal supplies only the part of the turn the record cannot
hold: the attempt that is running now. The client never draws the same step
from both sources.

**How the client finds the boundary with what it already has.** It cuts the
replayed stream at the last live segment whose identity the record carries:

- a tool row is identified by `turn.tool_call.callId` (`frames.go:194-198`) ↔
  `messageDTO.toolCalls[].id` (`dto.go:69`) — whether the row settled is not an
  identity;
- the prose and reasoning that precede that tool row belong to steps the record
  already holds, so they are dropped with it (a step's text is sealed into the
  same message as its tool calls);
- a `resync` marker inside the dropped range is kept — it is the only evidence
  that output is missing.

**Where the current code stands.** The live/stored merge is a blunt trim today:
`chat.js:322` sets `cutHistory` for every `afterSeq < 0` attach, and
`transcript.js:150-153` then drops history back to its **last user row**. That
cuts by position in the wrong list — the answer in front of a running turn
disappears, and a replayed prefix can still double against what the record
holds. The contract above replaces both: the cut belongs to the live stream,
not to history.

**The steer case — the frame is a boundary, the row is not yet an identity.**
A folded steer's row is committed **before** its `turn.steer` frame is emitted:
the fold returns on a failed write and emits only after a successful one
(`application/job/turn.go:120-128`), and the repository reports `false` only
together with an error (`infrastructure/repository/conversation/append.go:14-40`).
The frame is therefore honest evidence that the text is already stored, which
is why the projection may advance `persistedMark` on it (D-06, FE). It does
**not** make the stored row identifiable. Verified absent today: no `InputID`
anywhere in `infrastructure/domain/entity/`,
`infrastructure/repository/conversation/` or `dto.go`; `messageDTO`
(`dto.go:121-133`) projects only
`id/role/content/parts/userType/finishReason/toolCalls/attachments/createdAt`.
Consequence: the "folded steer" boundary exists for the **live stream only**.
A client cannot match a live steer bubble to its history row, so a steer is
drawn twice — until the terminal re-read, and again after a mid-turn reload —
and the merge rule above cannot cover it.

**T-18 (BE, breaking change): a stored steer row carries its `inputId`.** The
fold writes the `turn.steer` `inputId` onto the user message it appends — a
field on the domain message (`infrastructure/domain/entity`), a column/line
field in the conversation record (`infrastructure/repository/conversation`),
and a projection field in `messageDTO` (`dto.go:121-133`). No compatibility
path: records without it read as "no input id" and are never matched by text.
T-10's merge then covers steers by identity, the same way a tool row uses
`callId`. Owner BE; consumer FE (T-10).

**Never invented.** The client drops only what it can prove is stored: no
record, no cut; no identity match, no drop.

### Ticket(s)

T-06 (this decision) · T-02 (BE retry) · T-04 (BE watch) · T-09 (FE reset) ·
T-03 partially (the failure surface that produces the terminal's message/code)
· T-10 (merge live/history, D-06.1) · T-18 (stored steer `inputId`, D-06.1) · T-14 (black-box: watch with an impossible `afterSeq`).

### References

`application/job/turn.go:21-25,173-193,217,226-229,240-250,257-263,278-300,322-336,611-634`
· `application/job/retry_test.go:144,227` ·
`infrastructure/delivery/websocket/conversation/hub.go:449-520,539-547,573-634`
· `event.go:112-130` · `frames.go:182-187,252-268` ·
`infrastructure/delivery/transport/transport.go:42-84` ·
`docs/api/websocket.md:285-311,486-505,613-615,682-725` ·
`web/lib/turn.js:6-7,22-24,33-34,50-51,53-76` · `web/lib/chat.js:466-471` ·
`infrastructure/delivery/websocket/conversation/control_test.go:248,295,354,373-377,394`
· `infrastructure/delivery/websocket/conversation/{conn.go:16,53-67,frames.go:194-198}`
· `infrastructure/delivery/http/conversation/dto.go:69,121-133`
· `application/job/turn.go:113-130,120-128` · `web/lib/turn.js` (`persistedMark`, `applyFrame`) · `web/lib/chat.js:322` · `web/components/transcript.js:150-153`
· `../NusaShell/application/job/turn.go:211-262` ·
`../NusaShell/application/job/retry_test.go:188-283` ·
`../NusaShell/infrastructure/delivery/websocket/conversation/hub.go:744-770` ·
`../NusaShell/infrastructure/delivery/websocket/conversation/hub_test.go:1539-1615`
· `docs/adr/0003-turn-retry-and-watch-attachment.md`

---

## D-07 — Delta map: base vs NusaShell (T-07)

### Decision

The backlog's round 2 takes **nine** clusters from the fork — four of them
(rows 1–4) shared-core work with a contract behind it — and explicitly refuses
the rest: ~25k changed lines of product surface, a repo-wide vocabulary rename,
and three "fixes" that are really redesigns. Each taken cluster carries the
owner layer and the ticket it implies, so no round-2 item arrives as a
cherry-pick from a product the base does not have.

### Context

Neither repo has a `vendor/` directory; `data/`, `bin/`, `.git/` were excluded
through `git ls-files`.

```
git -C llm-base ls-files | grep -v '^vendor/' | sort   -> 573 files
git -C NusaShell ls-files | grep -v '^vendor/' | sort  -> 1058 files
common 455 · NusaShell-only 603 · base-only 118
```

Each common file was compared byte-wise, then `git diff --no-index --numstat`
counted changed lines. Because the fork renamed the module and one domain
concept, every file was also compared after normalising
`github.com/jahrulnr/nusashell → nusallm` (and, second pass,
`provider|providers → gateway|gateways`):

| Measure | Count |
| --- | --- |
| Common files that differ | 320 |
| … of which differ **only** by the module path | 66 |
| … and only by module path + Provider/Gateway vocabulary | 13 |
| Real content differences | **241** |
| Changed lines across those 241 files | **25,136** (`+19,860 / −5,276`) |

Churn by area (real diffs): `docs/api` 4.3k, `web/lib` 2.2k, `application/job`
2.0k, `infrastructure/service/core` 2.0k, `web/components` 1.9k,
`infrastructure/delivery/websocket` 1.6k, `infrastructure/delivery/http` 1.4k,
`application/conversation` 1.2k, `infrastructure/service/tool` 0.9k,
`infrastructure/repository/conversation` 0.9k.

**The headline: the raw diff overstates the delta by an order of magnitude.**
NusaShell is a different product (agents, projects, channels, automations,
browser, MCP, SQLite, auth) built on the same engine; the engine's own defect
surface is small and sits in a handful of clusters.

Snapshot caveats, so the numbers can be re-derived: NusaShell's working tree
carries unrelated uncommitted work (`application/job/{job,send}.go`,
`application/conversation/conversation.go`, +23 lines — a "new thread" effort
setting) which the counts include and none of the cited clusters touch; the base
tree carries concurrent QA work (`Makefile`, `test/repro/`, `web/tests/`) that
this audit did not read or count.

Attribution method: from NusaShell's `CHANGELOG.md` Unreleased `Fixed` list
(12 entries) and 0.1.0 `Fixed` (18 entries) back to code, then a file-by-file
read of the shared engine. The initial commit (`a85f024`, 2026-11-24) already
carries the rename and the product split, so per-commit attribution is not
possible; content is the only evidence.

### Classification legend

- **backport fix** — a defect in code the base still owns; the base should take
  it (possibly reshaped to the base's owner layer).
- **NusaShell-only** — a feature or product decision the base has no concept for.
- **base intentionally different** — both are coherent; the base's choice is
  its own and should not be churned.
- **base ahead** — work in the base NusaShell does not have.
- **redesign** — NusaShell calls it a fix; it changes an ownership boundary or a
  contract. Needs its own decision, not a cherry-pick.

### Options considered

| Option | Verdict |
| --- | --- |
| Cherry-pick the fork's `Fixed` list wholesale | Rejected: most entries ride surfaces the base does not have (agents, projects, channels, browser) and several change ownership |
| Re-diff by hand for every branch, no map | Rejected: 241 files, no way to rank or to spot redesigns |
| Classify every hunk of every shared file | Rejected as round-0 scope: 70% of the changed lines belong to four NusaShell product areas and cannot be hunks-classified into a base decision |
| Rank coherent clusters by impact × effort and state the class (chosen) | Enough for PA to sequence round 2; each cluster is traceable to `file:line` |

### Chosen rule — the ranked delta (impact × effort, what to take first)

Rank = (user-visible impact) ÷ (BE/FE effort). Rows 1–4 are the round-2 spine.

| # | Cluster | Class | Evidence (base → NusaShell) | Impact | Effort | Implied ticket |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | Retry gate judged by persistence | backport fix | `turn.go:226-229` breaks on `len(parts) > 0` → `../NusaShell/application/job/turn.go:247-250` drops it; tests `retry_test.go:144` → `../NusaShell/application/job/retry_test.go:188-283` | high (a 429 after the first delta kills a long answer) | S (one predicate + 3 tests + 3 doc clauses) | T-02 (contract D-06) |
| 2 | Watch attaches an active turn after any unsatisfiable answer | backport fix | `hub.go:606-611` returns unsubscribed → `../NusaShell/infrastructure/delivery/websocket/conversation/hub.go:754-770` subscribes; test `hub_test.go:1539-1615` | high (a reattached tab never sees the terminal) | S (5 lines + one test) | T-04 |
| 3 | Path meaning: `~`, `file:`, `%VAR%`, absolute reads | backport fix **reshaped** | base: no decoding anywhere (`read.go:73`); NusaShell: `patharg.Expand` + `placeOf`/`anchorOf` (`../NusaShell/infrastructure/system/patharg/expand.go`, `.../tool/file/root.go:27-95`) | high (`./~` folders; `file:`/`%VAR%` nonsense; `read_file` vs `read_media` disagree) | M (port resolve + syntax pkg + 4 call sites) | T-01 via D-05 |
| 4 | `turn.failed` leads with the provider's message | backport fix (FE) | base renders message under "Turn failed (status)" with a `'The turn failed.'` fallback (`chat.js:470`, `transcript.js:125-137`); NusaShell leads with the message, facts underneath (CHANGELOG Unreleased) | medium | S (presentation only) | T-06 R4 / FE |
| 5 | CRLF file + LF patch text | backport fix | base `apply_patch` compares raw strings (no fallback) → `../NusaShell/infrastructure/service/tool/file/patch.go:98-105` retries with `\r\n` | medium (Windows files never patch) | XS | new ticket (BE), T-03 family |
| 6 | Invalid UTF-8 in file reads / searches | backport fix | base sniffs content type only (`read.go:89-93`) → `../NusaShell/infrastructure/service/tool/file/read.go:107` rejects/handles non-UTF-8; `search.go:200` skips binaries the same way | medium (JSON encoding mangles bytes silently) | S | new ticket (BE) |
| 7 | Neighbouring text rendered as one Markdown block | backport fix (FE) | base renders every text item through its own `<Markdown>` (`transcript.js:218`) → `joinText` before grouping and copying (`../NusaShell/web/lib/turn.js:341-350`; call sites `transcript.js:354-355`) | medium | S–M (needs the "what separates blocks" list) | T-08 |
| 8 | Late watch duplication: live tail starts after the last step the record holds | backport fix (FE), needs a server-side invariant | base replays `-1` into a `cutHistory` trim (`web/lib/chat.js:124,319-322`) → `liveSegments` cuts the persisted prefix (`../NusaShell/web/lib/turn.js:309-336`) | medium-high (double prose, split tool runs) | M | T-10 |
| 9 | Thread refresh compares before writing | backport fix (FE) | base replaces wholesale: `Object.assign(c, {items…})` on every refresh (`web/lib/chat.js:80`) and `r.info = await api.conversation(...)` on every load (`chat.js:130`) — no comparison, no read-after-local-change guard; NusaShell adds both (CHANGELOG Unreleased) | medium (a rename/pin undone by a late read) | M | new FE ticket |
| 10 | `exec` reports "no output, exit 0" | backport fix | base returns an empty result for a silent success → `../NusaShell/infrastructure/service/tool/terminal/exec.go:147-151` | low-medium (honesty) | XS | new ticket (BE) |
| 11 | Responses stream: dead `stopSeen`/`terminal` state | simplification | `responses/stream.go` tracked a terminal it never used; NusaShell commits from the event branch | none (same behaviour) | XS | optional BE cleanup |
| 12 | `redact` fast-reject needle scan | NusaShell optimisation | `../NusaShell/infrastructure/redact/redact.go` (needle table + one pass) | low at base's scale | S | skip |
| 13 | Error contract names its own stable code (`Code()`, `NewCoded`, 409) | **redesign** | base `IAppError` has status/message/cause/caller and `ClassifyError` derives the code from status (`error.go:39-47`, `transport.go:55-84`); NusaShell removed `Caller`, added `Code()`/`NewCoded` | medium (semantics like "confirmation required" have no code today) | L (interface + all callers + docs) | needs a decision (BE+SA), not a cherry-pick |
| 14 | Watch replay capped by a burst bound → snapshot instead of deep replay | **redesign** (contract change) | `../NusaShell/.../hub.go` `replayBurst = outboundQueueCap/2`; doc promises verbatim replay (`websocket.md:297-300`) | medium (a socket-killing replay vs a legitimate deep replay) | S | decision needed: keep verbatim (base) or add the bound; then doc |
| 15 | Turn `startedAt` on `turn.opened` + `liveDoc` shadow snapshot | NusaShell-only protocol additions | `../NusaShell/.../frames.go`, `hub.go` (`startedAt`, `live *liveDoc`) | low for base | M | skip unless T-10 demands it |
| 16 | `read_file` paging (`offset`/`limit`, 16 MiB cap) | NusaShell-only feature | base returns the file or errors past the output budget (`read.go:82-93`); NusaShell pages | medium | S | feature ticket, not round 2 |
| 17 | Tool-output retention window in `exec_session` | NusaShell-only fix | head+tail retention in `Collect` (`../NusaShell/.../tool/terminal/session.go`) | low-medium (a flooding process) | M | later |
| 18 | Provider/upstream vocabulary + module rename | base intentionally different | 79 of 320 differing files are rename-only | none | XL if taken | **do not take** |

### Not taken (and why)

| Area | Why not |
| --- | --- |
| ACP agents, agent sessions/approvals/elicitations/plans | No such concept in the base; every touchpoint is a new domain field (`aggregate/conversation.go` +395 lines: `agentID`, `agentSession`, `agentOptions`, `agentContext`, `agentTitle`, `routing`, `projectID`, `pinned`, `memoryOff`, `label`) |
| Projects/folders, `ExtraFolders` | Product concept; it is also what makes NusaShell's `placeOf` accept absolute paths (`root.go:60`) |
| Channels (WhatsApp/Telegram), automations, browser/recordings, MCP, memory, media generation, i18n, panes, auth, SQLite storage | NusaShell-only surfaces (`603` files), no base counterpart |
| `web/**` growth (chat.js 1.2k lines added, composer, settings, sidebar, picker) | Rides the product surfaces above; the fixes inside it are rows 4, 7, 8, 9 |
| `docs/api/openapi.yaml` +3.3k lines, `docs/api/contract.md` +236 lines | Documents routes the base does not have |
| REPL, slash commands, `service/command/**`, `delivery/cli/**`, CLI history | **base ahead**, deliberately: NusaShell deleted them ("The terminal REPL and slash commands have been removed"). Nothing to import; the base keeps a surface NusaShell dropped |
| `Gateway` aggregate + `application/gateway` + `infrastructure/repository/gateway` | **base intentionally different**: base's vocabulary and file layout are its own; NusaShell renamed the concept to `Provider` |
| `apperror.Caller` capture, `CausedBy`, `ErrCause` | **base ahead** as diagnostics (NusaShell removed them); comes back in row 13 if the error contract is ever revisited |

### "Fixes" that are really redesigns

1. **`patharg` + `placeOf`/`anchorOf`** — presented as the `~` fix, it moves
   path semantics into the tool adapter and adds `os.Stat` there
   (`../NusaShell/.../file/root.go:70-95`), duplicating the rule in the media
   tools (`.../tool/media/media.go:277-300`) and breaking for a non-local
   driver. The base should take the *symptom fix* (D-05) and not the placement.
2. **Error `Code()`/`NewCoded`** — a contract redesign disguised as a mapping
   tweak; it deletes the capture-site `Caller` that `docs/architecture.md`
   advertises as part of the contract.
3. **`replayBurst`** — a robustness fix that silently changes what `turn.watch`
   promises (verbatim replay → snapshot). Defensible, but it must be decided and
   documented, not inherited.
4. **`Provider` vocabulary** — a repo-wide rename that reads like cleanup; it
   would churn every doc, test, and `AGENTS.md` line in the base for no
   behavioural gain.

### Ticket(s)

T-07 (this map) · D-07 rows 1–4 imply T-02, T-04, T-01 and the T-06 FE clause ·
rows 5/6/10 are unowned and want one BE ticket · rows 7/8/9 imply T-08, T-10 and
a new FE ticket · row 13/14/15/16/17 need a decision before any code.

### Consequences

- Round 2 can be sequenced without reading NusaShell: the engine's delta is
  rows 1–3 and fits in `application/job/turn.go`,
  `infrastructure/delivery/websocket/conversation/hub.go` and
  `infrastructure/service/tool/file/**`.
- The fork becomes evidence rather than a template: its product code stays out,
  and the three redesigns are named so nobody imports them by accident.
- The map is a snapshot. It must be regenerated (the two `ls-files` lists plus
  the numstat pass) before round 2 starts, because both trees are moving.

### References

`CHANGELOG.md:9-55` (base, 0.1.0 Added/Changed/Fixed) ·
`../NusaShell/CHANGELOG.md:15-29` (Unreleased Fixed) ·
`../NusaShell/CHANGELOG.md:87-117` (0.1.0 Fixed) ·
`docs/audit/backlog.md:118-140` (T-07's ask) · every `file:line` in the delta
table above.

### Open questions for the PA

1. **Accept ADR-0002 / ADR-0003?** They are written to be ratified or amended;
   T-01/T-02/T-04/T-09 implement against them.
2. **Does the base want the replay burst bound (row 14)?** If yes, T-04 grows a
   second clause and the doc loses "verbatim"; if no, a deep `-1` replay stays
   legal and the socket's bounded queue is the only protection (it drops the
   subscriber, never the turn — `websocket.md:759-767`).
3. **Row 9 (thread-refresh compare) — FE now or after T-08/T-10?** It is the
   largest FE item that is not already a ticket.
4. **Row 5/6/10 need owners.** They are small, shared-core, and unowned; a
   single BE ticket ("Windows and encoding hardening") closes all three.
5. **Who updates `docs/api/websocket.md` and `infrastructure/AGENTS.md:19-22`?**
   SA may write the wording, but neither file is in SA's write scope this round.
