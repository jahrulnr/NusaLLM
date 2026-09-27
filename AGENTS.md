# AGENTS.md - NusaLLM

## Architecture and ownership

Before changing behavior, identify the rule's owner and inspect that layer's
`AGENTS.md`. The package's location does not make a new rule appropriate there.

- `infrastructure/domain`: provider-independent state, invariants, and
  decisions about what a Conversation, Gateway, Message, Model, or ToolCall
  means. A rule that should hold for every caller belongs here.
- `application`: use cases and their sequencing: select a route, run a turn,
  compact, execute a permitted tool, coordinate persistence and events.
  It may enforce domain decisions, but must not invent a second definition
  of them.
- `infrastructure/repository`: storage format, hydration, atomic writes,
  projections, and other persistence mechanics.
- `infrastructure/service`: provider protocol translation, I/O, and tool
  execution. Provider-specific behavior stays with its adapter.
- `infrastructure/config`, `infrastructure/worker`, `infrastructure/prompt`,
  `infrastructure/error`, `infrastructure/redact`, `cmd`: supporting mechanisms
  and composition, not homes for conversation or gateway policy.

When a change spans layers, put each decision at its owner and update its
callers. If ownership is unclear, trace the behavior from use case through
domain port to adapter before editing. Do not place a rule in an outer layer
merely because that is where the current call site happens to be.

## Method boundaries

Keep a coherent single-use flow in its calling method, even when the method
is long. Length alone is not a reason to extract a helper. A helper called
once that is not an injected dependency should normally stay inline,
especially if it passes much of the caller's state through parameters or
forces the reader to jump files to follow one flow.

Extract only when the new boundary has an independent reason: a reused
operation, an interface/DI seam, a separately testable algorithm or
invariant, or a named phase whose isolation materially clarifies the flow.
Do not create a helper solely to make a method shorter. Before extracting,
state the reason in the change description; if there is none, keep it inline.

## Breaking Changes Policy

This project does not maintain backward compatibility with previous implementations.
- Treat the current codebase, schema, API, and behavior as the single source of truth.
- When refactoring, prefer a clean breaking change over compatibility layers, adapters, shims, fallbacks, migrations, or deprecated APIs.
- Do not preserve old behavior solely for compatibility with previous versions or implementations.
- Do not introduce versioned APIs, schemas, formats, or code paths such as v1, v2, legacy, or for compatibility unless explicitly required by the current design.
- Do not add compatibility code for historical data, old schemas, old APIs, or previous implementations.
- When a design change makes existing callers, data, or interfaces incompatible, update the affected code and tests to the new design instead of supporting both versions.
- Remove obsolete code rather than keeping it as a fallback or compatibility path.
- Prefer one clean implementation over multiple code paths supporting old and new behavior simultaneously.
- Tests should validate the current intended behavior, not preserve historical behavior that is no longer part of the design.

## Refactoring Principle

When changing an existing implementation:
> Break old contracts when necessary. Update consumers to the new contract. Do not preserve the old contract for compatibility.

Unless explicitly instructed otherwise, assume that backward compatibility is not a requirement.
