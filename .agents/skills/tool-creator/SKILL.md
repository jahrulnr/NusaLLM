---
name: tool-creator
description: >-
  Use when creating, modifying, or reviewing a tool in this repo — exec,
  file, web, terminal-session, or harness-side hydration/record tools like
  the workspace pin. Trigger for "new tool", ToolSchema, ToolPermission
  (agent/system/general), service.Tool, Execute signature, tools[] wire
  list, workspace pin, hydration record, or questions about where a tool
  belongs. Enforces the single tool boundary: tools live under
  infrastructure/service/tool/ and register in cmd/nusallm/wire.go —
  never in application/job, domain, or adapters.
---

# Tool creation — boundary and permission rules

## The one boundary

A tool is a `service.Tool` (`infrastructure/domain/service/tool.go`):
`Schema() valueobject.ToolSchema` + `Execute(ctx, call, emit) (ToolResult, error)`.
Implementations live under **`infrastructure/service/tool/<family>/`** —
`tool/file`, `tool/terminal`, `tool/web`, `tool/workspace`. They are
constructed and registered **only in `cmd/nusallm/wire.go`** into the
environment bundle's `Tools` map.

Never place a schema, executor, or tool catalog entry in:
- `application/job` — it selects and enforces, it does not define
  tools. Even a tool owned by one use case (the compaction `summary`
  contract) lives in `infrastructure/service/tool/summary` and reaches
  the job through `Deps` injection — application code must not import
  concrete service packages (dependency direction), so an ad-hoc tool
  that no driver registers still comes in through the `Deps` port.
- `infrastructure/domain` — it holds the `Tool` port and `ToolSchema`
  value object, no implementations.
- provider adapters — they translate, never invent capabilities.
- `infrastructure/domain` — it holds the `Tool` port and `ToolSchema`
  value object, no implementations.
- provider adapters — they translate, never invent capabilities.

## The catalog is one door

`EnvironmentBundle.ToolSchemas()` derives the sorted catalog from
`bundle.Tools` alone. Every consumer reads that door:

- `runTurn` → `conv.SetTools(driver.ToolSchemas())` — catalog is live
  config reapplied per turn, never trusted from stored state.
- provider adapters → emit `tools[]` on the wire **only** for schemas with
  `CallableByAgent()`.
- the turn's permission gate → iterates `conv.Tools()` for
  `CallableByAgent()`, then the switch evaluates `!permitted` first:
  `error: tool not permitted`, else `!registered` →
  `error: unknown tool <name>`, else `Execute`.
- `Conversation.EstimatedTokens` → counts only agent-callable schemas.

Do NOT append schemas ad hoc at `SetTools` call sites or in query paths —
register the tool in the bundle and the catalog follows. This mistake was
made once; it forks the source of truth.

## Permission is the whole contract

`enum.ToolPermission` decides who may invoke, fail-closed:

| Permission | `tools[]` wire list | Agent may call | System may call | Use |
|---|---|---|---|---|
| `agent` | yes | yes | no | model-callable tools (default) |
| `system` | **no** | no — imitation gets `tool not permitted` | yes | harness records/hydration |
| `general` | yes | yes | yes | tools both sides invoke |

`CallableByAgent()` = agent|general; `CallableBySystem()` = system|general.
Disabled or unknown permission → both false → callable by no one.

Your tool NEVER re-checks permission inside `Execute` — the gate already
ran. A `system` tool's `Execute` is unreachable from model-originated
calls; it exists to satisfy the contract and to answer truthfully if a
system-side path invokes it.

## Hydration / record tools (the `workspace` pattern)

A hydration tool is a real `service.Tool` whose calls the **harness**
writes as completed `TooltypeInjection` records — the model observes a
call+result pair it never made. Rules:

- `ToolPermissionSystem`, registered like any other tool — the name then
  resolves in every room's catalog instead of being an orphan.
- `Execute` must produce output **identical** to what the injection
  record stores (`tool/workspace` returns `workspace changed to <dir>`),
  so the recorded call is literally truthful.
