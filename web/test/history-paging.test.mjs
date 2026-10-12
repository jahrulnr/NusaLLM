import { test } from 'node:test';
import assert from 'node:assert/strict';

// history-paging drives the app shell headlessly — the same DOM
// stand-in style as app-shell.test.mjs, plus what the real
// message-thread widget touches when it renders for real: listeners
// that actually dispatch (widget events must reach the shell),
// ResizeObserver/rAF, layout rects, and createElementNS for icons.
// Global stubs must land BEFORE the widget modules evaluate: Widget
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
    this.scrollTop = 0;
    this.scrollHeight = 0;
    this.clientHeight = 0;
  }
  append(...kids) { this.children.push(...kids); }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  setAttribute(k, v) { this.attrs[k] = v; this[k] = v; }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  dispatchEvent(e) {
    this.events.push(e);
    (this.listeners[e.type] || []).forEach((fn) => fn.call(this, e));
    return true;
  }
  fire(type, e = {}) { this.dispatchEvent({ type, target: this, ...e }); }
  push(...items) { (this.pushed ||= []).push(...items); }
  getBoundingClientRect() {
    return { top: 0, left: 0, right: 0, bottom: 0, width: 0, height: 0 };
  }
  before() {}
  remove() {}
  get firstChild() { return this.children[0] || null; }
  removeChild(c) { this.children = this.children.filter((x) => x !== c); return c; }
}
class FakeHTMLElement extends FakeEl {
  attachShadow() { this.shadowRoot = new FakeEl('#shadow'); return this.shadowRoot; }
}
class FakeWS {
  static OPEN = 1;
  static CONNECTING = 0;
  static instances = [];
  constructor(url) {
    this.url = url;
    this.readyState = FakeWS.CONNECTING;
    this.sent = [];
    FakeWS.instances.push(this);
  }
  send(raw) { this.sent.push(JSON.parse(raw)); }
  close() { this.readyState = 3; }
  _open() { this.readyState = FakeWS.OPEN; this.onopen && this.onopen(); }
  _message(frame) { this.onmessage && this.onmessage({ data: JSON.stringify(frame) }); }
  _close() { this.readyState = 3; this.onclose && this.onclose(); }
}

const defined = {};
globalThis.HTMLElement = FakeHTMLElement;
globalThis.customElements = { define: (name, cls) => { defined[name] = cls; } };
globalThis.document = {
  createElement: (tag) => new FakeEl(tag),
  createElementNS: (_ns, tag) => new FakeEl(tag),
  addEventListener() {},
};
globalThis.matchMedia = (q) => ({ matches: q === '(min-width: 761px)', media: q, addEventListener() {} });
globalThis.location = { protocol: 'http:', host: 'test.local', hash: '', pathname: '/' };
globalThis.history = { replaceState() {} };
globalThis.addEventListener = () => {};
globalThis.WebSocket = FakeWS;
globalThis.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
globalThis.requestAnimationFrame = (fn) => setTimeout(fn, 0);
globalThis.cancelAnimationFrame = (id) => clearTimeout(id);

// fetch answers through `routes` ([regex, handler] pairs consulted in
// order) — a test replaces the table wholesale. Setting `deferred`
// instead parks every request until the test resolves it by hand, for
// the in-flight guard assertions.
const calls = [];
let routes = [];
let deferred = null;
const json = (body) => new Response(JSON.stringify(body));
const qp = (url, key) => new URL(url, 'http://t').searchParams.get(key);
globalThis.fetch = (url) => {
  calls.push(url);
  if (deferred) return new Promise((res, rej) => deferred.push({ url, res, rej }));
  const hit = routes.find(([re]) => re.test(url));
  return Promise.resolve(hit ? hit[1](url.match(hit[0]), url) : json({}));
};

await import('../widgets/app-shell/app-shell.js');
await import('../widgets/message-thread/message-thread.js');
const AppShell = defined['nusa-app'];
const MessageThread = defined['nusa-message-thread'];

// Depth-first class lookup — the harness has no selector engine.
function findAll(node, cls, out = []) {
  for (const child of node.children || []) {
    if (typeof child !== 'object' || !child.children) continue;
    if (child.className && String(child.className).split(/\s+/).includes(cls)) out.push(child);
    findAll(child, cls, out);
  }
  return out;
}
const find = (node, cls) => findAll(node, cls)[0] || null;

