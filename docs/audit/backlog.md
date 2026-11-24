# Backlog — maturing the base (round 0)

Tickets carry what is already known so no thread re-discovers it. Each
thread confirms or refutes the premise against the code, reproduces it, and
names the owner layer. `NusaShell` is consulted as evidence of a defect that
survived the fork, **not** as a template to copy: its fix may belong to a
design the base does not have.

## BE — backend (Go)

### T-01 — File-tool path policy: `~`, `file:` URL, `%VAR%`, absolute paths

Premise: the base resolves every path inside the workspace only. A leading
`~` is therefore a literal folder name, and a `file:` URL or a Windows
`%APPDATA%` becomes a nonsense path.

Evidence: `infrastructure/service/tool/file/root.go` has `checkDir` and
`callRoot` only — the root is the ctx workspace or the configured dir, and
the path is used as given. NusaShell's equivalent (`root.go` there) carries
`expandPath` + `placeOf` + `anchorOf`; its CHANGELOG records the exact
symptom: *"`~/…` now reaches the owner's home instead of creating a literal
`./~` folder in the workspace"*.

Reproduce: start `nusallm serve` against a scratch data dir and call
`read_file`/`write_file` with `~/probe.txt`, a `file:` URL, and (on Windows)
`%APPDATA%`; then inspect the workspace tree for a `~` directory.

Ask:
- Reproduce, and check every tool that takes a path: `read_file`,
  `write_file`, `apply_patch`, `list_dir`/glob, `read_media`, the media
  tools' input paths, and the HTTP preview route.
- Decide nothing alone: the placement rule is T-05. Report which layer
  currently *would* own it and what the base does for an absolute path
  outside the workspace (`root.OpenRegular` behaviour, symlink escapes).
- Also check `service.WorkspaceDir(ctx)` callers for the same assumption.

### T-02 — Transient failure after streamed output kills the turn

Premise: `application/job/turn.go` retries a transient upstream failure
(429/5xx/transport) only while nothing has reached the client:

```go
if !transient || attempt == len(sendRetryBackoff) || len(added) > 0 || len(parts) > 0 {
    break
}
```

So one 429 after the first delta ends the turn, even though nothing was
persisted and a retry would be safe. `retry_test.go:144`
(`TestRunTurnNoRetryAfterEmittedDelta`) and `retry_test.go:225`
(413 after emitted output) pin that behaviour as intended.

NusaShell changed the rule: it retries when nothing was *persisted*, and the
client discards the abandoned attempt. Its CHANGELOG: *"Transient 429/5xx kini
diulang walaupun delta sudah tampil, karena yang dinilai adalah apa yang
dipersist, bukan apa yang sudah di layar."*

Ask:
- Confirm the gate and every input to it (`parts`, `added`, `conv.Messages`),
  and what a retry would do to `parts` (line ~215 resets capture per attempt).
- Enumerate the case that matters: a stream that dies mid-delta with no
  persisted message. Is the user's only recovery the Retry button? What does
  `turn.failed` carry?
- This is a contract question (T-06): report the blast radius (what the
  client must reset, what the store must not have written), not a patch.

### T-03 — Provider stream robustness: truncation, retry-after, usage

Premise: the base's three wire styles (`completion`, `messages`, `responses`)
each decode SSE separately, and their failure paths disagree. NusaShell's
0.1.0 fixed a family here: *"Provider stream broken"* surfaced as a bare
502 with no message, `Retry-After` beyond a sane wait was unclear, and usage
could be reported against the wrong attempt.

Ask:
- Read `infrastructure/service/core/text/{completion,messages,responses}`
  and compare their handling of: EOF mid-stream, `[DONE]` missing, an
  `error` event inside the stream, a non-2xx body, `Retry-After`, and usage
  accounting (per step vs per attempt).
- Produce a table: wire style × case × what the user sees now × what
  `turn.failed` carries. Mark the cells that look wrong.
- Do not scope-creep into new features (no new adapters). This ticket is
  about the failure surface.

### T-04 — `turn.watch` answers without attaching a live turn

Premise: `infrastructure/delivery/websocket/conversation/hub.go` (~line 606):

```go
if after > t.seq-1 {                       // newer than the journal
    c.push(resyncFrame(...))
    // returns without subscribing
}
```

A client that claims a seq the turn never emitted gets a snapshot and is then
left off the subscriber set while the turn keeps running — the tab sits still
until it reloads. NusaShell found this while auditing `turn.watch` and now
keeps an active turn attached after the snapshot
(`TestWatchAttachesWhenTheClientIsCaughtUp`).

Ask:
- Confirm the branch, and whether anything else re-attaches that client
  (`conversation.activity`, a later `turn.watch`).
- Check the neighbouring contracts while you are there: `afterSeq = -1`
  (everything), `afterSeq` behind the journal window, terminal turns. State
  which of them are covered by tests today, with names.
