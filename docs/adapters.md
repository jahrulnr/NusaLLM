# Service adapters

`infrastructure/service/` holds NusaLLM's **outbound adapters**: the code that
translates domain ports into provider wire protocols, local file I/O, process
execution, and terminal-facing command mechanics. The ports themselves live in
`infrastructure/domain/service/` — `TextModelService`, `Tool`, `SlashCommand` —
and the domain never imports this tree. The boundary exists so that everything
provider- or OS-shaped accumulates at the edge: a malformed SSE frame, a
provider-specific replay field, or a symlink escape is the adapter's problem,
never the application's. Conversely, adapters own no business rules — route
selection, retries, tool permission, and compaction policy are decided in
domain and application code (see [`infrastructure/service/AGENTS.md`](../infrastructure/service/AGENTS.md)
and [./architecture.md](./architecture.md)).

This document covers what each adapter does on the wire and on disk, verified
against the current tree. For the port contracts and the domain model they
normalize into, see [./domain-model.md](./domain-model.md).

## Wire styles

`enum.Style` (`infrastructure/domain/enum/style.go`) names four API paradigms:
`completion`, `responses`, `messages`, and `generatecontent`. A style is a
property of a `Gateway` — the same model id can be spoken through different
paradigms depending on the route — and `nusallm gateway add --style` accepts
all four values (`infrastructure/delivery/cli/admin/gateway.go`).

All four are wired in `cmd/nusallm/main.go`, which builds the service map as

```go
services := map[enum.Style]dservice.TextModelService{
    enum.StyleCompletion:      textcompletion.New(textOpts),
    enum.StyleResponses:       textresponses.New(textOpts),
    enum.StyleMessages:        textmessages.New(textOpts),
    enum.StyleGenerateContent: textgeneratecontent.New(textOpts),
}
```

Every adapter shares the same skeleton: a stateless `Service` built once from
`coretext.Options` (`service/core/text/options.go`) that reads endpoint,
credentials, and stream preference off the `Gateway` argument of each `Send`.
They differ in everything the wire demands — auth header, content shape,
streaming grammar, usage counters, and what must replay byte-exact:

| Style | Surface | Auth | Content shape | Stream grammar | Replay-critical artifacts |
|---|---|---|---|---|---|
| `completion` | `POST {endpoint}/chat/completions` | `Authorization: Bearer` | role messages + `tool_calls`/`tool` | unnamed `data:` chunks, `[DONE]` sentinel | `completion.reasoning_content` (DeepSeek-style) |
| `responses` | `POST {endpoint}/responses` | `Authorization: Bearer` | typed input/output items | named SSE events, `response.completed` terminal | `responses.output_items` template (encrypted reasoning, server items) |
| `messages` | `POST {endpoint}/messages` | `x-api-key` + `anthropic-version` | content blocks | named SSE events, `message_stop` terminal | `messages.content_blocks` template (thinking signatures) |
| `generatecontent` | `POST {endpoint}/models/{model}:generateContent` (`:streamGenerateContent?alt=sse`) | `x-goog-api-key` | role-alternated contents of typed parts | `data:` partial responses, EOF terminal — no sentinel | `generatecontent.parts` template + `generatecontent.call_id` per call |

The shared rules all four honor: tools reach the wire only when
`CallableByAgent()`; generation params are omitempty and unset means
"provider default"; adapter-namespaced escape keys (`<style>.<param>` on
`model.OptionalContent()`) flatten verbatim into the request body;
attachments degrade in place (placeholder text, never a failed request) and
local references resolve through `wire` beneath `FileRoot`; errors map to
the `apperror` status contract (413 → compact, 429 → fallback, 401/403 →
dead credential); and provider quirks — the `x-opencode-session` header on
`opencode.ai` endpoints, `HTTP-Referer`/`X-Title` attribution — live inside
each adapter's request leg where `enum.Provider` can grow more.

The sections below cover `completion` in depth; the other three follow the
same structure, so only their wire-specific mechanics are described after.

## The completion adapter — `core/text/completion`

