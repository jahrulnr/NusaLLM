# Vendored Pele

- **Package**: `pele`
- **Version**: 1.13.0
- **License**: MIT (see `LICENSE`)
- **Upstream**: https://github.com/obsidianmd/pele
- **File**: `pele.global.js` — the standalone global build, registers
  `globalThis.pele` (classic script, no module graph, no CDN).

Pele renders Mermaid-syntax diagrams to SVG. It is synchronous, has no
runtime dependencies, and themes through CSS variables (`--pele-*`).
The global build includes every diagram type.

## Updating

1. Obtain `pele@<version>` `dist/pele.global.js` and `LICENSE` from a
   trusted release (npm tarball, verified against `npm view pele
   dist.shasum`/`dist.integrity`).
2. Replace `pele.global.js`, keep `LICENSE`, bump the version above.
3. Load the page once and confirm `globalThis.pele` registers and a
   closed ```mermaid fence renders, including at a narrow viewport.
