// Markdown → sanitized DOM. marked parses (GFM), DOMPurify sanitizes, and
// highlight.js colors finished code blocks. Rendering is block-incremental:
// the source is lexed into top-level blocks and only blocks whose raw text
// changed are re-rendered, so streaming deltas never rebuild the whole
// message, and finished diagrams/code keep their DOM (scroll, zoom).
import { Marked } from '/vendor/marked/marked.esm.js';
import DOMPurify from '/vendor/dompurify/purify.es.mjs';
import hljs from '/vendor/highlight/highlight.min.mjs';
import { mountDiagram, validateDiagramSource } from './diagram.js';
import { TAGS, ATTRS, SAFE_URI, MARK_ATTR, scrubChrome, diagramParts } from './mdsafe.js';

const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const DIAGRAM_LANGS = new Set(['mermaid', 'mmd']);

// The stamp this render mints for the elements the renderer itself emits.
// scrubChrome() keeps chrome only on elements carrying it, so source HTML
// cannot draw the app's own controls. Minted per renderInto call: a stale
// value never matches a later render.
let mark = '';

const marked = new Marked({ gfm: true, breaks: false });
marked.use({
  renderer: {
    code({ text, lang, raw }) {
      const language = (lang || '').trim().split(/\s+/)[0].toLowerCase();
      const closed = /\n[ \t]*(`{3,}|~{3,})[ \t]*\n*$/.test(raw ?? '');
      if (DIAGRAM_LANGS.has(language) && closed) {
        return `<div class="diagram" data-diagram ${MARK_ATTR}="${mark}"><div class="canvas" ${MARK_ATTR}="${mark}"></div>`
          + `<details><summary>Diagram source</summary><pre>${esc(text)}</pre></details></div>`;
      }
      const label = DIAGRAM_LANGS.has(language) && !closed ? `${language} · streaming` : (language || 'text');
      return `<div class="codeblock" ${MARK_ATTR}="${mark}"><header><span>${esc(label)}</span>`
        + `<button type="button" class="copybtn" data-copy ${MARK_ATTR}="${mark}">Copy</button></header>`
        + `<pre><code data-lang="${esc(language)}" ${MARK_ATTR}="${mark}">${esc(text)}</code></pre></div>`;
    },
    // Images are never fetched. Raw HTML passes through and is reduced to
    // the allowlist below by DOMPurify.
    image({ text }) { return `<span class="md-image" ${MARK_ATTR}="${mark}">[image: ${esc(text)}]</span>`; },
  },
  extensions: [{
    // GitHub-style footnote reference: [^id]. Definitions are lifted out of
    // the source in renderInto; an id without a definition stays literal.
    name: 'fnref',
    level: 'inline',
    start(src) { const i = src.indexOf('[^'); return i < 0 ? undefined : i; },
    tokenizer(src) {
      const m = /^\[\^([^\]\s]+)\]/.exec(src);
      if (m && footnotes.has(m[1])) return { type: 'fnref', raw: m[0], id: m[1] };
      return undefined;
    },
    renderer(t) {
      const n = footnotes.get(t.id).n;
      return `<sup class="md-fnref" ${MARK_ATTR}="${mark}"><button type="button" data-fn="${esc(t.id)}" ${MARK_ATTR}="${mark}" title="Footnote ${n}">${n}</button></sup>`;
    },
  }],
});

// Footnotes of the message being rendered: id -> { n, text }.
let footnotes = new Map();

// Lifts "[^id]: text" definitions (with indented continuation lines) out of
// the source and numbers them by first reference.
function extractFootnotes(src) {
  const defs = new Map();
  const lines = src.split('\n');
  const kept = [];
  const prose = []; // lines outside code fences, for reference numbering
  let fence = null;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const f = /^ {0,3}(`{3,}|~{3,})/.exec(line);
    if (fence) {
      if (f && f[1][0] === fence[0] && f[1].length >= fence.length && line.trim() === f[1]) fence = null;
      kept.push(line);
      continue;
    }
    if (f) { fence = f[1]; kept.push(line); continue; }
    const m = /^ {0,3}\[\^([^\]\s]+)\]:[ \t]?(.*)$/.exec(line);
    if (!m) { kept.push(line); prose.push(line); continue; }
    const body = [m[2]];
    while (i + 1 < lines.length && (/^(?: {2,}|\t)/.test(lines[i + 1]) || (lines[i + 1].trim() === '' && /^(?: {2,}|\t)/.test(lines[i + 2] ?? '')))) {
      body.push(lines[++i].replace(/^(?: {1,4}|\t)/, ''));
    }
    if (!defs.has(m[1])) defs.set(m[1], body.join('\n'));
  }
  const notes = new Map();
  let n = 0;
  for (const r of prose.join('\n').matchAll(/\[\^([^\]\s]+)\]/g)) {
    if (defs.has(r[1]) && !notes.has(r[1])) notes.set(r[1], { n: ++n, text: defs.get(r[1]) });
  }
  return { src: kept.join('\n'), notes };
}

