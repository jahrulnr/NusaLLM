# Findings — QA (round 0)

Status: ready
Tickets: T-12 (baseline + `make check`), T-13 (WebUI harness), T-14 (black-box
reproductions)

Round 0 work on `llm-base` at commit `9b2de17`. No production source was
modified: the only files written are `Makefile`, `web/tests/**`, `test/repro/**`
and this file. `make test` was not run (it executes `go fix`, which rewrites
sources); every Go result below comes from the explicit `go build ./... &&
go vet ./... && go test ./...` sequence.

## Round 1 — closing verification

Status: verified 2026-11-25, tree at `9b2de17` + the round-1 working diff (not
committed). No production file was touched by QA in this round either; the only
edits are `test/repro/**`, `web/tests/**` and one line of `web/embed_test.go`.

### Gate

| Command | Exit | Result | Time |
|---|---|---|---|
| `make check` | 0 | 68 packages `ok`; `test-web` runs 52 Node tests: 44 pass, 8 skipped, 0 fail | 4 s warm (13 s cold) |
| `node test/repro/run.mjs` | 0 | 14 scenarios / 64 checks: 10 scenarios PASS, 4 PENDING | 38 s |
| `go test -count=1 -race ./infrastructure/delivery/websocket/... ./application/job/... ./infrastructure/repository/workspace/...` | 0 | 4 packages `ok` (websocket, websocket/conversation, job, workspace) | 3 s |
| `gofmt -l` (whole tree, vendor excluded) | 0 | clean | — |

`go vet ./...` runs inside `make check` and is clean. `go test ./...` now holds
1046 `func Test…` (up from 1022 at round 0).

### Verdict per ticket

| Ticket | Verdict | Evidence | Note |
|---|---|---|---|
| T-01 | verified | `t01` 5/5, `t01b` 6/6 | `~`/`file:`/absolute/relative behave per ADR-0002; writes outside refuse naming the root |
| T-02 | verified | `t02` 5/5 | 429 after a delta retries, `retry_wait` announced (1000 ms), one assistant row, one user row |
| T-03 | still open | `t03` 0/5 | no change in `infrastructure/service/core/text/*`; five rows still reproduce, see below |
| T-04 | verified | `t04` 6/6 | impossible seq → resync then live frames + terminal on the same socket; finished/unknown watch answered, not attached |
| T-06 | verified (terminal clauses) | `tc` 6/6, `tp` 4/4 | message/status/code, `retryAfterMs` present only with a provider pause, `resyncRequired` + `userMessagePersisted` on a failed send |
| T-08 | verified | `t08` 6/6, `web/tests/turn.test.mjs` | `joinText` folds neighbours, keeps the first id, a tool stays a boundary; transcript calls it on both lists |
| T-09 | verified | `web/tests/turn.test.mjs` (T-09 cases) | reset stops at the last `turn.step_finished`/folded steer (`persistedMark`) |
| T-10 | still open | `t10` 0/5, `web/tests/live-merge.test.mjs` 8 skipped | `liveHistory`/`liveSegments` are not exported yet |
| T-11 | verified | `t11` 4/4 (headless Chrome DOM) | invalid `<div data-diagram>` no longer truncates the answer |
| T-16 | still open | `t16` 0/2 | provider 500/529 cross `status` correctly but `code` is `internal`, which the terminal clause forbids |
| T-17 | verified | `t17` 4/4, `tr` 3/3 | 92-frame journal → resync, no replay, socket alive, terminal delivered; a 64-frame window is refused because the 32-frame budget reserves headroom |
| T-18 | still open | `t18` 1/3 | the steer folds and persists, but the row carries no `inputId` and the projection no `steerInput` |

Statuses in `test/repro/expected.json`: `t01`, `t01b`, `t02`, `t04`, `t08`,
`t11`, `tc`, `tp`, `t17`, `tr` → `landed`; `t03`, `t10`, `t16`, `t18` →
`pending`.

`tr` moved to `landed` when `replayHeadroom = outboundQueueCap / 2` reached the
tree: a 64-frame window now exceeds the 32-frame budget, so the watch answers
`resync_required` and attaches deterministically instead of racing a full
queue — the scenario's observed frames show `0 replayed` and a delivered
terminal. Before the headroom landed it passed only by kernel buffering, which
is why it was reported as pending when the PA asked for it.

