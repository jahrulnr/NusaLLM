// Policy for the rendered Markdown surface: which tags and attributes
// survive sanitization, which attributes only the renderer may set, and what
// a link may point at.
//
// It lives outside markdown.js so the rules can be tested without a browser
// (web/tests/mdsafe.test.mjs) while markdown.js wires the same values into
// marked, DOMPurify and the post-sanitize scrub. Nothing here touches the
// DOM beyond the node API (`getAttribute`, `removeAttribute`, `children`),
// so a test can pass a plain stand-in for an element.

// Tags Markdown produces, plus the inline/structural HTML GitHub allows.
export const TAGS = [
  'a', 'abbr', 'b', 'blockquote', 'br', 'button', 'code', 'dd', 'del', 'details', 'div', 'dl', 'dt', 'em',
  'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'header', 'hr', 'i', 'input', 'ins', 'kbd', 'li', 'mark', 'ol', 'p',
  'pre', 's', 'samp', 'section', 'small', 'span', 'strike', 'strong', 'sub', 'summary', 'sup', 'table',
  'tbody', 'td', 'tfoot', 'th', 'thead', 'tr', 'u', 'ul', 'var',
];

// MARK_ATTR is the renderer's own stamp. Only a value minted by this module's
// caller survives the scrub below, so model HTML cannot claim a stamp it was
// never given.
export const MARK_ATTR = 'data-r';

// Attributes only the renderer may set. Source HTML that carries one loses it
// in scrubChrome, so a reply cannot draw a fake Copy button or diagram frame.
export const CHROME_ATTRS = ['data-copy', 'data-lang', 'data-diagram', 'data-fn', 'data-fn-def'];

export const ATTRS = [
  'href', 'title', 'type', 'checked', 'disabled', 'align', 'start', 'open', 'colspan', 'rowspan', 'target', 'rel',
  'class', MARK_ATTR, ...CHROME_ATTRS,
];

// Only the renderer's own class names survive, so pasted HTML cannot reach
// the app's utility classes (e.g. a fixed overlay over the UI).
export const OWN_CLASSES = new Set(['codeblock', 'copybtn', 'diagram', 'canvas', 'md-image', 'md-fnref', 'md-footnotes']);

// Only http(s), mailto, and same-origin paths survive. Bare #fragments are
// the app router's, so they are dropped too.
export const SAFE_URI = /^(?:https?:|mailto:|\/(?!\/)|\.\.?\/)/i;

// ownsChrome reports whether this element carries the mark minted for the
// render in progress. A missing or guessed mark is not ownership.
export function ownsChrome(node, mark) {
  return !!mark && node.getAttribute(MARK_ATTR) === mark;
}

// keepOwnClasses reduces a renderer element to the class names this module
// allows and drops its mark (the stamp has done its job).
export function keepOwnClasses(node) {
  const keep = (node.getAttribute('class') || '').split(/\s+/).filter((c) => OWN_CLASSES.has(c));
  if (keep.length) node.setAttribute('class', keep.join(' ')); else node.removeAttribute('class');
  node.removeAttribute(MARK_ATTR);
}

// stripSourceChrome takes every renderer-owned attribute off an element that
// came from source HTML — including its classes, which only the renderer may
// use — and drops any mark it tried to present.
export function stripSourceChrome(node) {
  for (const attr of CHROME_ATTRS) node.removeAttribute(attr);
  node.removeAttribute('class');
  node.removeAttribute(MARK_ATTR);
}

// scrubChrome walks a sanitized block and settles who owns what. Ownership is
// per element and never inherited: a footnote body renders source HTML inside
// the renderer's own <section>, and an inherited flag would hand that HTML the
// chrome it must not have.
export function scrubChrome(root, mark) {
  for (const child of root.children || []) visit(child, mark);
}

function visit(node, mark) {
  const owned = ownsChrome(node, mark);
  if (owned) keepOwnClasses(node); else stripSourceChrome(node);
  for (const child of node.children || []) visit(child, mark);
}

// diagramParts reads the two pieces a diagram block needs: the canvas to mount
// into and the source to render. A block missing either is not a diagram —
// callers fall back to plain text instead of dereferencing null.
export function diagramParts(box) {
  const canvas = box?.querySelector?.('.canvas') || null;
  const pre = box?.querySelector?.('details pre') || null;
  return { canvas, source: pre ? pre.textContent ?? '' : '' };
}
