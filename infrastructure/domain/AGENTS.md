# AGENTS.md — infrastructure/domain

Provider-agnostic LLM domain layer. **Thin and pure**: business rules and
normalized state only — no I/O, no wire formats, no provider vocabulary in
field names.

## Rule ownership

Put a provider-independent invariant on the aggregate, entity, value object,
or domain service that owns the state. Callers must use that decision rather
than reimplementing it in application jobs, repositories, or adapters. For
example, `Conversation.RewriteFrom` decides whether a message may be edited;
`ToolSchema.CallableByAgent` decides permission; `Usage.Add` defines
accounting. Keep ordering, retries, persistence, and event delivery in the
application layer; keep transport and storage constraints in their adapters.
Do not move a rule into domain merely because several lines mention it: it
must describe the model regardless of use case or provider.

Keep a coherent invariant in its owning method. Do not split it into
single-call helpers just to shorten a method; extract only for reuse, a
distinct invariant, or a meaningful interface boundary.

## Layout

| Package | Contents |
|---|---|
| `aggregate/` | `Conversation` (dialogue state + route/modelID refs), `Gateway` (endpoint surface) |
| `entity/` | `Message`, `ToolCall`, `Model` |
| `enum/` | Closed string sets, each with `String()` + `Parse*()` |
| `event/` | `TurnEvent` + `TurnEventHandler` — per-turn streaming sink for `TextModelService.Send` |
| `repository/` | Ports only — `Conversation`, `ConversationRepairer`, `Gateway`, `SecretStore`, `Logger` |
| `service/` | `TextModelService` port — one `Send` attempt; fallback/routing is app-layer work |
| `valueobject/` | `Usage`, `Pricing`, `ToolSchema`, `Attachment` |

## Hard rules

1. **No I/O in domain.** File reads, HTTP, stat — all adapter work.
2. **Strict aggregate references.** `Conversation` holds `route`/`modelID`
   strings, never embedded `Gateway`/`Model`. Resolve via the `Gateway`
   repository — keeps credentials out of conversation records.
3. **Provider artifacts go to `optionalContent`.** Adapter-namespaced keys
   (`gemini.thought_signature`, `deepseek.reasoning_content`). Never add a
   dedicated field for one provider's replay artifact; domain never
   interprets values.
4. **`Usage.inputTokens` is UNCACHED input.** Disjoint counters → `NewUsage`;
   inclusive total + cached subset → `NewUsageFromTotal`. Never map an
   inclusive total into `inputTokens` — it double-counts.
5. **Tool outputs are untrusted evidence.** `SetOutput` stores raw text and
   fixed latency; `NeedTruncate` and the head/tail/middle getters own the
   context budget. `OutputWithUntrustedTag` adds a bounded evidence envelope
   only for provider replay. Hydration restores raw output and latency via
   `SetStoredOutput`. Tool-produced media ride as `outputAttachments` — stored
   references resolved at send time, gated per-request by the bound
   model's `AcceptsInput` (non-support degrades to a text placeholder).
6. **Unset ≠ zero.** Generation params are `*T`; `enum.Effort` uses
   `IsUnset()` — absent means "don't send", not 0.
7. **Fail-closed vs fail-open is deliberate.** `ToolSchema.CallableBy*`
   fails closed; `Model.Accepts*` fails open (provider validates).
8. **Secrets are references, never plaintext.** Persisted values are
   `"env:NAME"` or `"secret:<key>"`, resolved via `SecretStore` at load.
9. **One error contract.** Return `apperror` — public `Message`, internal
   `Cause`, HTTP `StatusCode` for routing (413 = context overflow → compact,
   429 = rate-limited → fallback, 401/403 = dead credential).
10. **Diagnostics via `Logger`** — verbose JSONL sink; entries are scrubbed
    by `infrastructure/redact` before write.
11. **Execution environment is a room binding.** `Conversation.Environment()`
    identifies the workspace/command driver selected for this room. The
    application resolves the registered driver per turn; an unavailable
    environment must never silently fall back to local.

## Conventions

- Style lives on `Gateway` — one model id may be spoken through multiple
  paradigms depending on route.
- Hydration = `New*` + setters (`SetCallAt`, `SetStoredOutput`, …). No
  reconstitute constructors.
- Two read paths by consumer: `Load` = live context (what the model sees);
  `Info`/`List`/`LoadAt` = projections and ranged history for UIs.
- Compaction seals history into numbered segments; never split a
  tool-call/result pair at the boundary.
- Editing is `RewriteFrom`: user messages only, content patched and every
  message after it dropped (the caller regenerates the turn). A target in
  a sealed segment repromotes that era — earlier segments merge back into
  the live tail, later segments and stale summaries are discarded.
- Stored attachments are references (`file://`, `https://`) — `data:` URIs
  are wire-time, adapters materialize at send. Existence is checked at
  resolve time, never in domain. An attachment may carry a `sha256`
  content pin recorded by its producer; adapters verify resolved bytes
  against it and degrade on mismatch — pins bind local references only.
- `EstimatedTokens` is a budgeting heuristic; `NeedCompaction(budget)` takes
  the budget as an argument — model switches must apply immediately.
- Slice/map getters share internals by design — no defensive copies; re-
  fetch `Conversation.Last()` after `Append`.
- English for code, comments, and docs.

## Verify

```sh
make fmt-check && make test && make test-web
```

Media fixtures live in `test/media/` — pinned by `TestMediaFixtures`.
