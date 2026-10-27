import { test } from 'node:test';
import assert from 'node:assert/strict';

// update-dialog is a native <dialog> inside a shadow root. The stand-in covers
// what it touches: class names, open and close, focus, click listeners, and
// the custom events it emits. Stubs land before the module evaluates.
class FakeEl {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.style = {};
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.listeners = {};
    this.events = [];
    this.attrs = {};
    // Only a <dialog> has an `open` state; a widget's own open() must not be shadowed.
    if (String(tag).toLowerCase() === 'dialog') this.open = false;
  }
  // DOM semantics: append() turns a non-node argument such as null into text.
  append(...kids) {
    this.children.push(...kids.map((k) => (k !== null && typeof k === 'object' ? k : String(k))));
  }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  setAttribute(k, v) { this.attrs[k] = v; this[k] = v; }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  dispatchEvent(event) { this.events.push(event); return true; }
  focus() {}
  showModal() { this.open = true; }
  close() { this.open = false; }
  get firstChild() { return this.children[0] || null; }
  removeChild(child) {
    this.children = this.children.filter((node) => node !== child);
    return child;
  }
}
class FakeHTMLElement extends FakeEl {
  attachShadow() { this.shadowRoot = new FakeEl('#shadow'); return this.shadowRoot; }
}

const defined = {};
globalThis.HTMLElement = FakeHTMLElement;
globalThis.customElements = { define: (name, cls) => { defined[name] = cls; } };
globalThis.CustomEvent = class CustomEvent {
  constructor(type, init = {}) {
    this.type = type;
    this.detail = init.detail;
  }
};
globalThis.document = {
  createElement: (tag) => new FakeEl(tag),
  createElementNS: (_ns, tag) => new FakeEl(tag),
};

await import('../widgets/update-dialog/update-dialog.js');
const Dialog = defined['nusa-update-dialog'];

function textOf(node) {
  if (typeof node === 'string') return node;
  if (node.textContent) return node.textContent;
  return (node.children || []).map(textOf).join(' ');
}

function button(root, label) {
  let found = null;
  const walk = (node) => {
    for (const child of node.children || []) {
      if (!child || typeof child !== 'object') continue;
      if (!found && child.tagName === 'BUTTON' && textOf(child).trim() === label) found = child;
      walk(child);
    }
  };
  walk(root);
  return found;
}

const click = (node) => {
  for (const fn of node.listeners.click || []) fn({ type: 'click', target: node });
};

const info = {
  enabled: true,
  available: true,
  current: '0.10.3',
  latest: '0.10.4',
  installable: true,
  notes: '## [0.10.4] - 2026-10-27\n\n### Fixed\n- one',
  publishedAt: '2026-10-27T10:00:00Z',
  releaseUrl: 'https://example.com/release',
  installCommand: 'curl -fsSL https://example.com/install.sh | bash',
};

test('opening without a release to show does nothing', () => {
  const dialog = new Dialog();
  dialog.open();
  assert.equal(dialog.dialog.open, false);
});

test('Update now starts the install of that release and closes the dialog', () => {
  const dialog = new Dialog();
  dialog.info = info;
  dialog.open();
  assert.equal(dialog.dialog.open, true);
  click(button(dialog.body, 'Update now'));
  assert.equal(dialog.dialog.open, false);
  const event = dialog.events.find((e) => e.type === 'update-install');
  assert.equal(event.detail.version, '0.10.4');
});

test('Skip this version records the skip for that release only', () => {
  const dialog = new Dialog();
  dialog.info = info;
  dialog.open();
  click(button(dialog.body, 'Skip this version'));
  assert.equal(dialog.dialog.open, false);
  const event = dialog.events.find((e) => e.type === 'update-skip');
  assert.equal(event.detail.version, '0.10.4');
});

test('a build that cannot replace itself offers the command, not Update now', () => {
  const dialog = new Dialog();
  dialog.info = { ...info, installable: false };
  dialog.open();
  assert.equal(button(dialog.body, 'Update now'), null);
  assert.notEqual(button(dialog.body, 'Copy install command'), null);
  assert.ok(textOf(dialog.body).includes('cannot replace itself'));
});
