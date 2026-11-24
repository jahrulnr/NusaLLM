// Tests for lib/mdsafe.js — the allowlist, the link policy and the chrome
// ownership rule the Markdown renderer applies to what it sanitized.
//
// The module is DOM-free by design (documented at its top), so these run under
// node --test with a stand-in element that implements only the attribute API
// the scrub uses. What is covered here is the policy: which tags and
// attributes survive, what a source element may keep, which link targets are
// safe, and that an element the renderer did not stamp cannot borrow chrome.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  TAGS, ATTRS, CHROME_ATTRS, OWN_CLASSES, MARK_ATTR, SAFE_URI,
  ownsChrome, scrubChrome, diagramParts,
} from '../lib/mdsafe.js';

// FakeNode is the slice of Element the scrub touches: attributes by name and
// children, no rendering.
class FakeNode {
  constructor(attrs = {}, children = []) {
    this.attrs = new Map(Object.entries(attrs));
    this.children = children;
  }
  getAttribute(name) { return this.attrs.has(name) ? this.attrs.get(name) : null; }
  setAttribute(name, value) { this.attrs.set(name, String(value)); }
  hasAttribute(name) { return this.attrs.has(name); }
  removeAttribute(name) { this.attrs.delete(name); }
}

const root = (...children) => new FakeNode({}, children);
const MARK = 'mark-1';

// ---------- the allowlist ----------

test('the tag list carries no executable, embeddable or fetching element', () => {
  for (const tag of ['script', 'style', 'form', 'iframe', 'object', 'embed', 'img', 'svg', 'math', 'video', 'audio', 'link', 'meta', 'base', 'template']) {
    assert.equal(TAGS.includes(tag), false, `${tag} must not be allowed`);
  }
  for (const tag of ['p', 'pre', 'code', 'table', 'details', 'summary', 'kbd', 'del', 'sup']) {
    assert.equal(TAGS.includes(tag), true, `${tag} is part of the documented surface`);
  }
});

test('renderer chrome is an attribute subset, and every one is allowed through sanitize', () => {
  for (const name of CHROME_ATTRS) {
    assert.ok(ATTRS.includes(name), `${name} must be allowed on renderer nodes`);
  }
  assert.ok(ATTRS.includes(MARK_ATTR), 'the render stamp has to survive sanitize to be checked');
  assert.ok(ATTRS.includes('class'), 'classes are filtered by ownership, not dropped by the allowlist');
});

test('every allowed class name belongs to the renderer, none is a layout utility', () => {
  for (const name of OWN_CLASSES) {
    assert.equal(/\b(?:fixed|absolute|inset|z-\d|bg-\w+|text-\w+)\b/.test(name), false, `${name} is not renderer chrome`);
  }
});

// ---------- the link policy ----------

test('a link may point at http(s), mailto, a same-origin path or a relative one', () => {
  for (const href of ['https://example.com/x', 'http://127.0.0.1:10998/a', 'HTTPS://EXAMPLE.COM', 'mailto:a@b.c', '/api/conversations', './x', '../x', '/\\evil.example/x']) {
    assert.equal(SAFE_URI.test(href), true, `${href} stays`);
  }
});

test('a link may not point at script, data, protocol-relative or a bare fragment', () => {
  for (const href of ['javascript:alert(1)', 'JaVaScRiPt:alert(1)', 'java\tscript:alert(1)', 'data:text/html,x', 'data:image/svg+xml,x', '//evil.example/x', '#anchor', 'vbscript:x', 'file:///etc/passwd', '']) {
    assert.equal(SAFE_URI.test(href), false, `${href} is dropped`);
  }
});

// ---------- chrome ownership ----------

test('only the mark minted for this render owns an element', () => {
  assert.equal(ownsChrome(new FakeNode({ [MARK_ATTR]: MARK }), MARK), true);
  assert.equal(ownsChrome(new FakeNode({ [MARK_ATTR]: 'other' }), MARK), false);
  assert.equal(ownsChrome(new FakeNode(), MARK), false);
  assert.equal(ownsChrome(new FakeNode({ [MARK_ATTR]: MARK }), ''), false, 'no mark minted, no owner');
});

test('a source element loses every chrome attribute and every class', () => {
  const source = new FakeNode({
    class: 'codeblock fixed inset-0',
    'data-copy': '',
    'data-diagram': '',
    'data-fn': 'a',
    'data-lang': 'js',
    'data-fn-def': 'a',
    [MARK_ATTR]: 'guessed',
    href: 'https://example.com',
  });
  const wrap = root(source);
  scrubChrome(wrap, MARK);
  assert.equal(source.getAttribute('class'), null, 'source HTML never borrows a renderer class');
  for (const name of CHROME_ATTRS) assert.equal(source.hasAttribute(name), false, `${name} is renderer-only`);
  assert.equal(source.hasAttribute(MARK_ATTR), false, 'a guessed stamp is removed');
  assert.equal(source.getAttribute('href'), 'https://example.com', 'ordinary attributes are untouched');
});

test('a renderer element keeps its chrome and only its own classes', () => {
  const own = new FakeNode({
    class: 'codeblock fixed inset-0',
    'data-copy': '',
    [MARK_ATTR]: MARK,
  });
  scrubChrome(root(own), MARK);
  assert.equal(own.getAttribute('class'), 'codeblock', 'allowed class names survive, utilities do not');
  assert.equal(own.hasAttribute('data-copy'), true, 'the renderer keeps the hook decorate() looks for');
  assert.equal(own.hasAttribute(MARK_ATTR), false, 'the stamp is spent');
});

test('ownership does not descend: a footnote body cannot inherit the section\'s chrome', () => {
  const sourceButton = new FakeNode({ 'data-copy': '', class: 'copybtn' });
  const footnoteBody = new FakeNode({}, [sourceButton]);
  const section = new FakeNode({ class: 'md-footnotes', [MARK_ATTR]: MARK }, [footnoteBody]);
  scrubChrome(root(section), MARK);
  assert.equal(section.getAttribute('class'), 'md-footnotes', 'the renderer section keeps its class');
  assert.equal(sourceButton.hasAttribute('data-copy'), false, 'model HTML inside a footnote body is still source');
  assert.equal(sourceButton.getAttribute('class'), null);
});

test('a nested renderer element is owned on its own mark, not by its parent', () => {
  const button = new FakeNode({ 'data-copy': '', class: 'copybtn', [MARK_ATTR]: MARK });
  const div = new FakeNode({ class: 'codeblock', [MARK_ATTR]: MARK }, [new FakeNode({ class: 'header' }, [button])]);
  scrubChrome(root(div), MARK);
  assert.equal(button.hasAttribute('data-copy'), true);
  assert.equal(button.getAttribute('class'), 'copybtn');
  assert.equal(button.hasAttribute(MARK_ATTR), false);
});

// ---------- diagram preflight shape ----------

test('a diagram block without a canvas or a source reads as empty, never throws', () => {
  const bare = { querySelector: () => null };
  const parts = diagramParts(bare);
  assert.equal(parts.canvas, null);
  assert.equal(parts.source, '');
  assert.deepEqual(diagramParts(undefined), { canvas: null, source: '' });
  const sourceOnly = {
    querySelector: (sel) => (sel === 'details pre' ? { textContent: 'graph TD; A-->B' } : null),
  };
  assert.deepEqual(diagramParts(sourceOnly), { canvas: null, source: 'graph TD; A-->B' });
});
