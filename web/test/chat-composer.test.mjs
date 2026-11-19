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
