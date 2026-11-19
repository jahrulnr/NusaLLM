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
  setAttribute(k, v) {
    this.attrs[k] = v;
    // The .disabled DOM property is boolean — the attribute's presence,
    // not its value, disables the control.
    this[k] = k === 'disabled' ? true : v;
  }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  contains(node) {
    if (node === this) return true;
    for (const child of this.children || []) {
      if (child === node || (child && child.contains && child.contains(node))) return true;
    }
    return false;
  }
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

// The section parks focus while the Check control is disabled in flight;
// once the answer re-enables it, the pending key must land back on the
// button the user activated. Treating the section's own focus as the
// observed key clears the queued restore and leaves focus on the section.
test('a finished update check restores focus to its control', () => {
  const originalFocus = FakeEl.prototype.focus;
  FakeEl.prototype.focus = function focus() { globalThis.document.activeElement = this; };
  globalThis.document.activeElement = null;
  try {
    const page = new SettingsPage();
    page.update = release;
    const check = button(page.about, 'Check for updates');
    assert.ok(check, 'Check for updates rendered');
    check.focus();
    page.updateBusy = true;
    assert.equal(button(page.about, 'Checking…').attrs.disabled, '');
    page.updateBusy = false;
    const again = button(page.about, 'Check for updates');
    assert.ok(again, 'Check for updates re-enabled');
    assert.equal(globalThis.document.activeElement, again,
      'the completed check must return focus to its control');
  } finally {
    FakeEl.prototype.focus = originalFocus;
    globalThis.document.activeElement = null;
  }
});

// When a newer release stops being available while focus is on "Update
// now", that action disappears. Focus cannot stay on the detached node;
// the always-present Check for updates control is the successor.
test('retiring the focused install action moves focus to Check', () => {
  const originalFocus = FakeEl.prototype.focus;
  FakeEl.prototype.focus = function focus() { globalThis.document.activeElement = this; };
  globalThis.document.activeElement = null;
  try {
    const page = new SettingsPage();
    page.update = { ...release, available: true, latest: '0.10.4', installable: true };
    const install = button(page.about, 'Update now');
    assert.ok(install, 'Update now rendered');
    install.focus();
    page.update = { ...release, available: false };
    const check = button(page.about, 'Check for updates');
    assert.ok(check, 'Check for updates remains');
    assert.equal(globalThis.document.activeElement, check,
      'the removed install action must hand focus to the remaining check control');
  } finally {
    FakeEl.prototype.focus = originalFocus;
    globalThis.document.activeElement = null;
  }
});

// A failed gateway delete must reach the provider manager so it can drop
// its in-flight guard and re-enable the row. The shell writes
// settingsPage.gatewayDeleteResult; without a setter here that write
// lands on the host element and the delete trigger stays disabled
// forever.
test('gateway delete results reach the provider manager', () => {
  const page = new SettingsPage();
  const providers = page.providers;
  page.gatewayDeleteResult = { id: 'gw1', error: 'delete refused' };
  assert.deepEqual(providers.gatewayDeleteResult, { id: 'gw1', error: 'delete refused' });
});
