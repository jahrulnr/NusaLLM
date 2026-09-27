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
  (`APIClient`, `APIError`), `socket-client.js` (`TurnSocket`).
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
