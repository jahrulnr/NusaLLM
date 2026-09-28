import { Widget, el, clear } from '../../lib/dom.js';
import { parseMarkdown, sanitizeLink } from '../../lib/markdown.js';

// Safe Markdown renderer: builds the DOM exclusively with createElement
// and textContent. No innerHTML anywhere; link hrefs pass sanitizeLink
// (defense-in-depth — the parser already filters); images become inert
// text placeholders (never fetched); raw HTML in the source arrives as
// literal text nodes.
class MarkdownView extends Widget {
  static stylesheet = new URL('./markdown-view.css', import.meta.url);

  constructor() {
    super();
    this._content = '';
    this._raf = 0;
    this._blockCache = new Map();
    this._streamTextNode = null;
  }

  disconnectedCallback() {
    cancelAnimationFrame(this._raf);
  }

  // If a move (stream rebuild) detached the node between `content =` and
  // the frame, the canceled rAF would leave stale output — render on the
  // next connect instead.
  connectedCallback() {
    if (this._dirty) {
      this._dirty = false;
      cancelAnimationFrame(this._raf);
      this._render();
    }
  }

  // Rerenders coalesce to one animation frame — callers may set this on
  // every stream delta without quadratic DOM churn.
  set content(v) {
    this._content = v == null ? '' : String(v);
    this._dirty = true;
    if (!this.isConnected) return;
    cancelAnimationFrame(this._raf);
    this._raf = requestAnimationFrame(() => {
      this._dirty = false;
      this._render();
    });
  }

  // Live deltas append one text node. Markdown parsing runs once when
  // the segment closes, so a stream does not replace prose DOM per token.
  appendStreamText(chunk) {
    if (!chunk) return;
    if (!this._streamTextNode) {
      clear(this.root);
      this._streamTextNode = document.createTextNode('');
      this.root.append(el('div', { class: 'md stream-plain' }, [this._streamTextNode]));
    }
    this._content += chunk;
    this._streamTextNode.appendData(chunk);
  }

  finishStream() {
    if (!this._streamTextNode) return;
    this._streamTextNode = null;
    this._render();
  }

  _render() {
    // Build into a detached wrapper first — a parser/render failure can
    // never blank an already-rendered response; it degrades to the raw
    // source as plain text instead. Top-level code/diagram widgets are
    // cached by stable block path so a rendered diagram (and its zoom /
    // source-details state) survives later prose deltas.
    let wrap = el('div', { class: 'md' });
    const used = new Set();
    try {
      const blocks = parseMarkdown(this._content);
      blocks.forEach((block, i) => {
        const node = this._block(block, `b${i}`, used);
        if (node) wrap.append(node);
      });
    } catch {
      wrap = el('div', { class: 'md' }, [el('p', { text: this._content })]);
    }
    for (const key of [...this._blockCache.keys()]) {
      if (!used.has(key)) this._blockCache.delete(key);
    }
    clear(this.root);
    this.root.append(wrap);
  }

  // Cache only container-level heavy widgets (code/diagram). Key is a
  // stable block path; reuse happens only when the kind matches —
  // content still reassigns when it changed.
  _cachedBlock(key, kind, make) {
    let w = this._blockCache.get(key);
    if (!w || w._blockKind !== kind) {
      w = make();
      w._blockKind = kind;
      this._blockCache.set(key, w);
    }
    return w;
  }

