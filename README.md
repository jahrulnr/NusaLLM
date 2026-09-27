<div align="center">

# NusaLLM

**Every model. One client.**

A provider-agnostic conversation engine — today it meets you in the
terminal, tomorrow wherever you want the same conversations to follow.

</div>

---

Most AI tools ask you to move into their world: a web app, an IDE, a
subscription. NusaLLM takes the opposite bet — one engine that owns your
conversations, tools, and history, with interfaces that come to you. The
first of them is a terminal client. It won't be the last.

## Why it feels different

**Bring your own provider.** Any endpoint becomes a route — OpenRouter,
OpenAI, a model running on your own machine. The wire style is a detail
of the route, not a cage: new provider protocols plug in as adapters,
not rewrites. Keys go into a secret store, never into plaintext config.

**An agent, not a chat box.** Models can read, write, and patch files in
your workspace, run terminal commands, and look at media you attach —
all sandboxed to the directory you launched from.

**Sessions that survive.** Conversations persist to disk. Compact a long
history when it bloats, or edit an old message and regenerate the turn
from there.

**Rendered, not raw.** Replies stream in as real markdown — headings,
code blocks, tables — not a wall of monospace text.

**One binary, no toolchain drama.** Pure Go, SQLite with zero cgo. `go
build` produces the whole product.

## Sixty seconds in

```sh
go build -o nusallm ./cmd/nusallm
./nusallm onboard    # pick a provider, paste a key
./nusallm            # you're in
```

Inside the session: `/route` and `/model` switch providers mid-chat,
`/attach` drops files and images into context, `/edit` rewrites history,
`/usage` shows what a turn cost. `/help` lists the rest.

## The name

*Nusa* — from *Nusantara*, the Indonesian word for an archipelago. Many
islands, one sea. Many providers, one client.

<div align="center">

Built in Go · Conversations, keys, and logs live on your machine

</div>
