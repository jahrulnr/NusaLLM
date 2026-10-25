import { test } from 'node:test';
import assert from 'node:assert/strict';

// Socket lifecycle edge cases — same DOM/WS stand-in style as
// app-shell.test.mjs (see that file for the harness contract). Global
// stubs must land BEFORE the widget modules evaluate: Widget extends
// HTMLElement at class definition time, and customElements.define runs
// at module bottom.
class FakeEl {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.style = {};
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.hidden = false;
    this.listeners = {};
    this.value = '';
  }
  append(...kids) { this.children.push(...kids); }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  setAttribute(k, v) { this[k] = v; }
  dispatchEvent(e) {
    (this.listeners[e.type] || []).forEach((fn) => fn.call(this, e));
    return true;
  }
  clear() { this.value = ''; }
  focus() {}
  push(...items) { (this.pushed ||= []).push(...items); }
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
  addEventListener() {},
};
globalThis.matchMedia = (q) => ({ matches: q === '(min-width: 761px)', media: q, addEventListener() {} });
globalThis.location = { protocol: 'http:', host: 'test.local', hash: '', pathname: '/' };
globalThis.history = { replaceState() {} };
globalThis.addEventListener = () => {};
globalThis.WebSocket = FakeWS;
globalThis.fetch = async () => new Response(JSON.stringify({ data: [], total: 0 }));

const { TurnSocket } = await import('../lib/socket-client.js');
const { enqueue } = await import('../lib/pending-queue.js');
await import('../widgets/app-shell/app-shell.js');
const AppShell = defined['nusa-app'];

// The shell wired like the page just booted: socket still CONNECTING —
// the gap between module load and the first open is where Enter
// captures locally instead of dispatching.
function makeConnectingApp() {
  FakeWS.instances = [];
  const app = new AppShell();
  const ws = FakeWS.instances[FakeWS.instances.length - 1];
  return { app, ws };
}
function makeApp() {
  const { app, ws } = makeConnectingApp();
  ws._open();
  return { app, ws };
}
const flush = () => new Promise((r) => setTimeout(r));

// ── First-connect drain gap ─────────────────────────────────────────
// _onConnection('open') only refreshes/drains when `this._hasOpened` —
// a flag it sets on that very call. Lines queued while the socket was
// still CONNECTING (page load on a slow link, server restart, the app's
// own cold start) never get a later dispatch trigger: the room is
// dispatchable, yet nothing takes the entry.

test('a line queued during the initial connect drains when the socket opens', () => {
  const { app, ws } = makeConnectingApp();
  app.activeId = 'c1';
  app._send('queued while connecting');
  assert.equal(app.queue.items.length, 1);
  assert.equal(app.queue.items[0].state, 'queued');

  ws._open(); // the FIRST open — _hasOpened was still false
  assert.equal(app.socket.state, 'open');
  // The line is fresh intent (captured offline) — the first open must
  // dispatch it; the send is in flight, so the room is busy now.
  assert.equal(
    ws.sent.filter((f) => f.type === 'turn.send').length, 1,
    'the queued line should dispatch on first open — it stayed queued',
  );
  assert.equal(app.queue.items.length, 0);
});

test('a fresh view queued during the initial connect drains on first open too', () => {
  const { app, ws } = makeConnectingApp();
  app.activeId = null;
  app.freshToken = 'fresh:3';
  app._send('fresh queued while connecting');
  assert.equal(app.queue.items.length, 1);

  ws._open();
  assert.equal(
    ws.sent.filter((f) => f.type === 'turn.send').length, 1,
    'the fresh-view line should dispatch on first open — it stayed queued',
  );
  assert.equal(app.queue.items.length, 0);
});

