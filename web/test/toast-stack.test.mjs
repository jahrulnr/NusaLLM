import { test } from 'node:test';
import assert from 'node:assert/strict';

// A minimal connected DOM: enough for the toast's builder, timers, and
// focus assertions.
class FakeEl {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.className = '';
    this.style = {};
    this.isConnected = false;
    this._classes = new Set();
    this.classList = {
      add: (...names) => names.forEach((n) => this._classes.add(n)),
      remove: (...names) => names.forEach((n) => this._classes.delete(n)),
      toggle: (name, force) => {
        const on = force === undefined ? !this._classes.has(name) : !!force;
        if (on) this._classes.add(name); else this._classes.delete(name);
        return on;
      },
    };
    this._listeners = {};
  }
  append(...kids) {
    for (const kid of kids) {
      if (!kid) continue;
      kid.parentNode = this;
      kid.isConnected = this.isConnected;
      this.children.push(kid);
    }
  }
  removeChild(child) {
    this.children = this.children.filter((c) => c !== child);
    child.parentNode = null;
    child.isConnected = false;
    return child;
  }
  remove() { this.parentNode?.removeChild(this); }
  setAttribute(key, value) { this[key] = value; }
  addEventListener(type, fn) { (this._listeners[type] ||= []).push(fn); }
  focus() { globalThis.document.activeElement = this; }
  get firstChild() { return this.children[0] || null; }
}
class FakeHTMLElement extends FakeEl {
  attachShadow() { this.shadowRoot = new FakeEl('#shadow'); this.shadowRoot.isConnected = true; return this.shadowRoot; }
}

globalThis.HTMLElement = FakeHTMLElement;
globalThis.customElements = { define(name, cls) { globalThis.__ToastStack = cls; } };
globalThis.document = {
  activeElement: new FakeEl('body'),
  createElement: (tag) => new FakeEl(tag),
  createElementNS: (_ns, tag) => new FakeEl(tag),
};

// Capture timers so the test can drive the 6-second expiry without a real
// six-second sleep.
const originalSetTimeout = globalThis.setTimeout;
const timers = [];
globalThis.setTimeout = (fn, ms) => {
  timers.push({ fn, ms });
  return timers.length;
};
await import('../widgets/toast-stack/toast-stack.js');

function find(root, predicate) {
  if (!root) return null;
  if (predicate(root)) return root;
  for (const child of root.children) {
    const hit = find(child, predicate);
    if (hit) return hit;
  }
  return null;
}

test('auto-expiry does not strand focus in a removed toast', () => {
  try {
    const ToastStack = globalThis.__ToastStack;
    assert.ok(ToastStack, 'ToastStack registered for the test');
    const stack = new ToastStack();
    stack.root.isConnected = true;
    stack.push({ tone: 'info', title: 'Update', message: 'A new release is available', action: { label: 'What’s new', onClick() {} } });
    const action = find(stack.root, (n) => n.tagName === 'BUTTON' && n.className === 'toast-action');
    assert.ok(action, 'toast action rendered');
    action.focus();
    assert.equal(globalThis.document.activeElement, action);
    // 6000ms hide, then 200ms removal.
    const hide = timers.find((t) => t.ms === 6000);
    assert.ok(hide, 'expiry timer scheduled');
    hide.fn();
    const remove = timers.find((t) => t.ms === 200);
    assert.ok(remove, 'removal timer scheduled');
    remove.fn();
    const active = globalThis.document.activeElement;
    if (active === action) {
      assert.equal(action.isConnected, true,
        'focus stayed on the disappeared toast action');
    } else {
      assert.equal(active?.isConnected, true,
        'focus moved to a detached node after the toast expired');
    }
  } finally {
    globalThis.setTimeout = originalSetTimeout;
  }
});

// A manual Dismiss removes the control under the pointer/keyboard focus.
// The timer path moves focus to the stack, but the click path must do the
// same before the toast leaves the DOM.
test('manual dismiss does not strand focus in a removed toast', () => {
  const ToastStack = globalThis.__ToastStack;
  const stack = new ToastStack();
  stack.root.isConnected = true;
  stack.push({ tone: 'warn', title: 'Heads up', message: 'Something happened' });
  const close = find(stack.root, (n) => n.tagName === 'BUTTON' && n.className === 'toast-close');
  assert.ok(close, 'close control rendered');
  close.focus();
  close._listeners.click?.[0]?.({});
  const active = globalThis.document.activeElement;
  if (active === close) {
    assert.equal(close.isConnected, true, 'focus stayed on the dismissed toast');
  } else {
    assert.equal(active?.isConnected, true, 'focus moved to a detached node after dismiss');
  }
});
