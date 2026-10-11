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
  websocket-frame → ordered-segment projection), `pending-queue.js`
  (pure room-bound local queue + steering receipt tracker),
  `mermaid-runtime.js` (init-once + serialized render queue for the
  vendored bundle).
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
  Model import is automatic — `PUT /api/gateways/<id>` merges the
  provider's advertised model ids into the saved record: existing
  entries keep their parameters, new ids append, and the response
  carries an `added` array plus an optional `catalogError` when the
  catalog fetch failed (the save still succeeds). The editor's
  "Refresh models from provider" button calls `POST
  /api/gateways/<id>/import-models` against the stored record and
  reports the imported count or error inside the open dialog. The
  model list stays a searchable, read-only list of stored ids —
  searching never changes it.
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
- **Queue, Steer, Stop.** A turn never blocks typing: while a turn
  runs, the queue is paused/backlogged, or a post-terminal history
  refresh is still outstanding, Enter (IME `isComposing`-aware —
  composition Enter never submits) sends the draft to a **local,
  room-bound queue** and the shortcut reads Enter to queue; the send
  control stays an icon whose accessible name is Queue message. The pending
  panel above the input shows a count header, every entry's state
  (`Queued`, `Waiting for next step`, `Delivery unknown`), its owning
  room, and its full text in a bounded scroll area. Queued lines keep
  global submission order, dispatch oldest-first for the *visible*
  view only — a fresh (un-minted) view is its own queue key, so a
  second "New thread" never hijacks another's lines — and only after
  the previous turn's history refresh succeeded; the refresh is a
  per-room barrier, so nothing — Enter, Resume, navigation, a fresh
  view — sends into a room whose history is still settling. A failed
  or canceled turn, a socket drop, or a failed refresh pauses the
  queue until an explicit Resume. `Restore` returns local text to the
  draft (appended after a newline, never overwriting); `Remove` drops
  a local line. Entries owned by another room or fresh view show
  **Open** — they never mutate this draft — and **Delivery unknown**
  rows offer Check history, an explicit Copy-to-draft decision, and
  Dismiss instead of an automatic retry. **Steer** is a separate
  action visible only while the viewed room owns an opened send turn —
  it submits `turn.steer` to that exact turn, joins at the next model
  step (otherwise the input waits as pending until the turn ends), and
  its receipt (`steer_ack` → fold frame or terminal snapshot) drives
  the panel state; a still-pending input can be withdrawn via
  `turn.steer_recall` which restores the text, and Recall stays
  disabled while the socket can't carry it. **Stop** is a separate
  visible control scoped to the current room's turn — never a hidden
  global target, and the shell ignores a cancel that isn't in the
  owning room. Pre-open drafts survive until `turn.opened`; a send
  whose terminal reports `userMessagePersisted: false` returns its
  text to the owning draft or room queue — persistence is taken from
  the payload, never inferred. The queue is **in-memory only** — a
  reload forgets pending items by design, and nothing is written to
  web storage.

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
*in arrival order* — thinking segments, markdown text segments, tool
segments, and user **steer** segments interleave exactly as the server
emitted them, and output text never concatenates across a
thinking/tool/steer boundary. A `turn.steer` fold renders as ordered
plain user text (keyed by `inputId`, chunked chunks reassembled, a
`final` marker separating identical consecutive inputs) — it finishes
thinking and settles unresolved tools like any other boundary, and is
never mistaken for tool content. Replayed or doubled `seq` numbers are
dropped before any mutation, so a reconnect re-watch never re-appends
text. `message-thread`
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

## Known limitations

If the socket drops after a `turn.send`/`turn.edit` was issued but
before its `turn.opened` arrived, delivery is unknowable — the command
is dropped (never auto-resent) and the UI warns to check thread history
before retrying. Mid-turn drops are safe: the socket re-watches open
turns from their last `seq` and terminals refresh the authoritative
state from HTTP.

The same honesty applies to the pending queue and steering receipts:

- Local queued lines were never transmitted, so they survive a
  transient reconnect unchanged — but they live only in memory: a page
  reload forgets them.
- A `turn.steer`/`turn.steer_recall` whose ack never arrived is shown
  as **Delivery unknown** with a Check-history action — the outcome is
  resolved by the journaled ack replay, the `steering` snapshot on
  `turn.resync_required`/terminal frames, and the HTTP re-read. Nothing
  is auto-resent: an input the server may already hold is never
  submitted twice.
- Snapshot meaning depends on the verdict: in an **active** resync
  (`payload.turn === "active"`), `pending`/`claimed` inputs are still
  owned by the server — the receipt stays bound to the turn and its
  text is never requeued (the turn may yet fold it). Only a **terminal**
  snapshot treats `pending`/`claimed` as "ended uncommitted", falling
  the text back into the local queue at its original submission
  ordinal; `recalled` restores it to the draft; `persisted` drops the
  tracked entry (the message is in history). An input absent from an
  active snapshot stays **Delivery unknown** for a later frame.
- A `turn.resync_required` verdict of `completed`/`failed` replays the
  retained terminal payload through the normal terminal path; a
  verdict of `unknown` retires the command honestly, keeps every
  receipt unknown, and pauses for an explicit user decision.

## Development

Static checks only: `node --check` every `*.js`. The production path is
`GOPROXY=off go build ./...` + `nusallm serve`; there is no separate dev
server requirement.

Unit tests run via `make test-web` (node:test, DOM-free helpers plus
projection/socket harnesses — no browser needed).

Browser integration lives in `web/test-e2e/steer-flow.mjs`: isolated
per-scenario pages over a fully mocked backend (`page.route` for
`/api/*`, `routeWebSocket` for a scripted turn hub — truthful
`userMessagePersisted` terminals, active/terminal/unknown resync
verdicts, delayed and failing GETs, multi-room and fresh-view state).
It needs a locally installed Playwright + a Chrome/Chromium channel —
nothing is downloaded by the script:

```sh
# standard resolution (a local @playwright/test install)
node web/test-e2e/steer-flow.mjs

# or point at any playwright module explicitly
PLAYWRIGHT_MODULE=/path/to/@playwright/test/index.mjs \
  node web/test-e2e/steer-flow.mjs

# knobs: E2E_PORT (default 0 = auto-assigned),
#        E2E_ARTIFACTS (default /tmp/steer-web-artifacts)
```

Artifacts (screenshots per scenario, failure traces under
`fail-*.zip`) land in `E2E_ARTIFACTS`. The harness is intentionally not
wired into `make test-web` — it requires a browser channel the repo
does not vendor.
