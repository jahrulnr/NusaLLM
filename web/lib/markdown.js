// Pure Markdown → AST. No DOM access — this module is testable under
// Node and is the only Markdown authority for the WebUI.
//
// Safety contract: raw HTML in the source is never interpreted — it
// stays literal text inside paragraphs. Links pass sanitizeLink();
// images never render (no remote fetch). Malformed input degrades to
// literal text — the concatenated text of the AST never loses content.

// ---------------------------------------------------------------- links

// Only http(s)/mailto and same-origin-relative shapes are allowed.
// Everything else — javascript:, data:, file:, blob:, protocol-relative
// //x, or control-char obfuscation — returns null and renders as text.
export function sanitizeLink(raw) {
  if (typeof raw !== 'string') return null;
  const s = raw.trim();
  if (!s) return null;
  // Any interior ASCII whitespace/control char (e.g. "java\tscript:")
  // is an obfuscation attempt — reject rather than reinterpret.
  if (/[\x00-\x20]/.test(s)) return null;
  // Backslashes are path-separator tricks ("//evil" spelled "\evil") —
  // never allowed in a link target.
  if (s.includes('\\')) return null;
  if (s.startsWith('//')) return null;
  // Bare '#fragment' is reserved for the app's own hash router
  // (#conversation=…) — content links never get it.
  if (s.startsWith('#')) return null;
  if (s.startsWith('/') || s.startsWith('./') || s.startsWith('../')) return s;
  const m = s.match(/^([A-Za-z][A-Za-z0-9+.-]*):/);
  if (!m) return null; // bare relative paths are not link targets
  const scheme = m[1].toLowerCase();
  if (scheme === 'http' || scheme === 'https' || scheme === 'mailto') return s;
  return null;
}

// ---------------------------------------------------------------- inline

