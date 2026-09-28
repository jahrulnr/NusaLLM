# NusaLLM WebUI

Vanilla-JS browser client for `nusallm serve`, embedded into the binary
via `embed.go` and served by the same Gin origin as the HTTP API and the
`/api/ws` turn socket. No npm, no build step, no CDN, no external fonts.

## Layout

- `index.html` — wrapper only: loads `/styles/theme.css`, mounts
  `<nusa-app>`, imports `/app.js`.
- `app.js` — imports widget definitions only.
- `styles/theme.css` — design tokens + document chrome.
- `lib/` — shared mechanics: `dom.js` (element building, composed
  events, `Widget` base), `icons.js` (inline SVG), `api-client.js`
  (`APIClient`, `APIError`), `socket-client.js` (`TurnSocket`),
  `markdown.js` (pure block/inline Markdown → AST + `sanitizeLink`),
  `highlight.js` (pure syntax tokenizer), `turn-projection.js` (pure
  websocket-frame → ordered-segment projection), `mermaid-runtime.js`
  (init-once + serialized render queue for the vendored bundle).
  All are DOM-free and unit tested under `web/test/`
  (`node --test web/test/*.test.mjs`).
- `vendor/mermaid/` — pinned **Mermaid 11.17.2** standalone bundle
  (MIT, see `vendor/mermaid/README.md`), loaded once as a classic
  script from the same origin. No CDN, no network.
- `widgets/<name>/<name>.{js,css}` — autonomous custom elements with
  shadow DOM; every widget owns exactly its paired JS and CSS.

## Rules

- Untrusted/API content enters the DOM via `textContent` only — never
  `innerHTML`.
- No API response, conversation content, or credential is persisted to
  web storage.
- Events bubble + compose upward; properties flow downward.
- `app-shell` is orchestration only — API/socket instances, hash
  routing (`#conversation=<id>`), and property/event coordination.
- `#settings` is a full workspace page with collapsible provider, model import,
  and application settings. Gateway editing uses a scrollable dialog with
  persistent Save and Cancel actions; application leaves auto-save on change.
  Manual import lives inside the gateway editor — "Fetch models from provider" reads the provider's model catalog
  through `/api/gateways/<id>/available-models`; selected IDs join the
  gateway's searchable, read-only model list and persist through its normal
  `PUT` save. The fetched catalog shows already configured ids too, so it
  can be searched to check current provider availability. Searching never
  changes the gateway's model catalog.
  `serve` additionally auto-imports on boot and hourly, merging newly
  advertised model ids into each gateway record (existing entries keep
  their parameters; the stored key reference is never rewritten).
- The header's `nusa-model-picker` is a layered picker: a search field
  filters models grouped by gateway, then effort (`default`/`none`/`low`/
  `medium`/`high`/`xhigh`/`max`), then the upstream route — aggregator
  backends (OpenRouter-style) come from
  `/api/gateways/<id>/providers?model=`, with `Auto` leaving the gateway
  to route. Each completed layer emits `model-change` — a selection
  applies the moment it's coherent, so closing mid-flow never loses a
  pick — and the emitted selection rides `turn.send` as
  `effort`/`provider` overrides.
- Workspace is a separate room-level boundary: `nusa-workspace-picker`
  sits beside the model picker as its own popup file-manager fed by
  `/api/fs/browse` — directories only, home by default, ascendable to the
  filesystem root, never free-typed. It emits `workspace-change`, which
  rides `turn.send` as `workspace` and persists on the room.
- The composer shows context use as a compact percentage with a details
  panel for estimated and total tokens. `/api/context` refreshes on room
  load, selection change, and turn completion. The message field grows with
  the draft, and the send button enables when a message can be sent.

## Rendering

Assistant content is Markdown rendered by `nusa-markdown-view`:
ATX headings 1–6, paragraphs, soft/hard line breaks, fenced code with
language tokens (```` ``` ```` and `~~~`), ordered/unordered/task lists, blockquotes, pipe tables
with alignment rows, horizontal rules; inline emphasis, strong,
strikethrough, code spans, links, autolinks, escapes. Fenced blocks
become `nusa-code-block` with syntax highlighting for Go, JS/TS and
C-likes, Python, JSON, shell, HTML/CSS, Markdown, and diffs — other
languages render plain. Links are filtered through `sanitizeLink`
(http/https/mailto and same-origin `/`,`./`,`../` only — bare `#frag`
is reserved for the app router, and control chars, protocol-relative
`//`, and backslashes are rejected); images render as inert
`Image: alt` placeholders and are never fetched; raw HTML stays literal
text. Every parser/tokenizer preserves source text — malformed input
degrades to literal text, never lost characters. User messages render
as plain escaped text and are never interpreted as Markdown.

### Diagrams

A **closed** ```` ```mermaid ```` or ```` ```mmd ```` fence renders
through `nusa-mermaid-diagram` using the vendored Mermaid 11.17.2
bundle — offline, `securityLevel: 'strict'`, `suppressErrorRendering`,
serialized renders. Supported families are whatever the pinned library
ships (flowchart, sequence, state, class, ER, gantt, …); exotic or
experimental types follow upstream support.

Live text renders as Markdown on each animation frame. Completed blocks
append to the DOM after a blank line; only the unfinished tail is reparsed
as deltas arrive. An unterminated Mermaid fence renders as an ordinary
code block badged `mermaid · streaming` — Mermaid never sees incomplete
syntax. Render failures are nonfatal: the last valid diagram
stays up with `Waiting for complete diagram`, or a `Diagram could not
be rendered` fallback plus collapsible source. Zoom (0.6–2.0), Fit,
Copy source, and a `Diagram source` details block are built in.

