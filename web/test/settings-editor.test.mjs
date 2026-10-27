import { test } from 'node:test';
import assert from 'node:assert/strict';

// The settings editor renders real markup, so the DOM stand-in needs a
// little more than the app-shell harness: tag/[attr] matching for
// querySelectorAll, data-* -> dataset, focus tracking, and recorded
// listeners/events. Global stubs must land BEFORE the widget module
// evaluates: Widget extends HTMLElement at class definition time, and
// customElements.define runs at module bottom.
class FakeEl {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.style = {};
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.hidden = false;
    this.listeners = {};
    this.events = [];
  }
  append(...kids) { this.children.push(...kids); }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  setAttribute(k, v) {
    if (k.startsWith('data-')) {
      const camel = k.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase());
      (this.dataset ||= {})[camel] = v;
      return;
    }
    this[k] = v;
  }
  dispatchEvent(e) { this.events.push(e); return true; }
  fire(type) { (this.listeners[type] || []).forEach((fn) => fn({ target: this })); }
  focus() { document.activeElement = this; }
  blur() { if (document.activeElement === this) document.activeElement = null; }
  get firstChild() { return this.children[0] || null; }
  removeChild(c) { this.children = this.children.filter((x) => x !== c); return c; }
  querySelectorAll(selector) {
    const parts = selector.split(',').map((s) => s.trim());
    const out = [];
    const visit = (node) => {
      for (const child of node.children) {
        if (!(child instanceof FakeEl)) continue;
        if (parts.some((part) => matchSel(child, part))) out.push(child);
        visit(child);
      }
    };
    visit(this);
    return out;
  }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
}
function matchSel(node, part) {
  const m = part.match(/^([a-zA-Z]+)(?:\[([^\]]+)\])?$/);
  if (!m || node.tagName !== m[1].toUpperCase()) return false;
  return !m[2] || node[m[2]] !== undefined;
}
class FakeHTMLElement extends FakeEl {
  attachShadow() { this.shadowRoot = new FakeEl('#shadow'); return this.shadowRoot; }
}

const defined = {};
globalThis.HTMLElement = FakeHTMLElement;
globalThis.customElements = { define: (name, cls) => { defined[name] = cls; } };
globalThis.document = {
  createElement: (tag) => new FakeEl(tag),
  addEventListener() {},
  activeElement: null,
};

await import('../widgets/settings-editor/settings-editor.js');
const Editor = defined['nusa-settings-editor'];

const DOC = {
  server: { port: 10998, hosts: ['127.0.0.1', 'localhost'] },
  conversation: { mailbox_cap: 128, max_turn_steps: 512, compact_budget_pct: 90, compact_keep_tail: 4 },
  service: { header_timeout: '30s' },
  storage: { dir: './data' },
  log: { max_lines: 2000, max_files: 9, verbose: false },
};

// id -> element, for every input the editor rendered.
function collect(editor) {
  const out = {};
  for (const node of editor.root.querySelectorAll('input, textarea')) {
    if (node.id) out[node.id] = node;
  }
  return out;
}

test('a same-shape response updates values in place and keeps the focused draft', () => {
  const editor = new Editor();
  editor.settings = DOC;
  const inputs = collect(editor);
  const steps = inputs['s-conversation.max_turn_steps'];
  const tail = inputs['s-conversation.compact_keep_tail'];
  assert.ok(steps && tail, 'fields rendered');

  // The user is mid-draft on compact_keep_tail when the autosave
  // response for max_turn_steps lands — same document shape.
  tail.focus();
  tail.value = '7';
  editor.settings = {
    ...DOC,
    conversation: { ...DOC.conversation, max_turn_steps: 25 },
  };

  const after = collect(editor);
  // Nothing was rebuilt — every field is the same DOM node.
  assert.equal(after['s-conversation.max_turn_steps'], steps);
  assert.equal(after['s-conversation.compact_keep_tail'], tail);
  assert.equal(document.activeElement, tail, 'focus never left the draft');
  // The untouched field took the projected value; the draft survived.
  assert.equal(steps.value, '25');
  assert.equal(tail.value, '7');
  // The sync itself emitted nothing — only a user change emits.
  assert.equal(editor.events.length, 0);

  // The surviving node still emits the existing contract on change.
  tail.value = '9';
  tail.fire('change');
  const evt = editor.events.find((e) => e.type === 'setting-change');
  assert.deepEqual(evt.detail, { key: 'conversation.compact_keep_tail', value: 9 });
});

test('an unfocused same-shape sync refreshes every control kind', () => {
  const editor = new Editor();
  editor.settings = DOC;
  const inputs = collect(editor);
  editor.settings = {
    ...DOC,
    server: { port: 10998, hosts: ['10.0.0.1'] },
    log: { max_lines: 2000, max_files: 3, verbose: true },
  };
  const after = collect(editor);
  // Same nodes, new projected values — list, bool, and number alike.
  assert.equal(after['s-server.hosts'], inputs['s-server.hosts']);
  assert.equal(after['s-server.hosts'].value, '10.0.0.1');
  assert.equal(after['s-log.max_files'].value, '3');
  assert.equal(after['s-log.verbose'].checked, true);
  assert.equal(editor.events.length, 0);
});

test('a focused checkbox still syncs — checked is committed state, not a draft', () => {
  const editor = new Editor();
  editor.settings = DOC;
  const box = collect(editor)['s-log.verbose'];
  box.focus();
  // The failed-PATCH re-projection pushes the last good document; a
  // checkbox has no in-flight draft to protect, so it reverts even
  // while focused — and without leaving the DOM.
  editor.settings = { ...DOC, log: { ...DOC.log, verbose: true } };
  assert.equal(box.checked, true);
  assert.equal(collect(editor)['s-log.verbose'], box);
  assert.equal(document.activeElement, box, 'still attached and focused');
});

test('a changed shape falls back to a full render', () => {
  const editor = new Editor();
  editor.settings = DOC;
  const before = collect(editor)['s-conversation.max_turn_steps'];
  editor.settings = {
    ...DOC,
    conversation: { ...DOC.conversation, history_notice: 'shown' },
  };
  const after = collect(editor);
  assert.notEqual(after['s-conversation.max_turn_steps'], before);
  assert.ok(after['s-conversation.history_notice'], 'new leaf rendered');
});

test('a leaf kind change falls back to a full render', () => {
  const editor = new Editor();
  editor.settings = DOC;
  const before = collect(editor)['s-log.max_lines'];
  editor.settings = { ...DOC, log: { ...DOC.log, max_lines: 'all' } };
  const after = collect(editor);
  assert.notEqual(after['s-log.max_lines'], before);
});

test('a setting key reads as its leaf label', async () => {
  const { settingLabel } = await import('../widgets/settings-editor/settings-editor.js');
  assert.equal(settingLabel('log.max_lines'), 'max lines');
  assert.equal(settingLabel('server.port'), 'port');
});