const convDTO = (id) => ({ id, title: `Room ${id}`, canCompact: false });
const msgDTO = (id) => ({ id, role: 'user', userType: 'origin', content: `message ${id}` });
const convs = (n, prefix = 'c') =>
  Array.from({ length: n }, (_, i) => convDTO(`${prefix}${String(i).padStart(3, '0')}`));
const msgs = (n, prefix = 'm') => Array.from({ length: n }, (_, i) => msgDTO(`${prefix}${i}`));

// Handler-faithful pages (get.go / list.go): a negative start counts
// back from the tail, effectiveStart is clamped, nextStart always rides
// the wire — null when hasMore is false (messagePage.NextStart *int has
// no omitempty, so the key is never omitted) — hasMore =
// effective + slice.length < total.
const RE_MSG = /^\/api\/conversations\/([^/]+)\/messages/;
const RE_CONV = /^\/api\/conversations\/([^/?]+)(?:\?|$)/;
const RE_LIST = /^\/api\/conversations(?:\?|$)/;
function messagesPage(all, url) {
  const start = Number(qp(url, 'start'));
  const limit = Number(qp(url, 'limit'));
  const total = all.length;
  let effective = start < 0 ? start + total : start;
  effective = Math.max(0, Math.min(effective, total));
  const slice = all.slice(effective, effective + limit);
  const out = {
    messages: slice, effectiveStart: effective, total,
    hasMore: effective + slice.length < total,
  };
  out.nextStart = out.hasMore ? effective + slice.length : null;
  return out;
}
function conversationsPage(all, url) {
  const offset = Number(qp(url, 'offset') || 0);
  const limit = Number(qp(url, 'limit') || 100);
  const data = all.slice(offset, offset + limit);
  return {
    data,
    page: { offset, limit, total: all.length, hasMore: offset + data.length < all.length },
  };
}

// `messages` maps room id -> full history array, or to a custom handler
// (url) => Response for pages no handler could produce (overlap, error).
function installRoutes({ list = [], messages = {} } = {}) {
  routes = [
    [RE_MSG, (m, url) => {
      const spec = messages[m[1]];
      return typeof spec === 'function' ? spec(url) : json(messagesPage(spec || [], url));
    }],
    [RE_CONV, (m) => json(convDTO(m[1]))],
    [RE_LIST, (_m, url) => json(conversationsPage(list, url))],
    [/^\/api\/gateways/, () => json({ data: [], page: { offset: 0, limit: 100, total: 0, hasMore: false } })],
    [/^\/api\/settings/, () => json({})],
    [/^\/api\/context/, () => json({})],
  ];
}

const flush = () => new Promise((r) => setTimeout(r));

// A shell wired like a settled view: socket open, boot resolved.
async function makeApp() {
  FakeWS.instances = [];
  calls.length = 0;
  const app = new AppShell();
  const ws = FakeWS.instances[FakeWS.instances.length - 1];
  ws._open();
  await flush(); // boot settles
  await flush(); // the boot-triggered select/context chain settles
  return { app, ws };
}

// Resolve every parked request with a handler-faithful page; the first
// argument selects which URL family to answer. Entries of the other
// family are re-parked, not dropped.
async function drain(kind, { list = [], messages = {} } = {}) {
  const keep = [];
  for (const d of deferred.splice(0)) {
    if (kind === 'list' && d.url.includes('messages')) { keep.push(d); continue; }
    if (kind === 'messages' && !d.url.includes('messages')) { keep.push(d); continue; }
    if (d.url.includes('messages')) {
      const room = decodeURIComponent(d.url.match(RE_MSG)[1]);
      d.res(json(messagesPage(messages[room] || [], d.url)));
    } else if (RE_LIST.test(d.url)) {
      d.res(json(conversationsPage(list, d.url)));
    } else {
      d.res(json({}));
    }
  }
  deferred.push(...keep);
  await flush();
}

// --- thread: the "Load earlier" window ---------------------------------

test('select() books the newest window; a short room needs no control', async () => {
  const all = msgs(250);
  installRoutes({ messages: { c1: all } });
  const { app } = await makeApp();
  await app.select('c1');
  assert.equal(app.history.start, 150);
  assert.equal(app.history.total, 250);
  assert.equal(app.messages.length, 100);
  assert.equal(app.messages[0].id, 'm150');
  assert.equal(app.thread.historyNotice, true);

  // A room that fits in one page leaves earlier history nothing to load.
  installRoutes({ messages: { c2: msgs(40) } });
  await app.select('c2');
  assert.equal(app.history.start, 0);
  assert.equal(app.thread.historyNotice, false);
  calls.length = 0;
  app.thread.dispatchEvent({ type: 'history-earlier', detail: {} });
  await flush();
  assert.equal(calls.filter((u) => u.includes('/messages')).length, 0);
});

