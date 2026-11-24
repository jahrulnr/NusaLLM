# Findings — BE (round 0)

Status: ready
Tickets: T-01, T-02, T-03, T-04

Round 0 audit of `llm-base` at commit `9b2de17` (`go.mod` module
`github.com/jahrulnr/nusallm`). No production source was modified. The only
file written in this repo is this one; every probe ran against a copy at
`/tmp/be-audit/base` (`rsync -a --exclude .git --exclude bin --exclude data`),
so the numbers below can be re-derived without touching the tree.

Gate at audit start (real repo): `go build ./...` exit 0, `go vet ./...`
exit 0, `go test ./... -count=1` exit 0 (all packages `ok`). `make test` was
not run (it rewrites source through `go fix`).

Cross-references used while auditing: `docs/adr/0002-workspace-resolves-path-arguments.md`
(proposed, feeds T-01) and `docs/adr/0003-turn-retry-and-watch-attachment.md`
(proposed, feeds T-02/T-04). Both were written by SA during this round; this
file records what the code does now and where the ADR and the code disagree.

| Ticket | Verdict | Severity | One line |
| --- | --- | --- | --- |
| T-01 | CONFIRMED | user-visible | `~`, `file:` and `%VAR%` are literal names; `write_file` silently creates a `./~` (and `./file:`) tree; absolute paths are refused even *inside* the workspace, while `read_media` accepts them |
| T-02 | CONFIRMED | user-visible | one 429/5xx after the first delta ends the turn: no retry, no `retry_wait`, partial persisted as `interrupted`; only `turn.resume` recovers |
| T-03 | CONFIRMED (family) | user-visible / data | the three wires disagree on terminal evidence, in-stream errors, `Retry-After`, usage merging and truncated tool calls; 12 case rows, 7 fixable defects below |
| T-04 | CONFIRMED | user-visible | a `turn.watch` whose `afterSeq` is ahead of the journal answers `resync_required` and never subscribes; the tab misses the rest of the turn *and* the terminal |

---

## T-01 — File-tool path policy: `~`, `file:` URL, `%VAR%`, absolute paths

Verdict: CONFIRMED
Severity: user-visible (silent wrong placement on write; unexplained failure on
read; two tools disagree about the same argument)

### Evidence

Probe: `TestAuditPathPolicy` (a copy-only `_test.go` in
`infrastructure/service/tool/file/`), driving each tool through `Execute` with
`ctx = service.WithWorkspaceDir(ctx, workspace)` — the same ctx `runTurn` sets
at `application/job/turn.go:48`, so this is the turn's real path. Command:
`go test ./infrastructure/service/tool/file/ -run TestAuditPathPolicy -v`.
Observed (verbatim from the `t.Logf` lines):

```
read_file  {"path":"~/probe.txt"}                  => openat ~/probe.txt: no such file or directory
write_file {"path":"~/probe.txt","content":"x"}    => text="wrote 1 bytes to ~/probe.txt" err=<nil>
   workspace tree: <ws>/~ holds probe.txt;  $HOME/probe.txt still "home-file"
read_file  {"path":"file://<ws>/url.txt"}          => openat file://<ws>/url.txt: no such file or directory
write_file {"path":"file://<ws>/url.txt",…}        => text="wrote 7 bytes to file://<ws>/url.txt" err=<nil>
   workspace entries after: [file: url.txt ~]      ;  <ws>/url.txt still "url-file"
write_file {"path":"%APPDATA%/c.txt","content":"x"} => text="wrote 1 bytes to %APPDATA%/c.txt" err=<nil>
read_file  {"path":"<tmp>/outside.txt"}            => openat …: path escapes from parent
write_file {"path":"<tmp>/outside.txt",…}          => mkdirat <tmp>: path escapes from parent
apply_patch{"path":"<tmp>/outside.txt",…}          => openat …: path escapes from parent
read_file  {"path":"<ws>/inside.txt"}              => openat <ws>/inside.txt: path escapes from parent   (absolute, but inside)
read_media {"path":"<tmp>/pic.png"}                => "loaded image pic.png (image/png, 72 bytes) — …" 1 attachment
read_media {"path":"~/pic.png"}                    => "~/pic.png" is not a readable regular file: openat ~/pic.png: no such file or directory
read_media {"path":"file://<tmp>/pic.png"}         => same, no such file
read_file  {"path":"mid/~/b.txt"}                  => literal, as on both trees
```

Code that produces it:

- `infrastructure/service/tool/file/read.go:73` — `root.OpenRegular(input.Path)`
  with the raw string; schema promises "Path relative to the workspace root"
  (`read.go:38`).
- `write.go:74` `Stat(input.Path)`, `write.go:80` `MkdirAll(dir, 0o700)` where
  `dir = filepath.Dir(input.Path)`, `write.go:87` `WriteFile(input.Path, …)` —
  this is what silently materializes `./~` and `./file:` directories.
