# Service adapters

`infrastructure/service/` holds NusaLLM's **outbound adapters**: the code that
translates domain ports into provider wire protocols, repository-backed storage, process
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

All four are wired in `cmd/nusallm/wire.go` for the local environment, which
builds its service map as

```go
services := map[enum.Style]dservice.TextModelService{
    enum.StyleCompletion:      textcompletion.New(textOpts),
    enum.StyleResponses:       textresponses.New(textOpts),
    enum.StyleMessages:        textmessages.New(textOpts),
    enum.StyleGenerateContent: textgeneratecontent.New(textOpts),
}
```

The map is registered together with the local workspace repository and tools
as one `EnvironmentDriver`. The conversation's stored environment chooses
the entire bundle for a turn; a future remote bundle must provide media-aware
adapters for its own workspace repository.

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
local references resolve through `wire` and the injected `FileStore` beneath `FileRoot`; errors map to
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
  cache state per dialogue, and `prompt_cache_options.ttl` rides with it
  unconditionally as `"30m"` — prompt caching is always requested, not
  opt-in. `stream_options.include_usage` always rides with `stream: true`
  because providers omit the final usage chunk otherwise.
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
  application-originated hydration. On a `RoleSystem` record (the workspace
  pin) they encode as the completed `tool_calls` + `tool` pair they record —
  the harness ran them on the model's behalf — so observed evidence rides at
  tool-output trust rather than inside an instruction turn. On any other
  carrier (legacy user-role records) they fold into the message text
  instead, and their output attachments are never encoded.
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

`cmd/nusallm/wire.go` feeds `cfg.Service.HeaderTimeout`/`IdleTimeout`, `cfg.App.Name` as
`Title`, `cfg.App.Source` as `Referer`, the delivery's workspace root as `FileRoot`
(the REPL's cwd, `serve`'s owner home), and the
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

`messages.anthropic_beta` is the one adapter key that is not a body escape
hatch: a JSON string array, JSON string, or comma/whitespace separated list
is flattened into the comma-separated `anthropic-beta` header and skipped
from `RequestBody.Extra`. This is how interleaved-thinking and context-
management betas reach the wire — putting them in the request body would be
ignored by Anthropic.

Unlike open-source reasoning models, claude emits thinking blocks only when
asked, and the opt-in shape depends on the generation parsed out of the
`claude-*` id: ids older than 3.7 (2.x, 3.0/3.5) reject the field entirely
and get nothing; 3.7 through 4.5 take `{"type":"enabled","budget_tokens":n}`
with the budget mapped from the configured effort (skipped when effort is
unset, or when the budget would not fit under `max_tokens`); 4.6 and newer
take `{"type":"adaptive","display":"summarized"}` so the summary actually
reaches the user. `effort: none` sends nothing — there is no safe disabled
wire across generations. A `messages.thinking` optionalContent entry
suppresses the default entirely, keeping the verbatim escape hatch.

Tool results ride `tool_result` blocks inside the next user turn (the wire
has no tool role); a call's output attachments nest inside its own
`tool_result` block natively, and an interrupted call yields an
`<untrusted_tool>`-wrapped `(interrupted — no result was recorded)` marker so strict validation still
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
  turns keep their chain of thought; a stored reasoning item *without* a
  non-empty string blob is dropped at encode (documented ZDR behavior — the
  provider would reject it, and `null` is not a blob). `responses.include`
  is additive: configured values merge with the adapter default instead of
  replacing it.
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
`reasoning.effort` with `xhigh`/`max` clamped to `high` — the wire ceiling —
and asks for `reasoning.summary: "auto"` as well so the reasoning actually
surfaces to the user rather than running silently.

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
- **Injected evidence.** A system-role hydration record emits its
  injection calls as the completed call + result pair they record —
  bounded untrusted output at tool-evidence trust. Injection calls on
  other carriers still fold into turn text; output is never dropped.
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
  beneath `MediaRoot.Dir` via the injected `repository.Workspace` plus a
  `filepath.Rel` confinement check and
  base64-inlined, and `http(s)://` passes through unfetched — adapters do not
  fetch arbitrary URLs. A `sha256` pin is enforced on resolved bytes and is
  rejected outright on remote references. `ResolveBase64` additionally splits
  a `data:` URI into payload + `FormatName` (e.g. `audio/mpeg` → `mp3`) for
  wires like `input_audio`. The local workspace repository's `OpenRegular`
  uses `O_NONBLOCK` and rejects non-regular files, so FIFOs and device nodes
  cannot stall or confuse readers.
