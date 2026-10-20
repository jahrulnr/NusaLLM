# Architecture

NusaLLM is a provider-agnostic LLM conversation engine with a terminal
client on top of it (module `github.com/jahrulnr/nusallm`, Go 1.26.5).
The bet the codebase makes — stated in the [README](../README.md) — is
that the engine owns conversations, tools, and history while interfaces
come and go. The terminal REPL is the first transport, not the product,
and the layering exists to keep that promise cheap: swap the delivery
boundary or add a provider wire and nothing about what a conversation
*is* changes.

This document describes the layers, the rules that keep them honest, and
the end-to-end path of one turn. Companion documents go deeper:
[the domain model](./domain-model.md) covers aggregates, entities, and
enums; [the persistence layer](./persistence.md) covers storage formats;
[adapters](./adapters.md) covers provider wires and tools;
[the CLI guide](./cli-guide.md) covers the REPL surface; and
[extending](./extending.md) walks through adding a provider or tool.

## Layer map

```mermaid
flowchart TB
    main["cmd/nusallm — composition root"]

    subgraph delivery["infrastructure/delivery — transport boundary"]
        tui["cli/tui — bubbletea REPL"]
        adm["cli/admin — onboard · gateway · config"]
        sess["cli/session — per-terminal state"]
        httpd["http — REST reads/writes · WebUI assets"]
        wsd["websocket — multiplexed turn channel"]
    end

    subgraph app["application — use cases"]
        conv["conversation/ — per-room worker broker"]
        job["job/ — Send · Edit · Compact · Delete (runTurn)"]
        mgmt["gateway/ · setting/ · setup/ · command/"]
    end

    subgraph domain["infrastructure/domain — pure DDD, no I/O"]
        model["aggregate · entity · valueobject · enum"]
        ports["ports: repository/ · service/ · event/"]
    end

    subgraph adapters["infrastructure — adapters & mechanisms"]
        repo["repository/ — JSONL store · sqlite secrets"]
        svc["service/ — four text wire adapters · tools · slash commands · workspace"]
        mech["config · worker · prompt · error · redact · system"]
    end

    main --> tui
    main --> adm
    main --> httpd
    main --> wsd
    main -. wires .-> repo
    main -. wires .-> svc
    tui --> sess
    sess --> conv
    sess --> mgmt
    conv --> job
    job --> model
    job --> ports
    repo -. implements .-> ports
    svc -. implements .-> ports
    job --> mech
```

| Package | Role |
|---|---|
| `cmd/nusallm` | Composition root: argv mux, then wires repositories, adapters, tools, and apps in `wire.go` |
| `infrastructure/delivery/cli` | Terminal boundary — `tui/` bubbletea model, `session/` per-terminal state, `commands/` registry wiring, `admin/` operator subcommands, `term/` ANSI safety |
| `infrastructure/delivery/http`, `infrastructure/delivery/websocket` | WebUI transport — Gin REST (`nusallm serve`), embedded assets, loopback `Host`/`Origin` guards, and the multiplexed turn WebSocket |
| `application/conversation` | Broker: maps conversation ids to per-room `worker.Worker`s, owns queue order, preemption, eviction, shutdown |
| `application/job` | The use-case bodies: `Send`, `Edit`, `Compact`, `Delete`, `Rename`, and the shared `runTurn` agent loop |
| `application/gateway` | Gateway management: upserts, vault key-reference writes, sanitized `GatewayInfo` projections |
| `application/command` | `SlashSession` port + `Facade` — the surface slash-command handlers see |
| `application/setting`, `application/setup` | Operator view/mutation of the JSON config file; first-run config provisioning |
| `infrastructure/domain` | Pure domain: `aggregate/`, `entity/`, `enum/`, `valueobject/`, `event/`, plus ports in `repository/` and `service/` |
| `infrastructure/repository` | Storage adapters: conversation JSONL and tool output, gateway JSON, sqlite secrets, logs, config documents, workspace files, CLI history, and directory metadata |
| `infrastructure/service` | Outbound adapters: four `core/text/*` wire adapters (completion, responses, messages, generatecontent), `tool/` implementations, `command/` slash-command handlers, workspace attachment logic, and `wire/` encoding helpers; storage goes through injected repository ports |
| `infrastructure/config`, `worker`, `prompt`, `error`, `redact`, `system` | Mechanisms: settings parsing, the serial job executor, embedded prompts, the error contract, credential scrubbing, host-OS integration (the service-manager port and its per-manager implementations — systemd today, selected by build tag + presence probe) |

