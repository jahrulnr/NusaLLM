import { test } from 'node:test';
import assert from 'node:assert/strict';

// The About panel in settings renders into a shadow root. The stand-in covers
// the class names, attributes, click listeners, and custom events it uses.
// Stubs land before the module evaluates, since Widget extends HTMLElement.
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
  // DOM semantics: append() turns a non-node argument such as null into text.
  append(...kids) {
    this.children.push(...kids.map((k) => (k !== null && typeof k === 'object' ? k : String(k))));
  }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  setAttribute(k, v) { this.attrs[k] = v; this[k] = v; }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  dispatchEvent(event) { this.events.push(event); return true; }
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

await import('../widgets/settings-page/settings-page.js');
const SettingsPage = defined['nusa-settings-page'];

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

const release = {
  enabled: true,
  available: false,
  current: '0.10.3',
  latest: '0.10.3',
  installable: true,
  checkedAt: new Date().toISOString(),
};

test('before the first answer, About shows loading and offers no check yet', () => {
  const page = new SettingsPage();
  const text = textOf(page.about);
  assert.ok(text.includes('Loading…'));
  assert.ok(text.includes('Checking…'));
  assert.equal(button(page.about, 'Check for updates'), null);
});

test('a newer release offers Update now for that version, and What\'s new', () => {
  const page = new SettingsPage();
  page.update = { ...release, available: true, latest: '0.10.4' };
  assert.ok(textOf(page.about).includes('NusaLLM 0.10.4 is available.'));

  click(button(page.about, 'Update now'));
  click(button(page.about, "What's new"));
  assert.deepEqual(page.events.map((e) => e.type), ['update-install', 'update-open']);
  assert.equal(page.events[0].detail.version, '0.10.4');
});

test('a build that cannot replace itself points to What\'s new, not Update now', () => {
  const page = new SettingsPage();
  page.update = { ...release, available: true, latest: '0.10.4', installable: false };
  assert.equal(button(page.about, 'Update now'), null);
  assert.ok(textOf(page.about).includes('cannot replace itself'));
  assert.notEqual(button(page.about, "What's new"), null);
});

test('an up-to-date build says so and can check again', () => {
  const page = new SettingsPage();
  page.update = release;
  assert.ok(textOf(page.about).includes('Up to date (0.10.3).'));
  click(button(page.about, 'Check for updates'));
  assert.equal(page.events.at(-1).type, 'update-check');
});

test('a failed first load reports the error and still offers a retry', () => {
  const page = new SettingsPage();
  page.update = { error: 'Failed to fetch' };
  const text = textOf(page.about);
  assert.ok(text.includes("Couldn't check for updates."));
  assert.ok(text.includes('Failed to fetch'));
  assert.ok(!text.includes('release builds only'), 'a failed load is not a dev build');
  click(button(page.about, 'Check for updates'));
  assert.equal(page.events.at(-1).type, 'update-check');
});

test('a dev build says checks run in release builds and offers no check', () => {
  const page = new SettingsPage();
  page.update = { enabled: false, available: false, current: 'dev' };
  assert.ok(textOf(page.about).includes('Update checks run in release builds only.'));
  assert.equal(button(page.about, 'Check for updates'), null);
});

test('an install in flight shows its target and reopens its progress popup', () => {
  const page = new SettingsPage();
  page.update = { ...release, available: true, latest: '0.10.4' };
  page.updateInstall = { state: 'running', version: '0.10.4' };
  assert.ok(textOf(page.about).includes('Updating to 0.10.4…'));
  click(button(page.about, 'View progress'));
  assert.equal(page.events.at(-1).type, 'update-progress-open');
});

test('a manual check is disabled while it runs', () => {
  const page = new SettingsPage();
  page.update = release;
  page.updateBusy = true;
  const busy = button(page.about, 'Checking…');
  assert.notEqual(busy, null);
  assert.equal(busy.attrs.disabled, '');
});

test('a skipped version can be shown again', () => {
  const page = new SettingsPage();
  page.update = { ...release, available: true, latest: '0.10.4', skipped: true };
  assert.ok(textOf(page.about).includes('You skipped this version.'));
  click(button(page.about, 'Show again'));
  assert.equal(page.events.at(-1).type, 'update-unskip');
});

test('a failed refresh beside a known update says so and keeps the update', () => {
  const page = new SettingsPage();
  page.update = { ...release, available: true, latest: '0.10.4', error: 'rate limited' };
  const text = textOf(page.about);
  assert.ok(text.includes('NusaLLM 0.10.4 is available.'));
  assert.ok(text.includes('Last check failed: rate limited'));
});