- `error.go`: `StatusError`, the shared status→`apperror` convention above,
  and `UpstreamError` for keeping raw bodies in `Cause`.

## `service/core` — capability map

`core` is laid out by capability rather than by provider. Implemented today:

| Directory | Status |
|---|---|
| `core/text/completion` | **Implemented** — the adapter above |
| `core/text/messages`, `core/text/responses`, `core/text/generatecontent` | **Implemented** — Anthropic Messages, OpenAI Responses, and Gemini generateContent surfaces; all four styles are wired in `cmd/nusallm/wire.go` |
| `core/audio`, `core/decision`, `core/embedding`, `core/image`, `core/rerank`, `core/speech`, `core/transcription`, `core/video` | Scaffolded (`.gitkeep` only) — no port or adapter exists yet |

Modality *input* (images, audio, video, files attached to a conversation) is
not a separate service: it rides the completion wire as content parts. These
directories are reserved for generation/embedding/rerank-style endpoints,
should their ports ever be added to domain.

## Tool adapters — `service/tool/`

Tools implement the `service.Tool` port (`domain/service/tool.go`): the
constructor builds and validates the `ToolSchema` it advertises, and `Execute`
runs one model-originated call returning `ToolResult{Text, Attachments}`.
`Text` is untrusted evidence — the application turn stores it raw via
`ToolCall.SetOutput`, and provider adapters request the bounded
`<untrusted_tool latency="…">` envelope — while
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
| `exec_session` | `tool/terminal` (`exec_session.go`) | Open a persistent terminal session — see below |
| `write_stdin` | `tool/terminal` (`write_stdin.go`) | Write/poll an open session; writes need `tty`, `\x03` maps to Ctrl-C |
| `web_search` | `tool/web` (`web.go`) | Searchwire metasearch across Brave/Startpage/Wikipedia/GitHub (+ keyed sources); ranked results with partial-failure reporting |
| `web_fetch` | `tool/web` (`web.go`) | Searchwire fetch — one URL → readable text; truncated output spilled to a `tempFile` for follow-up `read_file` |
| `workspace` | `tool/workspace` | Harness-side hydration record — `ToolPermissionSystem`, pins the turn's working directory into history as a completed call |
| `skills` | `tool/skill` | `ToolPermissionGeneral`: discovery is automatically recorded as hydration and can also be called by the agent to refresh the catalog |
| `read_skill` | `tool/skill` | Agent-callable full SKILL.md read by catalog name, using the turn's selected workspace |
| `custom_tools` | `tool/custom` | System-only hydration of executable directories used by both terminal execution paths |
| `summary` | `tool/summary` | Compaction checkpoint contract — injected into `Deps`, never in a room's agent-facing catalog |

Skill discovery uses the injected `repository.Skills` port. The local repository
reads immediate child directories (including symlinked directories) containing
`SKILL.md` under `<workspace>/.agents/skills/`, `~/.agents/skills/`, and
`<storage.dir>/builtin-skills/`. YAML frontmatter requires nonempty `name` and
`description`; optional metadata and multiline YAML descriptions are supported.
The effective catalog is sorted by declared name, with workspace > global >
builtin precedence. Duplicate names within one root select the first directory
in lexical order and report a diagnostic. Missing roots are empty; malformed
skills and unreadable roots are reported as diagnostics without dropping valid
entries. `read_skill` returns the original source and its absolute directory;
supporting resources are read or executed from that directory as needed.

Composition embeds and atomically installs `skill-creator` and `tool-creator`
into the application-owned builtin root. Shipped files are refreshed from the
binary on startup; user customizations belong in global or workspace roots.
The source trees are `assets/skills/skill-creator/` and
`assets/skills/tool-creator/`; nested scripts, references, templates, icons,
and licenses are embedded and materialized with each skill.
The terminal environment provides custom executable paths and a lazy Python
runtime as described below.

### Custom executables and Python