test('history-earlier requests the window above and prepends in order', async () => {
  const all = msgs(250);
  installRoutes({ messages: { c1: all } });
  const { app } = await makeApp();
  await app.select('c1');

  calls.length = 0;
  app.thread.dispatchEvent({ type: 'history-earlier', detail: {} });
  // The request is on the wire synchronously — the control reports busy.
  assert.equal(app.thread.earlierLoading, true);
  const req = calls.find((u) => u.includes('/messages?'));
  assert.equal(qp(req, 'start'), '50');  // effectiveStart 150 - 100
  assert.equal(qp(req, 'limit'), '100');
  await flush();
  assert.equal(app.thread.earlierLoading, false);
  assert.equal(app.history.start, 50);
  assert.equal(app.history.total, 250);
  assert.equal(app.messages.length, 200);
  assert.deepEqual(
    app.messages.map((m) => m.id),
    all.slice(50).map((m) => m.id));
  assert.equal(app.thread.messages, app.messages);
  assert.equal(app.thread.historyNotice, true); // still 50 earlier rows

  // Reaching index 0 removes the control.
  app.thread.dispatchEvent({ type: 'history-earlier', detail: {} });
  await flush();
  assert.equal(app.history.start, 0);
  assert.equal(app.messages.length, 250);
  assert.equal(app.thread.historyNotice, false);
});

test('the earlier request clamps to limit=min(100, effectiveStart)', async () => {
  installRoutes({ messages: { c1: msgs(130) } });
  const { app } = await makeApp();
  await app.select('c1');
  assert.equal(app.history.start, 30);
  calls.length = 0;
  app.thread.dispatchEvent({ type: 'history-earlier', detail: {} });
  await flush();
  const req = calls.find((u) => u.includes('/messages?'));
  assert.equal(qp(req, 'start'), '0');
  assert.equal(qp(req, 'limit'), '30');
  assert.equal(app.messages.length, 130);
  assert.equal(app.thread.historyNotice, false);
});

test('an overlapping earlier page dedupes and keeps the window copies', async () => {
  const all = msgs(150);
  installRoutes({ messages: { c1: all } });
  const { app } = await makeApp();
  await app.select('c1'); // window m50..m149
  const windowCopy = app.messages.slice();

  // A page no longer index-aligned with the window — indices moved
  // under the request — overlaps the loaded span by ten records.
  routes[0] = [RE_MSG, () => json({
    messages: all.slice(40, 60), effectiveStart: 40, nextStart: 60,
    total: 150, hasMore: true,
  })];
  await app._loadEarlier();

  assert.equal(app.messages.length, 110); // 10 new + the 100 already shown
  assert.deepEqual(
    app.messages.map((m) => m.id),
    all.slice(40).map((m) => m.id));
  assert.equal(new Set(app.messages.map((m) => m.id)).size, 110);
  // The overlapping records are the window's own copies, not the page's.
  assert.equal(app.messages[10], windowCopy[0]);
  assert.equal(app.history.start, 40);
  assert.equal(app.thread.historyNotice, true);
});

test('a failed earlier fetch re-arms the control and reports a toast', async () => {
  const all = msgs(150);
  installRoutes({ messages: { c1: all } });
  const { app } = await makeApp();
  await app.select('c1');
  const before = app.messages;

  routes[0] = [RE_MSG, () => new Response(
    JSON.stringify({ code: 'history', detail: 'broker exploded' }), { status: 500 })];
  await app._loadEarlier();
  assert.equal(app.thread.earlierLoading, false); // busy released
  assert.equal(app.thread.historyNotice, true);   // control stays for retry
  assert.equal(app.messages, before);             // window untouched
  assert.equal(app.history.start, 50);
  const toast = (app.toasts.pushed || []).find((t) => t.tone === 'error');
  assert.ok(toast, 'error toast sent');
  assert.equal(toast.message, 'broker exploded');

  // The same control retries — the next click works again.
  routes[0] = [RE_MSG, (m, url) => json(messagesPage(all, url))];
  await app._loadEarlier();
  assert.equal(app.history.start, 0);
  assert.equal(app.messages.length, 150);
  assert.equal(app.thread.historyNotice, false);
});