// The allowlist and the link policy live in mdsafe.js so they can be tested
// without a browser; this module only wires them in.
DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName === 'INPUT' && node.getAttribute('type') !== 'checkbox') node.remove();
  if (node.tagName === 'A') {
    const href = node.getAttribute('href') || '';
    if (!SAFE_URI.test(href)) node.removeAttribute('href');
    node.setAttribute('target', '_blank');
    node.setAttribute('rel', 'noopener noreferrer');
  }
});
const PURIFY = { ALLOWED_TAGS: TAGS, ALLOWED_ATTR: ATTRS, ALLOW_DATA_ATTR: false };

function renderBlock(token, links) {
  const list = [token];
  list.links = links;
  const html = marked.parser(list);
  const wrap = document.createElement('div');
  wrap.className = 'contents';
  wrap.style.display = 'contents';
  wrap.innerHTML = DOMPurify.sanitize(html, PURIFY);
  // Sanitized first, chrome settled second: what the renderer marked keeps its
  // classes and data attributes, everything else loses them.
  scrubChrome(wrap, mark);
  return wrap;
}

function decorate(wrap, final) {
  for (const code of wrap.querySelectorAll('.codeblock code')) {
    const lang = code.dataset.lang;
    if (lang && hljs.getLanguage(lang)) {
      try { code.innerHTML = hljs.highlight(code.textContent, { language: lang, ignoreIllegals: true }).value; } catch { /* plain */ }
    }
  }
  for (const box of wrap.querySelectorAll('[data-diagram]')) {
    const { canvas, source } = diagramParts(box);
    const details = box.querySelector('details');
    // A block without a canvas or a source is not a diagram the renderer drew:
    // leave its text on screen instead of dereferencing null.
    if (!canvas || !source) {
      if (canvas) {
        canvas.textContent = 'Diagram source is missing — showing the block as text.';
        canvas.className = 'canvas text-xs text-muted';
      }
      if (details) details.open = true;
      box.removeAttribute('data-diagram');
      continue;
    }
    if (!validateDiagramSource(source)) {
      canvas.textContent = 'Diagram uses unsupported features — showing source.';
      canvas.className = 'canvas text-xs text-muted';
      if (details) details.open = true;
      continue;
    }
    try {
      box._pele = mountDiagram(canvas, source, {
        onError: () => { canvas.dataset.error = '1'; },
      });
    } catch {
      canvas.textContent = 'Diagram could not be rendered.';
      canvas.className = 'canvas text-xs text-muted';
      if (details) details.open = true;
    }
  }
  wrap._final = final;
}

function dispose(node) {
  for (const box of node.querySelectorAll?.('[data-diagram]') ?? []) {
    try { box._pele?.destroy(); } catch { /* already gone */ }
  }
}

