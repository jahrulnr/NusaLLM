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

## WebUI API (HTTP + WebSocket implemented)

The `api/` directory holds the **contract** for the browser WebUI — a
local, single-user surface over Gin HTTP plus one multiplexed
WebSocket. Phases 1–4 are live: `nusallm serve` binds `127.0.0.1` and
serves every `GET` route, the mutations (`PATCH`/`PUT`/`DELETE`/`POST`),
and the WebSocket turn commands (`turn.send`/`turn.edit`/`turn.cancel`/
`turn.watch` with bounded-journal replay) under the security envelope.
The Swagger UI is still a design document only.

- **[API contract](api/contract.md)** — trust model, security rules,
  endpoint surface, error model, implementation work, and the acceptance
  matrix.
- **[OpenAPI spec](api/openapi.yaml)** — OpenAPI 3.1 for every HTTP
  endpoint, including the `GET /api/ws` upgrade stub.
- **[WebSocket protocol](api/websocket.md)** — the normative frame spec
  for `turn.send`/`turn.edit`/`turn.cancel`/`turn.watch` and the
  `turn.*` event stream.

## Honest status

These docs describe the code as it exists. Where something is scaffolded
but not implemented — most `service/core` capability dirs and the
Swagger UI — the docs say so rather than document intent. The
`docs/api/` documents describe a contract whose HTTP surface and full
WebSocket turn lifecycle (`turn.send`/`turn.edit`/`turn.cancel`/
`turn.watch` with journal replay) are implemented: they say
"implementation work" wherever the current engine cannot yet supply
what they specify.
