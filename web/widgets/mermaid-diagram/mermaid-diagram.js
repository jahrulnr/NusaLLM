import { Widget, el, clear } from '../../lib/dom.js';
import { validateDiagramSource, mountDiagram } from '../../lib/diagram-runtime.js';

const DEBOUNCE_MS = 180;
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

// Mermaid-syntax diagram block: source debounces into a Pele mount
// that owns the canvas — adapting layout to the container width,
// re-rendering on resize, supplying zoom/pan controls, and reporting
// draw failures through onError. A rejected update never clears the
// last good diagram; collapsible source is always available.
class MermaidDiagram extends Widget {
  static stylesheet = new URL('./mermaid-diagram.css', import.meta.url);

  constructor() {
    super();
    this._source = '';
    this._mount = null;
    this._mounting = false;
    this._timer = 0;
    this._build();
    this._setStatus('waiting', 'Waiting for diagram');
  }

  disconnectedCallback() {
    clearTimeout(this._timer);
    // destroy() stops Pele's observers but leaves the markup; the
    // canvas is ours, so the stale drawing goes with the handle.
    try { this._mount?.destroy(); }
    finally { this._mount = null; clear(this.canvas); }
  }

  connectedCallback() {
    // The mount was destroyed on detach — redraw from the kept source.
    if (this._source && !this._mount && this.isConnected) {
      this._schedule(0);
    }
  }

  // Assigning the same source is a no-op; a new source debounces into
  // a mount update.
  set source(v) {
    const s = String(v ?? '');
    if (s === this._source) return;
    this._source = s;
    this._setSourceBlock();
    this._schedule(DEBOUNCE_MS);
  }

  _build() {
    clear(this.root);
    this.root.classList.add('diagram');

    this.statusEl = el('span', {
      class: 'status', role: 'status', 'aria-live': 'polite',
    });
    const badge = el('span', { class: 'lang', text: 'mermaid' });
    const copy = el('button', {
      class: 'ctl wide', type: 'button', 'aria-label': 'Copy diagram source',
      onclick: () => this._copy(copy),
    }, [el('span', { class: 'copy-label', text: 'Copy source' })]);

    this.root.append(el('div', { class: 'chrome' }, [
      badge, this.statusEl,
      el('span', { class: 'chrome-gap' }),
      el('div', { class: 'controls' }, [copy]),
    ]));

    // role=group keeps the region named without making pele's
    // interactive zoom controls presentational to assistive tech.
    this.viewport = el('div', {
      class: 'viewport', role: 'group', 'aria-label': 'Rendered diagram',
      tabindex: '0',
    });
    // Pele owns everything inside the canvas leaf.
    this.canvas = el('div', { class: 'canvas' });
    this.viewport.append(this.canvas);

    this.details = el('details', { class: 'src' }, [
      el('summary', { class: 'src-summary', text: 'Diagram source' }),
    ]);
    this.codeEl = document.createElement('nusa-code-block');
    this.codeEl.language = 'mermaid';
    this.details.append(this.codeEl);

    this.root.append(this.viewport, this.details);
  }

  _setSourceBlock() {
    if (this.codeEl._code !== this._source) this.codeEl.code = this._source;
  }

  _setStatus(kind, text) {
    this.statusEl.textContent = text;
    this.statusEl.dataset.kind = kind;
  }

  _schedule(delay) {
    this._setStatus('rendering', this._mount?.result ? 'Updating…' : 'Rendering');
    clearTimeout(this._timer);
    this._timer = setTimeout(() => this._render(), delay);
  }

  _render() {
    if (!this.isConnected) return;
    const src = this._source;
    // An invalid update never reaches the engine — the last good
    // diagram stays put, or the fallback stands when nothing drew yet.
    if (!validateDiagramSource(src)) { this._fail(); return; }
    try {
      if (this._mount) {
        // update() no-ops silently on unchanged source, so a
        // re-emitted identical block would leave the status stuck at
        // 'Updating…' — restore the verdict from the handle instead.
        if (this._mount.source !== src) { this._mount.update(src); return; }
        if (this._mount.error) this._fail();
        else this._setStatus('ready', 'Ready');
        return;
      }
      clear(this.canvas);
      // onError/onRender fire synchronously inside mount() — before
      // the handle comes back — so _mounting marks the canvas as
      // engine-owned while the call is in flight.
      this._mounting = true;
      this._mount = mountDiagram(this.canvas, src, {
        sanitize: (svg) => {
          const clean = sanitizeSvg(svg);
          if (!clean) throw new Error('unusable svg');
          return clean;
        },
        onRender: () => this._setStatus('ready', 'Ready'),
        onError: () => this._fail(),
      });
      this._mounting = false;
    } catch {
      this._mounting = false;
      this._fail();
    }
  }

  // Failure surface: whenever Pele owns the canvas its own display
  // stands — a dimmed last-good diagram or the inline parse problem —
  // and the status only needs the verdict. Our own fallback note is
  // for the case where nothing was ever mounted (preflight rejects,
  // missing engine).
  _fail() {
    if (this._mount || this._mounting) {
      const keep = !!this._mount?.result;
      this._setStatus(
        keep ? 'waiting' : 'error',
        keep ? 'Waiting for complete diagram' : 'Diagram could not be rendered',
      );
      return;
    }
    this._showFallback();
  }

  _showFallback() {
    clear(this.canvas);
    this.canvas.append(el('div', { class: 'fallback', role: 'note' }, [
      el('span', { class: 'fallback-mark', text: '◈' }),
      el('span', {
        class: 'fallback-text',
        text: 'Diagram could not be rendered — the source is shown below.',
      }),
    ]));
    this._setStatus('error', 'Diagram could not be rendered');
  }

  async _copy(btn) {
    const label = btn.querySelector('.copy-label');
    try {
      await navigator.clipboard.writeText(this._source);
      label.textContent = 'Copied';
    } catch {
      label.textContent = 'Copy failed';
    }
    setTimeout(() => { if (label.isConnected) label.textContent = 'Copy source'; }, 1400);
  }
}

customElements.define('nusa-mermaid-diagram', MermaidDiagram);