## The dependency rule

Dependencies point inward. `infrastructure/domain` imports nothing
outside the standard library and itself: the ports for persistence
(`infrastructure/domain/repository`), provider calls and tools
(`infrastructure/domain/service`), and streaming (`infrastructure/domain/event`)
live inside the domain as interfaces, and every implementation lives
outside it. `TextModelService.Send` in `infrastructure/domain/service/text.go`
is the canonical example — each registered execution-environment driver owns
its `map[enum.Style]dservice.TextModelService`, never an `*http.Client` in the
application.

The application layer speaks almost only to those ports.
`application/job.Deps` (`application/job/job.go`) is the whole reach of a
use case: two repository ports, a map of environment drivers, and
normalized config. Each driver bundles its workspace repository, provider
adapters, and tool executors. Compaction's scratch dialogue is itself the
use case, so its contract — the `summary` tool the model must answer
through — lives in the layer (`application/job/summary.go`) rather than
behind a port or in a service adapter. Everything else in `application/`
is port-shaped: nothing there knows what JSONL is or what SSE stands for.

Everything concrete meets in `cmd/nusallm`. `run` and `serve` load
config and open the sqlite secret store through `admin.LoadBoot`;
`compose` (`wire.go`) constructs the local filesystem repositories, builds the
four `enum.Style` text adapters (completion, responses, messages,
generatecontent), constructs the agent tools, and registers them as the
`local` environment driver for `appconversation.New`. Each room persists its
environment identity; Send/Edit/Compact select the corresponding bundle for
that room. An unavailable driver fails explicitly instead of falling back.
The result is surfaced through either `tui.NewREPL` or the
`serve` HTTP/WebSocket stack. `main.go`'s own comment sets the tone:
for admin subcommands "this file is the mux, not the handler" — routing
to `infrastructure/delivery/cli/admin` functions that return exit codes,
keeping even argv dispatch a delivery concern.

Storage access is injected by capability: file tools, CLI attachment handling,
and provider-time media resolution use `repository.Workspace`; terminal spill
output uses a conversation-owned `repository.ToolOutput`; settings and setup
use `config.Store`; REPL history and directory browsing use their own stores.
The process runner and HTTP/WebSocket transports remain separate from storage.
`ExecTool` receives a `terminal.Runner` alongside its output repository.
Registering a new environment means composing its workspace repository,
runner-backed tools, and media-aware provider adapters as one bundle; tool
and provider code need no per-backend branches. The current filesystem picker
and CLI attachment session remain local-only delivery features until a remote
driver and its browsing/attachment UX are implemented.

A consequence worth internalizing: `application/conversation` could host
a CLI, an HTTP handler, or a test harness with identical results — the
http and websocket packages are live transports on the same use cases,
not a parallel product.

## Who owns a rule

The repository's `AGENTS.md` files do something unusual: they assign every
*decision* an owner before assigning code a home. The root file states it
plainly — "the package's location does not make a new rule appropriate
there" — and the per-layer files refine it:

- **Domain** (`infrastructure/domain/AGENTS.md`) owns every invariant that
  should hold for any caller and any provider: whether a message may be
  edited (`Conversation.RewriteFrom`), whether a tool may be offered to the
  model (`ToolSchema.CallableByAgent`), how token accounting adds up
  (`Usage.Add`). It is "thin and pure" — business rules and normalized
  state, no I/O, no wire formats, no provider vocabulary in field names.