test('an offline-captured line is fresh intent — it drains on reconnect', async () => {
  const { app, ws } = makeApp();
  app.activeId = 'c1';
  // Drop the socket, then capture a line while it is down — the close
  // paused the lane for lines queued BEFORE the drop; a line captured
  // afterward is fresh intent and dispatches once the room's gates
  // clear on reconnect (takeQueued only refuses held entries).
  ws._close();
  app._send('held over the drop');
  ws._close(); // stale duplicate close — must not re-pause the lane
  app.socket.connect();
  const ws2 = FakeWS.instances[FakeWS.instances.length - 1];
  ws2._open();
  await flush(); // the refresh behind the reconnect drain resolves
  assert.equal(
    ws2.sent.filter((f) => f.type === 'turn.send').length, 1,
    'the offline-captured line should dispatch on reconnect',
  );
  assert.equal(app.queue.items.length, 0);
});

// ── Dead-tail detection drops every same-text UNKNOWN receipt ───────
// _markInterrupted consumes queue entries whose text matches the last
// real user line — the loop never stops at one. Two identical sends
// parked as UNKNOWN lose BOTH receipts though the tail proves only one.

test('the interrupted card consumes only the receipt it answers', () => {
  const { app } = makeApp();
  app.activeId = 'c1';
  // Two identical sends whose delivery is unknown (socket dropped
  // before turn.opened). Only one can be proven by the dead tail.
  const a = enqueue(app.queue, { text: 'hi', room: 'c1', opts: {} });
  const b = enqueue(app.queue, { text: 'hi', room: 'c1', opts: {} });
  a.state = 'unknown';
  b.state = 'unknown';
  app.messages = [
    { id: 'm1', role: 'user', content: 'hi' },
    { id: 'w1', role: 'system', content: '', toolCalls: [{ type: 'injection', name: 'workspace' }] },
  ];
  app._markInterrupted({ id: 'c1' });
  assert.ok(app.failedTurns.has('c1'), 'the dead tail parks its card');
  assert.equal(
    app.queue.items.length, 1,
    'the second identical receipt must stay — the tail answers only one',
  );
  assert.equal(app.queue.items[0].state, 'unknown');
});

// ── Stale onclose wipes a newer socket handle ────────────────────────
// TurnSocket's ws.onclose unconditionally nulls this.ws and schedules a
// retry when intentionalClose is false. A close event arriving late —
// after close() + connect() already replaced the handle — wipes the
// LIVE socket's reference: the app then reports 'open' while every
// command queues into the void, and the orphaned socket keeps
// delivering frames the client can no longer answer on.

function makeSocket() {
  FakeWS.instances = [];
  const s = new TurnSocket();
  const frames = [];
  const states = [];
  s.addEventListener('frame', (e) => frames.push(e.detail.frame));
  s.addEventListener('connection', (e) => states.push(e.detail.state));
  return { s, frames, states, ws: () => FakeWS.instances[FakeWS.instances.length - 1] };
}

test('a late onclose from the dead socket must not wipe the live handle', () => {
  const { s, frames } = makeSocket();
  s.connect();
  const ws1 = FakeWS.instances[FakeWS.instances.length - 1];
  ws1._open();
  s.close();                       // intentional teardown of ws1
  s.connect();                     // ws2 — the handle the client owns now
  const ws2 = FakeWS.instances[FakeWS.instances.length - 1];
  assert.notEqual(ws2, ws1);
  ws1.onclose();                   // the dead socket's close lands late
  assert.equal(
    s.ws, ws2,
    "the dead socket's onclose wiped the live handle — sends now queue into a void",
  );
  if (s.ws === null) {
    // Demonstrate the fallout the wipe produces: 'open' state with no
    // working handle — commands never leave, frames still arrive.
    ws2._open();
    assert.equal(s.state, 'open');
    s.sendTurn({ conversationId: 'c1', route: 'r', modelId: 'm', content: 'hi' });
    assert.equal(ws2.sent.length, 0, 'the command never reached a socket');
    assert.equal(s.queue.length, 1, 'it queued as if the socket were down');
    ws2._message({
      type: 'turn.opened', requestId: 'r', conversationId: 'c1', turnId: 't', seq: 0,
    });
    assert.equal(frames.length, 1, 'the zombie socket still feeds frames');
  }
});
