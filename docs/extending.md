# Extending NusaLLM

This guide is for the contributor — human or agent — making a first change
to NusaLLM, a provider-agnostic LLM conversation engine written in Go
(module `github.com/jahrulnr/nusallm`). It teaches three recipes: a new
wire-style adapter, a new tool, and a new slash command. Each recipe points
at a real implementation already in the tree; the fastest way through is to
open that file while you work and mirror its shape.

The architecture docs — [./architecture.md](./architecture.md) for the layer
map and [./domain-model.md](./domain-model.md) for the conversation model —
explain what the pieces *are*. This document is about where new behavior is
allowed to *live*. When they disagree with memory or habit, trust the code:
package layout and type names evolve, and the `AGENTS.md` file in each layer
is the authority for that layer's rules.

## Where a new rule belongs

Every change starts with the same question: who owns this decision? The root
`AGENTS.md` answers it, and the answer is enforced by direction of import —
domain ports never import their implementations.

- **`infrastructure/domain`** owns provider-independent invariants: what a
  `Conversation`, `Message`, `Model`, `ToolCall`, or `ToolSchema` *means*. If
  a rule must hold for every caller — "only real user messages are editable",
  "a disabled tool is never callable" — it is a method on the domain object
  that owns the state, e.g. `Conversation.RewriteFrom` in
  `infrastructure/domain/aggregate/conversation.go` or
  `ToolSchema.CallableByAgent` in
  `infrastructure/domain/valueobject/toolschema.go`. No I/O, no wire formats,
  no provider vocabulary in field names.
- **`application`** owns sequencing: which route and model a turn uses, when
  to compact, when to persist, which events to emit, how a canceled turn is
  handled. It *calls* domain decisions; it must not re-derive them with a
  second condition. See `application/AGENTS.md`.
- **`infrastructure/repository`** owns storage: records, serialization,
  hydration, atomic writes, projections, workspace files, and spill output. It validates filesystem boundaries,
  never business meaning. See [./persistence.md](./persistence.md).
- **`infrastructure/service`** owns translation and side effects: provider
  wire protocols, repository-backed file access, tool execution, slash-command handlers.
  Provider quirks stay inside their adapter.
- **`infrastructure/config`, `infrastructure/worker`,
  `infrastructure/prompt`, `infrastructure/error`, `infrastructure/redact`,
  `cmd`** are mechanisms and composition. Defaults live in config; meaning
  never does.

When a change spans layers, split it by owner and update callers rather than
mirroring the check. If ownership is unclear, trace the behavior from use
case through port to adapter before editing — and don't park a rule in an
outer layer just because that's where the call site happens to be.

### The method-boundary rule

NusaLLM keeps coherent single-use flows inline, even when the method grows
long. Extract a helper only when the new boundary has its own reason: a
reused operation, an interface/DI seam, a separately testable invariant, or
a named phase whose isolation genuinely clarifies the flow. A helper called
once that just forwards the caller's state should stay inline — length alone
buys nothing and costs a file jump. `streamResponse` in
`infrastructure/service/core/text/completion/stream.go` is one long select
loop and is better for it; `mergeCallDelta` is extracted because it's a
self-contained, testable algorithm with its own edge cases.

## Recipe: a new wire-style adapter

`enum.Style` (`infrastructure/domain/enum/style.go`) defines four paradigms —
`completion`, `responses`, `messages`, `generatecontent` — and all four have
adapters wired in `cmd/nusallm/wire.go`. Adding a fifth is: extend the enum,
write one `TextModelService`, register it, done. The gateway admin, the
persistence layer, and the application turn need no changes —
`application/job/turn.go` resolves the adapter with
`d.Services[model.WireStyle(gw.Style())]` — the model's explicit style pin
wins, the gateway's is the default — and a style with no registered adapter
fails as a deployment defect (500), not a user error.

Every text adapter follows the same five-file skeleton — `<style>.go`
(service lifecycle), `dto.go` (wire shapes), `encode.go` (domain → wire),
`decode.go` (wire → domain), `stream.go` (the SSE read loop). Pick the
reference whose wire grammar matches yours: `completion/` for role-message
wires, `responses/` for typed item streams, `messages/` for content-block
wires with signature-bound replay, `generatecontent/` for role-alternated
part contents. The replay-template pattern — verbatim provider parts under a
`<style>.` optionalContent key with `{"refill":…}`/type placeholders for
domain-owned content — generalizes to any wire that binds signatures to
bytes; `generatecontent/encode.go`'s `modelParts` is the most complete
example (signed parts consume their domain slot even when replayed
verbatim).