Safety starts *before* Mermaid runs: `validateMermaidSource` is a
preflight gate applied before the source ever reaches Mermaid's scratch
DOM (where a payload could fetch before any scrubber sees it). It fails
closed on HTML-like tags (literal or entity-encoded), `click`
directives, `img:`/`image:` resource shapes, external or dangerous
resource syntax (`http(s)://`, protocol-relative `//`, `javascript:`,
`data:`, `file:`, `blob:`, `url(`, `@import`), `%%{...}%%` config
directives, leading YAML frontmatter, control bytes, and sources over
100 KiB. Diagrams using those features intentionally fall back to the
collapsible source block rather than rendering. Arrows (`-->`,
`<-->`, `<|--`), class stereotypes (`<<interface>>`), and ordinary
labels stay valid.

The returned SVG is parsed with `DOMParser`, never `innerHTML`, then
scrubbed before insertion: `script`/`foreignObject`/`iframe`/`image`,
SMIL retargeting (`set`/`animate*`/`mpath`, plus any element whose
`attributeName` targets `href`/`src`), and metadata/loadable elements
(`feImage`/`cursor`/`link`/`meta`/`base`) are removed, `<a>` nodes are
unwrapped, every `on*` attribute is stripped, `href` survives only as a
local `#id`, and any external URL — `http(s)`, protocol-relative `//`,
`data:`/`file:`/`blob:`, `@import`, non-local `url(...)` — is dropped
from attributes and `<style>` blocks. A rendered diagram performs zero
network requests, same-origin or otherwise (asserted in browser).

## Visible provider data

This is a single-user loopback UI for its owner — **tool input and
output intentionally cross** so the owner sees what the agent saw, and
attachment `uri`/`name` (including `file://` local paths) are shown on
message and tool-output chips. Ordered message parts and `turn.reasoning`
also surface raw or summarized provider reasoning. Gateway key material,
provider replay artifacts in `optionalContent`, attachment bytes or
`sha256` pins, and internal error causes stay server-side.

- `nusa-thinking-block` renders ordered reasoning text from
  `turn.reasoning` chunks and persisted assistant `parts` as Markdown,
  including closed Mermaid fences. The card stays collapsed until the
  user opens it; a live thought does not expand or collapse the panel.
  Its orb is yellow and animated while reasoning is active, then solid
  green when that stretch finishes. The bounded body follows live text
  until the user scrolls up, and resumes following at the bottom.
  A card appears only when the provider supplied reasoning text.
- `nusa-tool-activity` renders call metadata plus the **exact** input
  and output (live chunks or the payload inside the persisted record), each in
  a visible section when the user expands the card. The card stays
  collapsed on its own. Its orb is yellow and animated while the call is
  `requested` or `streaming` (including a running exec that is still
  idle or waiting on output), dim and slowly pulsing when the record is
  `pending`, solid green when `settled` cleanly, and solid red when the
  stored output is a tool error (`error: …`), `interrupted by user`, a
  nonzero `(exit N)`, or a `(signal: …)` death. Content is inserted as text —
  never rendered as HTML or Markdown. Output scrolls within a bounded
  area and follows live chunks until the user scrolls up.
- Error bodies and internal causes stay server-side; terminals carry
  only status/code/message.

### Ordered stream timeline

`app-shell` delegates all stream interpretation to `turn-projection.js`:
each command owns a projection whose `segments` array records the turn
*in arrival order* — thinking segments, markdown text segments, and tool
segments interleave exactly as the server emitted them, and output text
never concatenates across a thinking/tool boundary. `message-thread`
keys one widget per `segment.id`, appends completed Markdown blocks while
rerendering the unfinished tail, and leaves earlier widgets in place. At completion, matching
widgets move into the persisted history without losing open details or
code scroll position.
Completed assistant messages persist the same ordered parts in history,
so a reload or backend restart renders that order without session-only
thinking state.

Tool statuses are honest protocol states, not guesses:

- `requested` — `turn.tool_call` arrived; nothing has run yet.
- `streaming` — `turn.tool_output` chunks were observed; execution may
  still be running. Every chunk appends to the shown output in order.
- `settled` — a later model step or the terminal proves the call ended
  (labeled `Settled — model activity resumed`, `Step ended`, or
  `Turn completed`/`Turn ended`). No output marker is fabricated.
- `pending` — a persisted call whose record shows no outcome.

Live `turn.tool_call` carries call metadata; ordered `turn.tool_input`
chunks reassemble into the exact argument text and `turn.tool_output`
chunks accumulate into `output`. Persisted `toolCalls` map `input`,
`output` (displayed without its storage envelope), and `calledAt` — shown as a
separate `Called` fact, never repurposed as an output timestamp. The
timeline derives marker labels from status but leaves `at` unset rather
than inventing times. An authoritative `completed` record becomes a
`settled` card with `resultStored: true` — it never pretends live
output activity was observed over the socket. Live-region announcements
stay metadata-only (`<name> requested/output activity/settled`) — tool
I/O text is never announced. Screen readers get a stable hidden live region fed only the
projection's incremental announcements (`Thinking`, delta text,
`<name> requested/output activity/settled`) — never a replayed subtree.

## Known limitation

If the socket drops after a `turn.send`/`turn.edit` was issued but
before its `turn.opened` arrived, delivery is unknowable — the command
is dropped (never auto-resent) and the UI warns to check thread history
before retrying. Mid-turn drops are safe: the socket re-watches open
turns from their last `seq` and terminals refresh the authoritative
state from HTTP.

## Development

Static checks only: `node --check` every `*.js`. The production path is
`GOPROXY=off go build ./...` + `nusallm serve`; there is no separate dev
server requirement.
