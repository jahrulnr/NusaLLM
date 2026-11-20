# NusaLLM WebUI

Browser client for `nusallm serve`, embedded into the binary (`embed.go`)
and served from the same loopback origin as the HTTP API and the `/api/ws`
turn socket. No npm, no build step, no CDN — every dependency is vendored
under `vendor/` (see `vendor/README.md`).

## Stack

- **Preact + htm** (`vendor/preact/htm-preact.mjs`) — components as tagged
  templates, no JSX compile.
- **Tailwind v4 browser build** — utilities compile in the page; theme
  tokens live in `index.html` (`@theme`) and map to the CSS variables in
  `styles/app.css`, which also owns rendered Markdown, syntax colors, and
  third-party widget styling.
- **marked + DOMPurify + highlight.js** — Markdown rendering.
- **Pele** — Mermaid-syntax diagrams, rendered to SVG locally.
- **slim-select** — styled `<select>` replacement (`components/ui.js` `Select`).

## Layout

```
index.html            shell: tokens, vendored scripts, #app
app.js                theme + layout + boot
lib/
  store.js            one mutable state tree, batched emit(), useStore()
  api.js              fetch wrapper → ApiError(status, code, message); timeouts
  socket.js           reconnecting turn socket
  chat.js             controller: routing, rooms, every socket command and frame
  turn.js             frames/history → transcript items; tool summaries
  markdown.js         block-incremental Markdown → sanitized DOM
  diagram.js          Pele seam: preflight gate, SVG scrubber, locked options
components/
  sidebar.js          thread list (paged), new thread, settings
  chat.js             thread header/actions, ChatView, HomeView
  transcript.js       messages, thoughts, tool groups, status, failure card
  toolcard.js         tool call row + terminal-style input/output card
  composer.js         draft, send/steer/queue/stop, pending tray
  pickers.js          model picker, workspace browser, context meter
  settings.js         providers, general settings, tools, about/update
  ui.js, icons.js     primitives
```

Routes are hash-based: `#/` (new thread), `#/c/<id>`, `#/settings/<section>[/<arg>]`.

## Behavior

- **Send, steer, queue, stop.** Idle: Enter sends. While a send turn runs,
  Enter steers that exact turn (`turn.steer`) and Ctrl/⌘+Enter queues for
  after it; while anything else keeps the room busy, Enter queues. Stop
  cancels the room's turn. The queue is per room and in memory only; it
  drains oldest-first after a clean finish and is held after a failure or
  stop until the user sends or presses *Send next*.
- **Steering receipts.** Pending steers can be recalled; a fold removes them
  from the tray, and a terminal snapshot returns uncommitted ones to the
  front of the queue (recalled ones to the draft). Nothing is resent
  automatically.
- **Failures.** `turn.failed` (except a user stop) shows a card with
  *Retry* (`turn.resume`, keeps history) and *Dismiss*. A send whose user
  line never persisted returns its text to the draft.
- **Reconnects.** Live turns re-watch from their last `seq`; a send still
  waiting for `turn.opened` has unknown delivery, so its text returns to the
  draft with a warning. Every terminal frame re-reads history over HTTP.
- **Rendering.** User text is plain text. Assistant prose is GFM Markdown
  plus GitHub-style footnotes (`[^id]`), sanitized by DOMPurify against a
  tag allowlist: inline HTML such as `<kbd>`, `<del>`, `<dl>`, `<details>`
  renders, while images are never fetched, scripts/styles/forms are dropped,
  and only the renderer's own class names survive. Links are limited to
  http(s)/mailto/same-origin paths. Closed
  ```` ```mermaid ```` fences render through Pele after the preflight gate
  in `lib/diagram.js`; anything it rejects shows as source.
- **Tool I/O** renders as escaped text, collapsed into "Used N tools"
  groups between prose. Each call expands to a terminal-style card
  (`components/toolcard.js`): shell commands show as highlighted bash
  "Command in <workspace>", other tools as pretty JSON; long input shows its
  head and long output its tail behind a fade, each expandable in place.
  The runner's trailing `(exit N)` / `(signal: X)` becomes the card footer.
- **History paging.** A thread opens on its newest 60 messages; older pages
  load automatically as the reader nears the top (skeleton placeholder,
  inline retry on failure) while the line being read stays in place.
- **Preferences** (theme, last model) are the only thing in localStorage.

## Development

```sh
make build && NUSALLM_WEB_DIR=web ./bin/nusallm serve --port 18080
```

`NUSALLM_WEB_DIR` serves the UI from disk instead of the embedded copy, so
edits show on reload. `make test-web` syntax-checks the sources.