1. **Create the package.** Add `infrastructure/service/core/text/<style>/`
   and a `Style<Name>` constant in `enum/style.go` (a `ParseStyle` case is
   what makes `nusallm gateway add --style` accept it). Give the package a
   doc comment like `completion.go`'s — which providers speak this wire and
   what it deliberately does not cover.

2. **Implement the port.** `dservice.TextModelService`
   (`infrastructure/domain/service/text.go`) is a single method:

   ```go
   Send(ctx context.Context, conv *aggregate.Conversation, gw *aggregate.Gateway,
       model entity.Model, onEvent event.TurnEventHandler) error
   ```

   The contract, taken from the port's own doc comment: the adapter is a
   stateless translator — endpoint, credentials, and stream preference are
   read from `gw` on every call (`gw.Endpoint()`, `gw.ApiKey()`,
   `gw.StreamMode()`), so a rotated key or moved endpoint takes effect
   immediately. `Send` mutates `conv`: append the assistant message with
   `conv.Append(msg)` and account the spend with `conv.AddUsage(usage)` — see
   `unaryResponse` in `completion.go` for the exact sequence. Route
   selection, retries, and fallback are application work; the adapter owns
   one attempt.

3. **Keep wire shapes in DTOs.** `dto.go` holds structs whose field names are
   wire names, tagged for `encoding/json`, `omitempty` throughout — an unset
   generation param must not serialize as a zero (the domain models params as
   `*T` for exactly this reason). Note `RequestBody.Extra` and its
   `MarshalJSON`: that's the escape hatch that flattens adapter-namespaced
   params into the top-level body.

4. **Map domain → wire in encode, wire → domain in decode.** Copy the
   obligations `encode.go`/`decode.go` fulfill, adapted to your wire's
   grammar:
   - Only `CallableByAgent()` tools reach the wire.
   - Attachments are stored references (`file://`, `https://`); the adapter
     materializes them at send time via `wire.ResolveURI` /
     `wire.ResolveBase64` (`infrastructure/service/wire/media.go`), and a
     modality the bound model doesn't accept degrades to a text placeholder
     through `model.AcceptsInput` — never silently dropped.
   - Tool calls the model originated decode as `entity.NewToolCall(id, name,
     args, enum.TooltypeOrigin)`; `enum.TooltypeInjection` entries are
     application-supplied hydration — on a `RoleSystem` carrier they emit
     as the completed call + result pair they record; on any other carrier
     they fold into text instead (see how `encodeMessage` separates the
     two).
   - Usage normalization goes through `valueobject.NewUsage` (disjoint
     counters) or `valueobject.NewUsageFromTotal` (inclusive total plus a
     cached subset). `decodeUsage` uses the latter because this wire's
     `prompt_tokens` is inclusive — get this wrong and input is
     double-counted. The domain rule is spelled out in
     `infrastructure/domain/AGENTS.md`.

5. **Namespace provider quirks in `optionalContent`.** Never add a
   provider-specific field to a domain entity. Adapters own string keys under
   a `<style>.` prefix: `completion.reasoning_content` on the message for
   replay, `messages.content_blocks` / `responses.output_items` /
   `generatecontent.parts` for the turn templates, `generatecontent.call_id`
   on the `ToolCall` for a provider-issued correlation id. `encode.go` reads
   `<style>.<param>` keys from `Model.OptionalContent()` into
   `RequestBody.Extra`. The domain never interprets these values. If a
   template entry is bound to the exact request prefix (signed thinking,
   encrypted reasoning), implement the optional
   `dservice.CompactionSanitizer` so compaction can strip it from both the
   summary request and the persisted tail; preserving array length lets the
   conversation aggregate and repository apply the same cut safely.
   Provider-epsilon on the wire itself — like
   the `x-opencode-session` header each adapter sets for
   `enum.OpenCode` endpoints — stays in the adapter; `enum.Provider`
   (`infrastructure/domain/enum/provider.go`) is the registry new
   per-gateway characteristics hook into.

6. **Map failures to the apperror contract.** Callers route on
   `StatusCode()`: 429 → try another route, 401/403 → dead credential,
   413 → compact rather than fall back, 502/503/504 → upstream trouble,
   other 4xx → fix the request. Use `wire.StatusError`
   (`infrastructure/service/wire/error.go`) for non-2xx responses — it
   already implements the `context_length`-as-400 → 413 remap and parks the
   raw upstream body in `Cause` where a frontend never sees it. Public
   `Message` must be safe to display. A caller-gave-up cancellation is a
   408, not a 502 — `Send` checks `ctx.Err()` to tell them apart. For
   mid-stream errors, `statusHint` in `stream.go` shows how to derive a
   status when the wire carries no HTTP code.

