import { test } from 'node:test';
import assert from 'node:assert/strict';

// Chat composer DOM harness. The composer only needs plain element
// mechanics plus a shadow root that reports its focused descendant (the
// widget uses shadowRoot.activeElement to decide whether focus lives in
// the pending panel before a reconcile).
class FakeEl {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.style = {};
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.hidden = false;
    this.listeners = {};
    this.attrs = {};
    this.value = '';
    this.scrollTop = 0;
    this.scrollHeight = 0;
    this.clientHeight = 0;
  }
  append(...kids) { this.children.push(...kids.filter((k) => k != null)); }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  setAttribute(k, v) { this.attrs[k] = v; this[k] = v; }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  removeChild(child) {
    this.children = this.children.filter((node) => node !== child);
    return child;
  }
  replaceChildren(...kids) { this.children = kids.filter((k) => k != null); }
  insertBefore(node, ref) {
    const i = ref == null ? this.children.length : this.children.indexOf(ref);
    this.children.splice(i < 0 ? this.children.length : i, 0, node);
    return node;
  }
  get firstChild() { return this.children[0] || null; }
  get childElementCount() { return this.children.length; }
  contains(node) {
    if (node === this) return true;
    for (const child of this.children) {
      if (child === node || (child && child.contains && child.contains(node))) return true;
    }
    return false;
  }
  focus() { globalThis.document.activeElement = this; }
  dispatchEvent(event) { (this.events ||= []).push(event); return true; }
}
class FakeHTMLElement extends FakeEl {
  attachShadow() { this.shadowRoot = new FakeEl('#shadow'); return this.shadowRoot; }
}

const defined = {};
globalThis.HTMLElement = FakeHTMLElement;
globalThis.customElements = { define: (name, cls) => { defined[name] = cls; } };
globalThis.document = {
  createElement: (tag) => new FakeEl(tag),
  createElementNS: (_ns, tag) => new FakeEl(tag),
  addEventListener() {},
  activeElement: null,
};
globalThis.CustomEvent = class CustomEvent {
  constructor(type, init = {}) {
    this.type = type;
    this.detail = init.detail;
  }
};

await import('../widgets/chat-composer/chat-composer.js');
const ChatComposer = defined['nusa-chat-composer'];

// The pending panel owns local text while the turn is gated. When the
// last row leaves, the panel hides — and if focus was on the Resume
// action inside it, the browser drops that focus to BODY. The composer
// already hands focus to its input when a focused row disappears; the
// footer action must follow the same rule or a keyboard user is stranded
// after resuming the queue.
test('closing the pending panel under Resume returns focus to the input', () => {
  globalThis.document.activeElement = null;
  const composer = new ChatComposer();
  composer.pendingItems = [{
    id: 'p1', text: 'held line', state: 'queued', roomLabel: 'Room', foreign: false, online: true,
  }];
  composer.resumeVisible = true;
  composer.canResume = true;
  composer.resumeButton.focus();
  composer.shadowRoot.activeElement = composer.resumeButton;

  composer.pendingItems = [];
  assert.equal(composer.pendingPanel.hidden, true, 'the empty panel is hidden');
  assert.equal(globalThis.document.activeElement, composer.input,
    'focus must return to the composer input when the panel closes under Resume');
});

function findComposerClass(node, cls) {
  if (!node) return null;
  if (node.className === cls) return node;
  for (const child of node.children || []) {
    const hit = findComposerClass(child, cls);
    if (hit) return hit;
  }
  return null;
}

// Resume disappears as soon as the held lane is released, even when local
// rows remain queued. Hiding its footer under the focused Resume button
// strands the keyboard user on BODY; the input is the stable successor.
test('hiding Resume while rows remain returns focus to the input', () => {
  globalThis.document.activeElement = null;
  const composer = new ChatComposer();
  composer.pendingItems = [
    { id: 'p1', text: 'held one', state: 'queued', roomLabel: 'Room', foreign: false, online: true },
    { id: 'p2', text: 'held two', state: 'queued', roomLabel: 'Room', foreign: false, online: true },
  ];
  composer.resumeVisible = true;
  composer.canResume = true;
  composer.resumeButton.focus();
  composer.shadowRoot.activeElement = composer.resumeButton;

  composer.resumeVisible = false; // queue resumed; rows still waiting
  assert.equal(composer.pendingPanel.hidden, false, 'rows keep the panel open');
  assert.equal(composer.pendingFoot.hidden, true, 'Resume is gone');
  assert.equal(globalThis.document.activeElement, composer.input,
    'hiding Resume under focus must hand focus to the composer input');
});

