// Diagram seam — the only module that talks to the vendored Pele bundle
// (globalThis.pele, loaded by index.html). It owns the preflight gate, the
// SVG scrubber, and the locked mount options; callers own the handle.

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


const BANNED_TAGS = new Set([
  'script', 'foreignobject', 'iframe', 'object', 'embed',
  'image', 'audio', 'video', 'canvas',
  // SMIL retargeting + resource/navigation-capable metadata elements:
  // these can mutate hrefs or trigger loads after insertion.
  'set', 'animate', 'animatemotion', 'animatetransform', 'mpath',
  'feimage', 'cursor', 'link', 'meta', 'base',
]);

// sanitizeSvg scrubs a detached, already-parsed SVG document's root:
// banned elements out, <a> unwrapped (children kept), on* handlers and
// non-local href/url() references stripped. Returns the imported root
// or null when the input isn't a usable <svg> document. Pele feeds
// every render through this hook before insertion — defense-in-depth
// on top of its own escaping, which the locked options extend by
// disabling links and images entirely.
export function sanitizeSvg(svgText) {
  const doc = new DOMParser().parseFromString(svgText, 'image/svg+xml');
  const rootEl = doc.documentElement;
  if (!rootEl || rootEl.localName.toLowerCase() !== 'svg' || doc.querySelector('parsererror')) {
    return null;
  }

  // Drop banned elements; unwrap <a> so click directives can never
  // navigate but their safe shape/text children survive. A surviving
  // element that targets href/src via attributeName (SMIL retargeting
  // that slipped the tag ban) is removed outright.
  for (const node of [...rootEl.querySelectorAll('*')]) {
    const smil = (node.getAttribute('attributeName') || '').trim().toLowerCase();
    if (smil === 'href' || smil === 'xlink:href' || smil === 'src') { node.remove(); continue; }
    const tag = node.localName.toLowerCase();
    if (BANNED_TAGS.has(tag)) { node.remove(); continue; }
    if (tag === 'a') node.replaceWith(...[...node.childNodes]);
  }

  const badUrl = /javascript:|data:|file:|blob:|@import/i;
  const externalUrl = (v) => {
    // url(#id) local refs (markers/gradients) stay; everything else is suspect.
    const rest = String(v).replace(/url\(\s*['"]?#[\w.-]+['"]?\s*\)/gi, '');
    return /url\(/i.test(rest) || /(https?:)?\/\//.test(rest) || badUrl.test(rest);
  };

  const scrubAttrs = (node) => {
    for (const attr of [...node.attributes]) {
      const name = attr.name.toLowerCase();
      const value = attr.value;
      if (name.startsWith('on')) { node.removeAttribute(attr.name); continue; }
      if (name === 'href' || name === 'xlink:href') {
        if (!/^#[\w.-]+$/.test(value)) node.removeAttribute(attr.name);
        continue;
      }
      if (externalUrl(value) || badUrl.test(value)) node.removeAttribute(attr.name);
    }
  };
  scrubAttrs(rootEl);

  // <style> survives only when fully internal — any external URL,
  // @import, or dangerous scheme drops the whole block.
  for (const node of [...rootEl.querySelectorAll('*')]) {
    scrubAttrs(node);
    if (node.localName.toLowerCase() === 'style') {
      const css = node.textContent || '';
      const stripped = css.replace(/url\(\s*['"]?#[\w.-]+['"]?\s*\)/gi, '');
      if (badUrl.test(stripped) || /url\(|(https?:)?\/\//i.test(stripped)) node.remove();
    }
  }
  return document.importNode(rootEl, true);
}

// mountDiagram attaches a live diagram to `container` and returns Pele's
// handle (update/refresh/destroy). Links and images are disabled and the
// preflight gate runs before the engine sees the source.
export function mountDiagram(container, source, hooks = {}) {
  if (!validateDiagramSource(source)) throw new Error('Diagram uses unsupported features');
  const p = globalThis.pele;
  if (!p || typeof p.mount !== 'function') throw new Error('Diagram renderer is unavailable');
  return p.mount(container, source, {
    links: false,
    images: false,
    limit: MAX_SOURCE_LEN,
    sanitize: hooks.sanitize ?? sanitizeSvg,
    onRender: hooks.onRender,
    onError: hooks.onError,
    zoom: { always: true },
  });
}