7. **Streaming: commit only at the terminal marker.** `stream.go` treats
   `finish_reason` / `[DONE]` as the sole commit point — EOF without one is
   a truncated stream (502), not a finished turn. It accumulates deltas,
   emits `event.NewDeltaEvent` / `NewReasoningEvent` as they arrive, bounds
   provider-controlled tool-call indexes (`maxCallIndex`), and resets an idle
   timer per chunk rather than running a fixed total deadline — reasoning
   models think for minutes, dead connections are not. `wire.ScanSSE` and
   `wire.SSEData` frame the stream for you. Whatever your wire's transport,
   preserve those invariants. If your adapter doesn't stream, the port
   requires the same observable sequence anyway: emit one delta carrying the
   full content, then finished — `unaryResponse` shows the shape. `onEvent`
   may be nil; the `emit` helper in `completion.go` nil-checks it.

8. **Log through the port, never around it.** Diagnostics go to the injected
   `drepo.Logger` (`Append(ctx, kind, entry)`) under kinds like
   `provider.request`, `provider.response`, `provider.error`,
   `provider.stream`. Entries are JSON-normalized and scrubbed by
   `infrastructure/redact` before they reach disk; logging must never break
   a send, so `logAppend` swallows the error deliberately.

9. **Register it.** In `cmd/nusallm/wire.go`, add your adapter to the
   `services` map keyed by its `enum.Style` — all four adapters share the
   same `coretext.Options` (`service/core/text/options.go`), which maps onto
   `config.ServiceConfig` timeouts and carries the `Logger`, `FileRoot`, and
   the `Referer`/`Title` attribution headers. If your adapter needs a new
   knob, it belongs in `infrastructure/config`, not as a literal in the
   adapter.

After that, `nusallm gateway add <id> --style <yourstyle>` produces a working
route end to end. Adapter behavior is verified through the port — the
existing tests exercise `Send` against `httptest` servers (see
`infrastructure/delivery/cli/commands/commands_test.go` for a minimal one),
not internal helper names.

## Recipe: a new tool

A tool is one callable capability behind `dservice.Tool`
(`infrastructure/domain/service/tool.go`): `Schema()` advertises a
`valueobject.ToolSchema`, `Execute` runs the side effect. The reference to
trace is `infrastructure/service/tool/file/read.go`; `read_media.go` adds
attachment output and `terminal/exec.go` shows a long-running, unsandboxed
tool.

1. **Create the package** under `infrastructure/service/tool/<family>/`.
   Constructor shape: `NewXTool(...) (service.Tool, error)` — it validates
   injected storage capabilities and builds the schema, failing on either.
   `NewFileReadTool(store, root)` asks `repository.Workspace.CheckRoot`
   to validate its fallback root; the local repository owns `os.OpenRoot`.

