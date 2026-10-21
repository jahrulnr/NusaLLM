# Domain Model

NusaLLM's domain layer lives in `infrastructure/domain/` and holds every
provider-independent rule the product obeys: what a conversation is, what a
message may carry, which tools a model may call, and how token accounting
adds up. It is deliberately thin and pure — no I/O, no wire formats, no
provider vocabulary in field names. Everything here is plain Go state plus
the ports outer layers implement; the adapters that translate this model
onto provider wires live in `infrastructure/service/` (see
[./adapters.md](./adapters.md)), and the storage mechanics behind the
repository ports are covered in [./persistence.md](./persistence.md).

The layer's own contract is documented in
`infrastructure/domain/AGENTS.md`. This document explains the same model
for readers approaching the code for the first time, grounding each rule
in the code that enforces it.

## Package map

| Package | Contents |
|---|---|
| `aggregate/` | `Conversation` (dialogue state + route/modelID references), `Gateway` (endpoint surface) |
| `entity/` | `Message`, `ToolCall`, `Model` |
| `enum/` | Closed string sets, each with `String()` and a `Parse*()` |
| `event/` | `TurnEvent` + `TurnEventHandler` — the per-turn streaming sink for `TextModelService.Send` |
| `repository/` | Ports only — `Conversation`, `Gateway`, `SecretStore`, `Logger` |
| `service/` | Ports only — `TextModelService`, `Tool`, `SlashCommand` plumbing |
| `valueobject/` | `Usage`, `Pricing`, `ToolSchema`, `Attachment`, `ID`, plus small support types |

Every struct in the layer uses unexported fields, a `New*` constructor,
and — where validation exists — a private `validate()` the constructor
calls. There are no reconstitute constructors: repositories hydrate by
calling `New*` and then the setters explicitly marked for hydration
(`SetID`, `SetCreatedAt`, `SetCallAt`, `SetStoredOutput`, …), so stored
data passes through the same invariants as live data.

## Aggregates

### Conversation — `aggregate/conversation.go`

`Conversation` is the aggregate root for dialogue state. Its doc comment
states the design in one sentence: it holds dialogue state only, and
"routing concerns such as provider style and model are decided per request
by the service layer, so a single history can be translated to any wire and
survive model fallback."

| Field | Meaning |
|---|---|
| `id` | `c_<random>` identity, minted by `NewConversation` via `valueobject.NewID("c", id)` |
| `route` | Bound gateway id — a strict reference, never an embedded `Gateway` |
| `modelID` | Bound model id on that route — again a reference, resolvable through `Gateway.Model` |
| `systemPrompt` | Conversation-level instruction; adapters map it to each provider's channel |
| `title` | Optional display title for conversation lists |
| `messages` | The live history tail (`[]entity.Message`) |
| `tools` | Tool schemas the agent may call (`[]valueobject.ToolSchema`) |
| `usage` | Accumulated provider-reported token accounting |

Two rules matter more than the fields. First, **aggregate roots reference
each other by id only**: `route`/`modelID` are strings resolved through the
`Gateway` repository, which keeps credentials out of conversation records
and lets a gateway update (rotated key, moved endpoint) propagate to every
conversation bound to it. `SetRoute` and `SetModelID` document this
directly on the setters. Second, the aggregate only ever holds the **live
tail**. Sealed history segments produced by compaction are a persistence
concern — the `Conversation` aggregate exposes no segment state at all.

**Appending and reading.** `Append` pushes a message; `Messages()` returns
the slice. `Last()` returns a pointer into the live backing array — it
aliases internal storage and detaches silently once `Append` reallocates,
so callers must re-fetch after appending. This is deliberate: slice and
map getters share internals throughout the layer, with no defensive
copies, because the domain is an internal boundary rather than a published
API.