Two "landed" entries are regression guards rather than proof of the fix, and
are reported as such: `tp` (the loop already returned on an append error before
round 1 — the scenario now pins the D-06 boundary), and `t01b` (the CRLF patch
and the `exec` escape already worked; the scenario exists because the T-01
rewrite could have locked them down).

### Production findings from the verification

1. **T-16 — provider 5xx is classified `internal`.** `t16` sends HTTP 500 and
   HTTP 529 from the stub; the terminal carries `status: 500/529`, `code:
   "internal"`. `docs/api/websocket.md` (terminal frames) says a
   provider-originated 5xx never reports `internal`; `codeFor(status, upstream)`
   (`infrastructure/delivery/transport/transport.go:55-85`) only consults
   `upstream` for 429, so 500/529 fall through to `internal`. Owner: BE (T-16).
2. **T-03 — five rows still reproduce** (`t03`):
   - `messages`: `stop_reason` without `message_stop` → `turn.failed 502
     "upstream stream truncated"` after 3 retries, and the streamed answer is
     stored as `interrupted` (the provider's verdict is discarded).
   - `responses`: an in-stream `error` with `Retry-After: 120` → `429
     "provider rejected the request (HTTP 429)"`, no `retryAfterMs`, three
     provider calls (the wait is lost, so the job retries a rate limit).
   - `completion`: HTTP 200 + JSON error envelope → `502 "upstream stream
     truncated"`, three calls, provider message lost.
   - usage: a later partial `usage` object zeroes `inputTokens` (observed 10,
     expected 100; `outputTokens` 7 is right).
   - truncated tool call: the turn **completes** (`turn.failed` only because no
     executable call survived — `502 "provider requested tool calls but sent
     none"`, one call, no retry) and the call is dropped, so a planned action
     vanishes without history holding it.
3. **T-10** — no merge seam in `web/lib/turn.js`; `web/tests/live-merge.test.mjs`
   skips its 8 assertions until `liveHistory`/`liveSegments` exist.
4. **T-18** — the folded steer row is stored (`content: "steered note"`) but
   `inputId` is absent from the record and from the projection.

### Contract clauses without a test, now covered

`docs/api/websocket.md` gained clauses with the round-1 fixes; the kit covers
the new ones explicitly:

- "A provider-originated 5xx never reports `internal`" → `t16` (red, T-16).
- "A replay that does not fit the socket's replay budget is answered like any
  other unsatisfiable window" → `t17` (92 frames) and `tr` (64 frames against
  the 32-frame budget).
- "A client that received `resync_required` for a live turn also receives the
  terminal" → `t04` check 2–3.
- "Watching a finished turn replays through the terminal or answers resync" and
  "an unknown turn answers `resync_required`" → `t04` checks 5–6.
- "`turn.failed` carries status/code/message, `resyncRequired`, and on send
  turns `userMessagePersisted`" → `tc` checks 2–3.
- "`retryAfterMs` is present only when the provider named a pause" → `tc`
  checks 4 and 6.

### Kit changes this round

- `test/repro/expected.json` — per-scenario `landed`/`pending`; a red check in a
  landed scenario exits 2, a red check in a pending scenario is reported as
  PENDING (never PASS). A pending scenario whose checks all pass prints a note
  to flip it.
- Scenarios now assert contracts, not the round-0 defects: `t01`, `t04`, `t08`,
  `t10` were inverted; `t02`, `t03`, `t11`, `t16`, `t17`, `t18`, `tc`, `tp`,
  `tr` are new.
- `t11` is a DOM check: headless Chrome `--dump-dom` over the seeded room (10 s
  virtual-time budget). It skips with a missing-browser check instead of
  pretending.
- `t01b` covers the T-01 regressions: `~` binding refused without a literal `~`,
  `read_media` on an absolute path with the stored sha256 pin, `apply_patch` on
  CRLF, write refusal naming the root, `exec` still writing anywhere.
- `tr` forces queue pressure from the client: `b.pause()` stops reads across the
  replay and the first live frames, and the stub is gated so the journal is
  frozen while the exact window is computed.
- `web/embed_test.go` now lists `lib/mdsafe.js`, so the new module is proven
  embedded rather than assumed.

## T-12 — baseline gate + `make check` parity

Verdict: DONE
Gate: `make check` — green at the time of writing.
Owner layer: `Makefile` (build/verification surface, QA-owned).