`textcompletion` is the OpenAI-shaped `/chat/completions` adapter. It is the
widest wire in the ecosystem — OpenAI, OpenRouter, DeepSeek, Groq, xAI,
Mistral, and the compatible long tail all speak it — so one adapter covers
every completion-style gateway. `Service` is a stateless translator: endpoint,
API key, and stream preference are read off the `Gateway` argument of every
`Send`, so a re-loaded gateway (rotated key, moved endpoint) takes effect
immediately and one instance serves all routes (`completion.go`).

### Request leg

`Send` is one POST to `strings.TrimRight(gw.Endpoint(), "/") + "/chat/completions"`.
`encodeRequest` (`encode.go`) maps domain state onto `RequestBody` (`dto.go`):

- The system prompt and every message on the conversation are re-encoded each
  call; the adapter keeps no session state.
- Only tools whose schema is `CallableByAgent()` reach the wire — system-only
  or disabled tools are filtered before marshal.
- Generation parameters are `*T` fields with `omitempty` throughout: unset
  means "provider default," and a zero is never sent. `enum.Effort` maps to
  `reasoning_effort` only when `IsUnset()` is false. `max_tokens` is emitted
  as `max_completion_tokens`; `top_k` is an OpenRouter extension that the
  compatible wire tolerates.
- `prompt_cache_key` is pinned to the conversation ID so providers can bucket
  cache state per dialogue; `stream_options.include_usage` always rides with
  `stream: true` because providers omit the final usage chunk otherwise.
- Adapter-namespaced escape hatch: any `model.OptionalContent()` key of the
  form `completion.<param>` lands verbatim as a top-level request field —
  `RequestBody.Extra` is flattened into the body by a custom `MarshalJSON`.
  This is how a provider-specific knob (a vendor beta flag, a cache TTL)
  reaches the wire without a domain field.

Headers: `Authorization: Bearer <gw.ApiKey()>`, `Content-Type: application/json`,
plus optional `HTTP-Referer`/`X-Title` (OpenRouter attribution, fed from
`Options.Referer`/`Options.Title`). One provider quirk is baked in: when the
endpoint contains `opencode.ai`, the adapter sets `x-opencode-session` to the
conversation ID. Quirks like this live here precisely so the rest of the
system never sees them.

### Messages, tools, and media on the wire

`encodeMessage` emits one wire message per domain message, or an assistant
message followed by `tool`-role messages when it carries executed calls.
Three details matter:

- **Injection vs origin calls.** Calls of `enum.TooltypeInjection` are
  application-originated; they hydrate as inline text appended to the message
  content rather than posing as provider `tool_calls`, and their output
  attachments are never encoded.
- **Tool output attachments ride a synthetic user message.** The
  chat-completions `tool` role is text-only, so media a call produced is sent
  as an openclaw-style follow-up user message labeling the call it came from.
  Those follow-ups land *after* the entire tool block — strict providers
  require an uninterrupted run of tool messages behind `tool_calls`, so a
  media-bearing call must not interleave.
- **Per-attachment degrade at send time.** Each attachment is checked against
  the bound model's `AcceptsInput` modality list; a modality the model can't
  consume degrades to a text placeholder ("(image attachment omitted: model
  does not accept it)"), as does an unresolvable reference. Because the check
  happens per request, swapping the bound model between turns just works.
  An interrupted call with no recorded output is rendered as an
  `<untrusted_tool>` stub so strict providers still see a result for every
  `tool_call_id`.

`encodeAttachment` maps a stored reference to the part shape for its modality:
`image_url` for images, `video_url` for video (an OpenRouter extension),
`input_audio` for audio — which must arrive as base64 because URL audio is
rejected upstream — and a `file`/`file_data` part for everything else. URI
materialization is delegated to `service/wire` (below).

### Response leg and finish reasons

