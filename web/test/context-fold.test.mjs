import { test } from 'node:test';
import assert from 'node:assert/strict';

// message-thread evaluates its widget class at load time and el() only needs
// createElement plus property writes, so a tiny element stand-in is enough.
class FakeEl {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
  }
  append(...kids) { this.children.push(...kids); }
  setAttribute(k, v) { this[k] = v; }
  addEventListener() {}
}
globalThis.HTMLElement = class extends FakeEl {};
globalThis.customElements = { define() {} };
globalThis.document = { createElement: (tag) => new FakeEl(tag) };

const { contextCount, contextEntry } = await import('../widgets/message-thread/message-thread.js');

test('contextCount names the first array of a JSON document', () => {
  const doc = JSON.stringify({ skills: [{ name: 'a' }, { name: 'b' }], paths: ['/x'] });
  assert.equal(contextCount(doc), '2 skills');
});

test('contextCount counts lines for plain text and singularizes one', () => {
  assert.equal(contextCount('line one\nline two\nline three'), '3 lines');
  assert.equal(contextCount('{"name":"x"}'), '1 line');
});

test('a short single-line injection stays a plain context line', () => {
  const node = contextEntry({ name: 'workspace', output: 'workspace changed to /w' });
  assert.equal(node.tagName, 'DIV');
  assert.equal(node.className, 'context-line');
  assert.equal(node.textContent, 'workspace changed to /w');
});

test('a document folds behind a summary and keeps the full output', () => {
  const output = JSON.stringify({ skills: Array.from({ length: 3 }, (_, i) => ({ name: `s${i}` })) }, null, 2);
  const node = contextEntry({ name: 'skills', output });
  assert.equal(node.tagName, 'DETAILS');
  assert.equal(node.className, 'context-fold');
  const [summary, body] = node.children;
  assert.equal(summary.textContent, 'skills · 3 skills');
  assert.equal(body.textContent, output);
});