- `patch.go:74` / `patch.go:111` — same raw string for read and write.
- `read_media.go:89-95` — the only path tool that branches:
  `if filepath.IsAbs(input.Path) { tool.store.OpenHost(...) }`, pinning the
  bytes with sha256. Schema says "or an absolute host path" (`read_media.go:53`).
- `infrastructure/service/tool/file/root.go:24-30` — `callRoot` decides only
  *which directory* (`WorkspaceDir(ctx)` else the tool's configured dir); it
  never looks at the argument.
- `infrastructure/repository/workspace/workspace.go:42-67` — `OpenHost` /
  `HostURI`: the port already carries the "reads may name a host path" half of
  the rule; `OpenRegular`/`WriteFile` confine to `os.Root`.
- `infrastructure/domain/repository/workspace.go:13-22` — the port's own
  comment: "read tools opt in when a call names an absolute path; writes never
  ride it".
- Base has **no** expansion anywhere: `grep -rn 'UserHomeDir\|ExpandEnv\|
  HasPrefix(path, "~")'` finds only `config/storage.go:20` (a leading `~` in
  `storage.dir` *is* expanded, config-layer), the CLI installer, the file-tree
  home lookup and systemd. No tool, no `service.WorkspaceDir` caller, no route.
- `service.WorkspaceDir` callers checked for the same assumption:
  `turn.go:48` (sets it), `exec.go:112` (shell cwd — no path argument),
  `skill.go:61,86` (skill name only), `custom/custom.go:39` (config paths),
  `workspace/workspace.go:52` (reports the binding), `environment.go:113`
  (injection record). None expand or interpret a model-written path.

All path-bearing surfaces in the base:

| Surface | Location | Argument accepted | `~` | `file:` | absolute outside ws | `%VAR%` |
| --- | --- | --- | --- | --- | --- | --- |
| `read_file` | `read.go:73` | workspace-relative | literal name | literal name | error `path escapes from parent` | literal name |
| `apply_patch` | `patch.go:74,111` | workspace-relative | literal | literal | error | literal |
| `write_file` | `write.go:74,80,87` | workspace-relative | **creates `./~`** | **creates `./file:`** | error | **creates `./%VAR%`** |
| `read_media` | `read_media.go:89-95` | relative **or absolute** | literal (fails) | literal (fails) | **accepted via `OpenHost` + sha256 pin** | literal |
| `exec` | `exec.go:112` | no path arg (cwd only) | n/a | n/a | n/a | n/a |
| `skills` | `skill.go:61,86` | name | n/a | n/a | n/a | n/a |
| `workspace` | `workspace.go:52` | none (reports binding) | n/a | n/a | n/a | n/a |
| `GET /api/fs/browse` | `delivery/http/fs/fs.go:51,57-74` | **absolute host path only**, directory listing | 400 "path must be absolute" | 400 | allowed (it is a picker over the whole host) | 400 |
| send/edit workspace override | `application/job/send.go:147-155` → `CheckRoot` | absolute dir; `CheckRoot` is `filepath.Abs` + `os.OpenRoot` | literal `<cwd>/~/…` → open error | literal → open error | allowed | literal |
| attachment resolution | `service/wire/media.go:87-150` | `file://` only; root-confined or sha256-pinned | n/a | n/a | only with a pin | n/a |

Two things the backlog premise got slightly wrong, worth recording:

1. An absolute path is **not** "used as given": `os.Root` refuses it outright,
   *including* an absolute path that points inside the workspace (`/ws/x` →
   `path escapes from parent`). So the base is stricter than "resolves
   everything inside the workspace only" — it refuses absolute paths entirely
   for read/write/patch, and accepts them for `read_media`.
2. The base has **no HTTP preview route**. `/api/fs/browse` is a directory
   picker for the workspace picker UI and never serves file content; NusaShell's
   `/api/files` preview is a feature the fork added. There is nothing to
   reproduce for "the preview route" here; the requirement is that a future
   route uses the same capability (ADR-0002 §5).

NusaShell's equivalent, for contrast only (not a template — its `placeOf`/
`anchorOf` sit in the adapter and `os.Stat` from there, which
`infrastructure/service/AGENTS.md:20` forbids): `infrastructure/service/tool/file/root.go:30-95`
plus `infrastructure/system/patharg/expand.go`. Its `TestFileToolsExpandHomePaths`
pins `~/notes/a.md` → `$HOME/notes/a.md`, `file://…` → the named file, and a
literal `%APPDATA%` on Linux.

### Root cause

There is no statement of what a path argument means. Each adapter interprets the
string itself: three interpret it as a workspace-relative name and hand it to
`os.Root`, one interprets an absolute path as a host path and reaches the port's
`OpenHost`. Nothing decodes `~`, `file:` or `%VAR%`, so those are names — and
`write_file`'s `MkdirAll(filepath.Dir(...))` turns a wrong interpretation into a
silently created directory instead of an error.

### Owner layer