Custom tools are ordinary commands executed through `exec` or `exec_session`.
There is no per-command manifest, Go registration, or provider `tools[]` entry.
Their companion skills describe selection, arguments, outputs, and dependencies.
The `custom_tools` hydration record advertises directories in PATH order:

1. `<workspace>/.agents/tools/bin/`
2. `~/.agents/tools/bin/`
3. `tools.paths`, in configured order (relative paths resolve against the workspace;
   `~/` expands against the user's home)
4. `<storage.dir>/tools/bin/`
5. `<storage.dir>/runtimes/bin/` contains managed `python` and `python3` launchers
6. The inherited host PATH

`LocalEnvironment` builds an environment for each command's workspace. Both
`LocalRunner` and `SessionStore` receive it through the `CommandEnvironment`
port, so one-shot exec, session pipes, and PTY share path resolution. It does
not mutate the daemon environment. Sessions retain the environment selected
when they were opened. New executables in existing directories are immediately
available; configuration changes take effect when the engine is restarted.

For example, configure additional paths with:

```sh
nusallm config set tools.paths '["./commands/bin","~/shared-tools/bin"]'
```

Place native binaries or executable wrappers in a PATH directory, and keep
supporting resources in the tool's own directory. On Unix, an executable Python
script can use `#!/usr/bin/env python3`; on Windows, use a native executable or
a `.cmd` wrapper. Invocation keeps the active workspace as cwd. The shipped
`tool-creator` skill explains the layout and companion skill contract.

Create and validate a workspace tool with the bundled helper scripts:

```sh
python3 <tool-creator-directory>/scripts/init_tool.py json-echo
python3 <tool-creator-directory>/scripts/validate_tool.py json-echo
```

Use `--scope global` for the user-wide roots, or pass `--tools-dir` and
`--skills-dir` for explicit locations. The initializer creates a Python
launcher, an implementation/resource directory, and a companion skill; it
does not overwrite existing targets. The validator checks local references
and runs `--help` plus a JSON probe.

The managed launchers invoke `nusallm python [args...]`. This command probes
host interpreters first: Python >=3.11 and <4 with functioning SSL, SQLite,
venv, archive, JSON, HTTP, filesystem, subprocess, and asyncio stdlib modules.
The probe excludes the managed launchers themselves to avoid recursion. If no
host interpreter qualifies, it validates a cached private installation. Only
when neither qualifies does it download pinned CPython 3.13.17 from Astral's
[python-build-standalone 20261021 release](https://github.com/astral-sh/python-build-standalone/releases/tag/20261021).
The published asset digest is checked and the staged interpreter is probed
before the repository atomically publishes its cache pointer. Published
installation paths are immutable so parallel launchers do not move a runtime
another process is using. Provisioning diagnostics go to stderr; program
arguments, stdin, stdout, stderr, and exit codes are forwarded.

Automatic downloads cover amd64/arm64 Linux (glibc or musl), macOS, and Windows.
Other platforms can reuse a qualifying host interpreter. Runtime downloads are
lazy: shell, Node, and native binaries remain usable without Python or network
access. A valid cached Python works offline. The feature does not install Node,
Go, third-party Python packages, or ML weights. `NUSALLM_RUNTIME_DIR`, set in
the child environment, points launchers at the engine's runtime store.
The `tool-creator` helpers use only Python's standard library. The
`skill-creator` frontmatter scripts use the bundled pure-Python PyYAML 6.0.1
modules and license; they do not require system PyYAML or a pip install.

The `web_*` tools are thin adapters over `github.com/jahrulnr/searchwire`
`Searcher.Tools()` — searchwire owns the tool contract (name, description,
input schema, execution) so NusaLLM, its MCP server, and any other host share
one definition. `ToolDef.Run` accepts `call.Input()` verbatim and returns a
JSON document that becomes the raw tool text. Searchwire's `answer` tool is
deliberately not registered: it delegates answering to a provider-side model,
while here the bound model should reason over raw search/fetch output itself.
Credentials resolve from searchwire's environment conventions
(`BRAVE_SEARCH_API_KEY`, `GITHUB_TOKEN`, …); every configured secret is
redacted from tool errors by the library.

The `web_*` tools stay one stable surface while their backend is chosen
per turn: `runTurn` binds the resolved gateway's identity into the tool
context (`service.WithWebProvider` — `Kind` is the gateway id, `Endpoint`
/`APIKey` its resolved credentials). When `Kind` is `omniroute` (or its
`9router` alias), the tool prefers the router's own APIs —
`POST {endpoint}/search` and `POST {endpoint}/web/fetch` — with the
gateway's bearer key, normalizing the response into the same JSON document
searchwire emits so the model sees one output contract. Any OmniRoute
failure (missing fetch-provider credentials, transport error, empty
results) falls back to searchwire in the same call, so a router-bound
gateway never makes web tools worse than the generic path.

When `Kind` is `opencode` (or `opencode-go`), `web_search` prefers the
gateway's own Responses API — `POST {endpoint}/responses` with the
`web_search` server tool and the turn's bound model — which works on both
the Zen (`/zen/v1`) and Go (`/zen/go/v1`) lanes since the path resolves
relative to the gateway endpoint. The response's `web_search_call`
actions, `url_citation` annotations, and grounded answer are normalized
into the search document. Models behind oa-compat lanes silently drop
non-function tools, so a response recording zero `web_search_call` items
fails loudly into the searchwire fallback rather than passing an
ungrounded answer off as a search. OpenCode has no fetch API, so
`web_fetch` always stays on searchwire — as does `web_search` when the
bound `Model` is absent.

Every document carries a `backend` field (`omniroute`/`opencode`/
`searchwire`) plus a bounded `backendFallback` note when the preferred
backend failed. Provider kinds with only model-mediated *answer* surfaces
(OpenRouter/OpenAI) resolve to searchwire directly: no intermediary model
answers on the agent's behalf.

Searchwire truncates over-limit fetch text and writes the FULL extracted
text to a `tempFile` under its temp dir — a path the turn's workspace-
confined `read_file` cannot reach. The adapter therefore re-spills that
artifact into the conversation's `ToolOutput` store (the same reservation
`exec` uses) and rewrites `tempFile` to the reachable reference, removing
the original. Likewise any web document over the call's output budget is
spilled whole and returned middle-truncated with a `full output:` pointer
— matching the exec contract. The searcher is constructed with
`MaxResponseBytes` under the output budget so truncation plus an artifact
triggers consistently instead of silent mid-document elision.

The four file tools receive the same `repository.Workspace` at construction;
`cmd/nusallm/wire.go` also passes the delivery's workspace root as their
fallback root (the REPL's cwd, `serve`'s owner home).
The local repository opens `os.Root` per call so every requested path resolves inside the workspace,
traversal (`..`) and symlink escapes included — except `read_media`,
which additionally accepts absolute host paths through `Workspace.OpenHost`
(an agent that wrote a screenshot to `/tmp` names it directly). `read_media` and
`/attach`-style resolution share the same discipline: sniff the first 512
bytes for `http.DetectContentType` (a `.csv` *is* text and is refused — use
`read_file`), enforce `wire.MaxAttachmentBytes` from the stat rather than a
read, then hash the content through the opened handle and pin the
`Attachment` with that sha256. The result carries a `file://` reference the
wire adapter reopens and re-verifies at send time, so a swapped file degrades
to a placeholder instead of sending unchecked bytes. For references outside
the workspace root the pin is mandatory — `readLocal` only opens a host
path when a sha256 was recorded by the producing tool, so an unmediated
`file://` escape stays rejected. How those attachments
ride back to the model — a labeled synthetic user message after the tool
block — is covered in the completion adapter section.

### `exec` — terminal execution

`tool/terminal` receives a `Runner` port and conversation-owned output
repository. Its local runner uses `sh -c` on Linux (own process group via `Setpgid`, group
SIGKILL on cancel — `exec_linux.go`) and `cmd /C` on Windows (`exec_windows.go`,
which can orphan grandchildren since Windows has no portable group kill). It
is inherently unsandboxed — there is no root to confine it to — so the schema
permission and the application turn are the entire gate. Timeout defaults to
120s, caller-settable up to 600s; `cmd.WaitDelay` (5s) caps how long `Wait`
blocks on pipes inherited by stray grandchildren. The optional `stdin` input
pipes one-shot bytes to the process's stdin (scripted answers, `sudo -S`);
interactive work is delegated to `exec_session`/`write_stdin` below — `exec`
itself deliberately stays one-shot. The child environment is
inherited from the daemon — the service unit pins the installer's login
`PATH` plus the stable user bin dirs, so agent commands resolve the same
toolchain the user's shell does.

stdout and stderr share one `outputSink`, so the merged stream keeps arrival
order and every `Write` is also a preview chunk to `emit`. Memory stays
bounded by the call's `OutputBudget`: while output fits, it buffers outright;
the first write past budget asks the injected conversation-owned
`repository.ToolOutput` spill reservation to open a file at
`<storage.dir>/conversations/<id>/tool-output/nusallm-exec-<random>-<sanitized call id>.log` (chosen before
start — the random name gives a reused call ID a distinct file; exclusive
creation fails on collision rather than overwriting a live marker), freezes
the buffer as the head region, and keeps a rolling tail while the stream
continues to disk — capped at 32 MiB. The returned text is a head (~60%) +
tail excerpt joined by a `[... N bytes omitted — full output: <path> ...]`
marker — `spilled output (truncated at 32 MiB): <path>` when the file hit the
cap, `spill to <path> failed: <err>` when the write itself failed — cut on
rune boundaries by `ToolCall.OutputWithMiddleTruncateWindow`; the adapter
supplies bounded stream samples and the spill reference. The exit status (`(exit 3)`, `(killed: …)`,
signal deaths) is appended as a suffix so it lands inside the tail and at the
end of the spill file. A spill-write failure degrades to head+tail with the
error in the marker. The spill file remains available until the user deletes
its conversation; repository deletion removes the whole conversation directory,
including tool output. No age- or process-based sweep removes live history's
referenced files.

### `exec_session` / `write_stdin` — terminal sessions

`exec_session` opens a persistent terminal session through the
`service.TerminalSessions` registry (`tool/terminal/session_store.go`) — one
instance shared across conversations, keyed by conversation id (resolved from
the turn's ctx) because tools are constructed once for all rooms. Unlike
`exec`, the child is spawned without `exec.CommandContext`: a session
outlives the call that opened it, so `Terminate` — not a job ctx — is the
only reaper. The session's reader and waiter goroutines are likewise
detached; ctx cancellation inside `Collect` returns the output gathered so
far and leaves the process running, which is exactly how an interrupted turn
keeps background work alive.

The registry registers the session before the first poll (store-before-yield)
so a racing `TerminateConversation` already sees it; conversely, if
`exec_session`'s ctx dies before the response is built, the tool kills the
session through the store itself — an id that never reached the model would
be an unkillable orphan. `job.Delete` calls `TerminateConversation` for a
removed room and `ConversationApp.Shutdown` calls `TerminateAll`; a room caps
at 64 sessions with LRU eviction preferring exited entries and protecting
the 8 most-recently-touched (mirroring codex's process store).

Two backends share the session contract: pipes (default) run `sh -c` under
`Setpgid` with merged stdout/stderr and a *closed* stdin — the child reads
EOF immediately, so a blocked `read` can never wait on input no call may
send (codex's `stdin_open=false` for non-tty); `tty:true` allocates a real
PTY on Linux via `/dev/ptmx` + `TIOCSPTLCK`/`TIOCGPTN` ioctls, spawning the
child `Setsid` + `Setctty` so `isatty` probes (password prompts on
`/dev/tty`, REPLs) answer true and stdin stays open for `write_stdin` —
BSD/Windows refuse `tty` with a clear error. Every session inherits the
daemon env with the interactive-unfriendly keys scrubbed — codex's set:
`TERM=dumb`, `NO_COLOR=1`, `COLORTERM=`, `PAGER`/`GIT_PAGER`/`GH_PAGER=cat`,
`C.UTF-8` locale, `NUSALLM_CI=1` — so output stays textual and
deterministic. Output lands in a 1 MiB rolling buffer drained per `Collect`
("chars since last poll") with an `... N bytes omitted ...` marker when the
cap dropped bytes; after exit the collector waits a short window for the
reader's EOF so trailing output is not lost.

`write_stdin` resolves `session_id` through the store's `Interact`, which
holds the session's interaction semaphore for the whole write+poll —
concurrent calls on one session share the draining buffer, so they queue
rather than race — and re-checks the registry by identity once the lock
lands, so an id reaped or reused while the call queued fails with
"not found" instead of landing on the wrong process (codex's
interaction_lock + ptr-eq guard). The same serialization means a `\x03`
queued behind an in-flight call waits it out — a long empty poll can hold
the semaphore up to the 300 s cap before the interrupt lands. Inside the
interaction, `chars` writes
to the tty master — pipes sessions reject it with "stdin is closed for
this session; rerun exec_session with tty=true" — while an empty `chars`
is a pure poll with a longer default wait. A single `\x03` is not written
but routed to `Interrupt`, which sends the byte through the pty line
discipline on TTY sessions and `SIGINT`s the process group on pipes — it
is accepted on both backends. Both tools return the same header contract —
`Process exited with code N` or `Process running with session ID N`, then
`Wall time` and `Original output: N bytes` (the pre-truncation count —
codex's `original_token_count`, in the bytes unit our budgets use) — so the
model always knows whether another poll is meaningful and the scale of
what was collected. Yield bounds differ by call shape: open waits default
10 s (clamp 250 ms–30 s), writes default 250 ms (clamp 250 ms–30 s, with a
100 ms settle after a tty write so the reaction lands in the same window),
empty polls default 5 s (clamp 5 s–300 s).

### `summary` — the compaction contract

The summary contract (`infrastructure/service/tool/summary`) is not a
user-or-agent tool in the usual sense: its `text` field *is* the result,
and `Execute` merely extracts it. The compaction job
(`application/job/compact.go`) builds a scratch conversation containing
only this tool's schema and drives `Send` until the model answers through
it, accepting plain text as a fallback. It is never registered in a
driver's `Tools` map — the composition root injects it through
`Deps.SummaryTool` like the terminal-session port.

**Prefix-bound replay artifacts.** Signed thinking cannot survive a fold.
The summary scratch replaces the tool catalog and the checkpoint replaces
the sealed prefix, so the job consults the adapter's optional
`dservice.CompactionSanitizer` twice: before building the scratch request
and on the retained tail it persists. `textmessages` drops
`thinking`/`redacted_thinking` blocks (keeping text/tool_use placeholders),
and `textcompletion` drops `completion.reasoning_details` while preserving
plain reasoning text. `textgeneratecontent` implements no sanitizer because
Gemini's stateless `generateContent` requires thought blocks to be sent
back; `textresponses` currently keeps encrypted reasoning. The repository
overlays only the sanitized `optionalContent` onto the stored tail lines —
every other field, plus all tool-output patch lines, remains byte-exact.

## `service/workspace` — the sandbox

`workspace.Root` holds an injected `repository.WorkspaceSession` (`root.go`):
`Open` validates the configured root through the repository, while `Path`,
`FS` (for listing/completion), `CreateFile`, and `Close` delegate to that
session. The local repository owns `os.OpenRoot` and regular-file checks.

`ResolveAttachment` (`attachment.go`) is the attachment path: validate the
requested path via `valueobject.NewWorkspace` (canonical slash-separated
form — rejects `\`, absolute/drive/UNC prefixes, and `..` segments), open
through `WorkspaceSession.OpenRegular`, sniff the media
type, reject text modalities, enforce the modality size limit, and pin the
bytes by sha256 so the later wire-time read resolves only what the user
selected. Materialization at send time — `file://` reopen + base64, `data:`
passthrough, `https://` passthrough — is `wire.ResolveURI`'s job through an
injected `wire.MediaRoot`, documented
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
  `services` map at the composition root — `cmd/nusallm/wire.go` today. Reuse
  `service/wire` for SSE framing, media resolution, and status mapping; keep
  provider quirks behind `completion.*`-style adapter-namespaced
  `optionalContent` keys, never new domain fields.
- A new tool is a package under `tool/` whose constructor validates its
  `ToolSchema` and receives an injected storage port if file-bound; wire it into the
  `tools` map in `cmd/nusallm/wire.go`. Permission is declared on the schema and enforced
  by the turn — never inside `Execute`.
- A new slash command is `command/<name>` implementing `SlashCommand` (plus
  `SlashCommandCompleter` if arguments complete), wired in
  `delivery/cli/commands/wire.go`, depending only on `SlashSession`.

See [./extending.md](./extending.md) for a worked example and
[./cli-guide.md](./cli-guide.md) for how delivery consumes the command and
workspace surfaces.