### Baseline, recorded before any change

Host: Linux amd64, `go1.27.4`, `node v26.12.1`, `2026-11-25T03:26+07:00`.

| Command | Result | Wall time |
|---|---|---|
| `gofmt -l $(go list -f '{{.Dir}}' ./... \| grep -v /vendor/)` | no output (clean) | — |
| `go build ./...` | exit 0 | 1.1 s (warm cache) |
| `go vet ./...` | exit 0 | 0.2 s |
| `go test -count=1 ./...` | exit 0 — 67 packages `ok`, 0 failures, 1022 `func Test…` in the tree | 18.7 s |
| `make test-web` | exit 0 (`node --check` over `web/app.js`, `web/lib/*.js`, `web/components/*.js`) | 0.34 s |
| `make fmt-check` | exit 0 | — |

CLI smoke (binary built to `/tmp/qa/nusallm`, `-ldflags "-X main.version=0.1.0-qa"`):

| Command | Observed |
|---|---|
| `nusallm version` | `nusallm 0.1.0-qa` |
| `nusallm --help` | usage block, exit 0 |
| `nusallm config validate` (scratch config) | `config at /tmp/qa/config.json is valid` |
| `nusallm serve` (`CONFIG_PATH=/tmp/qa/config.json`, `storage.dir=/tmp/qa/data`, port 18081) | boots, prints both host aliases |

HTTP smoke against that server:

| Probe | Observed |
|---|---|
| `GET /api/health` | `200` `{"status":"ok"}` |
| `GET /api/conversations` | `200` `{"data":[],"page":{"offset":0,"limit":30,"total":0,"hasMore":false}}` |
| `GET /` (WebUI) | `200`, 1644 bytes `index.html` |
| `GET /api/conversations` with `Host: evil.example:18081` | `403` (host guard closes) |
| WS upgrade without `Origin` | rejected (`403 forbidden_origin`) — expected; Node's built-in `WebSocket` cannot send one, which is why the kit carries a raw client |
| WS upgrade with `Origin: http://127.0.0.1:<port>` | `101`, `turn.watch` on an unknown turn answers `turn.resync_required` |

Config note: the `config.json` schema is strict (`DisallowUnknownFields`); a
`log.level` key fails the whole load with a bare `config load failed`. The
working minimal document is `server.port`, `server.hosts`, `storage.dir`.

### The gate

`make check` is read-only and separate from `make test`:

```
check: fmt-check test-web
	go build ./...
	go vet ./...
	go test ./...
	GOOS=windows go build ./...
	GOOS=darwin go build ./...
```

- `test` still runs `go fix ./...` first — it mutates the tree by design and
  must not be used for a verdict.
- `test-web` now also runs the Node projection tests (T-13), so one command
  covers the WebUI.
- The cross-compiles use `go build ./...` with no `-o`: nothing is written
  into `bin/`, and a failing platform names itself.
- Verified: `make check` exits 0 in ~13 s warm; `gofmt -l` stays clean.

## T-13 — WebUI test harness

Verdict: DONE (harness landed, 23 tests, all green)
Gate: `node --test web/tests/` and `make test-web` — green.
Owner layer: `web/tests/**` (QA-owned); it tests `web/lib/turn.js` as-is.

What was ported from NusaShell, adapted to the base's smaller module set:

- `web/tests/register.mjs` — `node:module` register hook, preloaded by `make
  test-web` (`--import`) or imported first by a test file.
- `web/tests/loader.mjs` — resolves the WebUI's absolute URL imports
  (`/vendor/...`, `/lib/...`) against `web/`, the root the server serves.
- `web/tests/env.mjs` — a just-enough browser: `window`, `location` with
  `hashchange`, `history.replaceState`, `localStorage`, `requestAnimationFrame`,
  `document`, `matchMedia`, a scriptable `fetch` (`mockApi`/`calls`) and a
  hand-driven `WebSocket` (`sockets`). Timers are unref'd so a leaked toast
  cannot hold the process; `sleep` keeps a real timer. This is what
  `lib/chat.js`, `lib/store.js` and `lib/api.js` need at module load.
- `web/tests/turn.test.mjs` — 23 tests over the projection: `applyFrame`
  (delta continuity, a tool call as the boundary, duplicate seq, tool
  input/output streaming, phase lifetimes, steering folding, usage and
  terminal settling), `historyItems` (parts order, fallback, workspace
  injection as `cwd`, missing calls, `interrupted`), `group`, `toolFailed`,
  `toolSummary`.

