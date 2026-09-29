# Vendored Mermaid

- **Package**: `mermaid`
- **Version**: 11.17.2 (pinned — do not mix versions)
- **License**: MIT (`LICENSE` in this directory)
- **Upstream**: https://github.com/mermaid-js/mermaid
- **File**: `mermaid.min.js` — the standalone dist bundle, registers
  `globalThis.mermaid` (classic script, no module graph, no CDN).

## Why vendored

The WebUI is local-first: no network, no CDN, no runtime dependency
manager. Diagrams render offline from this exact pinned artifact.

## Update procedure

1. Obtain `mermaid@<version>` dist/mermaid.min.js and LICENSE from a
   trusted local cache or an audited fetch — never a CDN URL at runtime.
2. Replace `mermaid.min.js`, keep `LICENSE`, bump the version above.
3. Re-run `go test ./web`, the node tests, and the browser smoke —
   verify `globalThis.mermaid` still registers and `securityLevel:
   'strict'` is still supported by the pinned API.
