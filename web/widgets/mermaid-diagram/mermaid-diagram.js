import { Widget, el, clear } from '../../lib/dom.js';
import { renderMermaid } from '../../lib/mermaid-runtime.js';

const DEBOUNCE_MS = 180;
const MIN_SCALE = 0.6;
const MAX_SCALE = 2.0;
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
// or null when the input isn't a usable <svg> document. Exported for
// fixture verification — this is the defense-in-depth layer on top of
// Mermaid's strict mode, which already rejects click directives.
export function sanitizeSvg(svgText) {
  const doc = new DOMParser().parseFromString(svgText, 'image/svg+xml');
  const rootEl = doc.documentElement;
  if (!rootEl || rootEl.localName.toLowerCase() !== 'svg' || doc.querySelector('parsererror')) {
    return null;
  }

  // Mermaid still emits <foreignObject> labels even with htmlLabels off
  // (each sits in a translated <g class="label">). Converting them to
  // SVG-native <text> keeps node labels visible without allowing HTML.
  for (const fo of [...rootEl.querySelectorAll('foreignObject')]) {
    const lines = [];
    let cur = '';
    const flush = () => {
      const t = cur.replace(/\s+/g, ' ').trim();
      if (t) lines.push(t);
      cur = '';
    };
    const walk = (el) => {
      for (const child of el.childNodes) {
        if (child.nodeType === 3) { cur += child.textContent; continue; }
        const tag = (child.localName || '').toLowerCase();
        if (tag === 'br') flush();
        else { walk(child); if (tag !== 'span' && tag !== 'a' && tag !== 'b' && tag !== 'i' && tag !== 'em' && tag !== 'strong') flush(); }
      }
    };
    walk(fo); flush();
    if (!lines.length) { fo.remove(); continue; }

    const num = (v) => { const n = parseFloat(v); return Number.isFinite(n) ? n : 0; };
    const cx = num(fo.getAttribute('x')) + num(fo.getAttribute('width')) / 2;
    const cy = num(fo.getAttribute('y')) + num(fo.getAttribute('height')) / 2;
    const lineHeight = 16;
    const text = doc.createElementNS('http://www.w3.org/2000/svg', 'text');
    text.setAttribute('x', String(cx));
    text.setAttribute('y', String(cy));
    text.setAttribute('text-anchor', 'middle');
    text.setAttribute('dominant-baseline', 'middle');
    text.setAttribute('fill', '#ece9df');
    text.setAttribute('font-family', 'Iosevka, JetBrains Mono, SFMono-Regular, Consolas, monospace');
    text.setAttribute('font-size', '14');
    if (lines.length === 1) {
      text.textContent = lines[0];
    } else {
      lines.forEach((line, i) => {
        const tspan = doc.createElementNS('http://www.w3.org/2000/svg', 'tspan');
        tspan.setAttribute('x', String(cx));
        tspan.setAttribute('dy', i === 0 ? String(-(lines.length - 1) * lineHeight / 2) : String(lineHeight));
        tspan.textContent = line;
        text.append(tspan);
      });
    }
    fo.replaceWith(text);
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

// Mermaid diagram block: debounced render through the serialized
// runtime, zoom controls, collapsible source, and a strict-scrubbed
// SVG viewport that never widens the page.
class MermaidDiagram extends Widget {
  static stylesheet = new URL('./mermaid-diagram.css', import.meta.url);

  constructor() {
    super();
    this._source = '';
    this._renderedSource = null;
    this._gen = 0;
    this._timer = 0;
    this._scale = 1;
    this._renderCounter = 0;
    this._build();
    this._setStatus('waiting', 'Waiting for diagram');
  }

  disconnectedCallback() {
    clearTimeout(this._timer);
    this._gen++; // suppress any in-flight result for a detached widget
  }

  connectedCallback() {
    // A render that resolved while detached left the canvas empty —
    // recover it on reattach instead of leaving a stuck status.
    if (this._source && !this._renderedSource && !this.canvas.firstChild) {
      const gen = ++this._gen;
      this._setStatus('rendering', 'Rendering');
      clearTimeout(this._timer);
      this._timer = setTimeout(() => this._renderDiagram(gen), 0);
    }
  }

  // Assigning the same source is a no-op; a new source debounces into
  // a serialized render and resets zoom.
  set source(v) {
    const s = String(v ?? '');
    if (s === this._source) return;
    this._source = s;
    this._scale = 1;
    this._applyZoom();
    this._setSourceBlock();
    clearTimeout(this._timer);
    const gen = ++this._gen;
    this._setStatus('rendering', this._renderedSource == null ? 'Rendering' : 'Updating…');
    this._timer = setTimeout(() => this._renderDiagram(gen), DEBOUNCE_MS);
  }

  _build() {
    clear(this.root);
    this.root.classList.add('diagram');

    this.statusEl = el('span', {
      class: 'status', role: 'status', 'aria-live': 'polite',
    });
    const badge = el('span', { class: 'lang', text: 'mermaid' });
    this.zoomOut = el('button', {
      class: 'ctl', type: 'button', 'aria-label': 'Zoom out', text: '−',
      onclick: () => this._zoomBy(-0.2),
    });
    this.zoomFit = el('button', {
      class: 'ctl wide', type: 'button', 'aria-label': 'Fit diagram', text: 'Fit',
      onclick: () => this._zoomFit(),
    });
    this.zoomIn = el('button', {
      class: 'ctl', type: 'button', 'aria-label': 'Zoom in', text: '+',
      onclick: () => this._zoomBy(0.2),
    });
    const copy = el('button', {
      class: 'ctl wide', type: 'button', 'aria-label': 'Copy diagram source',
      onclick: () => this._copy(copy),
    }, [el('span', { class: 'copy-label', text: 'Copy source' })]);

    this.root.append(el('div', { class: 'chrome' }, [
      badge, this.statusEl,
      el('span', { class: 'chrome-gap' }),
      el('div', { class: 'controls' }, [this.zoomOut, this.zoomFit, this.zoomIn, copy]),
    ]));

    this.viewport = el('div', {
      class: 'viewport', role: 'img', 'aria-label': 'Rendered diagram',
      tabindex: '0',
    });
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

  _zoomBy(d) {
    this._scale = Math.min(MAX_SCALE, Math.max(MIN_SCALE, this._scale + d));
    this._applyZoom();
  }

  _zoomFit() {
    this._scale = 1;
    this._applyZoom();
    this.viewport.scrollLeft = 0;
    this.viewport.scrollTop = 0;
  }

  _applyZoom() {
    if (!this.canvas) return;
    this.canvas.style.transform = `scale(${this._scale})`;
    this.canvas.style.transformOrigin = '0 0';
    // Transforms don't grow the scroll box — size the canvas to the
    // scaled diagram so the viewport can scroll the zoomed extent.
    if (this._baseW && this._baseH) {
      this.canvas.style.width = `${this._baseW * this._scale}px`;
      this.canvas.style.height = `${this._baseH * this._scale}px`;
    }
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

  async _renderDiagram(gen) {
    const src = this._source;
    try {
      const { svg } = await renderMermaid(`nusa-mmd-${++this._renderCounter}-${gen}`, src);
      if (gen !== this._gen || !this.isConnected) return; // stale/async result
      const clean = sanitizeSvg(svg);
      if (!clean) throw new Error('unusable svg');
      clear(this.canvas);
      this.canvas.append(clean);
      this._renderedSource = src;
      // Measure intrinsic size for the zoomed scroll box.
      let w = 0; let h = 0;
      try { const b = clean.getBBox(); w = b.width; h = b.height; } catch { /* not rendered yet */ }
      if ((!w || !h) && clean.viewBox && clean.viewBox.baseVal) {
        w = clean.viewBox.baseVal.width; h = clean.viewBox.baseVal.height;
      }
      this._baseW = w; this._baseH = h;
      this._applyZoom();
      this._setStatus('ready', 'Ready');
    } catch {
      if (gen !== this._gen || !this.isConnected) return;
      if (this._renderedSource != null) {
        // Keep the last valid diagram — the stream may still be typing.
        this._setStatus('waiting', 'Waiting for complete diagram');
      } else {
        this._showFallback();
      }
    }
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
}

customElements.define('nusa-mermaid-diagram', MermaidDiagram);