Two tests are deliberate characterizations of the round-0 defects and are
named for their tickets, so the fix round changes them on purpose:

- `T-08: adjacent text parts are separate items today` — two neighbouring text
  parts become two transcript items.
- `T-09: a retried attempt leaves its tail on screen today` — at
  `turn.phase: retry_wait` nothing is dropped, and the retry's delta opens a
  third segment after the abandoned text and tool row.

No npm, no `node_modules`: `node --test` only. Both invocations work —
`node --test web/tests/` (helper files do not match the test patterns, only
`*.test.mjs` runs) and `node --import ./web/tests/register.mjs --test
web/tests/*.test.mjs`.

Limitation, stated rather than hidden: the DOM is not shimmed. A test that
must see rendered markup (T-11's sanitizer, T-08's renderer-side fix) needs
the browser, not this harness; the harness covers projection and controller
logic.

## T-14 — black-box reproduction kit

Verdict: DONE (4 scenarios, 16/16 checks reproduce their ticket)
Gate: `node test/repro/run.mjs` — every scenario `CONFIRMED`, exit 0.
Owner layer: `test/repro/**` (QA-owned); it drives a real `nusallm serve`.

Each scenario gets its own temp root, `HOME`, data dir, workspace, ephemeral
port and teardown (`QA_KEEP=1` keeps the root for inspection). The provider is
always a local scripted HTTP stub speaking the `completion` SSE wire; no model
is called. The WebSocket client is a raw RFC 6455 implementation because the
handshake requires an exact `Origin`, which a browser `WebSocket` cannot send.

### T-01 — file-tool path policy: CONFIRMED

Evidence (`node test/repro/run.mjs t01`):
`write_file {"path":"~/probe.txt"}` → tool says `wrote 8 bytes to ~/probe.txt`
and the file is at `<workspace>/~/probe.txt`; `<home>/probe.txt` is missing.
`read_file {"path":"file:///etc/hostname"}` → `error: openat
file:///etc/hostname: no such file or directory` (the URL is a literal name).
`read_file {"path":"/etc/hostname"}` → `error: openat /etc/hostname: path
escapes from parent`.
Explains at `infrastructure/service/tool/file/root.go:24` (`callRoot` passes the
arg through) and `infrastructure/repository/workspace/workspace.go:31`
(`os.OpenRoot` treats it as a name under the root). The placement rule is T-05's;
this code currently owns "what a path means" only by accident.

### T-04 — `turn.watch` with an impossible `afterSeq`: CONFIRMED

Evidence (`node test/repro/run.mjs t04`): watching a live turn with
`afterSeq: 10000` answers `turn.resync_required {"turn":"active"}`; in the same
window the owning socket receives 4 more frames (`turn.delta#2`,
`turn.delta#3`, `turn.step_finished#4`, `turn.completed#5`) and the watching
socket receives **0**. Control: the same socket watching a later turn with
`afterSeq: 0` replays and then receives the live terminal, so the silence is
the attachment decision, not a dead client.
Explains at `infrastructure/delivery/websocket/conversation/hub.go:606`: the
newer-than-latest branch pushes the snapshot and returns before
`t.subscribed[c] = struct{}{}`.

### T-08 — one answer drawn as two Markdown documents: CONFIRMED