Per ADR-0002 (proposed): the **workspace repository** (`repository.Workspace`,
local implementation in `infrastructure/repository/workspace/`) owns placement
and confinement; path **syntax** (`~`, `file:`, `%VAR%`) belongs in a
`infrastructure/system/patharg`-style mechanism parameterized by host facts, so a
non-local driver decodes against its own host. The adapters pass the raw string
and consume a handle/error. This matches `infrastructure/repository/AGENTS.md`
("`workspace/` owns workspace-scoped file access and path confinement") and
`infrastructure/service/AGENTS.md` ("do not call `os.Open*` … from service
adapters"). Nothing belongs in `application/job`.

### Proposed fix

Shape (ADR-0002 §1-§5, implementation for round 1):

1. `patharg.Expand(raw) (string, error)` in `infrastructure/system`: `file:`
   URL (localhost/empty authority only, percent-decode, Windows `/C:` fixup), a
   leading `~`/`~/`/`~\`, Windows `%VAR%` (unset = error, `%%` = literal,
   `%name-with-bad-chars%` stays literal), everything else literal. Inject
   `(goos, sep, home, env)` so Windows rules are testable on Linux.
2. `Workspace` grows one resolve step that returns either a confined session +
   relative name (path inside the root) or a host reader (absolute, read-only),
   and an explicit error for a write outside the root naming the root. The
   local implementation keeps `os.Root` for the relative case.
3. `read_file` / `read_media` / `apply_patch` / `write_file` pass the raw string
   to that step; `read_media`'s `filepath.IsAbs` branch and `write.go:80`
   `MkdirAll` disappear (the repository creates parents inside the root).
4. `storage.dir`'s existing `~` rule (`config/storage.go:20`) either moves to
   `patharg` or calls it, so one expansion exists in the tree.

Blast radius:

- Behaviour change: `write_file {"path":"~/x"}` and `{"path":"file:…"}` stop
  creating `./~`/`./file:` and instead become an explicit error (writes never
  leave the workspace, ADR-0002 §3-4). Any prompt/skill that taught a model
  otherwise needs the same edit. The base's own builtin skills do name `~`
  paths — `assets/skills/skill-creator/SKILL.md:17,269,305,321` and
  `assets/skills/tool-creator/SKILL.md:19` ("`~/.agents/skills`",
  "`--path ~/work/skills`"). Those run through `exec`, where the shell expands
  `~` and nothing changes; the risk is the prose: a model that carries the same
  `~/…` into `read_file`/`write_file` gets a literal name today and an explicit
  "outside the workspace" error after the fix. The skill text should say which
  tool is meant.
- `read_file` starts accepting an absolute in-workspace path (today an error) —
  strictly additive.
- `read_media` keeps its current outcome for absolute paths (pin still minted),
  but its error text changes shape for `~`/`file:` (now works instead of
  failing).
- `repository.Workspace` port change touches every implementation and test fake:
  `repository/workspace/workspace.go`, `application/conversation/send_test.go:1071`
  (`virtualWorkspace`), and any driver bundle (`application/job/environment.go`);
  the interface is small and the fakes are three methods wide.
- No storage-format change, no wire change, no migration.

Tests to add (round 1): a table for `patharg.Expand` covering the ADR-0002
matrix (mirrors NusaShell's `expand_test.go` caselist but Windows rules must be
driven by injected facts, not `GOOS`); one tool-level test per tool proving `~`,
`file:` and an absolute out-of-root path; one test proving `write_file` cannot
create `./~`; one proving an absolute *inside* path reads; one proving
`read_media` still mints a pinned `file://` for a host path; a `CheckRoot` test
for a `~` workspace binding (explicit error, not a literal dir).

### Open question

- ADR-0002 cites `write.go:169-183`; the file is 93 lines and the relevant
  calls are `write.go:74,80,87`. Worth fixing the citation in the ADR before
  round 1.
- The ADR makes `write_file ~/notes.md` an **error** in the base (NusaShell
  instead anchors and writes there). That is a deliberate product difference; if
  the base's prompts or the FE's error copy assume NusaShell's behaviour, the PA
  should say which UX the base ships. No code decision is missing.

---

## T-02 — Transient failure after streamed output kills the turn

Verdict: CONFIRMED (behaviour); the contract that overrides it is ADR-0003
(proposed — PA ratifies)
Severity: user-visible

### Evidence

The gate, verbatim (`application/job/turn.go:216-229`):

```go
for attempt := 0; ; attempt++ {
    // Output capture resets per attempt — a retried call
    // rebuilds parts from its own stream only.
    parts = nil
    err = svc.Send(ctx, conv, gw, wireModel, captureEvent)
    added = conv.Messages()[before:]
    canceled = err != nil && (ctx.Err() != nil || apperror.As(err).StatusCode() == 408)
    if err == nil || canceled {
        break
    }
    status := apperror.As(err).StatusCode()
    transient := status == 429 || status >= 500
    if !transient || attempt == len(sendRetryBackoff) ||
        len(added) > 0 || len(parts) > 0 {
        break
    }
```

`parts` is the streamed capture (delta/reasoning/tool-call parts,
`turn.go:173-196`); it is reset per attempt, so the condition "anything reached
the client" is literally `len(parts) > 0`.

Probe (`TestAuditRetry*`, copy-only, same package):
`go test ./application/job/ -run TestAuditRetry -v`.

```
delta-after-429:      calls=1 status=429 msg="rate limited" retry_wait_frames=[]
                      tailRole="assistant" tailFinish="interrupted" tailContent="partial answer"
stored tail:          role="assistant" finish="interrupted" content="partial answer"
reasoning-only-503:   calls=1 status=503 tailRole="system" tailFinish="" tailContent="" parts=0
no-output-429:        calls=3 (1+2 retries) status=429 retry_wait_frames=[retry_wait retry_wait]
```

(`delta-after-429` emits one delta then returns a 429 carrying
`RetryAfter() = 30s`; `no-output-429` fails with the same 429 but emits
nothing — the control proves the loop does retry, and that a 30 s provider wait
is honoured twice, 60 s of turn time.)

Tests that pin today's behaviour:

- `application/job/retry_test.go:144` `TestRunTurnNoRetryAfterEmittedDelta` —
  "emitted output blocks retry", asserts `calls == 1`.
- `application/job/retry_test.go:173` `TestRunTurnKeepsStreamedTextOnFailure` —
  the delta becomes an `interrupted` assistant message, in memory and on disk.
- `application/job/retry_test.go:227` `TestRunTurn413AfterEmittedDeltaPropagates`
  — the 413 compact-retry keeps the same stricter gate.
- `TestRunTurnRetriesTransientUpstreamFailure` / `...RetryExhaustion` /
  `...HonorsRetryAfter` — the retry loop itself.

### Root cause

The gate answers "would a second call duplicate what the client already saw?"
The answer today is yes, because nothing tells the client to drop the abandoned
attempt: `turn.phase {phase:"retry_wait"}` (`turn.go:245`,
`delivery/websocket/conversation/event.go:120-129`) carries only the pause, and
`docs/api/websocket.md:489-494` says nothing about what to do with the output
already on screen. So the transport's missing reset rule forced a conservative
server policy. The failed attempt's output is then persisted as an interrupted
reply (`turn.go:290-305`, `interruptedReply` at `turn.go:572`), which makes the
abandoned tail history and leaves `turn.resume` as the only recovery.

### Owner layer

`application/job` owns the retry policy (sequence, when to persist, which
statuses are transient) — not domain: `infrastructure/domain/AGENTS.md` keeps
"ordering, retries, persistence, and event delivery" in application. The client
reset rule is the delivery contract (`docs/api/websocket.md`) plus FE. The
per-attempt usage accounting question touches `infrastructure/domain/valueobject`
(`Usage.Add`) only if attempts, not steps, become the accounting unit.

### Proposed fix

Per ADR-0003 §1-§3 (no patch in round 0):

1. Gate on persistence: retry iff transient ∧ not canceled ∧ `len(added) == 0`
   ∧ attempt budget ∧ (`Retry-After` absent or ≤ `maxRateLimitWait`); drop
   `len(parts) > 0` from the gate. Keep `parts = nil` per attempt.
2. Move the interrupted-reply write to *after* the loop decides not to retry
   (it already is: `turn.go:290`, inside `if err != nil && !canceled`) — with the
   new gate, an attempt that is retried never reaches it, so at most one
   assistant line survives.
3. Document (websocket.md, T-06 text) that `turn.phase {phase:"retry_wait"}`
   means "the client drops the trailing unpersisted run of the current step";
   the boundary is the last `turn.step_finished` / settled tool call. That frame
   is emitted before persistence, which is only safe because a failed persist
   ends the turn (`turn.go:318-333`) — the docs must say so.
4. Keep ADR-0003 §6: the 413 compact-and-retry still requires nothing streamed
   *and* nothing appended.

Blast radius (what a retry after a delta would move):

- **Client**: must reset on `retry_wait` — text, reasoning and tool rows of the
  current step without a recorded result; must not drop history, the user line,
  folded steers, or finished-step usage. FE owns T-09; this is the frame that
  enables it. Without it, the two attempts splice (already filed as T-09).
- **Journal / late watch**: seq keeps increasing across attempts, so a client
  that watches from `-1` mid-retry replays both attempts' deltas. The reset rule
  is a function of `retry_wait` position, so a replay is consistent (the
  abandoned tail precedes a `retry_wait` frame). No journal format change.
- **Store**: unchanged — the failed attempt persists nothing; with the new gate
  the `interrupted` line appears only when retries are exhausted, so T-02's fix
  *reduces* what reaches history.
- **Usage**: a failed attempt appends no message, so `conv.AddUsage` records
  nothing for it (correct for the stored reply, but the provider's spent tokens
  are invisible — see T-03 row "usage"). If a settled step is the reset
  boundary, no accounting change is needed; attempts must not become the unit.
- **`turn.failed` on exhaustion**: carries `status`/`code`/`message` +
  `userMessagePersisted`; it carries **no** `retryAfterMs`, so after the final
  429 the user learns nothing about when to retry (T-03 row "Retry-After").
- **Tests**: `TestRunTurnNoRetryAfterEmittedDelta` and
  `TestRunTurn413AfterEmittedDeltaPropagates` encode the old rule and must be
  rewritten in round 1 (the first becomes "retries after a delta, single
  persisted line"; the second keeps its stricter gate). Add: retry after a
  delta succeeds and leaves one assistant message; retry after a delta exhausts
  and leaves exactly one interrupted message; `retry_wait` is journaled before
  the second attempt; no duplicate user line.

### Open question

- Ratification of ADR-0003 §1-§3 (PA) before round 1; the gate change alone is
  not safe without T-09's reset.
- Should `turn.failed` carry the provider-asked wait (a `retryAfterMs` beside
  `status`/`code`) so a client can disable Retry until then? ADR-0003 §5 lists
  status/code/message/resync/receipts and does not mention it. Today a 429 with
  `Retry-After: 120` produces `turn.failed {status:429, code:"upstream_error",
  message:"rate limited"}` and the 120 s are lost (`turn.go:234-245`,
  `frames.go:259-266`).

---

## T-03 — Provider stream robustness: truncation, retry-after, usage

Verdict: CONFIRMED (family of defects; the table is the deliverable)
Severity: user-visible for 1-8, data for 9-10 (usage under-count, silently
dropped tool call)

"User sees" means: frames already streamed stay on screen; the job's error path
either retries (nothing streamed) or persists the shown text as an
`interrupted` assistant message and returns; the hub then sends one
`turn.failed` with `status`, `code` (`delivery/transport/transport.go:55-85`) and
the display-safe `message`, plus `conversation.activity`. In-stream error
events never cross (`event.go:131-133` — `TurnEventError` is ignored on purpose).

### Evidence — case × wire × observed

Probe files (copy-only): `completion/audit_stream_test.go`,
`messages/audit_stream_test.go`, `responses/audit_stream_test.go`; each builds
the adapter through the package's own `newService(t, true, handler)` and prints
`status`, `public message`, `RetryAfter()`, whether a message was appended,
`conv.Usage()`, and the emitted event kinds.

| # | Case | `completion` | `messages` | `responses` | User impact |
| --- | --- | --- | --- | --- | --- |
| 1 | EOF mid-stream after deltas, no terminal marker | 502 "upstream stream truncated", no commit, `ErrorEvent` (`stream.go:84-104`) | same (`stream.go:89-101`) | same (`stream.go:63-74`) | shown text survives as `interrupted`; toast says the stream was truncated |
| 2 | EOF with nothing streamed | same 502 | same 502 | same 502 | job retries (transient) up to 2×, then `turn.failed` |
| 3 | Terminal marker missing but verdict present (`finish_reason` / `stop_reason` / no `response.completed`) | **commits** (`stream.go:72-83`, probe `finish_reason_no_done` → answer "done", no error; pinned by `TestStreamResetAfterFinishReasonCommits`) | **502, discards the answer** (`stream.go:95`; probe `stop_reason_without_message_stop` → `err=true status=502`, tail is the user line; only the no-verdict case is pinned, `TestStreamTruncated`) | 502 unless `response.completed`/`incomplete` arrived (`stream.go:132-137`) | the same provider cut yields an answer on one wire and an error on another |
| 4 | `error` event inside the stream | `chunk.Error` → `statusHint` (429/401/413, else 502) + `Retry-After` header (`stream.go:115-121`); probe → 429, `retryAfter=2m`, msg "slow down" | `case "error"` when `se.Error != nil` → `streamStatusHint` (429/401/403/529/504/400/502) + `Retry-After` (`stream.go:145-152`); probe → 429, `2m0s` | `case "error"` → `streamStatusHint` and **no `Retry-After`** (`stream.go:144-149`); probe with header `Retry-After: 120` → status 429, **`retryAfter=0s`**, message "provider rejected the request (HTTP 429)" | responses loses both the provider's wording and the wait |
| 5 | `error` event without the nested `error` object (`{"type":"error","message":"…"}`) | n/a (envelope is `error`, so it is case 6) | **dropped**: the case body is `if se.Error != nil { … }` with no else, so the stream continues to EOF → 502 "truncated" (`stream.go:145-152`); probe confirms | not applicable — top-level fields are read (`stream.go:144-149`) | Anthropic-style proxies that flatten the envelope lose the reason entirely |
| 6 | HTTP 200 whose body is a JSON error envelope (no SSE frames) | non-stream path handles it (`completion.go:143-146`); **stream path does not** → scanner sees no `data:` line → EOF → 502 truncated; probe confirms | non-stream handles it (`messages.go:150-153`); stream path → 502 truncated; probe confirms | non-stream handles only `status:"failed"` (`responses.go:137-140`); a bare `{"error":…}` → 502 truncated; probe confirms | a gateway quota/overload error becomes "upstream stream truncated" (and is retried as transient) |
| 7 | Non-2xx response body | `wire.ReadBody` (32 MiB cap, idle deadline) then `statusError` + `Retry-After` (`completion.go:110-116`); read error discarded (`raw, _ :=`) | same (`messages.go:116-122`) | same (`responses.go:108-114`) | status/message/code are right; a body that idles out degrades to "provider rejected the request (HTTP N)" by design |
| 8 | `Retry-After` beyond a sane wait | carried as `AppError.RetryDelay` (`wire/error.go:41-63`) | same | same | job aborts the retry at > 60 s (`turn.go:237-239`) with **no** `retry_wait` frame, and the terminal carries no wait (see T-02) |
| 9 | Usage accounting | last `usage` chunk wins (`stream.go:123-125`); a later **partial** usage object zeroes earlier counters — probe: chunk1 `{prompt:100, completion:5}`, chunk2 `{completion:5}` → `inputTokens=0 outputTokens=5` | `usage = se.Usage` on `message_delta` replaces `message_start`'s (`stream.go:121-137`); probe with `message_delta {"usage":{"output_tokens":15}}` → `inputTokens=0 outputTokens=15` (25 input tokens lost) | usage only from the terminal (`stream.go:290-296`); a canceled stream commits through `commitCanceled` with a `nil` usage argument (`stream.go:56`), so its tokens are never recorded | context/usage indicators under-report; `Usage.Add` (`valueobject`) never sees the missing tokens |
| 10 | Truncated tool call + terminal marker | `completeToolCalls(..., true)` drops unclosed argument JSON (`stream.go:211-232`); no text → 502 "upstream returned an empty answer" (probe), with text → commits the text, **call silently dropped** | `replayableBlocks` drops incomplete `tool_use` (`stream.go:185-205`) and `commitStream` then commits the rest; probe `text_plus_truncated_tool_use_and_stop` → `err=false`, content "let me check", finish stop, **no call, no error** | the terminal output carries the truncated `arguments` verbatim → commits a call with invalid JSON; probe → `finish="tool_calls"`, a `tool_call` event, args `{"command":` | completion/messages: the model's planned action vanishes and the turn "completes"; responses: the tool fails later with an input error |
| 11 | Idle past `s.idle` | commits when `finish`/`[DONE]` already seen, else 502 "upstream stream idle" (`stream.go:72-83`) | same with `message_stop` (`stream.go:78-88`) | always 502 "upstream stream idle" (`stream.go:62`) because the terminal returns early | a stalled stream after a verdict still commits on two wires |
| 12 | Provider status outside the catalog (500, 529) | `statusError` keeps the raw status → 500 (`completion.go:110-116`) | 529 for `overloaded_error` (`messages/decode.go:148-167`) | `server_error`/`internal_error` → 502 | `delivery/transport/transport.go:81-83` maps any `>= 500` not in {502,503,504} to code **`internal`** — a provider 500/529 is reported as an engine fault, because `IsUpstream` is consulted only for 429 |

### Root cause

Each wire grew its own SSE state machine (reasonable — the payload grammars
differ) but also its own answers to four *wire-independent* questions: what
counts as terminal evidence, whether an in-stream error carries the HTTP
`Retry-After`, whether usage objects are cumulative-complete, and what a
truncated tool call means. Nothing in `infrastructure/service/core/text` states
those answers once, so the three adapters drift. Two of the drifts are outright
data loss (row 3 on messages; row 10 on completion/messages).

### Owner layer

- Per-wire decode: `infrastructure/service/core/text/{completion,messages,responses}`
  (protocol translation — `infrastructure/service/AGENTS.md` "Reject malformed
  provider data at the adapter; map failures to errors callers can handle").
- The shared rules that must hold for every provider (terminal evidence,
  in-stream error shape, usage merge, truncated-call policy): **domain or
  application**, enforced by all three adapters — `infrastructure/service/AGENTS.md`
  "When behavior must hold for every provider, define it in domain or
  application and make all adapters follow that contract". A shared helper in
  `infrastructure/service/wire` is the natural carrier for the mechanical parts
  (`SSE`/error envelope/usage merge), while the *decision* ("a settled commit
  needs a verdict; partial calls are dropped") belongs beside
  `coretext.EmptyAnswerError` (`infrastructure/service/core/text/reply.go`),
  which is already the one place a settled commit is policed.
- Status→code mapping: `infrastructure/delivery/transport` (delivery contract).

### Proposed fix (round 1, per finding)

1. **One terminal-evidence rule per family, stated.** Completion already treats
   `finish_reason` as sufficient; messages should do the same for `stop_reason`
   (the `message_stop` frame then becomes confirmation, not a requirement).
   Test: replay the two probes and assert a commit.
2. **In-stream error parity.** `responses` `error` must carry
   `wire.WithRetryAfter(err, wire.RetryAfter(resp.Header.Get("Retry-After")))`
   and use the event's own `message`/`code` (not `statusError`'s nested
   envelope parse) so the wording and wait survive; `messages` must treat an
   `error` event with no `error` object as an error (status 502, message from
   the event) instead of ignoring it.
3. **200-with-error-envelope in stream mode.** Either detect a non-SSE JSON
   body up front (the response's `Content-Type` or a first-block probe) and
   route it through the unary error path, or reject with the provider's message
   rather than "truncated". Completion and messages already do this for unary —
   reuse that code path.
4. **Usage merge, not replace.** `UsageBody` needs presence (`*int` or a
   second decode target) so a later cumulative object merges over the earlier
   counters; `messages` must keep `message_start`'s input/cache counters when
   `message_delta` omits them; `completion` must not zero `prompt_tokens` when a
   later chunk carries a partial object. A cancelled `responses` stream should
   commit the usage it last saw instead of `nil`.
5. **Truncated tool call is an error, not a silent drop.** If the terminal
   evidence is present but a call's arguments are not closed, either fail the
   step (502 "truncated tool call", retryable) or commit with an explicit
   interrupted marker the model can see. Silent drop (completion/messages)
   hides a planned action; verbatim invalid JSON (responses) turns it into a
   tool error. One rule for all three.
6. **`Retry-After` beyond the sane wait.** Keep the abort, but put the wait in
   the terminal (T-02 open question) or emit a final `retry_wait`-style phase
   with the reason before failing.
7. **Status→code for provider 500/529.** `codeFor` should consult
   `IsUpstream`: upstream 500/529 → `upstream_error` (or a new catalog member),
   never `internal`.

Blast radius: adapter-local except (4) `valueobject.UsageBody` decoding and (7)
`delivery/transport`; no wire-format change, no storage change. Row 1 changes
when a message is committed (messages), which the job's persist path sees as a
normal success; row 5 turns a silent success into a retryable failure (a
behaviour change FE should know). Each row gets a fixture in its own package;
the cross-wire rules (1, 5) get one table test per adapter so drift is visible.

### Open question

- Row 3 (messages: `stop_reason` without `message_stop`) needs a decision: is
  `message_stop` part of the wire's terminal contract (keep, and accept the data
  loss on a cut tail), or is `stop_reason` enough (commit, like completion)?
  Recommendation: commit, because the verdict is the semantic terminal and the
  other two wires already treat it that way.
- Row 5: retryable failure vs explicit interrupted marker. The first is safer
  for the user; the second preserves what the model intended. Recommendation:
  retryable 502 when nothing was committed, interrupted marker when text already
  committed.
- Row 12 needs the catalog owner: adding `upstream_error` for 5xx beyond
  502/503/504 may contradict `docs/api/websocket.md:726` ("`500` internal"),
  so the doc sentence and `codeFor` must change together.

---

## T-04 — `turn.watch` answers without attaching a live turn

Verdict: CONFIRMED
Severity: user-visible (a second tab / a reconnecting tab sits still until
reload; it never learns the turn ended)

### Evidence

The branch, `infrastructure/delivery/websocket/conversation/hub.go:604-611`:

```go
// Newer-than-latest afterSeq: nothing honest to replay, but the
// retained verdict and receipt snapshot still cross.
if after > t.seq-1 {
    p := h.resyncForLocked(t)
    h.mu.Unlock()
    c.push(resyncFrame(cmd.RequestID, convID, cmd.TurnID, p))
    return
}
```

No `t.subscribed[c] = struct{}{}` before the return — unlike the gap branch
directly below (`hub.go:612-625`, which attaches for an active turn) and the
normal replay path (`hub.go:626-634`). Nothing else attaches a socket: every
`subscribed[...]` write is in `turnWatch` (`hub.go:623,633`) or admission
(`hub.go:428`, the sender's own socket); `conversation.activity` is a broadcast
to already-attached sockets, not an attach.

Probe (`TestAuditWatchFutureAfterSeqDoesNotAttach`, copy-only):

```
watch(afterSeq=99) => type="turn.resync_required" turn="active" requestId="w1"
watcher during live turn: silent=true  frame="" seq=<nil>      (400 ms deadline)
watcher at terminal:      silent=true  frame=""                (400 ms deadline)
control watch(afterSeq=0) attached, got "turn.completed"
```

So: the client gets a truthful snapshot ("this turn is active"), then misses the
live deltas **and** the terminal frame — the exact "tab sits still until it
reloads" symptom. `turn.resync_required` tells it to re-read HTTP history, which
it can do, but nothing ever tells it the turn finished; it must poll or reload.

### Contract coverage today

`docs/api/websocket.md:297-312` promises both "Afterwards the socket stays
attached and receives live frames" (the normal path) and "On a gap in an active
turn … still attaches for live frames", but for "`afterSeq` exceeds the latest
`seq`" it only says `turn.resync_required`. So the code matches the page; the
page is the thing that is incomplete, and ADR-0003 §4 states the missing
sentence.

| `afterSeq` | turn state | branch | attaches? | test that pins it |
| --- | --- | --- | --- | --- |
| `-1` | active | replay from seq 0 | yes | `TestWatchAttachesLive` (`control_test.go:295`) |
| `-1` | terminal | replay incl. terminal | n/a | `TestWatchReplaysFinishedTurn` (`control_test.go:248`) |
| `-1`, journal truncated | active | gap → resync, then attach | **yes** | `TestWatchGapResyncThenLive` (`control_test.go:394`) |
| `-1`, journal truncated | terminal | gap → resync | no (by design) | **no test** |
| journal expired (retention) | terminal | evict → resync "unknown" | n/a | `TestWatchEvictsExpiredJournal` (`control_test.go:570`) |
| `n == last seq` | active | replay suffix, then attach | yes | `TestWatchSuffixAndResync` (`control_test.go:354`, watch `afterSeq:0`) |
| `n > last seq` | **active** | resync | **no** | `TestWatchSuffixAndResync` asserts the resync only — nothing asserts the missing attach |
| `n > last seq` | terminal | resync + stored outcome | n/a | `TestResyncCarriesOutcomeAndSnapshot` (`steer_test.go:430`), `TestOversizedTerminalStillReachesClient` (`hub_test.go:1063`) |
| `n > last seq`, turn unknown / foreign conversation | — | resync `turn:"unknown"` | n/a | `TestWatchSuffixAndResync` |
| missing or `< -1` | — | `invalid_argument` | n/a | `hub_test.go:387-388` |

`afterSeq = -1` and the behind-window case are well covered; the terminal case
is covered for the future-afterSeq branch; the active-turn future-afterSeq case
is the hole.

### Root cause

`turnWatch` has three exits that answer "you cannot replay from there" and only
two of them ask "…but are you still live?" The branch was written to deliver the
retained verdict to a late observer (the comment says so) and treated
"newer than the journal" as a finished-turn situation, which is true only when
`t.terminal`. The condition tests the seq, not the liveness.

### Owner layer

`infrastructure/delivery/websocket/conversation` — transport turn state (journal,
subscriber set, resync replies). The contract sentence belongs in
`docs/api/websocket.md` (SA/T-06), not in the hub comment.

### Proposed fix

ADR-0003 §4: every unsatisfiable watch on an **active** turn answers
`turn.resync_required` and then joins `t.subscribed` under the same lock (mirror
`hub.go:619-623`); a terminal turn keeps today's answer (resync + stored
outcome, no attach). The shape is two lines moved, but the value is the stated
guarantee: *if a socket received `turn.resync_required` for a turn that was
active at that moment, it will also receive that turn's terminal frame* — unless
its own socket dies or its queue overflows (the bounded-queue kill already
exists, `conn.enqueue`).

Blast radius: only `turnWatch`; no journal/seq change, no frame shape change.
One subtlety: after attaching, the socket will receive frames whose seqs start
above its claimed `afterSeq` (it skipped the gap), so the client must tolerate a
seq discontinuity — it already must for the gap branch, and `resync_required`
tells it to re-read. A socket that attaches and then overflows is closed by the
existing slow-consumer path, unchanged.

Tests: a copy of `TestAuditWatchFutureAfterSeqDoesNotAttach` asserting the live
delta and the terminal arrive after the resync; a terminal-turn variant
asserting *no* frames follow the resync (pins the asymmetry); a gap-branch
variant already exists (`TestWatchGapResyncThenLive`). Also add the missing
"terminal turn with a truncated journal attaches nothing" test while in there.

### Open question

- None for the code; ADR-0003 §4 needs ratifying and the sentence needs to land
  in `docs/api/websocket.md` §`turn.watch` (and the T-06 retry contract page)
  in the same round, so the doc stops implying that a resync reply ends the
  relationship with a live turn.

---

## Loose ends

- **Line-number drift in the ADRs.** ADR-0002 cites `write.go:169-183` (the file
  has 93 lines; the calls are `74,80,87`). ADR-0003 cites the retry gate at
  `turn.go:226-229` and `interruptedReply` at `turn.go:611`; verified numbers are
  `turn.go:225-227` and `turn.go:572`. Worth correcting before round 1 so the
  fixes cite the right lines.
- **Not verified here.** No Windows runtime (the `%VAR%` claim is a code-path
  claim: the base has no expansion at all, so on Windows the string stays
  literal; `read_media` still accepts a `C:\…` path because `filepath.IsAbs` is
  true there). No real provider was called; every wire behaviour above is an
  `httptest` fixture reproducing a documented provider shape. No live server run
  (no `nusallm serve`, no `CONFIG_PATH`), because the tool/`runTurn`/hub probes
  exercise the same functions the server calls; QA's T-14 owns the black-box kit.
- **Test tree.** All probes live under `/tmp/be-audit/base` and are not part of
  the repo; the fixtures needed to re-run them are quoted above per row. If QA
  wants them as permanent regression tests, they are ready to lift into
  `*_test.go` — none of them touches production code.