  _block(b, path = '', used = new Set()) {
    switch (b.type) {
      case 'heading': {
        const h = document.createElement(`h${b.level}`);
        h.append(...this._inlines(b.inline));
        return h;
      }
      case 'paragraph':
        return el('p', {}, this._inlines(b.inline));
      case 'code': {
        const isMermaid = /^(mermaid|mmd)$/i.test(b.lang || '');
        // A closed mermaid/mmd fence renders a diagram; an unterminated
        // fence is still streaming and degrades to a labeled code block —
        // Mermaid never sees incomplete syntax.
        const kind = isMermaid && b.closed ? 'mermaid' : 'code';
        used.add(path);
        const w = this._cachedBlock(path, kind, () =>
          document.createElement(kind === 'mermaid'
            ? 'nusa-mermaid-diagram' : 'nusa-code-block'));
        if (kind === 'mermaid') {
          if (w._src !== b.content) { w._src = b.content; w.source = b.content; }
        } else {
          const lang = isMermaid ? 'mermaid · streaming' : (b.lang || '');
          if (w._lang !== lang) { w._lang = lang; w.language = lang; }
          if (w._code !== b.content) { w._code = b.content; w.code = b.content; }
        }
        return w;
      }
      case 'blockquote': {
        const q = el('blockquote');
        b.blocks.forEach((child, j) => {
          const node = this._block(child, `${path}.q${j}`, used);
          if (node) q.append(node);
        });
        return q;
      }
      case 'list': {
        const list = el(b.ordered ? 'ol' : 'ul', { class: 'list' });
        if (b.ordered && b.start && b.start !== 1) list.start = b.start;
        for (const item of b.items) {
          const li = el('li', { class: item.task != null ? 'task' : '' });
          if (item.task != null) {
            li.append(el('input', {
              type: 'checkbox', checked: item.task, disabled: true,
            }));
          }
          li.append(...this._inlines(item.inline));
          list.append(li);
        }
        return list;
      }
      case 'table': {
        const headRow = el('tr');
        b.header.forEach((cell, i) => {
          const th = el('th');
          if (b.align[i]) th.style.textAlign = b.align[i];
          th.append(...this._inlines(cell));
          headRow.append(th);
        });
        const tbody = el('tbody');
        for (const row of b.rows) {
          const tr = el('tr');
          row.forEach((cell, i) => {
            const td = el('td');
            if (b.align[i]) td.style.textAlign = b.align[i];
            td.append(...this._inlines(cell));
            tr.append(td);
          });
          tbody.append(tr);
        }
        // Scroll wrapper keeps wide tables inside the column.
        return el('div', { class: 'table-scroll' }, [
          el('table', {}, [el('thead', {}, [headRow]), tbody]),
        ]);
      }
      case 'hr':
        return el('hr');
      default:
        return null;
    }
  }

  _inlines(nodes) {
    const out = [];
    for (const n of nodes) {
      switch (n.type) {
        case 'text':
          out.push(document.createTextNode(n.text));
          break;
        case 'softbreak':
          out.push(document.createTextNode('\n'));
          break;
        case 'hardbreak':
          out.push(el('br'));
          break;
        case 'code':
          out.push(el('code', { class: 'inline-code', text: n.text }));
          break;
        case 'strong':
          out.push(el('strong', {}, this._inlines(n.children)));
          break;
        case 'em':
          out.push(el('em', {}, this._inlines(n.children)));
          break;
        case 'strike':
          out.push(el('del', {}, this._inlines(n.children)));
          break;
        case 'link': {
          const href = sanitizeLink(n.href);
          // Defense-in-depth: accepted relative hrefs must resolve to
          // this origin — anything else renders as literal label/source.
          let ok = href != null;
          if (ok && !/^[a-z][a-z0-9+.-]*:/i.test(href)) {
            try {
              ok = new URL(href, location.href).origin === location.origin;
            } catch { ok = false; }
          }
          if (!ok) {
            out.push(document.createTextNode('['));
            out.push(...this._inlines(n.children));
            out.push(document.createTextNode(`](${n.href})`));
          } else {
            const a = el('a', { href, class: 'link' }, this._inlines(n.children));
            if (/^https?:/i.test(href)) {
              a.target = '_blank';
              a.rel = 'noopener noreferrer';
            }
            out.push(a);
          }
          break;
        }
        case 'image':
          out.push(el('span', { class: 'img-ph', text: `Image: ${n.alt}` }));
          break;
      }
    }
    return out;
  }
}

customElements.define('nusa-markdown-view', MarkdownView);