2. **Write the schema with honest permissions.**
   `NewToolSchema(name, description, parametersJSON, enabled, permission)`
   takes a JSON Schema for parameters and an `enum.ToolPermission`
   (`infrastructure/domain/enum/toolpermission.go`): `ToolPermissionAgent`
   for model-callable tools, `ToolPermissionSystem` for application-internal
   ones, `ToolPermissionGeneral` for both. The check is fail-closed —
   `CallableByAgent`/`CallableBySystem` return false for disabled or
   unrecognized permission. The application enforces it:
   `application/job/turn.go` permits a call only when a schema on the
   conversation is `CallableByAgent()`, and a denied or unregistered call
   records `error: tool not permitted` / `error: unknown tool <name>` as its
   output. Your tool never re-checks permission; it executes. The
   canonical `ToolPermissionSystem` example is `tool/workspace` — a
   schema-only record tool: the turn writes its completed injection call
   to pin the working directory, and registration in `Tools` makes the
   name resolve while keeping it off the provider's tool list.

   `tool/skill` demonstrates a dual-use executed hydration tool: the
   environment registers `skills` as `ToolPermissionGeneral`, so
   `application/job/hydration.go` invokes it before provider calls and the
   agent can also call it to refresh the catalog. The hydration result is
   recorded exactly. Its repository owns discovery across builtin, global,
   and workspace roots. A separate `read_skill` agent tool loads the selected
   source. See
   [Skill discovery](adapters.md) for layout, precedence, and diagnostics.

   The same executed hydration phase records `custom_tools` paths. This is a
   reused phase for two concrete system tools, rather than a second copy of
   execute/deduplicate/persist logic. Terminal-invoked user executables need
   only a PATH directory and, when useful, a companion skill; they do not follow
   the Go tool registration checklist. See [Custom executables and Python](adapters.md#custom-executables-and-python).

3. **Treat `call.Input()` as untrusted JSON.** Unmarshal it into a local
   struct, validate the fields (`exec.go` rejects an empty command even
   though the schema marks it required — the model wrote it, not you), and
   avoid unbounded I/O. `read.go` currently uses `call.OutputBudget()` as its
   own preflight read cap and `io.LimitReader` as a second bound; this is a
   tool-specific read policy, not a global rejection rule. Other tools may
   return over-budget raw text: `ToolCall.SetOutput` retains it and provider
   getters apply the default 32000-byte context budget without changing
   owner-visible output.

4. **Return `ToolResult{Text, Attachments}` — failures included.**
   `Execute` errors are recorded as the call's output (`"error: …"`) so the
   model sees an honest result; only `ctx.Err()` aborts the turn. `Text` is
   wrapped in the untrusted-evidence envelope by the provider getter
   (`infrastructure/domain/entity/toolcall.go`) — confirm, don't echo
   (`write.go` returns a byte count, not the content). `Attachments` carry
   *references*, never bytes: `read_media.go` emits a `file://` URI —
   under the workspace, or a host URI via `Workspace.HostURI` for the
   absolute paths it accepts — sniffs the real media type, caps by
   `wire.MaxAttachmentBytes(modality)`, and pins content with
   `att.SetSHA256` so send-time resolution detects a swapped file. Whether
   attachments reach the model is decided per-request by the bound model's
   `AcceptsInput`.

5. **Confine file access through the injected workspace repository.** File-touching
   tools receive `repository.Workspace` and a fallback root; the local
   implementation uses `os.OpenRoot` and `OpenRegular` so traversal,
   symlink escapes, and special files are rejected at the storage boundary.
   `Workspace.OpenHost` is the deliberate bypass for read tools that accept
   absolute host paths — references it produces only resolve at send time
   when a sha256 pin was recorded, so an unmediated `file://` escape stays
   rejected.
   `exec.go` is the deliberate counterexample:
   there is no root for a shell, and the doc comment says so — its
   permission bit is the entire gate. Don't build an unsandboxed tool
   without naming the trade-off.

6. **Stream previews through `emit`, not through the result.** The
   `ToolEmitter` is synchronous and may be nil; it's a live preview for the
   frontend (`event.NewToolOutputEvent` in the turn), never persisted
   history. `exec.go`'s `outputSink` forwards each `Write` as a chunk while
   bounding memory — past the budget it spills the full stream under the
   conversation directory and returns a head+tail excerpt naming where the rest went. Copy
   that approach for any tool whose output is unbounded. Tools that never
   emit still get their settled output exposed — the turn forwards the
   recorded text as one late chunk so the live card never settles blank.

7. **Register it — or inject it.** Agent-facing tools go in the `tools`
   map in `cmd/nusallm/wire.go`, keyed by `tool.Schema().Name()` in the
   existing loop. The selected `EnvironmentDriver.ToolSchemas()` catalog
   is sorted by name every turn. A tool owned by one use case's private
   dialogue instead skips the map: `infrastructure/service/tool/summary`
   is constructed at the composition root and injected through
   `Deps.SummaryTool` (the same seam as `Deps.Terminals`), so only the
	compaction job's scratch conversation ever offers it. The tool package
	still lives under `tool/` — the difference is the delivery channel,
	not the boundary.

## Authoring skills and custom executables

`skill-creator` and `tool-creator` are builtin skills with source under
`assets/skills/`. Read them with `read_skill` and use the returned skill
directory for script paths. Do not edit the application-owned builtin copy;
it is refreshed from the binary. NusaLLM discovers `name` and `description`
from `SKILL.md`; it does not interpret `agents/openai.yaml` UI or policy
fields.

Create and validate a workspace skill with:

```sh
python3 <skill-creator-directory>/scripts/init_skill.py my-skill
python3 <skill-creator-directory>/scripts/quick_validate.py .agents/skills/my-skill
```

`init_skill.py` defaults to `<cwd>/.agents/skills/`; use `--scope global` for
`~/.agents/skills/`, or `--path` for another root. It also creates the
optional `agents/openai.yaml`. Regenerate it only when a compatible host
consumes that metadata:

```sh
python3 <skill-creator-directory>/scripts/generate_openai_yaml.py .agents/skills/my-skill
```

The validator and metadata generator use the bundled pure-Python PyYAML 6.0.1
package and its license, so neither system PyYAML nor pip is required.

Create and validate a direct-PATH custom executable with:

```sh
python3 <tool-creator-directory>/scripts/init_tool.py json-echo
python3 <tool-creator-directory>/scripts/validate_tool.py json-echo
printf '{"message":"ready"}\n' | json-echo
```

The tool initializer defaults to `<cwd>/.agents/tools/bin/` and
`<cwd>/.agents/skills/`; use `--scope global`, `--tools-dir`, or `--skills-dir`
to select other roots. It creates a Python launcher, implementation and
resource directory, plus a companion skill, and refuses to overwrite any
existing target. The generated contract is one JSON value on stdin or as an
argument, one JSON result on stdout, diagnostics on stderr, and exit codes 0
(success), 2 (invalid input), or 1 (runtime failure). The helper scripts use
Python's standard library; the generated example uses the NusaLLM `python3`
launcher. Native binaries and wrappers for other installed runtimes are also
valid custom tools.

## Recipe: a new execution environment

Implement `repository.Workspace` for that environment and a
`service.CommandRunner` for its processes. Construct the file and exec tools
with those ports, then register their catalog and provider adapters together
as one `job.EnvironmentDriver` under a new `enum.Environment` key in
`cmd/nusallm/wire.go`. Note `service.TerminalSessions` is wired once at
composition and shared across environments — a remote driver wanting
`exec_session` would need its own registry keyed to that host. Provider adapters must resolve media through the
same workspace repository as the tools. The conversation stores the selected
environment and workspace path; Send, Edit, and Compact resolve the bundle
again for each room. A missing bundle returns an error instead of selecting
local. Keep conversation metadata and config storage in the application's
own repositories; these are distinct from the agent's workspace.

Each driver also reports `FallbackRoot()` — the directory a room with no
bound workspace should run in. `TurnWorkspace` prefers the room's stored
binding and falls back to that root; the resolved path is bound into
`dservice.WithWorkspaceDir` for file tools and `exec`, and the turn
injects it as a `workspace` context record behind the user's message so
the model hears where it is working. A remote driver should return its
own default root, not leak the delivery's cwd — the composition root
wires the local driver's fallback per front-end: the REPL passes its
launch directory, `serve` the owner's home (a service's launch dir is
the storage dir, not a workspace).