test('the post-terminal refresh resets the window to the newest page', async () => {
  const all = msgs(250);
  installRoutes({ messages: { c1: all } });
  const { app } = await makeApp();
  await app.select('c1');
  await app._loadEarlier();
  assert.equal(app.history.start, 50);
  assert.equal(app.messages.length, 200);

  // A completed turn re-reads history the same way a room reload does:
  // the window returns to the newest page — the documented reset rule.
  assert.equal(await app._refresh('c1'), 'ok');
  assert.equal(app.history.start, 150);
  assert.equal(app.messages.length, 100);
  assert.equal(app.messages[0].id, 'm150');
  assert.equal(app.thread.historyNotice, true);
});

test('an earlier page in flight drops when a refresh resets the window', async () => {
  const all = msgs(250);
  installRoutes({ list: convs(3), messages: { c1: all } });
  const { app } = await makeApp();
  await app.select('c1');
  await flush();

  deferred = [];
  try {
    const pEarlier = app._loadEarlier(); // parks start=50&limit=100
    const pRefresh = app._refresh('c1'); // conv + messages start=-100
    // Land the refresh first: its window write invalidates the page.
    for (const d of deferred.splice(0)) {
      if (d.url.includes('start=50')) { deferred.push(d); continue; }
      d.res(json(d.url.includes('messages') ? messagesPage(all, d.url) : convDTO('c1')));
    }
    assert.equal(await pRefresh, 'ok');
    // The refresh wrote a new window: the superseded request no longer
    // owns the flag, and the write itself must have released it — the
    // control can never be left stuck busy by a page that lands later.
    assert.equal(app.thread.earlierLoading, false);
    // Now the stale earlier page lands — the window it was computed
    // against is gone, so it must not apply.
    await drain('messages', { list: [], messages: { c1: all } });
    await pEarlier;
    assert.equal(app.history.start, 150);
    assert.equal(app.messages.length, 100);
    assert.equal(app.messages[0].id, 'm150');
    assert.equal(app.thread.earlierLoading, false);
  } finally {
    deferred = null;
  }
});

test('a second history-earlier while one is in flight sends nothing', async () => {
  const all = msgs(150);
  installRoutes({ messages: { c1: all } });
  const { app } = await makeApp();
  await app.select('c1');

  deferred = [];
  try {
    app.thread.dispatchEvent({ type: 'history-earlier', detail: {} });
    app.thread.dispatchEvent({ type: 'history-earlier', detail: {} });
    assert.equal(app.thread.earlierLoading, true);
    const parked = deferred.filter((d) => d.url.includes('/messages?'));
    assert.equal(parked.length, 1, 'the duplicate click never hit the wire');
  } finally {
    await drain('messages', { messages: { c1: all } });
    deferred = null;
  }
  // The surviving request applied normally — the window reached 0.
  assert.equal(app.history.start, 0);
  assert.equal(app.messages.length, 150);
});

test('an earlier page that no longer reaches the window drops without a gap', async () => {
  const all = msgs(150);
  installRoutes({ messages: { c1: all } });
  const { app } = await makeApp();
  await app.select('c1'); // window m50..m149
  const before = app.messages;

  // The room shrank server-side (foreign rewrite) and no refresh has
  // run yet — the page's tail cannot reach the loaded window's start.
  routes[0] = [RE_MSG, () => json({
    messages: all.slice(0, 20), effectiveStart: 0, total: 20, hasMore: false,
  })];
  await app._loadEarlier();

  assert.equal(app.messages, before); // untouched — no gapped prepend
  assert.equal(app.history.start, 50);
  assert.equal(app.thread.historyNotice, true); // retry stays armed
  assert.equal(app.thread.earlierLoading, false);
});