`decodeMessage` (`decode.go`) normalizes wire content — a bare string or a
parts array — into a domain `Message`, converts `tool_calls` into
`TooltypeOrigin` calls (empty `arguments` become `{}`, which upstreams
legitimately emit), and parses `finish_reason` into `enum.FinishReason`
(`stop`, `tool_calls`, `length`, `content_filter`; `enum/finishreason.go`).
Unknown finish values are preserved verbatim rather than rejected. A
provider-specific artifact like DeepSeek's `reasoning_content` is stored under
the adapter-namespaced key `completion.reasoning_content` in
`optionalContent`, and re-emitted on the next request — domain never
interprets it. `decodeUsage` routes through `NewUsageFromTotal` because
`prompt_tokens` on this surface is an *inclusive* total with cached tokens as
a subset; it also accepts both `cache_write_tokens` and Anthropic-via-OpenRouter's
`cache_creation_input_tokens` spelling.

### Streaming

`streamResponse` (`stream.go`) consumes the SSE body through `wire.ScanSSE`.
The discipline is deliberate: deltas accumulate, `tool_calls` fragments merge
by index (`mergeCallDelta`, bounded by `maxCallIndex = 1024` since the index is
provider-controlled and hostile values must not size the slice), and the
assistant message is committed to the conversation exactly once — on a
terminal `finish_reason` or the `[DONE]` sentinel. EOF with no terminal marker
is a 502 "upstream stream truncated", not a commit. Garbage lines that fail to
unmarshal are dropped rather than fatal, while a mid-stream `{"error": …}`
envelope is remapped through `statusHint` (429 / 401 / 413 heuristics from the
error code and type, defaulting to 502) so application routing sees the same
contract as the non-stream path. In non-stream mode `unaryResponse` emits a
synthetic delta + finished pair so callers consume both modes identically.

Both modes emit the same `event.TurnEvent` kinds (`domain/event/turn.go`):
`Delta`, `Reasoning`, `ToolCall`, `Finished` (finish reason + usage), `Error`.
A nil handler is fine; a slow one applies backpressure on the read loop.

### Options and transport

`Options` (`service/core/text/options.go`, shared by all four text adapters)
is the whole configuration surface:

| Option | Effect |
|---|---|
| `Client` | Inject a custom `*http.Client`; otherwise the adapter builds a transport with a response-header deadline only — interactive streams must not carry a fixed total timeout because reasoning models think for minutes |
| `HeaderTimeout` | First-byte deadline on the built-in transport (default 30s via `config.ServiceConfig.Normalize`) |
| `IdleTimeout` | Per-chunk deadline on the SSE read loop, reset on every event (default 90s); expiry is a 502 "upstream stream idle" |
| `Referer` / `Title` | Sent as `HTTP-Referer` / `X-Title` attribution headers |
| `FileRoot` | Root for local `file://` attachment resolution; empty disables local refs |
| `Logger` | `drepo.Logger` sink for `provider.request` / `provider.response` / `provider.error` / `provider.stream` entries; logging failures never break a send |

`main.go` feeds `cfg.Service.HeaderTimeout`/`IdleTimeout`, `cfg.App.Name` as
`Title`, `cfg.App.Source` as `Referer`, the process cwd as `FileRoot`, and the
redacted repository logger. Response bodies are read under a 32 MiB cap
(`readAll` in `stream.go`).

### Error mapping

The port contract is `apperror` with a status the application routes on
(`domain/service/text.go`): **413** means context overflow → compact rather
than fall back; **429** means rate-limited → try another route; **401/403**
means a dead credential. The adapter's obligations:

- Non-200 responses go through `wire.StatusError`, which remaps a 400 whose
  code or message mentions `context_length` into 413 and otherwise applies
  `apperror.FromStatus`. The upstream message is surfaced (it describes
  request problems and is frontend-safe); the raw body stays in `Cause`.
- Transport failures are 502 "upstream unreachable"; caller cancellation or
  deadline is 408. Unreadable or malformed bodies, empty `choices`, truncated
  streams, and idle streams are all 502s.
- Every failure path logs what it can to `Logger` before returning.

## The messages adapter — `core/text/messages`

`textmessages` is the Anthropic-shaped `/messages` adapter. Auth is
`x-api-key` plus the required `anthropic-version` header; `max_tokens` is
wire-required so the adapter fills a floor (16384) when the model leaves it
unset.

