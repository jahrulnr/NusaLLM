# AGENTS.md — infrastructure/repository

Repositories implement the ports in `infrastructure/domain/repository`.
They own storage records, serialization, hydration, query projections,
atomic writes, and recovery from partial writes. They do not decide what a
valid conversation or gateway means; use domain constructors and methods
for that.

- `conversation/` stores metadata and JSONL history. `Compact` and
  `RewriteFrom` perform segment surgery while preserving the domain's
  message semantics. `rewriteCut` currently repeats the editability check
  in `Conversation.RewriteFrom`; keep them aligned when touched and move
  shared decision logic into domain rather than adding another variant.
- `gateway/` stores gateway/model records, resolves key references for live
  loads, and offers raw-ref inspection for management. Keep key material out
  of read projections.
- `secret/` and `log/` are storage implementations, not policy owners.

Boundary validation may protect a filesystem path or storage format (for
example, rechecking an ID before joining a path). Do not turn that check
into a competing business rule. When a persisted field changes, update
record encoding/decoding and the affected application input/projection.

Keep an operation's read/transform/write path together unless a step is
reused or has an independent storage contract. File length is not a reason
to split a one-use operation into pass-through helpers.