test("a stale earlier page never releases another room's busy flag", async () => {
  const all = msgs(250);
  installRoutes({ list: convs(3), messages: { c1: all, c2: all } });
  const { app } = await makeApp();
  await app.select('c1');
  assert.equal(app.history.start, 150);

  // Room A's load-earlier goes on the wire, parked by hand.
  deferred = [];
  const pA = app._loadEarlier();
  const a = deferred.splice(0);
  deferred = null; // later requests resolve through the route table again
  assert.equal(a.length, 1);
  assert.ok(a[0].url.includes('start=50'));
  assert.equal(app.thread.earlierLoading, true);

  // The user switches rooms: select() hands the busy flag to the room
  // arriving on screen, and A's page is now answering a dead view.
  await app.select('c2');
  assert.equal(app.thread.earlierLoading, false);
  assert.equal(app.history.start, 150);

  // Room B's own load-earlier takes the busy flag.
  deferred = [];
  const pB = app._loadEarlier();
  const b = deferred.splice(0);
  deferred = null;
  assert.equal(b.length, 1);
  assert.equal(app.thread.earlierLoading, true);

  // A's stale response lands now — its finally must leave B's flag
  // alone, or B's control re-enables and a second click duplicates
  // the request.
  a[0].res(json(messagesPage(all, a[0].url)));
  await pA;
  assert.equal(app.thread.earlierLoading, true, "A's stale page must not unlock B's control");
  assert.equal(app._earlierLoading, true, 'the shell keeps its own busy flag too');
  assert.equal(app.history.start, 150, "A's stale window must not apply to B");

  // B's page lands and releases the flag it actually owns.
  b[0].res(json(messagesPage(all, b[0].url)));
  await pB;
  assert.equal(app.thread.earlierLoading, false);
  assert.equal(app._earlierLoading, false);
  assert.equal(app.history.start, 50);
  assert.equal(app.messages.length, 200);
});

// --- thread: an unopened edit must not strand the window ---------------

test('a pre-open edit failure restores the window it cut — no silent skip', async () => {
  const all = msgs(250);
  installRoutes({ messages: { c1: all } });
  const { app, ws } = await makeApp();
  await app.select('c1'); // window m150..m249
  assert.equal(app.history.start, 150);

  // The user edits a message — the command snapshots the cut history.
  assert.equal(app._edit('m150', 'edited'), true);
  const edit = app.command;
  assert.equal(edit.kind, 'edit');
  assert.equal(edit.turnId, null, 'still pre-open');

  // While the edit is pre-open, a "Load earlier" commits and moves the
  // window start under the snapshot.
  await app._loadEarlier();
  assert.equal(app.history.start, 50);
  assert.equal(app.messages[0].id, 'm50');

  // The edit then fails before opening: the cut messages come back.
  ws._message({ type: 'error', requestId: edit.requestId, code: 'edit', message: 'rejected' });
  await flush();
  assert.equal(app.command, null);
  assert.equal(app.messages.length, 100);
  assert.equal(app.messages[0].id, 'm150');

  // (a) The window comes back with them — the start is 150 again, not
  // the 50 the interrupted "Load earlier" left behind.
  assert.equal(app.history.start, 150);

  // (b) The next "Load earlier" asks for the page directly above the
  // RESTORED window — [50,150) — rather than the [0,50) the stale
  // start would have implied.
  calls.length = 0;
  await app._loadEarlier();
  const req = calls.find((u) => u.includes('/messages?'));
  assert.equal(qp(req, 'start'), '50');
  assert.equal(qp(req, 'limit'), '100');
  // The rendered list is contiguous: no range silently skipped.
  assert.deepEqual(
    app.messages.map((m) => m.id),
    all.slice(50).map((m) => m.id));

  // (c) The notice control still describes the restored window — the
  // shell's flag says "earlier messages exist", and the widget renders
  // an enabled control for it.
  assert.equal(app.thread.historyNotice, true);
  const probe = new MessageThread();
  probe.hasConversation = true;
  probe.messages = app.messages;
  probe.historyNotice = app.thread.historyNotice;
  const btn = find(probe.root, 'earlier-btn');
  assert.ok(btn, 'the notice control is offered for the restored window');
  assert.equal(btn.getAttribute('disabled'), null);

  // The sequence still converges: the last page reaches the start.
  await app._loadEarlier();
  assert.equal(app.history.start, 0);
  assert.deepEqual(app.messages.map((m) => m.id), all.map((m) => m.id));
  assert.equal(app.thread.historyNotice, false);
  probe.historyNotice = app.thread.historyNotice;
  assert.equal(find(probe.root, 'earlier-btn'), null);
});