The wire is content-block shaped end to end. A stored assistant turn replays
through an ordered **block template** kept in `optionalContent` under
`messages.content_blocks`: `{"type":"text"}` and `{"type":"tool_use"}`
entries are bare placeholders refilled from domain state, while `thinking`,
`redacted_thinking`, and server blocks are stored as raw JSON and re-emitted
byte-exact — Anthropic thinking signatures are bound to the prompt prefix,
so those blocks must never be reserialized through a narrower struct. The
decoder is why the DTO's `Content` is `[]json.RawMessage`.

Tool results ride `tool_result` blocks inside the next user turn (the wire
has no tool role); a call's output attachments nest inside its own
`tool_result` block natively, and an interrupted call yields an
`(interrupted — no result was recorded)` marker so strict validation still
sees a result per `tool_use`. Streaming is named SSE: blocks accumulate by
`content_block_*` index, `message_delta` carries the terminal `stop_reason`
and cumulative usage, and `message_stop` is the only commit point. Usage is
*disjoint* — `input_tokens` is already the uncached count — so decode goes
through `NewUsage`, not `NewUsageFromTotal`. Anthropic reports context
overflow as a 400 whose message says "prompt is too long"; the adapter
remaps that to the routing 413 itself.

## The responses adapter — `core/text/responses`

`textresponses` is the OpenAI `/responses` adapter. The wire speaks in typed
output/input items rather than role messages: `message`, `function_call`,
`function_call_output`, `reasoning`, plus server-tool items like
`web_search_call`.

Two deliberate stances define the request leg:

- **Stateless by construction.** `store: false` is sent always and the full
  item list goes every turn — the repository is the record, and edits would
  break `previous_response_id` chains anyway. `include:
  reasoning.encrypted_content` opts into replayable reasoning so stateless
  turns keep their chain of thought; a stored reasoning item *without* the
  blob is dropped at encode (documented ZDR behavior — the provider would
  reject it).
- **Template replay like messages, one level up.** The turn template sits in
  `optionalContent` under `responses.output_items` — `{"type":"message"}` and
  `{"type":"function_call"}` placeholders refill from domain state, reasoning
  and server items replay verbatim. `responses.id` stores the response id
  for support/debugging.

`function_call` ↔ `function_call_output` pair on `call_id`; output
attachments ride a labeled user item after the whole output block because
`function_call_output.output` is a string on this surface. `usage` is
*inclusive* (`input_tokens` contains `cached_tokens`) → `NewUsageFromTotal`.
Streaming is named SSE: text/reasoning deltas stream live,
`response.completed` carries the full response and is the commit point (the
loop closes on it rather than waiting for server EOF), with accumulated
`output_item.done` items as a fallback. `status: "incomplete"` +
`max_output_tokens` maps to finish reason `length`. Effort maps to
`reasoning.effort` with `xhigh`/`max` clamped to `high` — the wire ceiling.

## The generatecontent adapter — `core/text/generatecontent`

`textgeneratecontent` is the Gemini `generateContent` adapter. Auth is
`x-goog-api-key` (Bearer often fails on this host); the model id lives in
the URL path, so prefixed ids (`google/gemini-3.6-flash`, `models/x`)
normalize to the bare id. `maxOutputTokens` is always sent — the wire's
internal default is low enough to truncate agent turns early.

The wire is role-alternated `contents` of typed parts (`user`/`model` only —
`systemInstruction` is top-level). Its quirks, all handled at encode:

- **Same-role merge.** Adjacent same-role contents are invalid on the wire,
  so encoding folds neighbors together — this is what lets parallel
  `functionResponse` parts legally share one user turn, and what merges a
  following user message into the results turn.
- **Schema subset.** Gemini `Schema` is a subset of JSON Schema;
  `sanitizeSchema` whitelists keywords (`type`, `properties`, `items`,
  `anyOf`, `enum`, bounds…), drops `$schema`/`$ref`/`additionalProperties`,
  stringifies non-string enums, and prunes `required` entries that name no
  property — unsanitized declarations get a 400.
- **Call ids by marker.** `functionCall` parts may carry a provider `id`;
  when present it's stored on the call's `optionalContent` as
  `generatecontent.call_id` and echoed back as `functionResponse.id` for
  correlation. Without one, correlation falls back to name-matching.