**Editing.** `RewriteFrom(msgID, content)` is the edit-and-regenerate
primitive: it patches the message's content in place and truncates the
history to end at it, so the caller resends the turn from that point. Only
messages the user actually wrote qualify — the check is
`Message.Editable()`, and a synthetic user-role message (a compaction
checkpoint carries `userType=system`) fails with `ErrNotUserMessage`
because rewriting it would forge user intent. Ids not present in the live
tail fail with `ErrMessageNotFound`; sealed archives are the repository's
concern, handled by the `RewriteFrom` method on the conversation port (see
[Ports](#ports)). The implementation also `clear()`s the
dropped tail so edited-away messages don't stay reachable through the
backing array for the conversation's lifetime.

**Compaction.** `Compact(summary, dropBefore)` replaces `messages[:dropBefore]`
with the summary message, leaving `[summary] + messages[dropBefore:]` as
the new live history; `dropBefore` clamps to bounds. Callers mirror the
same cut on the repository port — `application/job/compact.go` calls
`Convs.Compact` first, then `conv.Compact`. The rule that a boundary must
never split a tool-call/result pair is structural here: tool calls and
their outputs ride *inside* the message that produced them, so any
message-aligned boundary is automatically safe.

**Budgeting.** `EstimatedTokens()` approximates context size with a
chars/4 heuristic over everything adapters replay onto the wire — system
prompt, agent-callable tool schemas, message content, `optionalContent`
values, and tool call input/output — plus a flat `attachmentTokenEstimate`
(1000) per attachment, since providers bill media by tokenization rather
than characters. It is a budgeting heuristic, not billing truth; `Usage()`
holds what providers actually reported, accumulated per turn through
`AddUsage`. `NeedCompaction(budget)` compares the estimate against a
budget the *caller* derives from the bound model
(`model.CompactionBudgetTokens`), so a mid-session model switch applies
immediately; a budget of zero or less fails open because compaction is a
budgeting concern, never a hard gate.

### Gateway — `aggregate/gateway.go`

`Gateway` is the aggregate root for a configured endpoint surface.

| Field | Meaning |
|---|---|
| `id` | Configured, human-named identity — never minted (`NewGateway` rejects an empty id) |
| `name` | Optional display name |
| `style` | `enum.Style` — which wire paradigm this endpoint speaks |
| `endpoint` | Base URL |
| `apiKey` | Secret material, or a `"env:"`/`"secret:"` reference (see below) |
| `models` | Catalog of `entity.Model` entries the gateway can serve |
| `stream` | Whether streaming mode is enabled; `NewGateway` defaults it to `true` |

**Style lives on the gateway, not the model**, because one model id can be
spoken through multiple paradigms depending on the route — OpenRouter, for
example, accepts an OpenAI-style request and routes it to a messages-style
backend. Each registered execution-environment driver keeps a
`map[enum.Style]TextModelService` (`application/job/environment.go`). The
application first selects the conversation's persisted environment, then
chooses that driver's adapter by `gw.Style()` at send time. A conversation
switching gateways changes wire formats without touching history.

`Gateway.Model(id)` resolves a catalog entry — the resume path resolves
`Conversation.ModelID()` through it. `ApiKey()` returns whatever is stored:
the persisted form is a reference (`"env:NAME"` or `"secret:<key>"`), which
the repository resolves via `SecretStore` when `Load` hydrates the
aggregate — the gateway file itself stays credential-free.

Conversation list labels and prefix filtering belong to
`infrastructure/service/command/internal/list_title.go`. That command view
uses `Message.Editable()` to avoid deriving a title from a synthetic user
message.

## Entities

### Message — `entity/message.go`

A `Message` is one provider-agnostic conversation entry. Its id is minted
at construction (`m_<random>`) and permanent — edit/regenerate features
address messages by id, never by position, because compaction shifts
indexes.

| Field | Meaning |
|---|---|
| `id` | Minted identity |
| `role` | `enum.Role` — required by `validate()` |
| `content` | Text body |
| `userType` | `enum.LLMUserType` — provenance of user-role messages |
| `finishReason` | `enum.FinishReason` — normalized stop reason for assistant turns |
| `parts` | Ordered assistant response text, reasoning, and tool-call positions |
| `toolCalls` | Tool invocations this message produced (`[]ToolCall`) |
| `attachments` | Non-text content references (`[]valueobject.Attachment`) |
| `optionalContent` | Provider replay artifacts, namespaced by adapter (`map[string]string`) |
| `createdAt` | Timestamp — `time.Now()` at construction, `SetCreatedAt` on hydration |

`Editable()` is the single rule behind message editing: `role == user`
*and* `userType != system`. The same decision is exported as
`EditableMessage(role, userType)` for storage tuples that haven't been
hydrated yet — the repository's rewrite path applies it to raw records,
which is why the rule exists in both shapes rather than being
reimplemented.

`SetOptionalContent(key, value)` stores provider-specific replay artifacts
under adapter-namespaced keys — the live example is
`"completion.reasoning_content"`, written by the completion adapter at
`infrastructure/service/core/text/completion/decode.go` and read back at
encode time so reasoning state survives a reload. Test fixtures exercise
`"gemini.thought_signature"` and `"openrouter.provider_order"`. The domain
stores the strings opaquely and never interprets them; a dedicated field
for one provider's artifact is exactly what this mechanism exists to
prevent.

`SetParts` stores the provider-independent assistant response order used
by history: exact text and reasoning parts plus references to tool calls.
Provider replay continues to use `optionalContent`; the history timeline
does not replace or rewrite those adapter-owned values.

Tool results attach to the calls on the message: `AddToolCall` records an
invocation, and `SetToolOutput(id, output)`,
`SetToolOutputAttachments(id, atts)`, and `SetStoredToolOutput(id, output, latency)`
all match by the provider-issued call id. `SetStoredToolOutput` exists for
hydration and patch application — stored output is raw and its latency
must remain fixed across replay.

### ToolCall — `entity/toolcall.go`

`ToolCall` records one tool invocation: `id`, `name`, `input`, raw `output`,
completion state, fixed output latency,
`outputBudget`, `outputAttachments`, provenance `ttype`, `callAt`, and its
own `optionalContent` map (for per-call artifacts such as a thought
signature bound to one function call).

`ttype` distinguishes `TooltypeOrigin` calls — originated by the model and
executed by the application — from `TooltypeInjection` calls, which are
hydration entries the application inserts (announcements, subagent
results). The distinction is load-bearing on the wire: the completion
adapter encodes origin calls as real `tool_calls`. Injection calls on a
system-role record (the workspace pin) encode as the completed call +
result pair they record — the harness ran them on the model's behalf —
so observed evidence rides at tool-output trust. The pin's `workspace`
name resolves against a real `ToolPermissionSystem` schema registered
with the room's tool catalog (`tool/workspace`), so the call the model
observed is a known-but-not-agent-callable tool. Injection calls on any
other carrier (legacy user-role records) still fold to inline text.

`SetOutput` retains raw output even when it exceeds `outputBudget` (default
32000, tunable via `SetOutputBudget`) and fixes the call latency at completion.
`HasOutput` distinguishes a completed empty result from a pending call.
`NeedTruncate`, `OutputHead`, `OutputTail`, and `OutputWithMiddleTruncate`
provide provider-context views without changing the stored result. The
provider getter limits the body to the budget, neutralizes literal
`<untrusted_tool` / `</untrusted_tool` delimiter text, then wraps it:

```
<untrusted_tool latency="12ms">
...raw output...
</untrusted_tool>
```

Runtime output goes through `SetOutput`; `SetStoredOutput` restores raw text
and latency on hydration. `OutputWithUntrustedTag()` returns the bounded
envelope for provider replay; `Output()` returns the exact stored text for
owner-facing history and exports. For streamed terminal output, the domain's
windowed middle-truncation method works from bounded head/tail samples while
the adapter handles process I/O and the spill file. `callAt` stamps `time.Now()` at
construction and is restored by `SetCallAt` on load, which is also what
makes the latency field meaningful.

`outputAttachments` carry media a tool produced (a screenshot, a generated
image) as `Attachment` references — never bytes. Whether they reach the
model is decided per request at encode time by the bound model's
`AcceptsInput`; an unsupported modality degrades to a text placeholder,
which is why the bound model changing between turns is safe.

### Model — `entity/model.go`

`Model` is a request-scoped route descriptor: which route (gateway
surface), which model id, and which generation parameters to apply.
`NewModel(id, route)` requires only a non-empty id; `route` is a gateway
selector resolved by the service layer and may be empty for catalog
entries, which resolve through their owning `Gateway`.

**Generation parameters are pointers for a reason.** `temperature`,
`topP`, `topK`, `maxOutputTokens`, `presencePenalty`, `frequencyPenalty`,
and `seed` are all `*T` — `nil` means *unset*, and adapters must not send
unset parameters, because support differs per provider and per model and a
zero would silently override the provider's default. `SetStopSequences`
takes a slice; `effort` uses `enum.Effort`'s `IsUnset()` sentinel instead
of a pointer (`""` and `"default"` both mean omit the field). Parameters
with no common field ride in `optionalContent` under adapter-namespaced
keys.

**Catalog metadata** — `name`, `canonical`, `aliasOf`, `outputLimit`,
`supportedEfforts`, `mtype`, `pricing`, `contextWindow`,
`inputModalities`, `outputModalities` — is descriptive, hydrated from the
gateway's model list, and never sent as request parameters. A zero
`contextWindow` means *unknown* rather than *zero*: `EffectiveContextWindow`
substitutes `DefaultUnknownContextWindow` (256K) for UI budgeting, and
`ApplyUnknownContextDefaults` returns a copy with the 256K window and the
output reserve (`maxOutputTokens` or a capped declared `outputLimit`)
filled in for wire defaults while persisted records stay as-is. Catalog
import (`ModelCatalog.ListModels`) backfills these fields from provider
declarations onto entries that leave them unknown. `Family()` groups a
model to its alias target or canonical base (minus `:variant` suffix and
trailing `-YYYYMMDD` snapshot date); `Variant()` reports the colon suffix
the id carries. `SupportsEffort` gates effort selection against the
declared `supportedEfforts` list — undeclared fails open like `Accepts*`.

**Modality predicates fail open.** `AcceptsInput(mod)` /
`AcceptsOutput(mod)` return true when the catalog declared no modality list
— the provider remains the final validator, so an incomplete catalog can't
silently strip content the model would have accepted. Deliberate contrast:
`ToolSchema.CallableBy*` fails *closed*. A capability check that can leak
gets the benefit of the doubt; a permission check that can leak does not.

**Compaction math.** `CompactionBudgetTokens(budgetPct)` returns the
estimated input size that triggers proactive compaction — a percentage of
`compactionInputAllowance`, which reserves output headroom: unknown
windows assume 256K total minus the reply reserve, declared windows below
256K reserve the request `maxOutputTokens`, else the declared
`outputLimit` capped at 64K, wider declared windows count the full total.
`CompactionInputAllowance` exposes the same number for the UI's
`~estimate/total` footer.

`application/gateway/catalog.go` owns model selection helpers: `MinListedContextWindow`
(126K) hides tiny-window entries from `/model` listings while unknown
windows stay visible, `ModelListedInPicker` applies the rule, and
`ListedModels` filters a catalog in gateway order.

## Enums — `enum/`

Every enum is a closed `string` set with `String()` and a `Parse*()`
returning `(value, ok)`. Persisted values and config text pass through
`Parse*`; anything outside the set fails there rather than deep in an
adapter.

| Enum | Values | Notes |
|---|---|---|
| `Role` | `system`, `user`, `developer`, `assistant` | Message authorship |
| `Style` | `completion`, `responses`, `messages`, `generatecontent` | Wire paradigm; the gateway owns it and the app keys adapters on it |
| `Tooltype` | `injection`, `origin` | App-injected hydration call vs model-originated call |
| `LLMUserType` | `origin`, `system` | Real user input vs system payload (compaction checkpoints are `system`) |
| `ToolPermission` | `system`, `agent`, `general` | Who may invoke a `ToolSchema` |
| `FinishReason` | `stop`, `tool_calls`, `length`, `content_filter` | Normalized turn end; `length` means tool arguments may be truncated and must not be executed |
| `Effort` | `default`, `none`, `low`, `medium`, `high`, `xhigh`, `max` | `IsUnset()` = `""` or `default` → omit from the wire; `max` requires validating model support first |
| `Modality` | `text`, `image`, `audio`, `video`, `file` | Content kinds for `AcceptsInput`/`AcceptsOutput` and `Attachment.Modality()` |
| `ModelType` | `text`, `embedding`, `video`, `image`, `speech`, `transcription`, `audio`, `rerank`, `decision` | Catalog categorization matching OpenRouter's model categories |
| `TurnEventKind` | `delta`, `reasoning`, `tool_call`, `tool_output`, `finished`, `error` | Which payload a `TurnEvent` carries |

Two extras live in the same package. `Provider` enumerates known provider
hosts (`api.openai.com`, `openrouter.ai`, `api.anthropic.com`,
`generativelanguage.googleapis.com`, and four more) as the registry of
per-gateway wire characteristics. The completion adapter already keys off
it: an endpoint containing `enum.OpenCode` gets an `x-opencode-session`
header (`service/core/text/completion/completion.go`), and the same
mechanism is where OpenRouter-style behaviors hang — `HTTP-Referer` /
`X-Title` attribution headers and the `prompt_cache_key` field
(`dto.go`). `ParseProvider` (which returns `"unknown"`, not `""`, on a
miss) is there for adapters that need to branch on provider identity;
nothing calls it yet — today the constants are consumed as
endpoint-match strings, and new quirks are expected to land the same
way. And `enum.TurnEventKind` values map one-to-one onto `TurnEvent`
constructors (see [Event](#event--eventturngo)).

## Value objects — `valueobject/`

**`Usage` — `usage.go`.** Token accounting for one provider request, and
home of the layer's most carefully worded invariant: `inputTokens` is the
*uncached* input count, because providers report cache counters in two
different layouts. Adapters with disjoint counters (Anthropic's
`input_tokens` separate from `cache_read_input_tokens` /
`cache_creation_input_tokens`) call `NewUsage` directly. Adapters whose
reported input total already includes the cached subset (OpenAI's
`prompt_tokens`, Gemini's `prompt_token_count`) call `NewUsageFromTotal`,
which derives the uncached portion and rejects inputs where cache counters
exceed the total. Mapping an inclusive total straight into `inputTokens`
double-counts cached tokens in `TotalInput()` — the two constructors exist
to make that mistake a compile-time-shape decision rather than a runtime
bug. `Total()` adds output; `Add` sums reports so `Conversation.AddUsage`
can accumulate per-turn billing.

**`Pricing` — `pricing.go`.** A per-1M-token USD rate card (`inputPerM`,
`outputPerM`, `cacheReadPerM`, `cacheWritePerM`), all non-negative.
`Cost(usage)` estimates cost against the card — informational only; the
provider's invoice stays authoritative.

**`ToolSchema` — `toolschema.go`.** A tool the agent may call: `name`
(required), `description`, `parameters` (`json.RawMessage`, translated by
adapters into each provider's tool definition), `enabled`, and
`permission`. `CallableByAgent()` and `CallableBySystem()` are fail-closed:
disabled or unknown permission returns false, so a schema with corrupt or
unset permission is callable by no one.

**`Attachment` — `attachment.go`.** A non-text content reference — `uri`
and `mediaType` required (the latter must parse via `mime.ParseMediaType`),
optional `name` (providers require filenames for file inputs) and optional
lowercase-hex `sha256` pin. Only references are stored — `https://`, local
paths/`file://`, provider file ids; `data:` URIs are wire-time and adapters
materialize them at send. A sha256 pin binds the reference to the exact
bytes its producer checked: adapters verify resolved bytes at send time and
degrade on mismatch, so a swapped file fails loudly instead of silently
sending different content. `Modality()` derives the coarse modality from
the MIME top-level type, defaulting to `ModalityFile` — that value is what
`AcceptsInput` gates on.

**`ID` — `id.go`.** Identity is a single path-safe segment — ids name
files and directories downstream, so `ValidID` rejects empty, `.`, `..`,
and anything containing `/` or `\`. `NewID(prefix, "")` mints
`prefix_<128-bit-random>` via `crypto/rand.Text()`; a supplied id is
validated. Repositories re-guard the same rule at their own boundary
before joining paths.

**Key references — `keyref.go`.** `KeyRefEnv` (`"env:"`) and
`KeyRefSecret` (`"secret:"`) are the stored-secret scheme shared by gateway
records and readers: `IsKeyRef` detects the shape, `KeyRefName` splits the
ref into the name it points at, `SecretRef` builds the vault form. Any
non-ref value is literal key material — writers persist refs verbatim, and
readers may surface a ref but never the resolved value.

**Support types.** `Workspace` (`workspace.go`) is a canonical
slash-separated workspace-relative path — platform-neutral because the
workspace may live on a different OS than the host. It rejects `..`
segments, rooted paths, drive-letter and UNC prefixes, and any `\` byte
(`ErrPathTraversal`); `ParseWorkspacePath`
accepts optionally double-quoted input. `Command` (`command.go`) wraps raw
command text and `SplitFirst` separates a leading token from its tail.
The terminal formatter `infrastructure/delivery/cli/term/token.go` renders
counts like `200K`/`1.2M` for the UI.

## Ports

Domain ports are interfaces the outer layers implement. Ports never import
their implementations.

### Repository ports — `repository/`

**`repository.Conversation`** (`conversation.go`) is the conversation
store. Its ordering contract: `Save` upserts the conversation-level record
(id, route/modelID binding, title, tools, usage — not the system prompt,
which is embedded and applied per turn) and must precede `AppendMessage`,
which persists one message per turn and reports
via a `persisted` bool whether the message line committed even if a later
metadata projection failed.

Two read paths exist by consumer. `Load` rebuilds the *live* context —
bindings plus the active segment, i.e. what the model actually sees;
sealed segments stay on disk. `Info`, `List`, and `LoadAt` serve
projections and ranged history for UIs: `ConversationInfo` carries counts
(`MessageCount` over the global history vs `ActiveCount` over the tail vs
`SealedSegments`), and `LoadAt(start, end)` slices the global logical
history — sealed segments first, then the tail — with clamped indexes,
negative-from-end `start`, and `end <= 0` meaning "through the latest".

`UpdateToolCallOutput` patches a stored call's raw output, fixed latency, and media
references by call id — calls persist pending execution and their results
arrive later. Each patch is an independent appended record, so concurrent
completions of parallel tool calls are safe and a crash mid-batch leaves
the remaining calls honestly pending; the call id resolves globally, so a
call compacted into a sealed segment before its output arrived is still
patched correctly.

`Compact` seals `messages[:dropBefore]` into the next numbered archive
segment and restarts the live history as `[summary] + tail`, mirroring the
aggregate method — the caller applies both. `RewriteFrom` is the
persistence counterpart of the edit primitive: a target in the live tail
truncates it; a target in a sealed segment *repromotes* that era — that
segment plus every earlier one merge back into the live tail so the model
regains the full pre-edit context verbatim, while later segments and their
now-stale summaries are deleted. Errors follow the `apperror` contract:
404 when the id is nowhere in history, 400 when it belongs to a non-user
message. `Delete` removes the conversation.

**`repository.Gateway`** (`gateway.go`) persists the second aggregate on
its own so credential, endpoint, and catalog updates propagate to every
conversation referencing it by route. `Load` hydrates with the apiKey
*resolved* — `"env:"`/`"secret:"` refs dereference into key material.
`Inspect` and `InspectList` are the management-plane reads: `GatewaySnapshot`
carries the raw `ApiKeyRef` (literal, ref, or empty — never resolved) so an
update to other fields can carry the ref forward instead of pinning
whatever it resolved to, plus `KeyResolved` — a non-fatal probe reporting
whether that ref currently resolves — and a record that can't even decode
still appears
as a snapshot with a nil `Gateway` rather than vanishing from listings.
`GatewaySecretKey(gatewayID)` is the canonical vault key
(`"gateway/<id>/api_key"`) shared by the repository and the management
plane. `Delete` leaves conversations' route references dangling — the
application owns the fallback when resolution fails.

**`repository.SecretStore`** (`secret.go`) is the key-value port for
credential material: `Get`, `Set`, `Delete` (404 apperrors on absent keys),
`Close`.

**`repository.Logger`** (`logger.go`) is the write-only diagnostic sink:
`Append(ctx, kind, entry)` serializes one JSONL record per call, `kind`
tags it for grep (`"provider.response"`, `"send.error"`), and verbose raw
payloads belong here — entries are scrubbed by `infrastructure/redact`
before write. Rotation and retention are implementation concerns.

### Service ports — `service/`

**`TextModelService`** (`text.go`) is the outbound LLM port, and it is
exactly one attempt:

```go
Send(ctx, conv *aggregate.Conversation, gw *aggregate.Gateway,
     model entity.Model, onEvent event.TurnEventHandler) error
```

Implementations are stateless translators — endpoint and credentials are
read from `gw` per call, so a re-`Load`ed gateway (rotated key, moved
endpoint) takes effect immediately. `Send` appends the resulting assistant
message and usage onto `conv`. In stream mode `onEvent` receives deltas,
completed tool calls, and the terminal finished/error events as they
arrive; in non-stream mode the implementation emits one delta with the
full content followed by finished, so callers consume both modes
uniformly. `onEvent` may be nil. Model selection, retries, and fallback
belong to the application layer — failures return `apperror.IAppError`
and the app routes on `StatusCode()`: 413 means compact rather than fall
back, 429 means try another route, 401/403 marks a gateway dead.

**`Tool`** (`tool.go`) is one callable capability: `Schema()` advertises
the `ToolSchema` its constructor validated, `Execute(ctx, call, emit)`
runs a model-originated call and returns a `ToolResult{Text,
Attachments}`. The caller records `Text` via `ToolCall.SetOutput`; provider
adapters obtain the bounded untrusted envelope from the getter. `emit`
(`ToolEmitter`) streams live preview chunks
that are *not* the persisted result. Execute failures are recorded *as*
tool output so the model can see and react to them — only context
cancellation aborts the surrounding turn.

**Command ports** (`command.go`) back the CLI's slash commands:
`CommandService.Execute` runs a named command with a synchronous
`CommandEmitter`, `SlashCommand` adds identity/help text, and
`SlashCommandCompleter` is optional argument completion. They are domain
plumbing for the terminal client rather than provider concepts — see
[./cli-guide.md](./cli-guide.md).

### Event — `event/turn.go`

`TurnEvent` is the domain event emitted while `Send` streams a response;
exactly one payload field is populated per `Kind()`. Constructors map
one-to-one onto `TurnEventKind`: `NewDeltaEvent` (text chunk),
`NewReasoningEvent` (thinking chunk — a preview for UI status, never part
of the answer), `NewToolCallEvent` (completed call), `NewToolOutputEvent`
(live chunk from a running call, with the call id riding in `ToolCall()` so
parallel calls stay attributable), `NewFinishedEvent` (finish reason plus
usage), `NewErrorEvent`. `TurnEventHandler` is a synchronous `func` —
slow handlers apply backpressure on the read loop, and implementations
must not invoke it concurrently.

## The invariants that define the model

Each rule below is stated once, in the place that owns it, and everything
else is expected to call that decision rather than reimplement it. This is
the same list as `infrastructure/domain/AGENTS.md` — here with the code
that enforces it.

1. **No I/O in domain.** Nothing under `infrastructure/domain/` reads a
   file, opens a socket, or stats a path — the standard-library imports are
   pure computation (`crypto/rand` for id minting in `valueobject/id.go`,
   `path/filepath` for separator logic in `valueobject/workspace.go`).
   File reads, HTTP, and stat are all adapter work.

2. **Strict aggregate references.** `Conversation` holds `route`/`modelID`
   strings (`aggregate/conversation.go`), never embedded `Gateway`/`Model`
   objects; the resume path resolves them via the `Gateway` repository and
   `Gateway.Model(id)`. Credentials therefore never appear in conversation
   records.

3. **Provider artifacts go to `optionalContent`.** `Message`,
   `ToolCall`, and `Model` each carry a `map[string]string` written through
   `SetOptionalContent` under adapter-namespaced keys —
   `"completion.reasoning_content"` is the real example
   (`infrastructure/service/core/text/completion/decode.go`). The domain
   never interprets the values, and no provider gets a dedicated field.

4. **`Usage.inputTokens` is uncached input.** `NewUsage` takes disjoint
   counters; `NewUsageFromTotal` derives the uncached portion from an
   inclusive total and rejects cache counters that exceed it
   (`valueobject/usage.go`). Mapping an inclusive total into `inputTokens`
   double-counts in `TotalInput()`.

5. **Tool outputs are untrusted evidence.** Runtime output goes through
   `ToolCall.SetOutput`, which retains the raw result and fixed latency.
   Provider getters apply the context budget and neutralize embedded
   envelope delimiters (`entity/toolcall.go`). Hydration restores raw
   text and latency via `SetStoredOutput` and
   `Message.SetStoredToolOutput`. Tool-produced media ride as
   `outputAttachments` — references resolved at send time and gated
   per-request by the bound model's `AcceptsInput`.

6. **Unset is not zero.** Generation params on `Model` are `*T`
   (`entity/model.go`); `enum.Effort` uses `IsUnset()` where `""` and
   `default` both mean omit the field. Absent means "don't send", and the
   provider default applies — never a synthesized zero.

7. **Fail-closed vs fail-open is deliberate.** `ToolSchema.CallableByAgent`
   / `CallableBySystem` return false for disabled or unknown permission
   (`valueobject/toolschema.go`). `Model.AcceptsInput` / `AcceptsOutput`
   return true on an empty modality list (`entity/model.go`) — the
   provider remains the final validator, so an incomplete catalog degrades
   nothing.

8. **Secrets are references, never plaintext.** Persisted credentials are
   `"env:NAME"` or `"secret:<key>"` (`valueobject/keyref.go`), resolved via
   `SecretStore` at `Load`. `GatewaySnapshot.ApiKeyRef` carries the raw
   stored value for management reads so refs round-trip through updates.

9. **One error contract.** Domain sentinel errors (`ErrMessageNotFound`,
   `ErrNotUserMessage`, `ErrPathTraversal`) are mapped by callers to
   `apperror` — public `Message`, internal `Cause`, HTTP `StatusCode` for
   routing: 413 = context overflow → compact, 429 = rate-limited →
   fallback, 401/403 = dead credential. See `infrastructure/error`.

10. **Diagnostics via `Logger`.** `repository.Logger` is a verbose JSONL
    sink; entries are scrubbed by `infrastructure/redact` before write.

Conventions worth internalizing before editing this layer:

- Hydration is `New*` + setters; there are no reconstitute constructors,
  and setters marked "repository hydration only" (`SetID`, `SetCreatedAt`,
  `SetCallAt`, `SetStoredOutput`) are not general mutation API.
- Slice and map getters share internal storage by design — no defensive
  copies. Re-fetch `Conversation.Last()` after `Append`.
- Compaction never splits a tool-call/result pair, and the model makes
  that easy: calls live inside their message, so message-aligned
  boundaries are safe by construction.
- `EstimatedTokens`/`NeedCompaction(budget)` take the budget as an
  argument so model switches apply immediately; estimation is a budgeting
  heuristic, `Usage()` is billing truth.

## Further reading

- [./architecture.md](./architecture.md) — how the layers fit together and why
- [./persistence.md](./persistence.md) — JSONL segments, atomic writes, hydration
- [./adapters.md](./adapters.md) — how this model maps onto provider wires
- [./cli-guide.md](./cli-guide.md) — the terminal client these ports serve
- [./extending.md](./extending.md) — adding a provider, tool, or command
- [../README.md](../README.md) — the product pitch