Evidence (`node test/repro/run.mjs t08`): a seeded assistant message with two
adjacent text parts reaches the transcript as two text items (38 and 23 bytes).
The boundary falls inside an open fence (odd number of ``` in item 1). Parsed
with `web/vendor/marked` per item: 2 code blocks; the same text as one document:
1 code block — item 2 renders `const b = 2;` as a paragraph and `Done.` inside a
code block it never belonged to.
Explains at `web/lib/turn.js:112` (one item per text part) and
`web/components/transcript.js:218` (each text item through its own `Markdown`).

### T-10 — late watch: history vs journal: CONFIRMED, with the precise shape

Evidence (`node test/repro/run.mjs t10`). One turn with a tool step then a slow
second step; a second client attaches mid-turn.

- Variant A — the second tab reads history after step 1 committed: history holds
  `user | text:"First answer." | user | text:"Step one." | tool:call_step1`, and
  the journal replay carries the **same** `Step one.` and `call_step1`. The
  duplication exists in the data. The transcript's trim
  (`web/components/transcript.js:150`) keeps history only up to the newest user
  item, which here *is* the running turn's prompt, so the duplicate is hidden —
  by a trim, not by a merge rule.
- Variant B — the second tab loaded the room **before** the turn started and is
  woken by `conversation.activity {state:"opened"}`: its page ends on the
  previous turn (`user | text:"First answer."`), so the same trim cuts *the
  previous answer* and the running prompt is not on the page at all. The drawn
  screen after the trim is `user:"first ask" | text:"Step one." | tool | text:"Step
  two streams here."` — the previous answer vanishes until the terminal re-read.

Explains at `web/lib/chat.js:322` (`cutHistory = afterSeq < 0`),
`web/lib/chat.js:124` and `:458` (both late watches ask for `-1`) and
`web/components/transcript.js:150`. The invariant the merge needs — "the live
tail starts after the last step history holds, keyed by the turn, not by the
last user row" — is T-06's to state.

## Tools now available

```
# the one read-only gate (format, WebUI syntax + tests, build, vet, tests,
# windows/darwin cross-compile). Never mutates the tree. ~13 s warm.
make check

# WebUI projection tests alone, with and without the loader hook
node --test web/tests/
node --import ./web/tests/register.mjs --test web/tests/*.test.mjs

# one reproduction, by prefix; no argument runs all four
node test/repro/run.mjs t01
node test/repro/run.mjs t04 t08 t10
node test/repro/run.mjs                # 16 checks, ~10 s

# keep a scenario's scratch root (server logs, data dir, workspace) for inspection
QA_KEEP=1 node test/repro/run.mjs t10
# use an already-built binary instead of `go build ./cmd/nusallm`
NUSALLM_BIN=/tmp/qa/nusallm node test/repro/run.mjs t01

# the old baseline sequence, still the right one to quote in a report
gofmt -l $(go list -f '{{.Dir}}' ./... | grep -v /vendor/)
go build ./... && go vet ./... && go test -count=1 ./...
make test-web
```

Exit codes of `node test/repro/run.mjs`: `0` every selected scenario reproduced
its ticket, `2` a check failed to reproduce (the premise did not hold), `1` a
scenario could not run (harness error, printed with the server log tail).

Writing a new reproduction: copy a file in `test/repro/scenarios/`, register its
prefix in `test/repro/run.mjs`, and return
`{ checks: [{ label, ok, observed: [...] }], confirmedVerdict, notes }`.
`startServer` gives `{ request, seedConversation, addGateway, workspace, home,
data, root, logs }`; `startStub` takes an async generator of SSE chunks
(`text`, `reasoning`, `toolCall`, `finish`, `usage`) and records every request
body for assertions.

## Reproductions ready for other tickets

| Ticket | Command | What it proves | Observed |
|---|---|---|---|
| T-01 | `node test/repro/run.mjs t01` | `~`, `file:` URL and absolute host paths are literal names under the workspace root | literal `~` dir + two `openat` errors |
| T-04 | `node test/repro/run.mjs t04` | an impossible `afterSeq` takes the snapshot and never subscribes the socket | 0 frames on the watcher, 4 on the owner; control attaches |
| T-08 | `node test/repro/run.mjs t08` | adjacent text parts become two Markdown documents, fence broken | 2 code blocks vs 1, `Done.` rendered inside code |
| T-10 | `node test/repro/run.mjs t10` | journal duplicates stored steps; the trim cuts by "last user row", which can be the previous turn | previous answer dropped from the second tab's view |
| T-09 | `web/tests/turn.test.mjs` (`T-09: …`) | `retry_wait` drops nothing; the retry splices after the abandoned tail | `text | tool | text` for one answer |
| T-02/T-03 | — | not covered by round-0 QA: both need a provider-failure script rather than a projection probe | follow-up round |

## Open questions for the PA

- T-10's fix shape depends on T-06's invariant. The kit measures "duplicate
  bytes in history + journal" and "what the trim cuts"; it does not assert a
  merge rule, because the rule does not exist yet.
- T-08's join point (projection vs renderer) is an FE decision; the harness can
  prove either, but the renderer path needs a DOM (see T-13's limitation).
- Every scratch server in this kit runs with its own `HOME`; a reproduction
  involving `~` must keep that, or it writes into the operator's home.