// Context stats can disappear while the popover is open (model switch,
// room change, failed stats read). Both the close control and the summary
// trigger are hidden with the details; focusing either hidden node is not
// a valid hand-off. The composer input is the surviving target.
test('hiding the context popover under focus returns focus to the input', () => {
  globalThis.document.activeElement = null;
  const composer = new ChatComposer();
  composer.context = { estimate: 12, total: 100 };
  const close = findComposerClass(composer.contextDetails, 'context-close');
  assert.ok(close, 'context close control rendered');
  composer.contextDetails.open = true;
  close.focus();
  composer.shadowRoot.activeElement = close;

  composer.context = null;
  assert.equal(composer.contextDetails.hidden, true, 'context stats hidden');
  assert.equal(globalThis.document.activeElement, composer.input,
    'hiding the popover must hand focus to the composer input, not a hidden trigger');
});

// Focus belongs to the entry, not to a numeric row index. A preceding
// row disappearing while another is appended shifts every index; using
// the old index then hands focus to a different entry's action.
test('a shifted pending row keeps focus on the same entry', () => {
  globalThis.document.activeElement = null;
  const composer = new ChatComposer();
  const item = (id, text) => ({ id, text, state: 'queued', roomLabel: 'Room', foreign: false, online: true });
  composer.pendingItems = [item('p1', 'one'), item('p2', 'two'), item('p3', 'three')];
  const rowFor = (text) => composer.pendingBody.children.find((row) =>
    findComposerClass(row, 'pending-text')?.textContent === text);
  const focusedRow = rowFor('three');
  const focused = focusedRow.children[2].children[0]; // Restore on p3
  focused.focus();
  composer.shadowRoot.activeElement = focused;

  composer.pendingItems = [item('p2', 'two'), item('p3', 'three'), item('p4', 'four')];
  const sameEntry = rowFor('three').children[2].children[0];
  assert.equal(globalThis.document.activeElement, sameEntry,
    'focus followed the old row index to a different entry');
});

// A steering receipt can go offline while its Restore action is focused;
// the rebuilt Restore is disabled. Focus must fall through to the input,
// not stay on the detached old button.
test('a disabled rebuilt pending action returns focus to the input', () => {
  const original = FakeEl.prototype.focus;
  FakeEl.prototype.focus = function focus() { if (this.disabled) return; globalThis.document.activeElement = this; };
  globalThis.document.activeElement = null;
  try {
    const composer = new ChatComposer();
    const item = { id: 'p1', text: 'steer', state: 'steering', roomLabel: 'Room', foreign: false, online: true };
    composer.pendingItems = [item];
    const row = composer.pendingBody.children[0];
    const restore = row.children[2].children[0];
    restore.focus();
    composer.shadowRoot.activeElement = restore;
    composer.pendingItems = [{ ...item, online: false }];
    assert.equal(globalThis.document.activeElement, composer.input,
      'focus must fall through the disabled action to the input');
  } finally {
    FakeEl.prototype.focus = original;
    globalThis.document.activeElement = null;
  }
});

// Resume stays visible while a refresh/offline barrier makes it
// ineligible. Disabling the focused Resume button drops browser focus;
// the composer input must be the explicit successor.
test('a disabled Resume under focus hands focus to the input', () => {
  globalThis.document.activeElement = null;
  const composer = new ChatComposer();
  composer.pendingItems = [{ id: 'p1', text: 'held', state: 'queued', roomLabel: 'Room', foreign: false, online: true }];
  composer.resumeVisible = true;
  composer.canResume = true;
  composer.resumeButton.focus();
  composer.shadowRoot.activeElement = composer.resumeButton;
  composer.canResume = false;
  assert.equal(globalThis.document.activeElement, composer.input,
    'focus must leave the disabled Resume control');
});