const ESCAPABLE = /[!"#$%&'()*+,\-./:;<=>?@[\\\]^_`{|}~]/;

// matchLinklike parses `[label](url)` at src[i]==='['. Returns null on
// no match — the caller emits the source literally.
function matchLinklike(src, i) {
  const close = src.indexOf(']', i + 1);
  if (close < 0 || src[close + 1] !== '(') return null;
  const end = src.indexOf(')', close + 2);
  if (end < 0) return null;
  return { label: src.slice(i + 1, close), url: src.slice(close + 2, end), end: end + 1 };
}

// matchRun finds `marker` after `from`; needs a nonempty interior so
// `****` degrades to literal rather than an empty node.
function matchRun(src, i, marker) {
  const close = src.indexOf(marker, i + marker.length);
  if (close <= i + marker.length || close < 0) return null;
  return { inner: src.slice(i + marker.length, close), end: close + marker.length };
}

export function parseInline(source) {
  const src = String(source ?? '');
  const out = [];
  let buf = '';
  let i = 0;
  const flush = () => { if (buf) { out.push({ type: 'text', text: buf }); buf = ''; } };

  while (i < src.length) {
    const c = src[i];

    // Hard break: two+ spaces before a newline.
    if (c === ' ') {
      const m = src.slice(i).match(/^ {2,}\n/);
      if (m) { flush(); out.push({ type: 'hardbreak' }); i += m[0].length; continue; }
      buf += c; i++; continue;
    }
    if (c === '\n') { flush(); out.push({ type: 'softbreak' }); i++; continue; }

    // Backslash escape — punctuation only, else keep the backslash.
    if (c === '\\' && i + 1 < src.length && ESCAPABLE.test(src[i + 1])) {
      buf += src[i + 1]; i += 2; continue;
    }

    // Code span — matching-length backtick run; unterminated is literal.
    if (c === '`') {
      const run = src.slice(i).match(/^`+/)[0];
      const close = src.indexOf(run, i + run.length);
      if (close >= 0) {
        flush();
        out.push({ type: 'code', text: src.slice(i + run.length, close) });
        i = close + run.length;
      } else {
        buf += run; i += run.length;
      }
      continue;
    }

    // Image — never rendered as a URL; becomes a labeled placeholder.
    if (c === '!' && src[i + 1] === '[') {
      const r = matchLinklike(src, i + 1);
      if (r) {
        flush();
        out.push({ type: 'image', alt: r.label, src: r.url });
        i = r.end;
      } else { buf += c; i++; }
      continue;
    }

    // Link — unsafe hrefs render the whole construct literally.
    if (c === '[') {
      const r = matchLinklike(src, i);
      if (r) {
        const href = sanitizeLink(r.url);
        if (href) {
          flush();
          out.push({ type: 'link', href, children: parseInline(r.label) });
        } else {
          buf += src.slice(i, r.end);
        }
        i = r.end;
      } else { buf += c; i++; }
      continue;
    }

    // Autolink <scheme:...> — other <…> stays literal (raw HTML is text).
    if (c === '<') {
      const m = src.slice(i).match(/^<([A-Za-z][A-Za-z0-9+.-]*:[^<>\s]*)>/);
      if (m) {
        const href = sanitizeLink(m[1]);
        if (href) {
          flush();
          out.push({ type: 'link', href, children: [{ type: 'text', text: m[1] }] });
        } else {
          buf += m[0];
        }
        i += m[0].length;
      } else { buf += c; i++; }
      continue;
    }

    // Strong / strike before emphasis so `**`/`~~` aren't eaten by `*`.
    const strongHit = src.startsWith('**', i) ? '**'
      : src.startsWith('__', i) ? '__'
      : src.startsWith('~~', i) ? '~~' : null;
    if (strongHit) {
      const r = matchRun(src, i, strongHit);
      if (r) {
        flush();
        out.push({ type: strongHit === '~~' ? 'strike' : 'strong', children: parseInline(r.inner) });
        i = r.end;
      } else {
        buf += strongHit; i += strongHit.length;
      }
      continue;
    }

    // Emphasis — single * or _; intraword underscores still parse, an
    // acceptable simplification: content is never lost either way.
    if (c === '*' || c === '_') {
      const close = src.indexOf(c, i + 1);
      if (close > i + 1) {
        flush();
        out.push({ type: 'em', children: parseInline(src.slice(i + 1, close)) });
        i = close + 1;
      } else {
        buf += c; i++;
      }
      continue;
    }

    buf += c; i++;
  }
  flush();
  return out;
}

// ---------------------------------------------------------------- blocks

const FENCE = /^ {0,3}(`{3,}|~{3,})\s*([A-Za-z0-9#+._-]*)\s*$/;
const HEADING = /^ {0,3}(#{1,6})(?:\s(.*)|\s*)$/;
const RULE = /^ {0,3}([-*_])(\s*\1){2,}\s*$/;
const QUOTE = /^ {0,3}> ?/;
const LIST = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/;
const TASK = /^\[([ xX])\]\s+(.*)$/;

function isDelimiterRow(line) {
  return /^\s*\|?[\s:|-]*-+[\s:|-]*\|?\s*$/.test(line) && line.includes('-');
}

function splitRow(line) {
  let s = line.trim();
  if (s.startsWith('|')) s = s.slice(1);
  if (s.endsWith('|')) s = s.slice(0, -1);
  return s.split('|').map((c) => c.trim());
}

const MAX_QUOTE_DEPTH = 32;

function isBlockStart(lines, i, depth) {
  const l = lines[i];
  return FENCE.test(l) || HEADING.test(l) || RULE.test(l) ||
    (depth < MAX_QUOTE_DEPTH && QUOTE.test(l)) || LIST.test(l);
}

export function parseMarkdown(source, depth = 0) {
  const lines = String(source ?? '').split('\n');
  const blocks = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];
    if (/^\s*$/.test(line)) { i++; continue; }

    // Fenced code — unterminated fences close at EOF (streaming-safe).
    let m = line.match(FENCE);
    if (m) {
      const fence = m[1][0];
      const re = new RegExp(`^ {0,3}${fence === '`' ? '`' : '~'}{${m[1].length},}\\s*$`);
      const body = [];
      let closed = false;
      i++;
      while (i < lines.length) {
        if (re.test(lines[i])) { i++; closed = true; break; }
        body.push(lines[i]); i++;
      }
      // `closed` distinguishes a finished fence from a still-streaming
      // one — renderers must not feed unterminated fences to diagram
      // engines.
      blocks.push({ type: 'code', lang: m[2] || '', content: body.join('\n'), closed });
      continue;
    }

    // HEADING requires whitespace or end after the hashes, so `#x`
    // fails to match and lands in a paragraph — per CommonMark.
    m = line.match(HEADING);
    if (m) {
      blocks.push({ type: 'heading', level: m[1].length, inline: parseInline((m[2] || '').trim()) });
      i++; continue;
    }

    if (RULE.test(line)) { blocks.push({ type: 'hr' }); i++; continue; }

    if (depth < MAX_QUOTE_DEPTH && QUOTE.test(line)) {
      const q = [];
      while (i < lines.length && QUOTE.test(lines[i])) {
        q.push(lines[i].replace(QUOTE, '')); i++;
      }
      blocks.push({ type: 'blockquote', blocks: parseMarkdown(q.join('\n'), depth + 1) });
      continue;
    }

    // Pipe table: header line + delimiter row + body rows.
    if (i + 1 < lines.length && line.includes('|') && isDelimiterRow(lines[i + 1])) {
      const header = splitRow(line);
      const align = splitRow(lines[i + 1]).map((c) =>
        c.startsWith(':') && c.endsWith(':') ? 'center'
          : c.endsWith(':') ? 'right'
          : c.startsWith(':') ? 'left' : '');
      i += 2;
      const rows = [];
      while (i < lines.length && lines[i].includes('|') && !/^\s*$/.test(lines[i])) {
        rows.push(splitRow(lines[i]).map((c) => parseInline(c)));
        i++;
      }
      blocks.push({ type: 'table', align, header: header.map((c) => parseInline(c)), rows });
      continue;
    }

    m = line.match(LIST);
    if (m) {
      const ordered = /^\d/.test(m[2]);
      const items = [];
      while (i < lines.length) {
        const lm = lines[i].match(LIST);
        if (!lm || /^\d/.test(lm[2]) !== ordered) break;
        let content = lm[3];
        const task = content.match(TASK);
        const item = { task: task ? task[1] !== ' ' : null, inline: null, children: null };
        if (task) content = task[2];
        // Indented continuation lines join the item's paragraph.
        const parts = [content];
        i++;
        while (i < lines.length && /^ {2,}\S/.test(lines[i]) && !LIST.test(lines[i])) {
          parts.push(lines[i].trim());
          i++;
        }
        item.inline = parseInline(parts.join('\n'));
        items.push(item);
      }
      blocks.push({
        type: 'list',
        ordered,
        start: ordered ? parseInt(m[2], 10) : null,
        items,
      });
      continue;
    }

    // Paragraph — everything else until a blank line or a block start.
    // Raw HTML lines land here and render literally.
    const para = [];
    while (i < lines.length && !/^\s*$/.test(lines[i]) && !isBlockStart(lines, i, depth)) {
      // A table delimiter row after a `|`-bearing line interrupts the
      // paragraph so the table branch can take it.
      if (para.length && lines[i].includes('|') && i + 1 < lines.length && isDelimiterRow(lines[i + 1])) break;
      para.push(lines[i]); i++;
    }
    blocks.push({ type: 'paragraph', inline: parseInline(para.join('\n')) });
  }
  return blocks;
}
