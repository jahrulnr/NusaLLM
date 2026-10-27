import { test } from 'node:test';
import assert from 'node:assert/strict';

// update-progress renders into a shadow root and owns a <dialog>. The
// stand-in covers what that widget touches: class names, dialog open and
// close, focus, and click listeners. Stubs land before the module evaluates,
// since Widget extends HTMLElement at class definition time.
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

await import('../widgets/update-progress/update-progress.js');
const Progress = defined['nusa-update-progress'];

function textOf(node) {
  if (typeof node === 'string') return node;
  if (node.textContent) return node.textContent;
  return (node.children || []).map(textOf).join(' ');
}

function walk(node, visit) {
  for (const child of node.children || []) {
    if (!child || typeof child !== 'object') continue;
    visit(child);
    walk(child, visit);
  }
}

function button(root, label) {
  let found = null;
  walk(root, (node) => {
    if (!found && node.tagName === 'BUTTON' && textOf(node).trim() === label) found = node;
  });
  return found;
}

const click = (node) => {
  for (const fn of node.listeners.click || []) fn({ type: 'click', target: node });
};

const make = (view) => {
  const popup = new Progress();
  popup.view = view;
  return popup;
};

test('an idle popup has no install to show, so it does not open', () => {
  const popup = new Progress();
  popup.open();
  assert.equal(popup.dialog.open, false);
});

test('a running download shows its headline, the route, and the byte bar', () => {
  const popup = make({
    state: 'running', phase: 'download', version: '0.10.4', from: '0.10.3', bytesDone: 50, bytesTotal: 100,
  });
  popup.open();
  const text = textOf(popup.panel);
  assert.equal(popup.dialog.open, true);
  assert.ok(text.includes('Downloading NusaLLM 0.10.4'));
  assert.ok(text.includes('0.10.3 → 0.10.4'));
  assert.ok(text.includes('50 B of 100 B · 50%'));
  assert.ok(text.includes('Closing this window does not stop the update.'));
  assert.equal(text.includes('null'), false, 'an empty slot must not print as text');
  assert.equal(button(popup.panel, 'Try again'), null);

  click(button(popup.panel, 'Close'));
  assert.equal(popup.dialog.open, false);
});

test('a finished update offers a reload that the shell handles', () => {
  const popup = make({ state: 'succeeded', phase: 'restart', version: '0.10.4', from: '0.10.3' });
  popup.open();
  assert.ok(textOf(popup.panel).includes('NusaLLM 0.10.4 is running'));
  assert.equal(textOf(popup.panel).includes('null'), false);
  click(button(popup.panel, 'Reload NusaLLM'));
  assert.deepEqual(popup.events.map((event) => event.type), ['update-reload']);
});

test('a failed download shows the error and offers a retry; nothing was installed', () => {
  const popup = make({
    state: 'failed', phase: 'download', version: '0.10.4', error: 'connection reset',
  });
  popup.open();
  const text = textOf(popup.panel);
  assert.ok(text.includes('connection reset'));
  assert.ok(text.includes('Nothing was installed.'));
  assert.equal(text.includes('null'), false);
  click(button(popup.panel, 'Try again'));
  assert.equal(popup.events.at(-1).type, 'update-retry');
});

test('a failed restart offers no retry, because the new version is already installed', () => {
  const popup = make({
    state: 'failed', phase: 'restart', version: '0.10.4', error: 'did not come back',
  });
  popup.open();
  assert.equal(button(popup.panel, 'Try again'), null);
  assert.ok(textOf(popup.panel).includes('Restart NusaLLM to run it.'));
});

test('a service restart says which restart is happening and shows no byte bar', () => {
  const popup = make({ state: 'running', phase: 'restart', version: '0.10.4', restart: 'service' });
  popup.open();
  const text = textOf(popup.panel);
  assert.ok(text.includes('Restarting the service'));
  assert.ok(text.includes('This window reconnects when it is back.'));
  assert.equal(text.includes('null'), false);
  let bar = null;
  walk(popup.panel, (node) => { if (node.attrs.role === 'progressbar') bar = node; });
  assert.equal(bar, null);
});
