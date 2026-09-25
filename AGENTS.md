# AGENTS.md - llm-base

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