- **Thought signatures.** `thoughtSignature` fields on model parts must
  replay byte-exact (function-call turns 400 without them). The template —
  `generatecontent.parts` — stores signed and thought parts verbatim with
  `{"refill":"text"}`/`{"refill":"functionCall"}` placeholders; a verbatim
  signed call still consumes its domain call slot so refills stay aligned.
- **Family-aware thinking.** `generationConfig.thinkingConfig` maps effort
  by model family: `gemini-3` ids take `thinkingLevel` (clamped to `high`),
  `gemini-2.5` takes `includeThoughts` alone (invented budgets get
  rejected), `gemma` omits the config entirely, and unknown families get
  `includeThoughts` only.

All modalities carry — image, audio, video, and files go `inlineData`
base64 for local/inline references or `fileData` for remote URLs. Streaming
emits partial `GenerateContentResponse` payloads per `data:` line with **no
terminal sentinel** — EOF is the commit point, so a stream that died before
its first chunk reports truncated while a normal close commits whatever
accumulated. Consecutive same-kind text parts merge (a `thoughtSignature`
on the last fragment binds the merged whole). `usageMetadata` is inclusive
and `thoughtsTokenCount` folds into output tokens.

`wire` holds the mechanics that don't change per adapter family; wire-shaped
part assembly stays inside each adapter.

- `sse.go`: `ScanSSE`/`SplitSSE` cut a stream into event blocks on the earliest
  blank-line boundary (`\r\n\r\n` or `\n\n`, mixed-delimiter safe), with an
  8 MiB scanner buffer for fat chunks. `SSEData` joins a block's `data:` lines,
  skipping keepalive comments; `SSENamed` additionally extracts the `event:`
  name for families whose dispatch keys off it (messages, responses).
- `media.go`: attachment resolution. `MaxAttachmentBytes` caps payloads by
  modality (10 MiB image, 50 MiB audio, 100 MiB video, 20 MiB otherwise).
  `ResolveURI` turns a stored reference into a sendable URL: `data:` passes
  through (size-checked, pin-verified when pinned), `file://` is reopened
  beneath `fileRoot` via `os.Root` plus a `filepath.Rel` confinement check and
  base64-inlined, and `http(s)://` passes through unfetched — adapters do not
  fetch arbitrary URLs. A `sha256` pin is enforced on resolved bytes and is
  rejected outright on remote references. `ResolveBase64` additionally splits
  a `data:` URI into payload + `FormatName` (e.g. `audio/mpeg` → `mp3`) for
  wires like `input_audio`. `OpenRegularFile` opens beneath an `os.Root` with
  `O_NONBLOCK` and rejects non-regular files, so FIFOs and device nodes can't
  stall or confuse readers.
- `error.go`: `StatusError`, the shared status→`apperror` convention above,
  and `UpstreamError` for keeping raw bodies in `Cause`.

## `service/core` — capability map

`core` is laid out by capability rather than by provider. Implemented today:

| Directory | Status |
|---|---|
| `core/text/completion` | **Implemented** — the adapter above |
| `core/text/messages`, `core/text/responses`, `core/text/generatecontent` | **Implemented** — Anthropic Messages, OpenAI Responses, and Gemini generateContent surfaces; all four styles are wired in `main.go` |
| `core/audio`, `core/decision`, `core/embedding`, `core/image`, `core/rerank`, `core/speech`, `core/transcription`, `core/video` | Scaffolded (`.gitkeep` only) — no port or adapter exists yet |

Modality *input* (images, audio, video, files attached to a conversation) is
not a separate service: it rides the completion wire as content parts. These
directories are reserved for generation/embedding/rerank-style endpoints,
should their ports ever be added to domain.

## Tool adapters — `service/tool/`

