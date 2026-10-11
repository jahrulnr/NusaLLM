import { test } from 'node:test';
import assert from 'node:assert/strict';

// app-shell is orchestration-only — it renders no markup of its own, so
// a minimal DOM stand-in (elements as plain objects) is enough to drive
// its room-gating contract headlessly. Global stubs must land BEFORE
// the widget modules evaluate: Widget extends HTMLElement at class
// definition time, and customElements.define runs at module bottom.
class FakeEl {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.style = {};
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.hidden = false;
  }
  append(...kids) { this.children.push(...kids); }
  addEventListener() {}
  setAttribute(k, v) { this[k] = v; }
  dispatchEvent() { return true; }
  clear() {}
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

await import('../widgets/app-shell/app-shell.js');
const AppShell = defined['nusa-app'];

// A shell wired like a settled room view: socket open, no boot data.
function makeApp() {
  FakeWS.instances = [];
  const app = new AppShell();
  const ws = FakeWS.instances[FakeWS.instances.length - 1];
  ws._open();
  return { app, ws };
}

test('a watched turn gates only its own room', () => {
  const { app, ws } = makeApp();
  app.activeId = 'c1';
  app._attachLiveTurn({ id: 'c1', activeTurnId: 't-foreign', activeTurnKind: 'send' });
  assert.equal(app.command.watched, true);
  assert.equal(ws.sent.filter((f) => f.type === 'turn.watch').length, 1);
  // Its own room is gated — Enter captures locally instead of sending.
  assert.equal(app._dispatchable('c1'), false);
  app._send('queued behind the watch');
  assert.equal(app.queue.items.length, 1);
  assert.equal(app.queue.items[0].room, 'c1');
  // Another room is free — Enter dispatches, and the new command takes
  // the single slot (the foreign watch is displaced).
  app.activeId = 'c2';
  assert.equal(app._dispatchable('c2'), true);
  app._send('other room send');
  assert.equal(app.command.kind, 'send');
  assert.equal(app.command.conversationId, 'c2');
  assert.equal(app.command.watched, undefined);
  const sent = ws.sent.find((f) => f.type === 'turn.send');
  assert.equal(sent.conversationId, 'c2');
});

test('busy/stop/queue affordances follow the watched turn\'s room', () => {
  const { app } = makeApp();
  app.activeId = 'c1';
  app._attachLiveTurn({ id: 'c1', activeTurnId: 't-f', activeTurnKind: 'send' });
  app._syncComposer();
  assert.equal(app.composer.busy, true);
  assert.equal(app.composer.canStop, true);
  assert.equal(app.composer.queueMode, true);
  assert.equal(app.thread.commandsBusy, true);
  // In a different room the same watch leaves the composer idle.
  app.activeId = 'c2';
  app._syncComposer();
  assert.equal(app.composer.busy, false);
  assert.equal(app.composer.canStop, false);
  assert.equal(app.composer.queueMode, false);
  assert.equal(app.thread.commandsBusy, false);
});

test('a locally driven command still gates every room', () => {
  const { app } = makeApp();
  app.activeId = 'c1';
  app._send('my send');
  assert.equal(app.command.watched, undefined);
  // Global single-slot: even another room captures locally.
  app.activeId = 'c2';
  assert.equal(app._dispatchable('c2'), false);
  app._send('still local');
  assert.equal(app.queue.items.length, 1);
  assert.equal(app.queue.items[0].room, 'c2');
  app._syncComposer();
  assert.equal(app.composer.busy, true);
  assert.equal(app.composer.queueMode, true);
});

test('a fresh un-minted send binds to its fresh token room', () => {
  const { app } = makeApp();
  app.activeId = null;
  app.freshToken = 'fresh:9';
  app._send('mint me');
  const cmd = app.command;
  assert.equal(cmd.conversationId, null);
  assert.equal(app._commandRoomKey(cmd), 'fresh:9');
  assert.equal(app._commandOwnsView(cmd), true);
  assert.equal(app._dispatchable('fresh:9'), false);
  // Once minted the room key follows the conversation id.
  cmd.conversationId = 'm1';
  assert.equal(app._commandRoomKey(cmd), 'm1');
});

