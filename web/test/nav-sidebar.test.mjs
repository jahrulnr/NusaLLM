import { test } from 'node:test';
import assert from 'node:assert/strict';

// nav-sidebar renders real markup into a shadow root, so the DOM
// stand-in carries what the widget actually touches: classList.toggle
// (the collapsed rail), setAttribute/inert, append/clear, recorded
// listeners, and attribute presence (a `disabled` attribute is empty
// string, so presence — not truthiness — is what disables the button).
// Global stubs must land BEFORE the widget module evaluates: Widget
// extends HTMLElement at class definition time, and customElements.define
// runs at module bottom.
class FakeEl {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.style = {};
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.hidden = false;
    this.listeners = {};
    this.events = [];
    this.attrs = {};
  }
  append(...kids) { this.children.push(...kids); }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  setAttribute(k, v) { this.attrs[k] = v; this[k] = v; }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  dispatchEvent(e) { this.events.push(e); return true; }
  fire(type) { (this.listeners[type] || []).forEach((fn) => fn({ target: this })); }
  get firstChild() { return this.children[0] || null; }
  removeChild(c) { this.children = this.children.filter((x) => x !== c); return c; }
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
};

await import('../widgets/nav-sidebar/nav-sidebar.js');
const Sidebar = defined['nusa-nav-sidebar'];

// Depth-first lookup by class — the harness has no selector engine.
function findAll(node, cls, out = []) {
  for (const child of node.children) {
    if (typeof child !== 'object' || !child.children) continue;
    if (child.className && String(child.className).split(/\s+/).includes(cls)) out.push(child);
    findAll(child, cls, out);
  }
  return out;
}
const find = (node, cls) => findAll(node, cls)[0] || null;
// A button's visible label lives in its single child span.
const label = (btn) => btn.children[0].textContent;

const ROWS = [
  { id: 'c1', title: 'Room One', updatedAt: '2026-10-01T09:00:00Z' },
  { id: 'c2', title: 'Room Two', updatedAt: '2026-10-02T09:00:00Z' },
  { id: 'c3', title: 'Room Three', updatedAt: '2026-10-03T09:00:00Z' },
];

// A sidebar wired the way the shell drives it: three loaded rows, 240
// threads on the server, one more page behind them.
function makeSidebar() {
  const sidebar = new Sidebar();
  sidebar.connectedCallback();
  sidebar.conversations = ROWS;
  sidebar.activeId = 'c2';
  sidebar.connection = 'open';
  sidebar.total = 240;
  sidebar.hasMore = true;
  return sidebar;
}

test('hasMore renders the Load more control and the coverage hint', () => {
  const sidebar = makeSidebar();
  const btn = find(sidebar.listEl, 'more-btn');
  assert.ok(btn, 'Load more button rendered after the rows');
  assert.equal(btn.tagName, 'BUTTON');
  assert.equal(btn.getAttribute('type'), 'button');
  assert.equal(label(btn), 'Load more');
  assert.equal(btn.getAttribute('disabled'), null, 'idle button is clickable');

  // The hint states how much of the server set is actually loaded, so
  // the client-side search's reach is visible rather than hidden.
  const coverage = find(sidebar.listEl, 'coverage');
  assert.equal(coverage.textContent, 'Loaded 3 of 240');

  // The control sits after the conversation rows, not before them.
  const rows = findAll(sidebar.listEl, 'item');
  assert.equal(rows.length, 3);
  assert.equal(sidebar.listEl.children.indexOf(find(sidebar.listEl, 'more')), 3);

  // The footer stays where it was — the control lives inside the list.
  assert.equal(find(sidebar.footerEl, 'more-btn'), null);
  assert.equal(find(sidebar.footerEl, 'conn-label').textContent, 'live');
});

test('a click emits exactly one conversation-more', () => {
  const sidebar = makeSidebar();
  const btn = find(sidebar.listEl, 'more-btn');
  btn.fire('click');
  const emitted = sidebar.events.filter((e) => e.type === 'conversation-more');
  assert.equal(emitted.length, 1, 'one emit per click');
  assert.deepEqual(emitted[0].detail, {}, 'the event carries no detail');

  // A second click on the same control emits exactly one more — the
  // widget neither swallows nor doubles the request.
  btn.fire('click');
  assert.equal(sidebar.events.filter((e) => e.type === 'conversation-more').length, 2);
});