- The fix shape is small; the value is the contract statement (feeds T-06 /
  S-07). Report the shape, not the patch, if the contract is unsettled.

## SA — systems analysis

### T-05 — Path & placement ownership decision (feeds T-01)

Where does "what a path argument means" belong — the workspace repository
port (`infrastructure/domain/repository`), the tool adapter
(`infrastructure/service/tool/file`), or system patharg helpers? Answer with
the base's own rules (`AGENTS.md`, `infrastructure/AGENTS.md`,
`domain/AGENTS.md`, `service/AGENTS.md`) and a written decision that T-01 can
implement. Include: sandbox confinement vs. "an absolute path outside the
workspace is a normal call"; `~user`; `$HOME` staying literal; Windows
`%VAR%`; `file:` URLs; the HTTP preview route.

### T-06 — Retry contract (feeds T-02, T-04, T-09)

One page that settles: when may a provider call be retried (persisted vs
streamed), what a client must discard on `turn.phase: retry_wait`, what
`turn.watch` may answer with and what it must guarantee about attachment,
and what `turn.failed` must always carry (message, status, code). Cite the
frame spec in `docs/api/websocket.md` and the code that implements each
sentence; where they disagree, say which one is wrong.

### T-07 — Cross-repo delta map

NusaShell is the fork with four more days of fixes. Build the map the backlog
does not have yet: for the shared files that differ
(`git diff --no-index` between this repo and `../NusaShell`, excluding
`vendor/`), classify each hunk as *backportable bug fix*, *belongs to a
NusaShell-only feature*, *base is intentionally different*, or *base is
ahead*. Start from the CHANGELOG `Fixed` sections of NusaShell and work
backwards to the code. Output: `docs/audit/decisions.md` section "Delta map"
with a ranked list (impact × effort) of what the base should take, and the
tickets it implies. This is the backlog's backbone for round 2 — be concrete
about file and line, and say when a "fix" is really a redesign.

## FE — frontend (`web/`)

### T-08 — Adjacent text blocks parse as separate Markdown documents

Premise: `lib/turn.js` projects each assistant part (and each live text
segment) as its own item, and `components/transcript.js` (~line 218) renders
each item through its own `Markdown` component. Two neighbouring text items
therefore become two Markdown documents: a paragraph split at the boundary
becomes two paragraphs, a fenced block split there becomes two broken blocks,
and copy adds a blank line between them. NusaShell fixed exactly this
(*"Neighbouring text is drawn as one block"*).

Ask:
- Reproduce from history (a turn whose text is split by a tool call, then
  continues) and from the live stream. A fixture is enough if the render
  path is the same; say which fixture.
- List the flows that must *not* be joined (text separated by a tool, steer,
  plan, media, approval) and how the transcript knows the difference today.
- Propose the join point: in `historyItems`/`group`, or in the renderer.
  Follow the base's own comment that live and stored turns share one path.

### T-09 — Retried attempt leaves its abandoned tail on screen

Premise: `applyFrame` handles `turn.phase` by storing `{phase, retryAfterMs}`
and nothing else (`lib/turn.js` ~line 55). The backend announces
`retry_wait` before re-calling the provider (`event.go` ~line 121,
`frames.go` ~line 184), and the retried call streams its answer from the
start. The first attempt's text, reasoning and unsettled tool rows are never
removed, so the two attempts splice into one answer.

Ask:
- Reproduce with a stubbed socket frame sequence: `delta → tool_call →
  phase(retry_wait) → delta`; show the rendered result today.
- Establish what the client can know at `retry_wait` time: is the last
  persisted segment identifiable from history? What does
  `turn.failed`/`resync_required` carry that helps?
- Do not invent the rule; T-06 settles it. Report the current behaviour and
  the minimal reset that would satisfy T-06.

### T-10 — Late-watch duplication: live journal vs stored history

Premise: opening a room whose turn is already running calls
`watch(r, turnId, kind, -1)` (`lib/chat.js` ~line 124, ~line 458) with
`cutHistory = true`. The journal replays the whole turn from its first frame
while HTTP history already holds the steps that finished before the tab
arrived, so the same prose/tool run can appear twice — and `cutHistory` is a
blunt trim (NusaShell's CHANGELOG documents the failure mode: the trim could
cut the *previous* turn instead of the running one).

Ask:
- Reproduce: start a turn in one tab, open the room in a second tab mid-turn,
  and describe exactly what doubles (prose? tool rows? the user line?).
- Explain what `cutHistory` cuts and why, and where history and journal are
  merged (`onOpened`, `onTerminal`, `loadHistory`).