test('an earlier page in flight across a pre-open edit failure still lands', async () => {
  const all = msgs(250);
  installRoutes({ messages: { c1: all } });
  const { app, ws } = await makeApp();
  await app.select('c1'); // window m150..m249
  assert.equal(app.history.start, 150);

  // The user edits a message — the command snapshots the cut history.
  assert.equal(app._edit('m150', 'edited'), true);
  const edit = app.command;
  assert.equal(edit.turnId, null, 'still pre-open');

  // A "Load earlier" goes on the wire while the edit is still pre-open.
  // Its fetch is parked, so the page is in flight when the edit fails.
  deferred = [];
  try {
    const pEarlier = app._loadEarlier();
    const parked = deferred.splice(0).filter((d) => d.url.includes('/messages?'));
    assert.equal(parked.length, 1);
    assert.equal(qp(parked[0].url, 'start'), '50');
    assert.equal(qp(parked[0].url, 'limit'), '100');
    assert.equal(app.thread.earlierLoading, true);

    // The edit then fails before opening: the cut messages come back,
    // and with them the window they describe — start is 150 again.
    ws._message({ type: 'error', requestId: edit.requestId, code: 'edit', message: 'rejected' });
    await flush();
    assert.equal(app.command, null);
    assert.equal(app.messages.length, 100);
    assert.equal(app.messages[0].id, 'm150');
    assert.equal(app.history.start, 150);

    // The parked page lands now. It was computed for exactly the window
    // the failure restored, so it is still the correct page for it: the
    // restore must not have thrown it away.
    parked[0].res(json(messagesPage(all, parked[0].url)));
    await pEarlier;
  } finally {
    deferred = null;
  }

  // (a) It IS adopted: the older messages are prepended contiguously —
  // the window now reaches m50 with no range silently skipped, and the
  // rendered list is the same array the shell holds.
  assert.equal(app.history.start, 50);
  assert.equal(app.history.total, 250);
  assert.equal(app.messages.length, 200);
  assert.deepEqual(
    app.messages.map((m) => m.id),
    all.slice(50).map((m) => m.id));
  assert.equal(app.thread.messages, app.messages);
  assert.equal(app.thread.historyNotice, true);

  // (b) The page's own release ran — the control is usable again.
  assert.equal(app.thread.earlierLoading, false);
  assert.equal(app._earlierLoading, false);
});

// --- sidebar: list paging ----------------------------------------------

test('boot forwards page meta; load more appends and coverage follows', async () => {
  const list = convs(250);
  installRoutes({ list });
  const { app } = await makeApp();
  assert.equal(app.allConversations.length, 100);
  assert.equal(app.sidebar.conversations.length, 100);
  assert.equal(app.sidebar.hasMore, true);
  assert.equal(app.sidebar.total, 250);

  await app._loadMoreConversations();
  const req = calls.find((u) => RE_LIST.test(u) && u.includes('offset=100'));
  assert.ok(req, 'next page fetched at offset=100');
  assert.equal(qp(req, 'limit'), '100');
  assert.equal(app.allConversations.length, 200);
  assert.equal(app.sidebar.conversations.length, 200);
  assert.equal(app.sidebar.hasMore, true);

  await app._loadMoreConversations(); // offset=200 — last page
  assert.equal(app.allConversations.length, 250);
  assert.equal(app.sidebar.hasMore, false);
  assert.equal(app.sidebar.total, 250);

  // The end of the server set makes further clicks inert — no request.
  calls.length = 0;
  app.sidebar.dispatchEvent({ type: 'conversation-more', detail: {} });
  await flush();
  assert.equal(calls.filter((u) => RE_LIST.test(u)).length, 0);
});

test('a second conversation-more while one is in flight sends nothing', async () => {
  const list = convs(250);
  installRoutes({ list });
  const { app } = await makeApp();

  deferred = [];
  try {
    app.sidebar.dispatchEvent({ type: 'conversation-more', detail: {} });
    app.sidebar.dispatchEvent({ type: 'conversation-more', detail: {} });
    assert.equal(app.sidebar.moreBusy, true);
    const pending = deferred.splice(0).filter((d) => RE_LIST.test(d.url));
    assert.equal(pending.length, 1, 'the duplicate click never hit the wire');
    pending[0].res(json(conversationsPage(list, pending[0].url)));
    await flush();
    assert.equal(app.sidebar.moreBusy, false);
    assert.equal(app.allConversations.length, 200);
    // And the guard released — the next click fetches again.
    app.sidebar.dispatchEvent({ type: 'conversation-more', detail: {} });
    assert.equal(deferred.filter((d) => RE_LIST.test(d.url)).length, 1);
  } finally {
    await drain('list', { list });
    deferred = null;
  }
  assert.equal(app.allConversations.length, 250);
});

test('a stale refresh response cannot overwrite a newer one', async () => {
  const older = convs(100, 'a');
  const newer = convs(100, 'b');
  installRoutes({ list: older });
  const { app } = await makeApp();
  assert.equal(app.allConversations.length, 100);

  deferred = [];
  try {
    const first = app._refreshList();  // seq N
    const second = app._refreshList(); // seq N+1 — supersedes the first
    const reqs = deferred.splice(0).filter((d) => RE_LIST.test(d.url));
    assert.equal(reqs.length, 2);
    reqs[1].res(json(conversationsPage(newer, reqs[1].url)));
    await second;
    assert.equal(app.allConversations[0].id, 'b000');
    // The older response arrives after — it answers a stale span.
    reqs[0].res(json(conversationsPage(older, reqs[0].url)));
    await first;
    assert.equal(app.allConversations[0].id, 'b000');
    assert.equal(app.allConversations.length, 100);
  } finally {
    deferred = null;
  }
});

