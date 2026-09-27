# AGENTS.md — application/command

## Role

This package defines the **slash-command session surface**: the use-case port
that REPL and other UIs share when running `/` commands. It exposes
conversation and gateway operations, workspace file access, session bindings,
and sanitized output — without leaking concrete application structs or
repository implementations to command handlers.

## Ownership

| Belongs here | Belongs elsewhere |
|--------------|-------------------|
| What a command session may ask for in one place (list, resume, edit, attach, export, …) | Parsing input lines, palettes, and TUI state — delivery |
| Facade wiring over existing conversation/gateway use cases | Per-command behavior and registry dispatch — infrastructure service command |
| Port types for bindings, workspace, and output | List-title and message editability rules — domain aggregate and entities |

When adding a capability commands need, extend the session port and facade
here first. Implement the slash command in a dedicated service package and
wire it from the CLI composition root.

## Constraints

**Must**

- Keep handlers depending only on the session port defined in this package.
- Reuse domain decisions (titles, editability, tool permission) instead of
  re-stating them in command code.
- Treat this layer as coordination and delegation — not a second home for
  provider or filesystem details.

**Must not**

- Import delivery or TUI packages from here.
- Embed transport-specific argument shapes in the port; delivery passes text,
  service commands parse it.
- Duplicate conversation or gateway policy that already lives in domain or
  sibling application packages.

## When you change behavior

1. Read root `AGENTS.md` and `application/AGENTS.md` for layer boundaries.
2. Decide whether the rule is domain, application use-case, command handler,
   or delivery — put it at the owner; update callers, not mirrors.
3. If both the REPL and a command package touch the same user flow (for
   example message edit), keep usage text and invariants aligned; inspect
   both sides before merging.
4. Prefer observable tests at delivery or application boundaries over
   documenting symbols in this file.

## Source of truth

Package layout and type names change. **Read the Go sources in this directory**
for the current port, facade constructor, and dependencies. For command
implementations and shared completion helpers, read the command service tree
and its internal helpers. Do not treat this document as a catalog of files or
identifiers.
