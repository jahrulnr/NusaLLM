import { test } from 'node:test';
import assert from 'node:assert/strict';

// model-picker builds its popover with createElement; a listener-aware
// stand-in lets the test drive the real click/keydown handlers.
class FakeEl {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.listeners = {};
    this.style = { setProperty() {} };
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.hidden = false;
    this.disabled = false;
    this.textContent = '';
    this.value = '';
  }
  append(...kids) { this.children.push(...kids.filter((k) => k != null)); }
  prepend(...kids) { this.children.unshift(...kids.filter((k) => k != null)); }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  removeEventListener(type, fn) { this.listeners[type] = (this.listeners[type] || []).filter((f) => f !== fn); }
  dispatch(type, event = {}) { for (const fn of this.listeners[type] || []) fn(event); }
  dispatchEvent() { return true; }
  setAttribute(k, v) { this[k] = v; }
  getAttribute(k) { return this[k]; }
  getBoundingClientRect() { return { top: 0, left: 0, right: 400, bottom: 40, width: 400, height: 40 }; }
  focus() { globalThis.document.activeElement = this; }
  contains(node) {
    if (this === node) return true;
    return this.children.some((child) => typeof child.contains === 'function' && child.contains(node));
  }
  get firstChild() { return this.children[0] || null; }
  removeChild(c) { this.children = this.children.filter((x) => x !== c); return c; }
  replaceWith() {}
  remove() {}
  scrollIntoView() {}
  querySelector() { return null; }
  querySelectorAll() { return []; }
}
class FakeHTMLElement extends FakeEl {
  attachShadow() { this.shadowRoot = new FakeEl('#shadow'); return this.shadowRoot; }
}
const docListeners = {};
globalThis.HTMLElement = FakeHTMLElement;
globalThis.CustomEvent = class { constructor(type, init) { this.type = type; this.detail = init?.detail; } };
let PickerClass = null;
globalThis.customElements = { define: (name, cls) => { if (name === 'nusa-model-picker') PickerClass = cls; } };
globalThis.document = {
  createElement: (tag) => new FakeEl(tag),
  createElementNS: (_ns, tag) => new FakeEl(tag),
  activeElement: null,
  addEventListener(type, fn) { (docListeners[type] ||= []).push(fn); },
  removeEventListener(type, fn) { docListeners[type] = (docListeners[type] || []).filter((f) => f !== fn); },
  dispatch(type, event = {}) { for (const fn of docListeners[type] || []) fn(event); },
};
globalThis.window = {
  addEventListener() {},
  removeEventListener() {},
  innerWidth: 1280,
};
globalThis.innerWidth = 1280;
globalThis.innerHeight = 800;
globalThis.matchMedia = () => ({ matches: false, addEventListener() {} });

await import('../widgets/model-picker/model-picker.js');
const ModelPicker = PickerClass;

function firstOption(node) {
  if (node.role === 'option') return node;
  for (const child of node.children || []) {
    const hit = firstOption(child);
    if (hit) return hit;
  }
  return null;
}

// A popover advertised with aria-haspopup must be dismissible with Escape
// even after a re-render moved focus to the body. Mouse users can click
// outside; keyboard users otherwise have no way to close it.
test('Escape closes the model picker after a model is chosen', () => {
  const picker = new ModelPicker();
  picker.gateways = [{ id: 'gw', name: 'Gateway', models: [{ id: 'm1' }] }];
  picker._openMenu();
  const option = firstOption(picker.root);
  assert.ok(option, 'model option rendered');
  option.dispatch('click');
  assert.equal(picker._open, true, 'effort step keeps the popover open');
  globalThis.document.activeElement = null; // re-render dropped focus
  globalThis.document.dispatch('keydown', { key: 'Escape', preventDefault() {} });
  assert.equal(picker._open, false, 'Escape must close the open popover');
});

// Tab is not a dismissal. The picker closes only once focus has actually
// left the menu; tabbing from the search field onto the first result must
// keep it open so keyboard users can reach the options at all.
test('Tab keeps the model picker open while focus is still inside the menu', async () => {
  const picker = new ModelPicker();
  picker.gateways = [{ id: 'gw', name: 'Gateway', models: [{ id: 'm1' }] }];
  picker._openMenu();
  const option = firstOption(picker.root);
  assert.ok(option, 'model option rendered');
  option.focus();
  picker._onMenuKeydown({ key: 'Tab' });
  await new Promise((resolve) => setTimeout(resolve, 5));
  assert.equal(picker._open, true, 'focus is still on a menu option');
});


// Picking the final route closes the popover. Focus must land back on the
// trigger — the option node is removed with the menu, so leaving it there
// drops a keyboard user onto the page body.
test('choosing a route returns focus to the picker trigger', () => {
  const picker = new ModelPicker();
  picker.gateways = [{ id: 'gw', name: 'Gateway', models: [{ id: 'm1' }] }];
  picker._openMenu();
  picker._draft = { route: 'gw', modelId: 'm1', effort: 'default', provider: '' };
  picker._providers = [];
  picker._step = 'route';
  picker._render();
  const option = firstOption(picker.root);
  assert.ok(option, 'route option rendered');
  option.focus();
  assert.equal(globalThis.document.activeElement, option);
  option.dispatch('click');
  assert.equal(picker._open, false);
  assert.equal(globalThis.document.activeElement, picker.trigger);
});
