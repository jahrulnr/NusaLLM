import { test } from 'node:test';
import assert from 'node:assert/strict';

// conversation-header renders into a shadow root and owns a <dialog>.
// The stand-in covers what that widget touches: class selectors, dialog
// open/close (close dispatches `close` only after open is false), and
// the desktop matchMedia listener. Stubs land BEFORE the widget module
// evaluates — Widget extends HTMLElement at class definition time, and
// customElements.define runs at module bottom.
class FakeEl {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.style = {};
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.listeners = {};
    this.events = [];
    this.attrs = {};
    this.open = false;
  }
  append(...kids) { this.children.push(...kids); }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  setAttribute(k, v) { this.attrs[k] = v; this[k] = v; }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  dispatchEvent(event) { this.events.push(event); return true; }
  fire(type, extra = {}) {
    const event = { target: this, ...extra };
    for (const fn of this.listeners[type] || []) fn(event);
  }
  focus() {}
  showModal() { this.open = true; }
  // Native dialog.close() drops `open` and then fires `close` synchronously.
  // Escape uses this path and never calls _closeSheet.
  close() {
    if (!this.open) return;
    this.open = false;
    for (const fn of [...(this.listeners.close || [])]) fn({ type: 'close', target: this });
  }
  get firstChild() { return this.children[0] || null; }
  removeChild(child) {
    this.children = this.children.filter((node) => node !== child);
    return child;
  }
  querySelector(selector) {
    const parts = selector.split(',').map((part) => part.trim());
    const matches = (node, part) => {
      const wanted = part.split('.').filter(Boolean);
      if (!wanted.length) return false;
      const tokens = String(node.className || '').split(/\s+/);
      return wanted.every((token) => tokens.includes(token));
    };
    const walk = (node) => {
      for (const child of node.children || []) {
        if (!child || typeof child !== 'object') continue;
        if (parts.some((part) => matches(child, part))) return child;
        const found = walk(child);
        if (found) return found;
      }
      return null;
    };
    return walk(this);
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
globalThis.getComputedStyle = () => ({ transform: 'none' });
globalThis.document = {
  createElement: (tag) => new FakeEl(tag),
  createElementNS: (_ns, tag) => new FakeEl(tag),
  addEventListener() {},
};
globalThis.matchMedia = (query) => {
  const listeners = [];
  return {
    query,
    matches: false,
    addEventListener(type, fn) { if (type === 'change') listeners.push(fn); },
    removeEventListener() {},
    fire(matches) {
      for (const fn of [...listeners]) fn({ matches, media: query });
    },
  };
};

await import('../widgets/conversation-header/conversation-header.js');
const Header = defined['nusa-conversation-header'];

function findAll(node, cls, out = []) {
  for (const child of node.children || []) {
    if (!child || typeof child !== 'object' || !child.children) continue;
    if (child.className && String(child.className).split(/\s+/).includes(cls)) out.push(child);
    findAll(child, cls, out);
  }
  return out;
}
const find = (node, cls) => findAll(node, cls)[0] || null;

function labelOf(node) {
  for (const child of node.children || []) {
    if (child.textContent) return child.textContent;
  }
  return node.textContent || '';
}
function findButton(root, text) {
  const buttons = [];
  const walk = (node) => {
    for (const child of node.children || []) {
      if (!child || typeof child !== 'object') continue;
      if (child.tagName === 'BUTTON' && labelOf(child) === text) buttons.push(child);
      walk(child);
    }
  };
  walk(root);
  return buttons[0] || null;
}

function makeHeader() {
  const header = new Header();
  header.conversation = { id: 'c1', title: 'Hello' };
  return header;
}
function openSheet(header) {
  find(header.root, 'more-btn').fire('click');
  assert.equal(header.sheet.open, true);
}

test('Escape dismiss clears a confirm row the header already painted', () => {
  const header = makeHeader();
  openSheet(header);
  findButton(header.sheet, 'Delete thread').fire('click');
  assert.equal(header._confirmingDelete, true);
  // A later header render while the sheet is confirming paints the
  // desktop row. Phones hide it; it must not survive the dismiss.
  header.connection = 'open';
  assert.ok(find(header.root, 'confirm'));

  let closes = 0;
  const orig = header.sheet.close;
  header.sheet.close = function () {
    closes += 1;
    return orig.call(this);
  };
  header.sheet.close();

  assert.equal(closes, 1, 'the close render must not call close() again');
  assert.equal(header.sheet.open, false);
  assert.equal(header._confirmingDelete, false);
  assert.equal(find(header.root, 'confirm'), null);
});

test('backdrop dismiss stays clear across a later header render', () => {
  const header = makeHeader();
  openSheet(header);
  findButton(header.sheet, 'Delete thread').fire('click');
  header.connection = 'open';
  assert.ok(find(header.root, 'confirm'));

  // Backdrop click is the _closeSheet path (X uses the same method).
  header.sheet.fire('click');
  assert.equal(header.sheet.open, false);
  assert.equal(header._confirmingDelete, false);
  assert.equal(find(header.root, 'confirm'), null);

  header.connection = 'closed';
  assert.equal(header._confirmingDelete, false);
  assert.equal(find(header.root, 'confirm'), null);
});

test('desktop inline confirm is unchanged when the sheet never opens', () => {
  const header = makeHeader();
  assert.equal(header.sheet.open, false);

  findAll(header.root, 'danger')[0].fire('click');
  assert.equal(header._confirmingDelete, true);
  assert.ok(find(header.root, 'confirm'));
  assert.equal(header.sheet.open, false);

  // The desktop listener only closes an open sheet. It must not eat a
  // confirm that was never inside the dialog.
  header._desktopQuery.fire(true);
  assert.equal(header._confirmingDelete, true);
  assert.ok(find(header.root, 'confirm'));

  find(header.root, 'confirm-no').fire('click');
  assert.equal(header._confirmingDelete, false);
  assert.equal(find(header.root, 'confirm'), null);

  findAll(header.root, 'danger')[0].fire('click');
  find(header.root, 'confirm-yes').fire('click');
  assert.equal(header.events.filter((event) => event.type === 'conversation-delete').length, 1);
});

test('widening to desktop closes the sheet and drops its confirm', () => {
  const header = makeHeader();
  openSheet(header);
  findButton(header.sheet, 'Delete thread').fire('click');
  header.connection = 'open';
  assert.ok(find(header.root, 'confirm'));

  header._desktopQuery.fire(true);
  assert.equal(header.sheet.open, false);
  assert.equal(header._confirmingDelete, false);
  assert.equal(find(header.root, 'confirm'), null);
});

test('opening the sheet and Keep both clear a header confirm row', () => {
  const header = makeHeader();
  findAll(header.root, 'danger')[0].fire('click');
  assert.ok(find(header.root, 'confirm'));

  openSheet(header);
  assert.equal(header._confirmingDelete, false);
  assert.equal(find(header.root, 'confirm'), null);
  assert.equal(find(header.sheet, 'sheet-confirm'), null);
  assert.ok(findButton(header.sheet, 'Delete thread'));

  findButton(header.sheet, 'Delete thread').fire('click');
  header.connection = 'open';
  assert.ok(find(header.root, 'confirm'));
  assert.equal(header.sheet.open, true);

  find(header.sheet, 'sheet-no').fire('click');
  assert.equal(header.sheet.open, true);
  assert.equal(header._confirmingDelete, false);
  assert.equal(find(header.root, 'confirm'), null);
  assert.equal(find(header.sheet, 'sheet-confirm'), null);
  assert.ok(find(header.sheet, 'sheet-action'));
});

test('rename from the sheet still ends in rename mode', () => {
  const header = makeHeader();
  openSheet(header);
  findButton(header.sheet, 'Rename thread').fire('click');
  assert.equal(header.sheet.open, false);
  assert.equal(header._renaming, true);
  assert.equal(header._confirmingDelete, false);
  assert.ok(find(header.root, 'rename-input'));
});

test('close does not clear a rename that was already started', () => {
  const header = makeHeader();
  header._renaming = true;
  header.sheet.showModal();
  header.sheet.close();
  assert.equal(header._renaming, true);
  assert.ok(find(header.root, 'rename-input'));
  assert.equal(header._confirmingDelete, false);
});

test('sheet Delete closes, then emits conversation-delete', () => {
  const header = makeHeader();
  openSheet(header);
  findButton(header.sheet, 'Delete thread').fire('click');

  const order = [];
  const origClose = header.sheet.close;
  header.sheet.close = function () {
    order.push('close');
    return origClose.call(this);
  };
  const origDispatch = header.dispatchEvent;
  header.dispatchEvent = function (event) {
    order.push(event.type);
    return origDispatch.call(this, event);
  };

  find(header.sheet, 'sheet-yes').fire('click');
  assert.deepEqual(order, ['close', 'conversation-delete']);
  assert.equal(header.sheet.open, false);
  assert.equal(header._confirmingDelete, false);
  assert.equal(find(header.root, 'confirm'), null);
});
