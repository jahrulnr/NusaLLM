# AGENTS.md — infrastructure/repository

Repositories implement the ports in `infrastructure/domain/repository`.
They own storage records, serialization, hydration, query projections,
atomic writes, and recovery from partial writes. They do not decide what a
valid conversation or gateway means; use domain constructors and methods
for that.

Storage I/O is centralized here, including workspace-scoped file access,
conversation-owned spill output, config documents, CLI input history, and
directory metadata for workspace selection. These capabilities are injected
into their consumers; repository implementations may be swapped without
changing tool or use-case logic. Network transports and command execution are
not storage and remain outside this tree.

- `conversation/` stores metadata and JSONL history. `Compact` and
  `RewriteFrom` perform segment surgery while preserving the domain's
  message semantics. `rewriteCut` applies the shared editability rule
  (`entity.EditableMessage`) on storage tuples before hydration — the
  same rule `Message.Editable` delegates to. Extend it there rather
  than adding a repository-side variant.
- `gateway/` stores gateway/model records, resolves key references for live
  loads, and offers raw-ref inspection for management. Keep key material out
  of read projections.
- `secret/` and `log/` are storage implementations, not policy owners.
- `config/`, `history/`, and `filetree/` implement config documents, CLI
  input history, and directory metadata access.
- `workspace/` owns workspace-scoped file access and path confinement.

Boundary validation may protect a filesystem path or storage format (for
example, rechecking an ID before joining a path). Do not turn that check
into a competing business rule. When a persisted field changes, update
record encoding/decoding and the affected application input/projection.

Keep an operation's read/transform/write path together unless a step is
reused or has an independent storage contract. File length is not a reason
to split a one-use operation into pass-through helpers.