- Name the invariant the merge needs (e.g. "live tail starts after the last
  step history holds") and whether the frames carry enough to enforce it.
  That invariant is T-06's business; here it is evidence.

### T-11 — Markdown sanitizer, link policy, and copy fidelity

Premise: `lib/markdown.js` allowlists tags/attrs and a `SAFE_URI`, and
`handleCopyClick` copies source text. Both are security- and trust-relevant
and are only syntax-checked today (no test harness — T-13).

Ask:
- Adversarial pass: script/style/form injection, `javascript:`/`data:`
  hrefs, `target=_blank` without `rel`, footnotes, nested fences, tables,
  mermaid preflight, `details`/`kbd` passthrough, image `src` never fetched.
- Verify copy yields the answer's text without renderer artefacts
  (blank lines between joined parts, tool cards, footnotes).
- Report holes with the exact payload and the DOM result; do not rewrite the
  sanitizer without T-13 landing first.

## QA — verification

### T-12 — Baseline gate + `make check` parity

Record the base's baseline before any fix: `gofmt -l`, `go build ./...`,
`go vet ./...`, `go test ./...`, `make test-web`, and the CLI/HTTP smoke
set. Then add a read-only `check` target to `Makefile` (host build, vet,
tests, WebUI syntax check, cross-compile `windows`/`darwin`) so later rounds
have one command that cannot mutate the tree. Document the exact command in
`docs/audit/README.md` if it differs from `make check`.

### T-13 — WebUI test harness in the base

The base has no JavaScript tests — `make test-web` only runs `node --check`.
NusaShell has a working harness (`web/tests/register.mjs`, `loader.mjs`,
`env.mjs`, controller/projection suites). Port the *mechanism* (module
loader, DOM/Preact stubs, test runner invocation), adapt it to the base's
smaller module set, and land the first tests for `lib/turn.js`
(`applyFrame` cases, `historyItems`, `group`) so T-08/T-09/T-10 have a place
to prove their fixes. Keep it dependency-free: `node --test` only, no npm.

### T-14 — Black-box reproduction kit for T-01/T-04/T-08/T-10

Scripts under `test/` that reproduce a ticket end to end against a live
`nusallm serve` on a scratch data dir: the path-expansion probe (T-01), a
raw WebSocket client that watches with an impossible `afterSeq` (T-04), a
seeded conversation that renders split Markdown (T-08), and a two-client
late-watch scenario (T-10). Output must be a pass/fail with the observed
bytes, not a screenshot. Keep the fake provider local (an HTTP stub), never
a real model.

## Round 2 and later (from the delta map, D-07)

### T-15 — Thread refresh compares before writing

`web/lib/chat.js:80` replaces a room's item list wholesale on every
refresh, and `chat.js:130` assigns `r.info` from every read. A read that
started before a local change (rename, pin, model pick) lands after it and
undoes it. NusaShell added a compare-before-write and a read-again guard
(CHANGELOG Unreleased). Round 2: define what "the answer says what we
already have" means for a room and for a row, then apply it in one place.

### T-16 — Windows & encoding hardening

Three small shared-core defects from the delta map, one ticket:

- `apply_patch` compares raw strings, so an LF `old_string` never matches a
  CRLF file (NusaShell retries with `\r\n`, `patch.go:98-105` there).
- Invalid UTF-8 in `read_file`/search is sniffed by content type only; the
  bytes reach JSON unchanged and are mangled on encode.
- A silent `exec` (no output, exit 0) returns an empty result; it should say
  it exited 0 (NusaShell `terminal/exec.go:147-151`).

Each needs a failing fixture first.

### T-17 — Deep `turn.watch` replay kills the socket

`turnWatch` enqueues every journaled frame with `conn.enqueue`, which calls
`kill()` when the queue is full (`conn.go:53-67`, `outboundQueueCap = 64`).
A `turn.watch` with `afterSeq: -1` on a turn with more than 64 frames
therefore disconnects the client mid-replay; the client reconnects with no
applied frame, asks for `-1` again, and loops. Rule (ratified in round 1):
replay verbatim while the window fits the socket's free queue space **minus
a headroom the server keeps for live traffic**; beyond it answer
`turn.resync_required`, and still attach when the turn is live. An
exact-fit replay is not allowed either: it leaves the queue full, so the
next live frame kills the socket the replay just saved. The doc loses the
unconditional "verbatim" promise.

### T-18 — A folded steer's stored row carries its `inputId`

The wire already names a steer — `turn.steer`, `turn.steer_ack`, and the
journaled frame all carry `inputId` — but the stored message does not:
`messageDTO` (`infrastructure/delivery/http/conversation/dto.go:120-135`)
projects id, role, content, parts, userType, finishReason, toolCalls,
attachments, createdAt, and nothing in `entity` or
`repository/conversation` mentions `InputID`. So the client cannot match
the live steer bubble to the history row that folded it: the bubble appears
twice until the turn ends, and again after any mid-turn reload.

Decision (D-06.1): the row a fold writes stores the steer's `inputId`, in
the entity, the record, and the DTO. Breaking change, no compatibility path
— a record without the id reads as "no input id" and is never matched by
text. T-10 consumes it. BE owns the change; it is the enabler, not the fix.


