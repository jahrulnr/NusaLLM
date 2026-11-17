// Diagram runtime seam — the only place that talks to the vendored
// global bundle (Pele, registered as globalThis.pele by pele.global.js).
// Pele renders synchronously and keeps no shared render state — no
// global config or scratch DOM — so there is nothing to initialize or
// serialize here: the seam owns the preflight gate, the locked render
// options, and the mount contract.
// Error objects never carry the diagram source.

const MAX_SOURCE_LEN = 100 * 1024;

// Preflight gate, run before Pele ever sees the source. Diagrams in
// this UI must not carry their own behavior or resources: anything
// that could fetch, navigate, or restyle itself per-diagram is
// rejected up front and the caller falls back to showing the source.
// Fail closed: a false positive costs a diagram, never safety.
export function validateDiagramSource(source) {
  const s = String(source ?? '');
  if (!s || s.length > MAX_SOURCE_LEN) return false;
  // Control bytes (tab/LF/CR are the only sane whitespace controls).
  if (/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/.test(s)) return false;
  // YAML frontmatter and %%{init/config}%% directives smuggle config and
  // can re-enable unsafe features per diagram.
  if (/^---[ \t]*\r?\n/.test(s) || /%%[ \t]*\{/.test(s)) return false;
  // click directives attach navigation/JS callbacks to nodes.
  if (/^[ \t]*click[ \t]+\S/im.test(s)) return false;
  // img:/image: shape declarations load resources by URL (any scheme,
  // including same-origin paths).
  if (/\b(?:img|image)\s*:/i.test(s)) return false;
  // External/dangerous resource syntax anywhere in the source.
  if (/https?:\/\/|\/\/|javascript:|data:|file:|blob:|url\(|@import/i.test(s)) return false;
  // HTML-like tags, literal or entity-encoded. Class-diagram stereotypes
  // like <<interface>> are stripped first; arrow shapes never put a
  // letter or ! right after '<' so they pass.
  const deStereotyped = s.replace(/<<\s*[a-zA-Z][\w -]*\s*>>/g, '');
  if (/<\/?[a-zA-Z!]/.test(deStereotyped)) return false;
  if (/&(?:lt|#0*60|#x0*3c);?\s*\/?[a-zA-Z!]/i.test(s)) return false;
  return true;
}

// mountDiagram attaches a live diagram to `container` and returns the
// Pele mount handle — the caller owns update()/refresh()/destroy()
// from there, plus the sanitize/onRender/onError hooks. Render options
// are locked here: no links, no images, and the preflight gate runs
// before the engine ever sees the source.
export function mountDiagram(container, source, hooks = {}) {
  if (!validateDiagramSource(source)) {
    throw new Error('Diagram could not be rendered');
  }
  const p = globalThis.pele;
  if (!p || typeof p.mount !== 'function') {
    throw new Error('Diagram runtime is unavailable');
  }
  return p.mount(container, source, {
    links: false,
    images: false,
    limit: MAX_SOURCE_LEN,
    sanitize: hooks.sanitize,
    onRender: hooks.onRender,
    onError: hooks.onError,
    // Zoom stays available even when a diagram already fits — readers
    // may still enlarge it. Pele supplies the controls and pan.
    zoom: { always: true },
  });
}