- **Application** (`application/AGENTS.md`) owns sequencing and policy:
  queue order and preemption live in `conversation/`; route selection,
  when to persist, when to compact, tool execution, and event emission
  live in `job/`; gateway lifecycle and sanitized projections live in
  `gateway/`. It *enforces* domain decisions but must never reimplement
  them — the file warns explicitly that "existing code is not proof of
  correct ownership", citing `gateway/buildModel`'s negative-parameter
  check as input validation that would belong on `entity.Model` if it
  ever became a model-wide rule.
- **Repositories** (`infrastructure/repository/AGENTS.md`) own storage
  mechanics — record shapes, hydration, atomic writes, segment surgery —
  and may re-guard storage constraints (an id about to become a path
  segment is revalidated), but never restate a business rule. Shared
  decisions live in domain: `rewriteCut` applies the editability rule by
  calling `entity.EditableMessage` on the storage tuple — the same
  function `Message.Editable` delegates to — so the check has one owner
  even though the repository runs it pre-hydration.
- **Services** (`infrastructure/service/AGENTS.md`) own protocol
  translation and side effects: one request/response leg on the wire,
  one tool execution, one slash command. Route selection, retries,
  conversation mutation, and tool *permission* are all explicitly owned
  elsewhere.
- **Mechanisms** (`infrastructure/AGENTS.md`) — `config`, `worker`,
  `prompt`, `error`, `redact` — implement contracts and nothing else.
  `prompt/` loading text must not silently redefine a domain invariant;
  `worker/` schedules interruptible jobs "without knowledge of
  conversations"; `config/` defaults are configuration, "not domain
  meaning".

The practical upshot for anyone editing the tree: when a change spans
layers, trace from use case through port to adapter, put each decision at
its owner, and update the callers. Do not mirror the check in the outer
layer because that is where the stack trace happened.

## Anatomy of a turn

Everything below is the real path, traced through the code. The cast:
`infrastructure/delivery/cli/tui/model.go` (the Elm-style update loop),
`infrastructure/delivery/cli/session/session.go` (terminal session
state), `application/conversation/send.go` (the broker entry),
`application/job/send.go` and `turn.go` (the use case),
`infrastructure/domain/service/text.go` (the port), and
`infrastructure/service/core/text/completion/` (the adapter).

### 1. Delivery: input becomes a submission

In `replModel.submit` (`tui/model.go`), an Enter keypress trims the line
and pushes it onto `m.queued`. A turn in progress defers the line
("queued — runs after this turn"); `/cancel` is the exception, dispatched
immediately because it is an interrupt, not work. `drain` pops the queue
and `dispatch` classifies it: every `/name` line resolves through the
slash registry — worker-bound commands (`/edit`, `/compact`) satisfy
`command.JobCommand`, so `Registry.Job` validates and binds the closure
and `beginJob` runs it as a turn — and plain text becomes `beginTurn`.

`beginTurn` resolves routing *on the update loop* via
`Session.ResolveRoute` — explicit `/route` + `/model` picks, then the
conversation's stored binding, then the gateway app's auto-selection
(`GatewayApp.ResolveSelection`: the sole usable gateway, then its sole
listed model). The turn goroutine receives the resolved
pair as arguments so nothing races the status line's reads. It then
launches a `tea.Cmd` calling `Session.Send` with a `turnPump` handler
that funnels `TurnEvent`s back into the loop as `turnEventMsg`s.

`Session.Send` (`cli/session/session.go`) keeps only the
transport-specific UX: warn (via `Notify`, never fail) when a pending
attachment's modality exceeds `Model.AcceptsInput`. Validation —
an empty submission, a broken or missing gateway, a model outside the
catalog — is the use case's call in `ConversationApp.Send`/`job.Send`.
Then it calls `ConversationApp.Send`.

### 2. Broker: the conversation is a room with one worker

`ConversationApp.Send` (`application/conversation/send.go`) mints a
`c_<base32>` id when the caller passes none — the returned `SendResult`
always names the room so streamed events can be attributed — then posts
an `appjob.Send` to the room's worker through `post`.

