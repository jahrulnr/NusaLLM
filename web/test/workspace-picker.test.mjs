import { test } from 'node:test';
import assert from 'node:assert/strict';

// workspace-picker clamps an open menu with getBoundingClientRect and
// innerWidth, and must drop that inline left while the phone sheet
// query matches. Stubs land BEFORE the widget module evaluates.
const BOUNDS_LEFT = 10;
const MENU_WIDTH = 280;
const VIEW_WIDTH = 200;
// The wide-viewport clamp. Pinned so a resize-to-phone fix cannot
// quietly change the desktop formula.
const CLAMPED_LEFT = '-2px';

class FakeEl {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.style = {};
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.listeners = {};
    this.events = [];
    this.attrs = {};
  }
  append(...kids) { this.children.push(...kids); }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  setAttribute(k, v) { this.attrs[k] = v; this[k] = v; }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  dispatchEvent(event) { this.events.push(event); return true; }
  fire(type) {
    for (const fn of this.listeners[type] || []) fn({ target: this, preventDefault() {} });
  }
  focus() {}
  getBoundingClientRect() {
    return { left: BOUNDS_LEFT, top: 0, right: BOUNDS_LEFT + MENU_WIDTH, bottom: 40, width: MENU_WIDTH, height: 40 };
  }
  get firstChild() { return this.children[0] || null; }
  removeChild(child) {
    this.children = this.children.filter((node) => node !== child);
    return child;
  }
}
class FakeHTMLElement extends FakeEl {
  attachShadow() { this.shadowRoot = new FakeEl('#shadow'); return this.shadowRoot; }
}

let sheetMatches = false;
const resizeListeners = [];
const pointerListeners = [];

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
globalThis.innerWidth = VIEW_WIDTH;
globalThis.matchMedia = (query) => ({
  get matches() { return query === '(max-width: 760px)' ? sheetMatches : false; },
  addEventListener() {},
  removeEventListener() {},
});
globalThis.window = {
  addEventListener(type, fn) { if (type === 'resize') resizeListeners.push(fn); },
  removeEventListener(type, fn) {
    if (type !== 'resize') return;
    const index = resizeListeners.indexOf(fn);
    if (index >= 0) resizeListeners.splice(index, 1);
  },
};
globalThis.document = {
  createElement: (tag) => new FakeEl(tag),
  createElementNS: (_ns, tag) => new FakeEl(tag),
  addEventListener(type, fn) { if (type === 'pointerdown') pointerListeners.push(fn); },
  removeEventListener(type, fn) {
    if (type !== 'pointerdown') return;
    const index = pointerListeners.indexOf(fn);
    if (index >= 0) pointerListeners.splice(index, 1);
  },
};
// Opening the menu browses the filesystem. Leave the request pending so
// a late response cannot re-render over a resize assertion.
globalThis.fetch = () => new Promise(() => {});

await import('../widgets/workspace-picker/workspace-picker.js');
const Picker = defined['nusa-workspace-picker'];

function findAll(node, cls, out = []) {
  for (const child of node.children || []) {
    if (!child || typeof child !== 'object' || !child.children) continue;
    if (child.className && String(child.className).split(/\s+/).includes(cls)) out.push(child);
    findAll(child, cls, out);
  }
  return out;
}
const find = (node, cls) => findAll(node, cls)[0] || null;

function fireResize() {
  for (const fn of [...resizeListeners]) fn();
}
function openPicker() {
  const picker = new Picker();
  find(picker.root, 'trigger').fire('click');
  return picker;
}

test('an open wide menu clears left when the viewport crosses 760px', () => {
  sheetMatches = false;
  const picker = openPicker();
  try {
    const menu = find(picker.root, 'menu');
    assert.equal(menu.style.left, CLAMPED_LEFT);
    assert.equal(resizeListeners.length, 1);
    assert.equal(pointerListeners.length, 1);

    // A picker re-render while open must replace the listener, not stack one.
    picker.selection = { workspace: '/tmp/proj' };
    assert.equal(resizeListeners.length, 1);
    assert.equal(pointerListeners.length, 1);
    assert.equal(find(picker.root, 'menu').style.left, CLAMPED_LEFT);

    sheetMatches = true;
    fireResize();
    assert.equal(find(picker.root, 'menu').style.left, '');

    sheetMatches = false;
    fireResize();
    assert.equal(find(picker.root, 'menu').style.left, CLAMPED_LEFT);

    // Header re-renders move the node. The resize listener has to come
    // back still pointing at the open menu.
    picker.disconnectedCallback();
    assert.equal(resizeListeners.length, 0);
    assert.equal(pointerListeners.length, 0);
    picker.connectedCallback();
    assert.equal(resizeListeners.length, 1);
    assert.equal(pointerListeners.length, 1);
    sheetMatches = true;
    fireResize();
    assert.equal(find(picker.root, 'menu').style.left, '');
  } finally {
    picker.disconnectedCallback();
    sheetMatches = false;
  }
});

test('opening on a phone leaves left empty immediately', () => {
  sheetMatches = true;
  const picker = openPicker();
  try {
    assert.equal(find(picker.root, 'menu').style.left, '');
    assert.equal(resizeListeners.length, 1);
  } finally {
    picker.disconnectedCallback();
    sheetMatches = false;
  }
});

test('closing the menu removes the resize listener', () => {
  sheetMatches = false;
  const picker = openPicker();
  try {
    assert.equal(resizeListeners.length, 1);
    find(picker.root, 'trigger').fire('click');
    assert.equal(picker._open, false);
    assert.equal(resizeListeners.length, 0);
    assert.equal(pointerListeners.length, 0);
    sheetMatches = true;
    fireResize();
    assert.equal(find(picker.root, 'menu'), null);
  } finally {
    picker.disconnectedCallback();
    sheetMatches = false;
  }
});
