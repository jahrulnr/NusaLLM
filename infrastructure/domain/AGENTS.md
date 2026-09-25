# AGENTS.md — infrastructure/domain

Provider-agnostic LLM domain layer. Deliberately **thin and pure**: business
rules and normalized state only — no I/O, no wire formats, no provider
vocabulary in field names.

## Layout

| Package | Contents |
|---|---|
| `aggregate/` | `Conversation` (dialogue state + route/modelID refs), `Gateway` (endpoint surface: style, endpoint, apiKey, model catalog, stream flag) |
| `entity/` | `Message`, `ToolCall`, `Model` (request-scoped route descriptor + gen params + catalog metadata) |
| `enum/` | Closed string sets, each with `String()` + `Parse*()` roundtrip for persistence |
| `event/` | `StreamEvent` + `LLMEventHandler` — streaming sink for `LLMService.Send` |
| `repository/` | Ports only: `Conversation` (Save / AppendMessage / UpdateToolCallOutput / Info / List / Load / LoadAt / Compact / Delete), `Gateway` (Save / Load / List / Delete), `SecretStore` (Get / Set / Delete) |
| `service/` | `LLMService` port — one `Send` attempt; fallback/routing is application-layer work |
| `valueobject/` | `Usage`, `Pricing`, `ToolSchema`, `Attachment` |

## Hard rules

1. **No I/O in domain.** File reads, HTTP, stat — all adapter work.
   `Attachment` stores a URI only; existence is checked at resolve time by
   the adapter (a file may disappear between attach and send).
2. **Strict aggregate references.** `Conversation` holds `route`/`modelID`
   strings, never embedded `Gateway`/`Model` objects. Resolve via the
   `Gateway` repository — keeps credentials out of conversation records.
3. **Provider artifacts → `optionalContent map[string]string`.**
   Adapter-namespaced keys (`gemini.thought_signature`,
   `deepseek.reasoning_content`, `openai.encrypted_content`). Never add a
   dedicated domain field for one provider's replay artifact; the domain
   never interprets values.
4. **`Usage.inputTokens` is UNCACHED input.** Provider layouts differ:
   disjoint counters (Anthropic) → `NewUsage`; inclusive total + cached
   subset (OpenAI, Gemini) → `NewUsageFromTotal`. Never map an inclusive
   total into `inputTokens` — it double-counts in `TotalInput()`.
5. **Tool outputs are untrusted evidence.** `SetOutput` wraps
   `<untrusted_tool>` envelopes and neutralizes embedded delimiter prefixes
   (including attribute-bearing variants). Runtime output never bypasses
   `SetOutput`; hydration uses `SetStoredOutput` verbatim.
6. **Unset ≠ zero.** Generation params are `*T` (nil = omit from wire);
   `enum.Effort` uses `IsUnset()` (`""`/`default` → send nothing).
7. **Fail-closed vs fail-open is deliberate.** `ToolSchema.CallableBy*`
   fails closed (disabled/unknown permission → deny). `Model.Accepts*` fails
   open (empty modality list = undeclared catalog, provider is the final
   validator).

## Conventions

- Style lives on `Gateway` (the wire surface), never on `Model` or
  `Conversation` — one model id may be spoken through multiple paradigms
  depending on route (OpenRouter translation).
- `Model.route` is optional: empty for catalog entries, which resolve
  through their owning `Gateway`.
- Hydration = `New*` constructor + setters (`SetCallAt`, `SetStoredOutput`,
  `SetOptionalContent`, ...). No reconstitute constructors.
- Slice/map getters share internals by design — internal domain, no
  defensive copies. `Conversation.Last()` pointers detach on the next
  `Append` (realloc); re-fetch after appending.
- Two read paths, by consumer: `Load` = the LIVE context (active segment —
  what the model sees) for the send path; `Info`/`List`/`LoadAt` =
  lightweight projections and ranged lazy-load over the global logical
  history for UIs.
- Compaction seals history: `Conversation.Compact(summary, dropBefore)`
  mirrors the repo's `Compact`, which archives the dropped prefix into a
  numbered segment (`0.jsonl`, `1.jsonl`, …) and restarts `active.jsonl`
  with the summary. Never pick a boundary that splits a tool-call/result
  pair — orphaned results are rejected by providers.
- Stored attachments must be references (`file://`, `https://`) — `data:`
  URIs are a wire-time form adapters materialize at send.
- Credentials are references too: persisted `apiKey` is `"env:NAME"` or
  `"secret:<key>"` (resolved through `SecretStore` at load), never committed
  plaintext. Non-secret config stays plain — secrets only in the store.
- `EstimatedTokens()` is a chars/4 budgeting heuristic (+ flat 1000 per
  attachment — providers bill media by size/duration, not chars).
  `NeedCompaction(budget)` takes the budget as an argument — model switches
  must be reflected immediately, so never store the budget on the aggregate.
- `AddUsage` accumulates per-request reports into the conversation total.
- Errors share one contract (`infrastructure/error`): `AppError` = HTTP
  status + frontend-safe `Message` + internal `Cause`. Consumers route via
  `apperror.As(err).StatusCode()`. LLM call conventions: 401/403 = dead
  credentials, 429 = rate-limited (try another route), 413 = context
  overflow (compact), 502/503/504 = upstream unreachable.
- Enums serialize as their string value; persist via `String()`, restore
  via `Parse*`.
- English for code, comments, and docs.

## Verify

```sh
gofmt -w . && go build ./... && go vet ./... && go test ./...
```

Fixtures for media tests live in `test/media/` (image.png, video.mp4,
audio.m4a) — pinned by `TestMediaFixtures`.