`post` (`application/conversation/conversation.go`) owns the worker map:
spawn on demand, `Submit` FIFO for sends, `Preempt` for edit/delete —
preemption interrupts the running job *and* cuts ahead of the wait line,
so an edit lands on the turn it just killed while queued sends still run
against rewritten history. A full mailbox answers 429; a submit racing
teardown gets `ErrClosed` and is reposted onto the successor worker —
"a job that arrives after the room died belongs to the next room, not the
corpse." Workers are reaped when idle, and `Delete` retires one
explicitly by returning `worker.ErrTerminate`, which drains the queue to
410 replies and frees the id.

The worker itself (`infrastructure/worker/worker.go`) is deliberately
dumb: one goroutine, a bounded queue, `Execute` on its own context,
`Interrupt` as the soft stop and `Terminate` as the hard one. It knows
nothing about conversations — which is exactly why conversation policy
stays testable in the application layer.

### 3. The job: load, bind, persist the user line

`Send.Execute` (`application/job/send.go`) runs on the worker's goroutine
under a ctx that dies on caller cancel, `Interrupt`, or worker kill —
`Base.Cancellable` composes all three, and `finish` guarantees a reply so
a wedged waiter never happens.

The body reads plainly because the rules are already placed:

1. `Convs.Load` the aggregate; a 404 means create it (an FE-minted id
   becomes the room's name, a job-level 400 covers an invalid one).
2. `resolveRoute` picks route and model — job overrides win, then the
   stored binding — and resolves them to live objects: the `Gateway`
   aggregate via `Gateways.Load` (key refs dereference here, and only
   here), the `Model` from `gw.Model(modelID)`, and the adapter from
   `Services[gw.Style()]`. No adapter registered for the style is a 500
   "deployment defect, not a user error."
3. Build the user `entity.Message`, attach uploads, `conv.Append`.
4. On a fresh room, bind route/model and derive the title from the first
   line *before* the first write — a failed send still leaves a room that
   can retry with empty overrides.
5. Apply the embedded system prompt when the aggregate lacks one. This is
   deliberate: the prompt "is code, not data," shipped in the binary via
   `infrastructure/prompt` (embedded `system/conversation.md`) and never
   persisted, so a template upgrade reaches every room on its next send.
6. `AppendMessage` the user line under the `PersistenceReceipt` — the
   receipt answers "did my message commit?" even when the caller's ctx
   won the race, which is what lets the UI distinguish "your message is
   in history but the turn died" from "nothing happened." A fresh room
   whose user line never committed is rolled back via `Delete`.

### 4. The agent loop: `runTurn`

`Deps.runTurn` (`application/job/turn.go`) is shared by Send and Edit —
the one extraction the layer's own method-shape rule sanctions, since it
is reused. Each iteration:

- **Maybe compact.** `maybeCompact` compares `conv.EstimatedTokens()`
  (a chars/4 heuristic plus a flat 1000 per attachment — a budgeting
  number, never billing truth) against `model.CompactionBudgetTokens(
  CompactBudgetPct)`. The budget is computed per call so a mid-session
  model switch applies immediately. At most one compaction per turn; a
  tail that still overflows after folding is "the provider's call."
- **Pin the workspace behind editable input.** One directory rules a
  turn — `TurnWorkspace` resolves the room's bound workspace, else the
  driver's `FallbackRoot()` — and `runTurn` binds it into
  `dservice.WithWorkspaceDir` for file tools and the `exec` cwd. When
  the latest history message is editable (the submitted line or a folded
  steer), `runTurn` appends a workspace context record before the call:
  a system user message with no text and one injection-typed `workspace`
  tool call — `{"path":…}` input, stored output citing the active path,
  `(unset)` when neither source supplies one. It persists like any
  message; adapters replay it as untrusted evidence alongside tool
  results; the system prompt never carries it.
- **Call the provider.** `svc.Send(ctx, conv, gw, wireModel, onEvent)` —
  one attempt. Before the loop, `conv.SetTools(driver.ToolSchemas())` reapplies
  the live tool catalog every turn because a stored conversation can
  predate the catalog; without it "the model invents names it was never
  offered."
- **413 means believe the provider.** If the estimate said fine but the
  provider returned `StatusPayloadTooLarge` and no compaction ran yet,
  `runTurn` compacts once and retries — the one place the error
  contract's status drives a behavior today.
- **Verify the port contract.** The port promises one assistant append
  per call; `runTurn` diffs `conv.Messages()[before:]` rather than
  trusting `conv.Last()`, so a misbehaving adapter cannot silently
  duplicate or drop.
- **Persist immediately.** Each appended message goes through
  `AppendMessage`, then `Save` flushes meta (bindings, usage). A crash
  mid-loop leaves messages and accounting consistent — the paid-for
  reply survives even if the caller already walked away.
- **Execute pending calls.** `pendingCalls` selects `TooltypeOrigin`
  calls with no output on the last message. For each, `execTool` checks
  the schema's `CallableByAgent` — the permission decision lives on the
  `ToolSchema` value object and fails closed — then runs
  `tool.Execute(ctx, call, emit)`. Executor failures become the tool's
  output ("the model deserves honest results"); only cancellation aborts
  the turn. `msg.SetToolOutput` records raw text and fixed latency;
  `UpdateToolCallOutput` persists a patch
  record keyed by call id — resolvable globally, so a call sealed into an
  archive segment before its output arrived still patches correctly.
- **Loop or stop.** No pending calls ends the turn. Hitting
  `MaxTurnStep` (default 512) returns a 500 and *keeps* the unanswered
  calls in history — "the model did ask," and a retry replays them.

Back in `Send.Execute`, a successful turn re-binds the resolved
route/model and `Save`s — an existing room adopts an override only when
the turn under it succeeded, so a failed retry leaves the old binding
intact.

### 5. The adapter: one wire leg

`textcompletion.Service.Send`
(`infrastructure/service/core/text/completion/completion.go`) is a
stateless translator — endpoint, key, and stream flag come off the `gw`
argument every call, so a re-`Load`ed gateway (rotated key, moved
endpoint) takes effect without touching the service.

`encodeRequest` maps domain state onto the OpenAI-shaped body: system
prompt to a system message, messages through `encodeMessage` (model-origin
calls become `tool_calls` plus `tool` role messages; injection calls are
folded into text — they must not pose as provider calls; output media ride
a synthetic user message after the tool block because the tool role is
text-only), only `CallableByAgent` schemas reach the wire, pointer-based
generation params omit themselves when unset, and `optionalContent` keys
namespaced `completion.*` land verbatim as extra request fields.
Attachments — `file://`, `https://`, `data:` — are materialized here by
`infrastructure/service/wire` helpers: local files reopen under the
workspace root, per-modality byte caps apply, and a recorded `sha256` pin
must still match the resolved bytes or the part degrades to a text
placeholder.

The POST goes to `{endpoint}/chat/completions` with the resolved key.
In stream mode (`gw.StreamMode()`), `streamResponse` consumes SSE frames
with a per-chunk idle deadline — reasoning models think for minutes, so
only first-byte and between-chunk deadlines exist, never a total timeout.
Deltas and reasoning chunks become `TurnEvent`s as they arrive;
`tool_calls` fragments merge by index (capped — the index is
provider-controlled); the terminal `finish_reason`/`[DONE]` is the only
commit point, so EOF without one is a 502 "upstream stream truncated,"
not half a message. `commitStream` appends exactly one assistant message
and the usage onto `conv`, then emits tool-call and finished events.
Non-stream mode emits the synthetic delta+finished pair the port promises
so both modes consume uniformly.

Every failure is `apperror`: upstream statuses pass through
`wire.StatusError` with a frontend-safe `Message` and the raw body in
`Cause`; an unreachable host is 502; a dead stream is 502; ctx death is
always 408 — "a cancel landing mid-body-read" can surface as an upstream
error inside the adapter, so `runTurn` re-normalizes on `ctx.Err()`
before the status can misroute.

### 6. Events flow back; history lands

Events travel `adapter → onEvent → turnPump.Handle → tea.Program.Send →
turnEventMsg → applyEvent`, which folds them into the live region: deltas
accumulate markdown, reasoning text stays in arrival order in the
transcript, tool calls and output render as activity, and `Finished`
seals the block with a usage line. `finishTurn`
flushes the live block to scrollback and drains the input queue.

The product exposes reasoning and tool activity received from providers as
transcript data. The system prompt does not ask the model to suppress that
data. The example about hiding private chain-of-thought in
`infrastructure/prompt/system/conversation.md` is an instruction for the
assistant implementing this product, not a restriction on the product's
event stream.

On disk the whole time: `data/conversations/<id>/` holds `meta.json`,
`active.jsonl` (the live tail), and numbered sealed segments. Messages
and `tool_output` patch records share one append-only line stream; writes
go through tmp+fsync+rename; a truncated final line on read is treated as
a crash signature and dropped. `Compact` seals a prefix into the next
numbered segment; `RewriteFrom` truncates `active.jsonl` or repromotes a
sealed era when the edit target predates it. Details live in
[persistence.md](./persistence.md).

## Decisions worth their rationale

**Aggregates reference by id, never by object.** `Conversation` stores
`route` and `modelID` strings; resolving them to a `Gateway` happens
through the gateway repository at turn time (`aggregate/conversation.go`).
Two wins: conversation records carry no credentials — you can read or
ship a history file without leaking an apiKey — and a gateway edit
propagates to every room that names it. The mirror-image rule is that
`Gateway` owns style, endpoint, key, and catalog (`aggregate/gateway.go`)
because "a gateway may translate paradigms internally" — OpenRouter
accepts completion-style requests and routes them to messages-style
backends — so the same model id is legitimately spoken through different
styles depending on the route. Style on `Gateway`, not `Model`, is what
makes that true.

**Secrets are references.** Persisted apiKeys are `"env:NAME"` or
`"secret:<key>"` (`valueobject/keyref.go`); `repogateway.Load` is the
single point of dereference. Literal keys are vaulted under the canonical
name `gateway/<id>/api_key` by `GatewayApp.Upsert`, which sequences vault
write → record save → stale-ref cleanup so a dangling ref can't brick a
gateway. Reads answer `GatewayInfo` — `KeyRef`, `KeySet`, `Broken`, never
material — which is why `toInfo` reads through `Inspect`, the
hydration path that deliberately skips resolving the key. An unset env
var fails the load rather than resolving to `""`, because a silent empty
key becomes a misleading upstream 401.

**One error contract.** `infrastructure/error` defines `IAppError`:
`StatusCode()` for routing, `Message` for the frontend, `Cause` for
internal diagnostics, plus the capture-site `Caller`. The taxonomy is the
routing table — 413 compact, 429 fallback, 401/403 dead credential —
and adapters uphold it mechanically (`statusHint` even derives a status
from mid-stream error envelopes, which carry no HTTP status). Honesty
demands noting what consumes it *today*: the turn loop acts on 413
(compact-and-retry once, `turn.go`), the broker uses 429 for a full
mailbox and 410 for a dead room, and everything else surfaces its public
message. The contract is built for a multi-route application that can
mark credentials dead or fall over to another gateway; the current engine
binds one route per turn, so 429s and 401s reach the operator rather than
a fallback path that does not yet exist.

**Tool output is untrusted evidence.** `ToolCall.SetOutput` retains raw text
and fixes latency. Provider getters apply the domain's context budget,
neutralize embedded delimiter strings, and wrap the chosen view in an
`<untrusted_tool>` envelope (`entity/toolcall.go`). Hydration restores raw
text and latency; the envelope is derived at provider replay, not stored.

**Unset is not zero.** Generation params are pointers; `enum.Effort`
carries an `IsUnset()` sentinel. "Absent" means "don't send," so provider
defaults apply per model — a codebase-level acknowledgment that parameter
support differs per provider and inventing a zero would send a wrong one.

**Fail-closed vs. fail-open is deliberate.** `ToolSchema.CallableBy*`
refuses on disabled or unknown permission — a schema you can't classify
never reaches the wire. `Model.Accepts*` returns true on an empty modality
list — an undeclared catalog defers to the provider as final validator.
The asymmetry is the point: permission is our rule to enforce, capability
is theirs.

**Usage counts uncached input.** `Usage.inputTokens` excludes cache hits;
`NewUsage` takes disjoint counters while `NewUsageFromTotal` splits an
inclusive total. Mapping an inclusive total into `inputTokens`
double-counts, which is why the rule lives on the value object
(`valueobject/usage.go`) instead of in each adapter's decoder.

**A room is a single-writer cell.** The worker-per-conversation model is
what lets `Send.Execute` read-mutate-save without locks on the aggregate:
serialization is structural. The interesting consequences — preemption
semantics, the persistence receipt, 410-on-delete — all fall out of that
one choice.

**The system prompt is code.** Because it is applied per turn and never
persisted, upgrading the binary upgrades every room's prompt; because it
is embedded, a room file is fully user-owned data. Workspace context
never enters it — the turn injects the active path as a persisted
`workspace` record behind the user's message instead, so history
carries what the room actually saw.

## Composition root specifics

`main` does two things before the REPL exists. First it strips leading
argv noise — people retype the binary name or a `go run` source path as
an argument, and `go run ./cmd/nusallm` substitutes a temp binary for
argv[0], so the invoked name is matched rather than assumed (a token
that exists on disk is dropped; one merely ending in `.go` is an
argument). Then it routes admin verbs —
`onboard`, `gateway`, `config` — to `delivery/cli/admin` handlers and
exits with their code. `onboard` and `gateway` use `LoadBoot(false)` for
config, the secret store, and the gateway app — and nothing
conversation-shaped. The `config` handler resolves the file path directly
with `resolveConfigPath()`, so `validate` and `unset` can inspect or repair
a file that fails strict validation, including a malformed `storage.dir`.

The REPL path (`run`) boots strict, generates a missing config once via
`application/setup.EnsureConfig`, and wires: `repogateway` +
`reposecret` (sqlite, `0600`, busy-timeout so a one-shot `gateway add`
retries behind a running REPL), `repoconversation`, `repolog` (JSONL with
line-count rotation into compressed archives; diagnostic bodies are always
omitted so conversation content is not duplicated), the `services`
map (all four `enum.Style` values — `StyleCompletion` → `textcompletion`,
`StyleResponses` → `textresponses`, `StyleMessages` → `textmessages`,
`StyleGenerateContent` → `textgeneratecontent` — sharing one
`coretext.Options`; a style with no registered adapter would 500 as
"no adapter for gateway style"), the tools
(`read_file`, `write_file`, `apply_patch`, `read_media`, `exec`,
`web_search`, `web_fetch`), then
`appconversation.New` and `tui.NewREPL`. Workspace root for tools and
attachments is the process cwd — `os.Root`-confined for file tools and
`file://` resolution; `exec` is intentionally unsandboxed, gated instead
by the `ToolPermissionAgent` check in `execTool`. The same cwd is wired
as the local driver's `FallbackRoot`, so a room with no bound workspace
runs its tools there — and the injected `workspace` context record
cites it. `serve` shares `compose` but passes the owner's home as that
root instead: under the systemd unit the process cwd is the storage
dir, not a workspace.

Config resolves as an exported `CONFIG_PATH`, then the user file at
`~/.config/nusallm/config.json` when this process is the installed
binary, then a `.env` in cwd for a source build, then `default` struct
tags. When `repl.Run` returns, the deferred teardown runs in reverse: the REPL
releases its handles first, then the conversation app shuts down (reject
posts, terminate every worker, wait for their loops), and the boot
resources — including the sqlite secret store — close last.

## Working in this codebase

Two cultural rules from the root `AGENTS.md` shape every diff. The
breaking-changes policy is absolute: the current code is the single
source of truth, so refactors break old contracts cleanly rather than
growing shims, compat paths, or versioned APIs — tests validate intended
behavior, not history. The method-boundary rule is subtler: keep one
coherent flow in its calling method even when long, and extract only for
reuse, a DI seam, a separately testable invariant, or a named phase —
never for length alone. Both rules exist to keep a turn readable as one
flow; this document is the map, but the `AGENTS.md` chain (root →
`application/` → `infrastructure/` → `domain` / `service` /
`repository`) carries the constitution.

The read-only verification loop:

```sh
make fmt-check && make test && make test-web
```
