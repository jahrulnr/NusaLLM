# NusaLLM documentation

This directory is the long-form companion to the [README](../README.md):
the README tells you what NusaLLM is, these documents tell you how it's
built and how to work on it. The `AGENTS.md` files scattered through the
tree remain the binding rules for each layer — these docs explain, they
don't govern.

## Reading order

New to the codebase? Start at the top and stop when you've had enough:

1. **[Architecture](architecture.md)** — the layered design, who owns
   which rule, and the anatomy of one turn from keystroke to provider
   response. Read this before changing anything.
2. **[Domain model](domain-model.md)** — the aggregates, entities, enums,
   and value objects, plus the invariants that define them.
3. **[Extending](extending.md)** — recipes for adding a wire-style
   adapter, a tool, or a slash command, each traced from a real file.

## Reference

- **[CLI guide](cli-guide.md)** — installing, `onboard`, the `gateway`
  and `config` subcommands, and every slash command in the REPL.
- **[Persistence](persistence.md)** — what lands on disk: the JSONL
  conversation format, the gateway store, the SQLite secret vault, and
  the diagnostic log.
- **[Adapters](adapters.md)** — the `infrastructure/service/` layer:
  wire styles, the completion adapter, tools, and the workspace sandbox.

## Honest status

These docs describe the code as it exists. Where something is scaffolded
but not implemented — the `responses`/`messages` wire styles, the HTTP
and WebSocket deliveries, most `service/core` capability dirs — the docs
say so rather than document intent.