Tools implement the `service.Tool` port (`domain/service/tool.go`): the
constructor builds and validates the `ToolSchema` it advertises, and `Execute`
runs one model-originated call returning `ToolResult{Text, Attachments}`.
`Text` is untrusted evidence — the application turn wraps it via
`ToolCall.SetOutput` into the `<untrusted_tool latency="…">` envelope — while
`Attachments` are stored media references that resolve at send time by the
same rules as message attachments, never consuming the text budget. Execution
failures are recorded *as the tool output* so the model can react; only
context cancellation aborts the turn (`application/job/turn.go`, `execTool`).
Whether a call is permitted is checked against the schema's permission by the
application turn before `Execute` ever runs — tools themselves hold no
permission logic. `emit` is a synchronous live-preview channel; slow consumers
backpressure the tool, and chunks are never persisted.

| Tool | Package | Notes |
|---|---|---|
| `read_file` | `tool/file` (`read.go`) | UTF-8 text under the root; rejects non-regular files and anything over the call's `OutputBudget` before reading |
| `write_file` | `tool/file` (`write.go`) | Create/overwrite; parent dirs `0o700`, new files `0o600` (agent output may carry secrets); confirms rather than echoing content |
| `apply_patch` | `tool/file` (`patch.go`) | Literal search/replace — deliberately not unified diff; files > 4 MiB are rejected in favor of `exec`/`sed`; ambiguous anchors must gain context or `replace_all` |
| `read_media` | `tool/file` (`read_media.go`) | Attaches a non-text file to the result as a pinned `file://` reference — see below |
| `exec` | `tool/terminal` (`exec.go`) | Shell command, merged output — see below |
| `summary` | `tool/summary` (`summary.go`) | Compaction checkpoint contract — not in the agent-facing catalog |

The three file tools bind an `os.Root` at construction — `main.go` passes the
process cwd — so every requested path resolves inside the workspace,
traversal (`..`) and symlink escapes included. `read_media` and
`/attach`-style resolution share the same discipline: sniff the first 512
bytes for `http.DetectContentType` (a `.csv` *is* text and is refused — use
`read_file`), enforce `wire.MaxAttachmentBytes` from the stat rather than a
read, then hash the content through the confined handle and pin the
`Attachment` with that sha256. The result carries a `file://` reference the
wire adapter reopens and re-verifies at send time, so a swapped file degrades
to a placeholder instead of sending unchecked bytes. How those attachments
ride back to the model — a labeled synthetic user message after the tool
block — is covered in the completion adapter section.

### `exec` — terminal execution

`tool/terminal` runs `sh -c` on Linux (own process group via `Setpgid`, group
SIGKILL on cancel — `exec_linux.go`) and `cmd /C` on Windows (`exec_windows.go`,
which can orphan grandchildren since Windows has no portable group kill). It
is inherently unsandboxed — there is no root to confine it to — so the schema
permission and the application turn are the entire gate. Timeout defaults to
120s, caller-settable up to 600s; `cmd.WaitDelay` (5s) caps how long `Wait`
blocks on pipes inherited by stray grandchildren.

stdout and stderr share one `outputSink`, so the merged stream keeps arrival
order and every `Write` is also a preview chunk to `emit`. Memory stays
bounded by the call's `OutputBudget`: while output fits, it buffers outright;
the first write past budget opens a spill file at
`os.TempDir()/nusallm-exec-<sanitized call id>.log` (chosen before start), freezes
the buffer as the head region, and keeps a rolling tail while the full stream
continues to disk. The returned text is a head (~60%) + tail excerpt joined by
a `[... N bytes omitted — full output: <path> ...]` marker, cut on rune
boundaries; the exit status (`(exit 3)`, `(killed: …)`, signal deaths) is
appended as a suffix so it lands inside the tail and at the end of the spill
file. A spill-write failure degrades to head+tail with the error in the
marker.

### `summary` — the compaction contract

`tool/summary` is not a user-or-agent tool in the usual sense: its `text`
field *is* the result, and `Execute` merely extracts it. The compaction job
(`application/job/compact.go`) builds a scratch conversation containing only
this tool's schema and drives `Send` until the model answers through it,
accepting plain text as a fallback. It is never registered in the `tools` map
in `main.go`.

## `service/workspace` — the sandbox

`workspace.Root` wraps `os.OpenRoot` on one directory (`root.go`): `Open`
(defaults `"."`), `Path`, `FS` (for listing/completion), `CreateFile`,
`OSRoot` for callers that still need the raw root, and `Close`. `FromRoot`
adopts an existing pair.