test('moreBusy disables the button and the list stays usable', () => {
  const sidebar = makeSidebar();
  const before = find(sidebar.listEl, 'more-btn');
  sidebar.moreBusy = true;

  const btn = find(sidebar.listEl, 'more-btn');
  assert.notEqual(btn, before, 'the control row re-rendered');
  assert.equal(btn.getAttribute('disabled'), '', 'disabled while a page is in flight');
  assert.equal(label(btn), 'Loading…');
  // Load-more never borrows the list-wide loading state: the rows, the
  // selection, and the hint all survive the in-flight request.
  assert.equal(sidebar._loading, false);
  assert.ok(!findAll(sidebar.listEl, 'hint').some((n) => n.textContent === 'Loading…'),
    'no list-wide Loading… placeholder');
  assert.equal(findAll(sidebar.listEl, 'item').length, 3);
  assert.deepEqual(
    findAll(sidebar.listEl, 'item').map((n) => n.children[0].textContent),
    ['Room One', 'Room Two', 'Room Three']);
  const activeRow = findAll(sidebar.listEl, 'item').find((n) => n.getAttribute('aria-current') === 'true');
  assert.equal(activeRow.children[0].textContent, 'Room Two');
  assert.equal(find(sidebar.listEl, 'coverage').textContent, 'Loaded 3 of 240');
  // Emits nothing on its own — only a user click does.
  assert.equal(sidebar.events.filter((e) => e.type === 'conversation-more').length, 0);

  sidebar.moreBusy = false;
  const settled = find(sidebar.listEl, 'more-btn');
  assert.equal(settled.getAttribute('disabled'), null);
  assert.equal(label(settled), 'Load more');
});

test('the coverage hint counts loaded threads, not filtered rows', () => {
  const sidebar = makeSidebar();
  // The widget's own search narrows the visible rows…
  const search = find(sidebar.root, 'search');
  search.value = 'room two';
  search.fire('input');
  // …and the shell re-projects the full loaded array on the search event.
  sidebar.conversations = ROWS;
  assert.equal(findAll(sidebar.listEl, 'item').length, 1, 'the query narrowed the rows');

  // The hint still counts the LOADED set: three threads are loaded even
  // though only one matches, so the search's reach stays honest.
  assert.equal(find(sidebar.listEl, 'coverage').textContent, 'Loaded 3 of 240');
  assert.ok(find(sidebar.listEl, 'more-btn'), 'the control survives filtering');
});

test('hasMore false renders neither button nor hint', () => {
  const sidebar = new Sidebar();
  sidebar.connectedCallback();
  sidebar.conversations = ROWS;
  sidebar.total = 240; // a total behind the loaded set is not enough
  sidebar.hasMore = false;
  assert.equal(find(sidebar.listEl, 'more-btn'), null);
  assert.equal(find(sidebar.listEl, 'coverage'), null);
  assert.equal(findAll(sidebar.listEl, 'item').length, 3);

  // Reaching the end of the server set removes a control that was there.
  sidebar.hasMore = true;
  assert.ok(find(sidebar.listEl, 'more-btn'));
  sidebar.hasMore = false;
  sidebar.total = 3;
  assert.equal(find(sidebar.listEl, 'more-btn'), null);
  assert.equal(find(sidebar.listEl, 'coverage'), null);
  // Search filtering is unchanged and still happens without the control:
  // the query drives the same input listener it always did.
  const search = find(sidebar.root, 'search');
  search.value = 'room two';
  search.fire('input');
  assert.deepEqual(
    sidebar.events.filter((e) => e.type === 'conversation-search').map((e) => e.detail),
    [{ query: 'room two' }]);
  // The shell re-projects the list on that event; _query still narrows
  // whatever list it is handed.
  sidebar.conversations = ROWS;
  assert.equal(findAll(sidebar.listEl, 'item').length, 1);
});