// renderInto reconciles `root` against `source`. Blocks keep their DOM when
// their raw text is unchanged; the trailing block re-renders while streaming.
export function renderInto(root, source) {
  let tokens;
  // A fresh stamp per render: it authorizes exactly the elements this call's
  // renderers emit, and scrubChrome removes it again.
  mark = 'r' + Math.random().toString(36).slice(2, 10) + Date.now().toString(36);
  // marked rejects a closing fence followed by a tab ("```\t"), which
  // CommonMark allows; trim trailing blanks on bare fence lines first.
  const fenced = String(source ?? '').replace(/^( {0,3}(?:`{3,}|~{3,}))[ \t]+$/gm, '$1');
  const { src, notes } = extractFootnotes(fenced);
  footnotes = notes;
  // Blocks re-render when the footnote set changes, since references in
  // them resolve against it.
  const sig = [...notes].map(([id, v]) => id + '\u0001' + v.n + '\u0001' + v.text).join('\u0002');
  try { tokens = marked.lexer(src); } catch { tokens = [{ type: 'paragraph', raw: src, text: src, tokens: [{ type: 'text', raw: src, text: src }] }]; }
  const blocks = tokens.filter((t) => t.type !== 'space');
  if (notes.size) blocks.push({ type: 'footnotes', raw: '\u0000footnotes' });
  const kids = [...root.children];
  for (let i = 0; i < blocks.length; i++) {
    const t = blocks[i];
    const have = kids[i];
    if (have && have._raw === t.raw && have._sig === sig) continue;
    const next = t.type === 'footnotes' ? renderFootnotes(notes, tokens.links) : renderBlock(t, tokens.links);
    next._raw = t.raw;
    next._sig = sig;
    // One block that cannot be decorated must not take the rest of the answer
    // with it: the text stays, the widget is what the user loses.
    try { decorate(next); } catch { /* plain block */ }
    if (have) { dispose(have); root.replaceChild(next, have); } else root.appendChild(next);
  }
  for (let i = blocks.length; i < kids.length; i++) { dispose(kids[i]); kids[i].remove(); }
}

function renderFootnotes(notes, links) {
  let html = `<section class="md-footnotes" ${MARK_ATTR}="${mark}"><hr><ol>`;
  for (const [id, v] of notes) {
    const body = marked.lexer(v.text);
    body.links = links;
    html += `<li data-fn-def="${esc(id)}" ${MARK_ATTR}="${mark}">${marked.parser(body)}</li>`;
  }
  html += '</ol></section>';
  const wrap = document.createElement('div');
  wrap.style.display = 'contents';
  wrap.innerHTML = DOMPurify.sanitize(html, PURIFY);
  // A footnote body renders source HTML inside the renderer's own section; the
  // scrub is per element, so only the <section> and <li> keep their chrome.
  scrubChrome(wrap, mark);
  return wrap;
}

export function disposeTree(root) { dispose(root); }

// Copy is the answer, not the renderer: the code label, the word "Copy", the
// footnote digits and the diagram toggle live inside the prose's own DOM, and
// whether a browser serializes them into the copied text depends on how it
// treats user-select, on whether the selection came from a mouse or a range a
// script built, and on the platform. Rebuilding the plain text from a clone
// with that chrome removed makes the payload the same however it was made.
// The cost is deliberate: the copy is text/plain, never the rendered HTML.
const BLOCK_TAGS = new Set(['P', 'DIV', 'PRE', 'LI', 'UL', 'OL', 'BLOCKQUOTE', 'TABLE', 'TR', 'SECTION',
  'DETAILS', 'H1', 'H2', 'H3', 'H4', 'H5', 'H6', 'HR']);
const CHROME_SELECTOR = '.codeblock header, .copybtn, .md-fnref, .md-footnotes hr, .diagram summary';

function blockText(node) {
  let out = '';
  for (const child of node.childNodes) {
    if (child.nodeType === Node.TEXT_NODE) { out += child.nodeValue; continue; }
    if (child.nodeType !== Node.ELEMENT_NODE) continue;
    if (child.tagName === 'BR') { out += '\n'; continue; }
    const inner = blockText(child);
    out += BLOCK_TAGS.has(child.tagName) ? `\n${inner}\n` : inner;
  }
  return out;
}

export function handleCopy(ev) {
  const data = ev.clipboardData;
  const sel = globalThis.getSelection?.();
  if (!data || !sel || sel.isCollapsed || sel.rangeCount === 0) return;
  const holder = document.createElement('div');
  holder.append(sel.getRangeAt(0).cloneContents());
  for (const el of holder.querySelectorAll(CHROME_SELECTOR)) el.remove();
  const text = blockText(holder).replace(/[ \t]+\n/g, '\n').replace(/\n{3,}/g, '\n\n').trim();
  if (!text) return;
  ev.preventDefault();
  data.setData('text/plain', text);
}

// Copy buttons are delegated so rendered HTML carries no handlers.
export function handleCopyClick(ev) {
  const fn = ev.target.closest?.('[data-fn]');
  if (fn) {
    const def = [...(fn.closest('.md')?.querySelectorAll('[data-fn-def]') ?? [])].find((x) => x.dataset.fnDef === fn.dataset.fn);
    def?.scrollIntoView({ behavior: 'smooth', block: 'center' });
    return;
  }
  const btn = ev.target.closest?.('[data-copy]');
  if (!btn) return;
  const code = btn.closest('.codeblock')?.querySelector('code');
  if (!code) return;
  navigator.clipboard?.writeText(code.textContent).then(() => {
    btn.textContent = 'Copied';
    setTimeout(() => { btn.textContent = 'Copy'; }, 1200);
  }, () => { btn.textContent = 'Copy failed'; });
}