## Recipe: a new slash command

Slash commands live behind a hard boundary: handlers depend on
`appcommand.SlashSession` — the port in `application/command/session.go` —
never on concrete application structs. The session facade exposes
conversation/gateway use cases, workspace file access, output, and session
bindings (`Bindings`, `WorkspacePort`, `OutputPort` in
`application/command/ports.go`). Each command is a package under
`infrastructure/service/command/<name>/`; the registry in
`infrastructure/service/command/registry.go` only dispatches wired
instances.

1. **Check the port first.** Does `SlashSession` already expose what the
   command needs? If not, extend the port and its `Facade` in
   `application/command/` *first* — that is the layer's own rule
   (`application/command/AGENTS.md`) — delegating to the existing
   conversation/gateway use cases rather than reimplementing them. If the
   capability itself doesn't exist yet, it belongs to whichever layer owns
   the rule; the session port is where commands gain access to it, not where
   it's built.

2. **Write the command.** Minimal shape is `command/pwd/pwd.go`; a command
   with reads and a completer is `command/route/route.go`. Implement
   `dservice.SlashCommand` (`infrastructure/domain/service/command.go`):
   `Name()`, `Usage()`, `Description()`, and
   `Execute(ctx, command, emitter, args...)`. The REPL passes the whole
   remainder of the input line as `args[0]`; parse it yourself — plain
   `strings.TrimSpace` for simple commands, `valueobject.NewCommand(raw).SplitFirst()`
   for `id + tail` shapes. Emit output through `h.Write(emitter, format, args...)`
   so the transport can sanitize; read session state through the bindings
   (`h.ConversationID()`, `h.Route()`, `h.SetModelID`, …). Reuse domain
   decisions — `commandinternal.DisplayTitles` applies the command service's
   list-title rule; shared helpers used by several
   commands live in `infrastructure/service/command/internal/`.

