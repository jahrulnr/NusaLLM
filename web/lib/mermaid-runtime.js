// Mermaid runtime seam — the only place that talks to the vendored
// global bundle. Initialized exactly once with strict security; renders
// are serialized through a single Promise queue because Mermaid shares
// global config and a scratch DOM. A failed job rejects its own promise
// but never poisons the queue. Error objects never carry the diagram
// source.

let _initialized = false;
let _queue = Promise.resolve();

const MAX_SOURCE_LEN = 100 * 1024;

// Preflight gate, run before Mermaid ever sees the source. Mermaid's own
// parser materializes label text into a scratch DOM where an HTML-ish or
// resource-bearing payload can fetch or navigate before our post-render
// SVG scrubber runs — so anything that could produce a side effect is
// rejected up front and the caller falls back to showing the source.
// Fail closed: a false positive costs a diagram, never safety.
export function validateMermaidSource(source) {
  const s = String(source ?? '');
  if (!s || s.length > MAX_SOURCE_LEN) return false;
  // Control bytes (tab/LF/CR are the only sane whitespace controls).
  if (/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/.test(s)) return false;
  // YAML frontmatter and %%{init/config}%% directives smuggle config and
  // can re-enable unsafe Mermaid features per diagram.
  if (/^---[ \t]*\r?\n/.test(s) || /%%[ \t]*\{/.test(s)) return false;
  // click directives attach navigation/JS callbacks to nodes.
  if (/^[ \t]*click[ \t]+\S/im.test(s)) return false;
  // img:/image: shape declarations load resources by URL (any scheme,
  // including same-origin paths).
  if (/\b(?:img|image)\s*:/i.test(s)) return false;
  // External/dangerous resource syntax anywhere in the source.
  if (/https?:\/\/|\/\/|javascript:|data:|file:|blob:|url\(|@import/i.test(s)) return false;
  // HTML-like tags, literal or entity-encoded. Class-diagram stereotypes
  // like <<interface>> are stripped first; arrows (-->, <-->, <|--)
  // never put a letter or ! right after '<' so they pass.
  const deStereotyped = s.replace(/<<\s*[a-zA-Z][\w -]*\s*>>/g, '');
  if (/<\/?[a-zA-Z!]/.test(deStereotyped)) return false;
  if (/&(?:lt|#0*60|#x0*3c);?\s*\/?[a-zA-Z!]/i.test(s)) return false;
  return true;
}

export function initializeMermaid() {
  if (_initialized) return;
  const m = globalThis.mermaid;
  if (!m || typeof m.initialize !== 'function') {
    throw new Error('Mermaid runtime is unavailable');
  }
  m.initialize({
    startOnLoad: false,
    securityLevel: 'strict',
    suppressErrorRendering: true,
    htmlLabels: false,
    logLevel: 'fatal',
    theme: 'base',
    flowchart: { htmlLabels: false, useMaxWidth: true, curve: 'basis' },
    sequence: { useMaxWidth: true, wrap: true },
    themeVariables: {
      background: '#0b0d0c', primaryColor: '#151a16', primaryTextColor: '#ece9df',
      primaryBorderColor: '#475044', lineColor: '#9ba394', secondaryColor: '#111512',
      tertiaryColor: '#0e120f', noteBkgColor: '#151a16', noteTextColor: '#ece9df',
      noteBorderColor: '#475044',
      fontFamily: 'Iosevka, JetBrains Mono, SFMono-Regular, Consolas, monospace',
    },
  });
  _initialized = true;
}

// renderMermaid resolves to {svg, bindFunctions?} — the caller owns
// sanitization and insertion; nothing here touches the document.
export function renderMermaid(id, source) {
  // Reject before enqueue so an invalid source never touches Mermaid's
  // scratch DOM — the preflight is the side-effect boundary.
  if (!validateMermaidSource(source)) {
    return Promise.reject(new Error('Diagram could not be rendered'));
  }
  const job = _queue.then(() => {
    initializeMermaid();
    const m = globalThis.mermaid;
    return m.render(id, source).catch(() => {
      // Display-safe: mermaid errors can embed the raw source — surface
      // only a generic failure and let the caller show its own fallback.
      throw new Error('Diagram could not be rendered');
    });
  });
  // The queue continues regardless of this job's outcome.
  _queue = job.catch(() => {});
  return job;
}