test('the older-start refresh that lands first must not displace the newer one', async () => {
  const older = convs(100, 'x');
  const newer = convs(100, 'b');
  installRoutes({ list: convs(100, 'a') });
  const { app } = await makeApp();
  assert.equal(app.allConversations[0].id, 'a000');

  deferred = [];
  try {
    const first = app._refreshList();  // seq N
    const second = app._refreshList(); // seq N+1 — supersedes the first
    const reqs = deferred.splice(0).filter((d) => RE_LIST.test(d.url));
    assert.equal(reqs.length, 2);
    // The OLDER-start refresh lands FIRST. Start order already lost, so
    // it must commit nothing — committing would bump _listSeq and make
    // the newer response drop, leaving the older data on screen.
    reqs[0].res(json(conversationsPage(older, reqs[0].url)));
    await first;
    assert.equal(app.allConversations[0].id, 'a000', 'the loser committed nothing');
    // The newer response lands last and wins outright.
    reqs[1].res(json(conversationsPage(newer, reqs[1].url)));
    await second;
    assert.equal(app.allConversations[0].id, 'b000');
    assert.equal(app.allConversations.length, 100);
    assert.equal(app.sidebar.conversations[0].id, 'b000');
    assert.equal(app.conversationsPage.total, newer.length);
  } finally {
    deferred = null;
  }
});

test('a load-more that commits mid-refresh invalidates the stale refresh', async () => {
  const list = convs(250);
  installRoutes({ list });
  const { app } = await makeApp();

  deferred = [];
  try {
    const pRefresh = app._refreshList();          // parks page 0, seq N
    const pMore = app._loadMoreConversations();   // parks offset=100
    const reqs = deferred.splice(0).filter((d) => RE_LIST.test(d.url));
    assert.equal(reqs.length, 2);
    // The append lands first — the in-flight refresh now answers a
    // span that no longer describes the loaded set.
    reqs[1].res(json(conversationsPage(list, reqs[1].url)));
    await pMore;
    assert.equal(app.allConversations.length, 200);
    // The refresh's page-0 response lands with a different set — its
    // whole-span write must not shrink the appended rows away.
    const alt = convs(100, 'x');
    reqs[0].res(json(conversationsPage(alt, reqs[0].url)));
    await pRefresh;
    assert.equal(app.allConversations.length, 200);
    assert.equal(app.allConversations[0].id, 'c000');
    assert.equal(app.allConversations[199].id, 'c199');
  } finally {
    deferred = null;
  }
});

test('a load-more started before a refresh still commits once it lands', async () => {
  const list = convs(250);
  installRoutes({ list });
  const { app } = await makeApp();

  deferred = [];
  try {
    const pMore = app._loadMoreConversations(); // parks offset=100 first
    const pRefresh = app._refreshList();        // parks its span fetch after
    const reqs = deferred.splice(0).filter((d) => RE_LIST.test(d.url));
    assert.equal(reqs.length, 2);
    // The click's page lands mid-refresh — the append is honest work
    // and wins: it is the refresh's pre-append span that went stale.
    reqs[0].res(json(conversationsPage(list, reqs[0].url)));
    await pMore;
    assert.equal(app.allConversations.length, 200);
    // The refresh response lands into the post-append list — dropped,
    // its answer would shrink the appended rows back to the old span.
    const alt = convs(100, 'x');
    reqs[1].res(json(conversationsPage(alt, reqs[1].url)));
    await pRefresh;
    assert.equal(app.allConversations.length, 200);
    assert.equal(app.allConversations[0].id, 'c000');
    assert.equal(app.allConversations[199].id, 'c199');
  } finally {
    deferred = null;
  }
});