test('a watched turn in the viewed room vetoes edit; elsewhere it does not', () => {
  const { app } = makeApp();
  app.activeId = 'c1';
  app._attachLiveTurn({ id: 'c1', activeTurnId: 't-f', activeTurnKind: 'send' });
  app.messages = [{ id: 'u1', role: 'user', content: 'old' }];
  assert.equal(app._edit('u1', 'new'), false);
  // In another room the edit dispatches and displaces the watch.
  app.activeId = 'c2';
  app.messages = [{ id: 'u1', role: 'user', content: 'old' }];
  assert.equal(app._edit('u1', 'new'), true);
  assert.equal(app.command.kind, 'edit');
  assert.equal(app.command.conversationId, 'c2');
  assert.equal(app.command.watched, undefined);
});

test('attach displaces a stale watch but never a local command', () => {
  const { app, ws } = makeApp();
  app.activeId = 'c1';
  app._attachLiveTurn({ id: 'c1', activeTurnId: 't-a', activeTurnKind: 'send' });
  const firstRid = app.command.requestId;
  // Re-attaching the same turn is a no-op — no second turn.watch.
  app._attachLiveTurn({ id: 'c1', activeTurnId: 't-a' });
  assert.equal(app.command.requestId, firstRid);
  assert.equal(ws.sent.filter((f) => f.type === 'turn.watch').length, 1);
  // A different live turn in the room on screen takes the slot.
  app._attachLiveTurn({ id: 'c2', activeTurnId: 't-b' });
  assert.equal(app.command.turnId, 't-b');
  assert.equal(app.command.conversationId, 'c2');
  // A command this tab drove is never displaced by an attach.
  app.activeId = 'c3';
  app._send('mine');
  assert.equal(app.command.kind, 'send');
  app._attachLiveTurn({ id: 'c4', activeTurnId: 't-c' });
  assert.equal(app.command.kind, 'send');
  assert.equal(app.command.conversationId, 'c3');
});

// A failed turn is driven through _onFrame like the real socket path:
// send → opened → failed terminal.
function failTurn(app, ws, payload) {
  app.activeId = 'c1';
  app._send('hi');
  const cmd = app.command;
  app._onFrame({ type: 'turn.opened', requestId: cmd.requestId, turnId: 't1', conversationId: 'c1', seq: 1 });
  app._onFrame({ type: 'turn.failed', turnId: 't1', conversationId: 'c1', seq: 2, payload });
}
const flush = () => new Promise((r) => setTimeout(r));

test('a persisted failure keeps a retry row; fresh input still sends', async () => {
  const { app, ws } = makeApp();
  app.composer.value = '';
  failTurn(app, ws, { status: 502, message: 'upstream exploded', userMessagePersisted: true });
  const e = app.queue.items.find((x) => x.state === 'failed');
  assert.ok(e, 'failed row parked');
  assert.equal(e.text, 'hi');
  assert.equal(e.room, 'c1');
  assert.equal(e.note, 'upstream exploded');
  assert.equal(e.retryMessageId, null); // resolved lazily from history
  // Nothing else was queued — the pause owns only what it captured,
  // and an empty backlog means nothing is held.
  assert.equal(app.queue.paused, false);
  await flush(); // the barrier refresh lands and clears the gate
  // "." is fresh intent — a definitive failure does not capture it.
  app._send('.');
  assert.equal(app.command.kind, 'send');
  assert.equal(ws.sent.filter((f) => f.type === 'turn.send').length, 2);
  // The failed row itself is still there waiting on an explicit call.
  assert.equal(app.queue.items.filter((x) => x.state === 'failed').length, 1);
});

