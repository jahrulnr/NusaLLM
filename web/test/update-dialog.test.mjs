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
  contains(node) {
    if (node === this) return true;
    for (const child of this.children || []) {
      if (child === node || (child && child.contains && child.contains(node))) return true;
    }
    return false;
  }
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

// The update poller keeps running while the dialog is open. A status
// refresh rebuilds every control in the modal; if the focused action is
// replaced without a successor, the native modal leaves the keyboard
// user on BODY and Tab can escape the dialog. The release action is the
// stable successor for a same-release refresh.
test('a status refresh while open keeps focus inside the dialog', () => {
  const originalFocus = FakeEl.prototype.focus;
  FakeEl.prototype.focus = function focus() { globalThis.document.activeElement = this; };
  globalThis.document.activeElement = null;
  try {
    const dialog = new Dialog();
    dialog.info = info;
    dialog.open();
    const before = button(dialog.body, 'Update now');
    assert.ok(before, 'install action rendered');
    before.focus();
    dialog.info = { ...info, latest: '0.10.5', notes: '## [0.10.5]\n\n- newer' };
    const after = button(dialog.body, 'Update now');
    assert.ok(after, 'install action survives the refresh');
    assert.equal(globalThis.document.activeElement, after,
      'a rerender under an open modal must hand focus to the new action');
  } finally {
    FakeEl.prototype.focus = originalFocus;
    globalThis.document.activeElement = null;
  }
});

// A status refresh can retire the release the dialog was showing (the
// server loses its cached release, a check returns empty). Rendering an
// empty body must dismiss the modal; leaving it open presents a blank,
// inert sheet over the app.
test('losing the release while open closes the dialog', () => {
  const dialog = new Dialog();
  dialog.info = info;
  dialog.open();
  assert.equal(dialog.dialog.open, true);
  dialog.info = { ...info, latest: '', available: false };
  assert.equal(dialog.dialog.open, false,
    'an empty update body must close the open modal');
});

// "Skip this version" and "Later" share the class "btn ghost". A status
// refresh while focus is on Later must land on Later again, not jump to
// the first same-class control (Skip).
test('a refresh keeps focus on the same footer action', () => {
  const originalFocus = FakeEl.prototype.focus;
  FakeEl.prototype.focus = function focus() { globalThis.document.activeElement = this; };
  globalThis.document.activeElement = null;
  try {
    const dialog = new Dialog();
    dialog.info = info;
    dialog.open();
    const later = button(dialog.body, 'Later');
    assert.ok(later, 'Later action rendered');
    later.focus();
    dialog.info = { ...info, latest: '0.10.5', notes: '## [0.10.5]\n\n- newer' };
    const again = button(dialog.body, 'Later');
    assert.ok(again, 'Later survives the refresh');
    assert.equal(globalThis.document.activeElement, again,
      'focus must stay on Later, not move to the first .btn ghost (Skip)');
  } finally {
    FakeEl.prototype.focus = originalFocus;
    globalThis.document.activeElement = null;
  }
});

function findDialogClass(node, cls) {
  if (!node) return null;
  if (node.className === cls) return node;
  for (const child of node.children || []) {
    const hit = findDialogClass(child, cls);
    if (hit) return hit;
  }
  return null;
}

// A refresh can remove the release URL while the dialog is open. The
// focused "View release" link dies with that decision; leaving focus on
// the detached anchor strands the keyboard outside the modal. Any
// surviving control may own it, but focus must not fall to BODY.
test('a removed release link keeps focus inside the dialog', () => {
  const originalFocus = FakeEl.prototype.focus;
  FakeEl.prototype.focus = function focus() { globalThis.document.activeElement = this; };
  globalThis.document.activeElement = null;
  try {
    const dialog = new Dialog();
    dialog.info = info;
    dialog.open();
    const link = findDialogClass(dialog.body, 'release-link');
    assert.ok(link, 'release link rendered');
    link.focus();
    dialog.info = { ...info, releaseUrl: '' };
    assert.notEqual(globalThis.document.activeElement, link,
      'focus stayed on the detached release link');
    assert.ok(globalThis.document.activeElement,
      'focus fell to BODY after the release link disappeared');
  } finally {
    FakeEl.prototype.focus = originalFocus;
    globalThis.document.activeElement = null;
  }
});
