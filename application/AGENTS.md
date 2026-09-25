# AGENTS.md — application

This layer coordinates use cases through domain objects and ports. Read the
root and `infrastructure/domain/AGENTS.md` before changing behavior here.

## Ownership

- `conversation/` brokers calls to per-conversation workers. Queue order,
  preemption, cancellation, and worker lifecycle belong here with `worker/`.
- `job/` runs Send/Edit/Delete and the turn/compaction flows. It chooses
  routes, calls ports, persists each result, executes tools, and emits events.
- `gateway/` handles gateway management input, key-reference writes, and
  sanitized read projections.

Keep domain invariants in the domain: for example, whether a message is
editable, whether a tool schema permits an agent call, or how usage adds up.
Application code may call those decisions and map failures to use-case
errors; it must not duplicate them with independent conditions. If the same
rule must hold outside a specific use case, move the decision to a domain
method and update the callers.

Existing code is not proof of correct ownership. For example,
`gateway/buildModel` checks negative model parameters at the input boundary;
if those limits become model-wide rules, define them on `entity.Model` and
have every caller use the same decision.

Use application policy for order and coordination: when to compact, which
gateway/model to use, when to persist, how to handle a canceled turn, and
which events to emit. Keep provider request/response fields and filesystem
format out of this layer. Dependencies should be domain ports or explicit
use-case collaborators; do not add interfaces just to enable a one-use
helper.

## Method shape

Preserve the readable sequence in `Execute`, `runTurn`, and management
methods. Do not split a single-use phase solely because the method grows.
Extract when the operation is shared (for example, turn execution across
Send and Edit), needs its own testable contract, or is a real dependency
boundary. Prefer a small local block when no independent contract exists.

When changing a use case, check the domain rule, relevant port, adapter,
and tests for the same behavior. Test observable ordering and state, not
the presence of a helper.