3. **Optionally complete arguments.** Implement
   `dservice.SlashCommandCompleter` — `Complete(ctx, arg) []CommandCompletion`
   — and the palette picks it up automatically (`route.go` completes gateway
   ids). Rows are capped at `commandinternal.MaxRows` by the registry.

4. **Wire it.** Commands are registered in
   `infrastructure/delivery/cli/commands/wire.go` — `commands.New` builds the
   session facade via `appcommand.NewFacade` and constructs every command
   explicitly, the same spirit as tool wiring in `cmd/nusallm/wire.go`. Add yours to the
   `base` slice; `NewRegistry` indexes by `Name()` (lowercased) and `/help`
   enumerates the wired set through `svccommand.Describe`, so help text is
   free once you're registered.

5. **Mind the delivery special paths.** Input UX — parsing `/name args`,
   the palette, rendering — lives in `infrastructure/delivery/cli/tui`
   (`input.go` parses, `model.go` dispatches through `m.commands.Execute`).
   Commands whose work belongs on the conversation worker — `/edit`,
   `/compact` — implement `command.JobCommand`: `Job(args)` validates the
   line and returns a `JobFunc` closure the TUI runs as a turn
   (`beginJob`, with turn-event streaming and ctrl+c), while `Execute`
   runs the same closure inline. Validation, usage text, and the work
   itself live in the command package once — the TUI only owns the turn
   lifecycle.

## Conventions to match

- **Objects**: unexported fields; `New*(...) (T, error)` constructors that
  run `validate()`; explicit getters; hydration via setters
  (`NewToolCall` + `SetCallAt`/`SetStoredOutput`), never a "reconstitute"
  constructor. Slice and map getters share internals by design — no
  defensive copies.
- **Unset ≠ zero**: generation params are `*T`; `enum.Effort` has
  `IsUnset()`. Absent means "don't send", never a zero on the wire.
- **Fail-closed vs fail-open is deliberate**: `ToolSchema.CallableBy*`
  denies on unknown permission; `Model.Accepts*` allows on an empty modality
  list because the provider is the final validator.
- **Errors**: one contract — `apperror` (`infrastructure/error/error.go`).
  Public `Message`, internal `Cause`, HTTP `StatusCode` for routing;
  `apperror.As` normalizes foreign errors to a 500 with no leakage.
- **Diagnostics**: the `drepo.Logger` port is a write-only JSONL sink;
  every entry passes through `infrastructure/redact` before disk, and
  request/response bodies are always stripped
  (see the `Redact` hook in `cmd/nusallm/wire.go`).
- **Secrets**: persisted values are `"env:NAME"` or `"secret:<key>"`
  references resolved through the `SecretStore` port — never plaintext.
- **Language**: English for code, comments, and docs.

## Breaking changes are fine — half-measures are not

The root `AGENTS.md` is explicit: the current codebase is the single source
of truth, and there is no backward-compatibility obligation. Prefer a clean
break over compatibility layers, shims, fallbacks, or versioned paths — no
`v1`/`v2` splits, no deprecated APIs kept alive beside their replacements.
When a contract change breaks callers, update the callers and their tests to
the new contract in the same change, and delete what the change makes
obsolete. Tests assert current intended behavior, not historical behavior.

## The verify loop

```sh
make fmt-check && make test && make test-web
```

Run the whole chain — it's the bar each `AGENTS.md` sets, and it's fast in
this module. The `Makefile` mirrors it: `make build` produces
`bin/nusallm`, `make cli` builds and launches the REPL, `make serve`
launches the WebUI, and `make gateway-add` is a convenience recipe for one
provider's route. `make fmt-check` fails on unformatted files instead of
rewriting them, `make test` builds `bin/nusallm` before vet + test, and
`make test-web` runs the `node --test`
suite under `web/test/`. Media fixtures for end-to-end
attachment paths live in `test/media/` and are pinned by
`TestMediaFixtures` in `infrastructure/domain/valueobject/attachment_test.go`
— if an adapter change touches attachment serialization, that test is the
tripwire. Beyond that, test the contract at the boundary you changed —
observable port behavior, ordering, persisted state — not helper names.

---

*Sibling docs: [architecture](./architecture.md) ·
[domain model](./domain-model.md) · [adapters](./adapters.md) ·
[persistence](./persistence.md) · [CLI guide](./cli-guide.md) ·
[README](../README.md)*