- The producer (`appendWorkspaceContext` in `application/job/turn.go`)
  writes the record; the tool package owns the name, schema, and output
  contract. The application layer may NOT import the concrete tool
  package (dependency direction), so the recorded line is replicated —
  treat the Execute output as the canonical format, document it on the
  tool, and let the tool's tests pin the literal.
- Model imitating the call → `error: tool not permitted`. Correct: the
  tool exists, it is not for the agent.
- Domain record rides `RoleSystem` + `TooltypeInjection`; adapters emit
  the completed pair at tool-output trust (untrusted-evidence envelope).
  Domain role is provenance, not wire role — adapters own the encoding
  decision; a workspace pin must never be encoded as user-authored text
  (that demotes it to the lowest trust plane and reads as a user claim).

## `TooltypeOrigin` vs `TooltypeInjection`

- `origin` — the model emitted the call; the turn executes it through the
  permission gate. `pendingCalls` picks these up.
- `injection` — the application wrote a completed call+result; never
  executed, skipped by the executor loop, encoded on the wire as the
  completed pair it records.

## Execute contract

- `call.Input()` is untrusted JSON — unmarshal to a local struct and
  validate even "required" fields; the model wrote it, not you.
- Return failures as `error` — the turn formats them into the recorded
  output as `error: <msg>` (do NOT pre-prefix `error:` in `ToolResult.Text`
  or it doubles). Only `ctx.Err()` propagates as a turn abort.
- `emit` streams live preview chunks; they are never persisted — only the
  final `ToolResult` lands in history.
- `ctx` carries `dservice.WorkspaceDir` / `ConversationID` /
  `WebProviderFrom` — read them; never widen the `Execute` signature.
- Storage I/O goes through injected `repository` ports
  (`Workspace`, tool-output store) — no `os.*` calls in service adapters.
- Output is untrusted evidence: store raw, let `ToolCall` budget getters
  and `OutputWithUntrustedTag` bound/tag it on the wire.

## Creation checklist

1. `infrastructure/service/tool/<family>/<name>.go` — constructor
   `NewXTool(...) (service.Tool, error)` that validates deps + builds the
   schema. Pick the honest `ToolPermission`.
2. Register the instance in `cmd/nusallm/wire.go`. Room-catalog tools go
   in the driver `Tools` map — nothing else needed (catalog, wire list,
   `/tools`, estimates all follow). A tool one use case consumes ad hoc
   (e.g. `tool/summary`, offered alone inside the compaction scratch
   dialogue) is NOT registered — construct it at the composition root and
   inject it into `Deps`, the same seam `Deps.Terminals` uses.
3. Tests in the tool package: schema name/permission flags + `Execute`
   happy path and failure-as-output. If provenance/wire encoding changed,
   add adapter wire-shape tests under `infrastructure/service/core/text/*/`.
4. Update `docs/extending.md` (permission section) and, if the catalog or
   provenance contract moved, `docs/domain-model.md` plus
   `docs/adapters.md`/`docs/architecture.md` (wire-shape and hydration
   flow).
5. Verify: `make fmt-check && make test && make test-web`.

## Common mistakes (all made once — don't repeat)

- Schema defined in `application/job` or appended ad hoc at `SetTools`
  sites → forks the catalog. Register the tool; the bundle is the door.
- `system` tool left out of `Tools` map → its recorded call name is an
  orphan: no catalog entry (`/tools`, `/api/tools`, `driver.Tool`
  resolution). Imitation still reads `tool not permitted` — the
  `!permitted` branch runs before the `!registered` branch — so the real
  cost is an unresolvable name, not a wrong denial message.
- Rechecking permission inside `Execute` → dead code; the gate owns it.
- Assuming `tools[]` on the wire equals the room catalog → adapters
  filter `CallableByAgent`; system tools ride history only.
- Injection record written without a matching registered schema → the
  call the model observed has no catalog entry.