`ResolveAttachment` (`attachment.go`) is the attachment path: validate the
requested path via `valueobject.NewWorkspace` (clean, reject absolute paths
and `..`), open beneath the root with `wire.OpenRegularFile`, sniff the media
type, reject text modalities, enforce the modality size limit, and pin the
bytes by sha256 so the later wire-time read resolves only what the user
selected. Materialization at send time — `file://` reopen + base64, `data:`
passthrough, `https://` passthrough — is `wire.ResolveURI`'s job, documented
above; this package produces the pinned references, adapters resolve them.

Delivery consumes it through `application/command.WorkspacePort` (`application/command/ports.go`)
— `delivery/cli/session/session.go` opens it from the REPL's workspace
option — which is what lets `/attach`, `/export`, and completion helpers share
one confinement boundary with the file tools.

## Slash commands — `service/command/`

`domain/service/command.go` defines the port: a `SlashCommand` is a
`CommandService` (`Execute(ctx, command, emitter, args...)`) plus
`Name`/`Usage`/`Description`, and may optionally implement
`SlashCommandCompleter` for argument completion. The args convention: the
caller passes the remainder of the input line as `args[0]` and the handler
parses it.

`command.Registry` (`registry.go`) dispatches wired instances by lowercased
name — it owns no separate catalog, just like tool wiring. It also derives the
surfaces delivery needs: `Describe` for `/help` and the palette, `Completable`,
and `Complete`, which only engages on a single-line `/cmd <arg>` buffer and
caps rows at `internal.MaxRows` (50). `command.AttachmentPath` (`export.go`)
is the shared `/attach` argument validator. `command/internal/` holds helpers
commands share — conversation-id completion (`Conversations`), display-title
resolution for list labels (`DisplayTitles`/`Resolve`, which load only a
bounded history prefix via `TitleHistoryEnd`), and the size constants.

Each command lives in `service/command/<name>` with its own constructor and
depends on `application/command.SlashSession` — the facade over conversation
and gateway use cases, workspace port, sanitized output, and session bindings
— never on concrete application structs. The CLI wires 24 commands in
`delivery/cli/commands/wire.go` (`attach`, `cancel`, `clear`, `compact`,
`copy`, `delete`, `detach`, `diff`, `edit`, `exit`, `export`, `help`,
`history`, `list`, `model`, `new`, `pwd`, `quit`, `resume`, `route`,
`status`, `title`, `tools`, `usage`), and the TUI drives the registry's
`Execute`/`Complete` from `delivery/cli/tui/model.go`. Two representative
shapes: `/attach` resolves a workspace path through the session's
`ResolveAttachment` and appends to pending bindings; `/diff` shells out to
`git -C <workspace> diff` and caps output at 64 KiB.

`service/autocomplete/` exists on disk as an empty directory — untracked, with
not even a `.gitkeep` — and contains no code. Argument completion today is
entirely the per-command `Complete` methods above plus the palette plumbing in
delivery; treat the directory as an unused placeholder rather than a component.

## Extending the layer

The invariants to preserve, distilled from `infrastructure/service/AGENTS.md`:

- A new provider paradigm gets a new adapter under `core/text/<family>` (or a
  new capability dir), implements `TextModelService`, and is registered in the
  `services` map at the composition root — `main.go` today. Reuse
  `service/wire` for SSE framing, media resolution, and status mapping; keep
  provider quirks behind `completion.*`-style adapter-namespaced
  `optionalContent` keys, never new domain fields.
- A new tool is a package under `tool/` whose constructor validates its
  `ToolSchema` (and binds its `os.Root`, if file-bound); wire it into the
  `tools` map in `main.go`. Permission is declared on the schema and enforced
  by the turn — never inside `Execute`.
- A new slash command is `command/<name>` implementing `SlashCommand` (plus
  `SlashCommandCompleter` if arguments complete), wired in
  `delivery/cli/commands/wire.go`, depending only on `SlashSession`.

See [./extending.md](./extending.md) for a worked example and
[./cli-guide.md](./cli-guide.md) for how delivery consumes the command and
workspace surfaces.