test('retry re-issues turn.edit against the failed user line', async () => {
  const { app, ws } = makeApp();
  app.composer.value = '';
  failTurn(app, ws, { status: 502, message: 'boom', userMessagePersisted: true });
  await flush();
  const e = app.queue.items.find((x) => x.state === 'failed');
  app.messages = [{ id: 'm1', role: 'user', content: 'hi' }];
  app._queueRetry(e.id);
  const edit = ws.sent.find((f) => f.type === 'turn.edit');
  assert.equal(edit.conversationId, 'c1');
  assert.equal(edit.payload.messageId, 'm1');
  assert.equal(edit.payload.content, 'hi');
  assert.equal(app.command.kind, 'edit');
  assert.equal(app.queue.items.length, 0); // consumed by the dispatch
});

test('an unpersisted failure restores text instead of parking a retry row', async () => {
  const { app } = makeApp();
  app.composer.value = '';
  failTurn(app, null, { status: 502, message: 'boom', userMessagePersisted: false });
  assert.equal(app.queue.items.filter((x) => x.state === 'failed').length, 0);
  // Owning view gets its text back in the draft.
  assert.equal(app.composer.value, 'hi');
});

test('an interrupted turn never parks a retry row', () => {
  const { app } = makeApp();
  app.composer.value = '';
  failTurn(app, null, { status: 408, code: 'interrupted', userMessagePersisted: true });
  assert.equal(app.queue.items.filter((x) => x.state === 'failed').length, 0);
});

test('retry target skips an older identical line via dispatch-time history ids', async () => {
  const { app, ws } = makeApp();
  app.composer.value = '';
  failTurn(app, ws, { status: 502, message: 'boom', userMessagePersisted: true });
  await flush();
  const e = app.queue.items.find((x) => x.state === 'failed');
  // The send's own line is the NEWER identical record — the older one
  // predates the dispatch and rides historyIds.
  e.historyIds = new Set(['m-old']);
  app.messages = [
    { id: 'm-old', role: 'user', content: 'hi' },
    { id: 'm-new', role: 'user', content: 'hi' },
  ];
  assert.equal(app._retryMessageId(e), 'm-new');
  app._queueRetry(e.id);
  const edit = ws.sent.find((f) => f.type === 'turn.edit');
  assert.equal(edit.payload.messageId, 'm-new');
});

test('a failed line no longer at the tail cannot retry — only dismiss', async () => {
  const { app, ws } = makeApp();
  app.composer.value = '';
  failTurn(app, ws, { status: 502, message: 'boom', userMessagePersisted: true });
  await flush();
  const e = app.queue.items.find((x) => x.state === 'failed');
  // A later message landed after the failure — editing the failed
  // line now would silently rewind it.
  app.messages = [
    { id: 'm1', role: 'user', content: 'hi' },
    { id: 'm2', role: 'user', content: 'sent after' },
  ];
  assert.equal(app._retryTargetId(e), null);
  app._queueRetry(e.id);
  assert.equal(ws.sent.filter((f) => f.type === 'turn.edit').length, 0);
  assert.ok(app.queue.items.includes(e)); // the row itself survives
  app._queueDismiss(e.id);
  assert.equal(app.queue.items.length, 0);
});

test('resume while offline keeps the held backlog held', async () => {
  const { app } = makeApp();
  app.composer.value = '';
  failTurn(app, null, { status: 502, message: 'boom', userMessagePersisted: true });
  // Park a held line the way the failure would have.
  const { enqueue } = await import('../lib/pending-queue.js');
  const e = enqueue(app.queue, { text: 'held', room: 'c1', opts: {} });
  e.held = true;
  app.queue.paused = true;
  app.socket.state = 'closed';
  await app._queueResume();
  assert.equal(e.held, true);
  assert.equal(app.queue.notice, 'Reconnect before resuming the queue');
});

test('a failed row in another room never retries through this view', async () => {
  const { app, ws } = makeApp();
  app.activeId = 'c1';
  const { enqueue } = await import('../lib/pending-queue.js');
  const e = enqueue(app.queue, { text: 'hi', room: 'c2', opts: {} });
  e.state = 'failed';
  e.retryMessageId = 'm1';
  app.messages = [{ id: 'm1', role: 'user', content: 'hi' }];
  app._queueRetry(e.id);
  assert.equal(ws.sent.filter((f) => f.type === 'turn.edit').length, 0);
  assert.ok(app.queue.items.includes(e));
});