test('a stale list response cannot resurrect a deleted row', async () => {
  const list = convs(150);
  installRoutes({ list, messages: { c000: [] } });
  const { app } = await makeApp();
  assert.equal(app.allConversations.length, 100);
  await app.select('c000');

  deferred = [];
  try {
    const pRefresh = app._refreshList(); // parks the page-0 span fetch
    const pDelete = app._delete();       // parks DELETE c000
    // The delete commits first — every response already on the wire
    // answered a roster that still contained this row.
    const parked = deferred.splice(0);
    const del = parked.find((d) => d.url.includes('c000'));
    deferred.push(...parked.filter((d) => d !== del));
    del.res(json({}));
    await pDelete;
    assert.equal(app.allConversations.some((c) => c.id === 'c000'), false);
    assert.equal(app.allConversations.length, 99);
    assert.equal(app.conversationsPage.total, 149);
    // The stale span answer lands — the row must stay deleted and the
    // delete's local meta must survive.
    await drain('list', { list });
    await pRefresh;
    assert.equal(app.allConversations.some((c) => c.id === 'c000'), false);
    assert.equal(app.allConversations.length, 99);
    assert.equal(app.sidebar.conversations.length, 99);
    assert.equal(app.sidebar.total, 149);
  } finally {
    await drain('list', { list });
    deferred = null;
  }
});

test('search re-projects the full loaded set so coverage stays honest', async () => {
  const list = convs(250);
  installRoutes({ list });
  const { app } = await makeApp();
  assert.equal(app.allConversations.length, 100);

  // The widget narrows by its own _query — the shell always hands it
  // the full loaded set, so "Loaded X of Y" counts loaded threads and
  // never the match count.
  app.sidebar.dispatchEvent({ type: 'conversation-search', detail: { query: 'c001' } });
  assert.equal(app.sidebar.conversations.length, 100);
  assert.equal(app.sidebar.hasMore, true);
  assert.equal(app.sidebar.total, 250);

  await app._loadMoreConversations();
  app.sidebar.dispatchEvent({ type: 'conversation-search', detail: { query: 'c001' } });
  assert.equal(app.sidebar.conversations.length, 200);
});

test('a conversation push orders total before hasMore so the hint never reads a stale total', async () => {
  installRoutes({ list: convs(100) });
  const { app } = await makeApp();

  // The widget renders its coverage hint the instant `hasMore` flips
  // true and reads `total` as it does — so `total` must already be
  // current. Pin the exact push order: a future `await` between the
  // setters must still never expose a stale total to that read.
  const order = [];
  const recorder = {};
  for (const k of ['conversations', 'total', 'hasMore', 'moreBusy']) {
    Object.defineProperty(recorder, k, {
      configurable: true,
      get: () => undefined,
      set: () => order.push(k),
    });
  }
  const real = app.sidebar;
  app.sidebar = recorder;
  try {
    app.allConversations = convs(200);
    app.conversationsPage = { offset: 100, limit: 100, total: 250, hasMore: true };
    app._pushConversations();
  } finally {
    app.sidebar = real;
  }

  assert.deepEqual(order, ['conversations', 'total', 'hasMore', 'moreBusy']);
});

// --- widget: the notice's earlier control ------------------------------

test('the notice control emits history-earlier and disables while busy', () => {
  const thread = new MessageThread();
  thread.hasConversation = true;
  thread.messages = [msgDTO('m1'), msgDTO('m2')];
  assert.equal(find(thread.root, 'earlier-btn'), null); // no notice, no control

  thread.historyNotice = true;
  const btn = find(thread.root, 'earlier-btn');
  assert.ok(btn, 'Load earlier button rendered inside the notice block');
  assert.equal(btn.tagName, 'BUTTON');
  assert.equal(btn.textContent, 'Load earlier messages');
  assert.equal(btn.getAttribute('disabled'), null);
  // The notice text survives next to the control — and stays honest
  // after the window has already grown ("latest window" would not).
  const notice = find(thread.root, 'notice');
  assert.equal(notice.children[0].textContent,
    'Earlier messages exist — this window does not reach the start.');

  btn.fire('click');
  const emitted = thread.events.filter((e) => e.type === 'history-earlier');
  assert.equal(emitted.length, 1);
  assert.deepEqual(emitted[0].detail, {});

  // Busy is pushed into the live node — no re-render swaps the button.
  thread.earlierLoading = true;
  assert.equal(find(thread.root, 'earlier-btn'), btn);
  assert.equal(btn.disabled, true);
  assert.equal(btn.textContent, 'Loading…');
  thread.earlierLoading = false;
  assert.equal(btn.disabled, false);
  assert.equal(btn.textContent, 'Load earlier messages');

  thread.historyNotice = false;
  assert.equal(find(thread.root, 'earlier-btn'), null);
  assert.equal(find(thread.root, 'notice'), null);
});
