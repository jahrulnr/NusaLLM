import { test } from 'node:test';
import assert from 'node:assert/strict';

// A scriptable WebSocket stand-in: sockets record every sent frame and
// the test drives open/message/close by hand.
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

globalThis.WebSocket = FakeWS;
if (!globalThis.location) {
  globalThis.location = { protocol: 'http:', host: 'test.local' };
}

const { TurnSocket } = await import('../lib/socket-client.js');

function makeSocket() {
  FakeWS.instances = [];
  const s = new TurnSocket();
  const frames = [];
  const states = [];
  s.addEventListener('frame', (e) => frames.push(e.detail.frame));
  s.addEventListener('connection', (e) => states.push(e.detail.state));
  return { s, frames, states, ws: () => FakeWS.instances[FakeWS.instances.length - 1] };
}

test('commands queue until open, then flush in order', () => {
  const { s, ws } = makeSocket();
  s.connect();
  const rid = s.sendTurn({ conversationId: 'c1', route: 'r', modelId: 'm', content: 'hi' });
  assert.equal(s.queue.length, 1); // CONNECTING — nothing sent yet
  ws()._open();
  assert.deepEqual(ws().sent.map((f) => f.type), ['turn.send']);
  assert.equal(ws().sent[0].requestId, rid);
  assert.equal(ws().sent[0].conversationId, 'c1');
  assert.equal(ws().sent[0].payload.content, 'hi');
});

test('steerTurn returns the inputId — the command requestId', () => {
  const { s, ws } = makeSocket();
  s.connect();
  ws()._open();
  const inputId = s.steerTurn({ conversationId: 'c1', turnId: 't1', content: 'steer text' });
  const f = ws().sent.find((x) => x.type === 'turn.steer');
  assert.equal(f.requestId, inputId);
  assert.equal(f.turnId, 't1');
  assert.equal(f.conversationId, 'c1');
  assert.deepEqual(f.payload, { content: 'steer text' });
});

test('steerRecall targets the exact turn and inputId', () => {
  const { s, ws } = makeSocket();
  s.connect();
  ws()._open();
  const rid = s.steerRecall({ conversationId: 'c1', turnId: 't1', inputId: 'in-1' });
  const f = ws().sent.find((x) => x.type === 'turn.steer_recall');
  assert.equal(f.requestId, rid);
  assert.equal(f.turnId, 't1');
  assert.deepEqual(f.payload, { inputId: 'in-1' });
});

test('in-flight turns re-watch from the last observed seq after reconnect', () => {
  const { s, ws } = makeSocket();
  s.connect();
  ws()._open();
  ws()._message({ type: 'turn.opened', requestId: 'r1', conversationId: 'c1', turnId: 't1', seq: 1 });
  ws()._message({ type: 'turn.delta', conversationId: 'c1', turnId: 't1', seq: 7, payload: { text: 'x' } });
  assert.equal(s.turns.get('t1').lastSeq, 7);
  // The socket drops; queued commands are discarded, never retried.
  s.queue.push(JSON.stringify({ type: 'turn.send', requestId: 'lost' }));
  ws()._close();
  assert.equal(s.queue.length, 0);
  // Reconnect — a fresh socket re-watches t1 from seq 7.
  s.connect();
  const fresh = ws();
  fresh._open();
  const watch = fresh.sent.find((f) => f.type === 'turn.watch');
  assert.equal(watch.turnId, 't1');
  assert.equal(watch.conversationId, 'c1');
  assert.equal(watch.afterSeq, 7);
  assert.equal(fresh.sent.find((f) => f.requestId === 'lost'), undefined);
});

test('terminal frames retire the turn — no re-watch after settle', () => {
  const { s, ws } = makeSocket();
  s.connect();
  ws()._open();
  ws()._message({ type: 'turn.opened', requestId: 'r1', conversationId: 'c1', turnId: 't1', seq: 1 });
  ws()._message({ type: 'turn.completed', conversationId: 'c1', turnId: 't1', seq: 9, payload: {} });
  assert.equal(s.turns.has('t1'), false);
  ws()._close();
  s.connect();
  const fresh = ws();
  fresh._open();
  assert.equal(fresh.sent.find((f) => f.type === 'turn.watch'), undefined);
});

test('a replayed sequenced frame is dispatched exactly once', () => {
  const { s, ws, frames } = makeSocket();
  s.connect();
  ws()._open();
  ws()._message({ type: 'turn.opened', requestId: 'r1', conversationId: 'c1', turnId: 't1', seq: 1 });
  const ack = { type: 'turn.steer_ack', conversationId: 'c1', turnId: 't1', seq: 2, payload: { inputId: 'i1' } };
  ws()._message(ack);
  ws()._message(ack); // replay — the shell must see it once
  assert.deepEqual(frames.map((f) => f.type), ['turn.opened', 'turn.steer_ack']);
  assert.equal(s.turns.get('t1').lastSeq, 2);
});

test('an older frame arriving after newer ones is ignored', () => {
  const { s, ws, frames } = makeSocket();
  s.connect();
  ws()._open();
  ws()._message({ type: 'turn.opened', requestId: 'r1', conversationId: 'c1', turnId: 't1', seq: 1 });
  ws()._message({ type: 'turn.delta', conversationId: 'c1', turnId: 't1', seq: 9, payload: { text: 'new' } });
  ws()._message({ type: 'turn.delta', conversationId: 'c1', turnId: 't1', seq: 3, payload: { text: 'old' } });
  ws()._message({ type: 'turn.steer_recalled', conversationId: 'c1', turnId: 't1', seq: 5, payload: { inputId: 'i1' } });
  assert.deepEqual(frames.map((f) => f.type), ['turn.opened', 'turn.delta']);
  assert.equal(s.turns.get('t1').lastSeq, 9); // the cursor never regresses
});

test('a replayed turn.opened never resets the tracked cursor', () => {
  const { s, ws } = makeSocket();
  s.connect();
  ws()._open();
  ws()._message({ type: 'turn.opened', requestId: 'r1', conversationId: 'c1', turnId: 't1', seq: 1 });
  ws()._message({ type: 'turn.delta', conversationId: 'c1', turnId: 't1', seq: 7, payload: { text: 'x' } });
  ws()._message({ type: 'turn.opened', requestId: 'r1', conversationId: 'c1', turnId: 't1', seq: 1 });
  assert.equal(s.turns.get('t1').lastSeq, 7);
});

test('reconnect keeps the cursor — re-watch never regresses afterSeq', () => {
  const { s, ws } = makeSocket();
  s.connect();
  ws()._open();
  ws()._message({ type: 'turn.opened', requestId: 'r1', conversationId: 'c1', turnId: 't1', seq: 1 });
  ws()._message({ type: 'turn.delta', conversationId: 'c1', turnId: 't1', seq: 12, payload: { text: 'x' } });
  // Replay arrives in-stream before the drop — cursor stays put.
  ws()._message({ type: 'turn.opened', requestId: 'r1', conversationId: 'c1', turnId: 't1', seq: 1 });
  ws()._message({ type: 'turn.delta', conversationId: 'c1', turnId: 't1', seq: 4, payload: { text: 'stale' } });
  ws()._close();
  s.connect();
  const fresh = ws();
  fresh._open();
  const watch = fresh.sent.find((f) => f.type === 'turn.watch');
  assert.equal(watch.afterSeq, 12);
});

test('a replayed terminal frame is dropped via the seq marker', () => {
  const { s, ws, frames } = makeSocket();
  s.connect();
  ws()._open();
  ws()._message({ type: 'turn.opened', requestId: 'r1', conversationId: 'c1', turnId: 't1', seq: 1 });
  const done = { type: 'turn.completed', conversationId: 'c1', turnId: 't1', seq: 9, payload: { userMessagePersisted: true } };
  ws()._message(done);
  ws()._message(done); // replay after settle
  assert.deepEqual(frames.map((f) => f.type), ['turn.opened', 'turn.completed']);
});

test('resync verdicts retire or keep tracking accordingly', () => {
  const { s, ws } = makeSocket();
  s.connect();
  ws()._open();
  ws()._message({ type: 'turn.opened', requestId: 'r1', conversationId: 'c1', turnId: 't-run', seq: 1 });
  ws()._message({ type: 'turn.opened', requestId: 'r2', conversationId: 'c1', turnId: 't-done', seq: 2 });
  ws()._message({ type: 'turn.opened', requestId: 'r3', conversationId: 'c1', turnId: 't-lost', seq: 3 });
  ws()._message({ type: 'turn.resync_required', conversationId: 'c1', turnId: 't-run', seq: 4, payload: { turn: 'active', steering: [] } });
  ws()._message({ type: 'turn.resync_required', conversationId: 'c1', turnId: 't-done', seq: 5, payload: { turn: 'completed' } });
  ws()._message({ type: 'turn.resync_required', conversationId: 'c1', turnId: 't-lost', seq: 6, payload: { turn: 'unknown' } });
  assert.equal(s.turns.has('t-run'), true);   // still running — keep watching
  assert.equal(s.turns.get('t-run').lastSeq, 4);
  assert.equal(s.turns.has('t-done'), false); // settled — never re-watch
  assert.equal(s.turns.has('t-lost'), false); // expired — never re-watch
  ws()._close();
  s.connect();
  const fresh = ws();
  fresh._open();
  const watches = fresh.sent.filter((f) => f.type === 'turn.watch').map((f) => f.turnId);
  assert.deepEqual(watches, ['t-run']);
});

test('terminal seq markers stay bounded at 64', () => {
  const { s, ws } = makeSocket();
  s.connect();
  ws()._open();
  for (let i = 0; i < 80; i++) {
    ws()._message({ type: 'turn.opened', requestId: `r${i}`, conversationId: 'c1', turnId: `t${i}`, seq: i * 2 + 1 });
    ws()._message({ type: 'turn.completed', conversationId: 'c1', turnId: `t${i}`, seq: i * 2 + 2, payload: {} });
  }
  assert.equal(s.terminals.size, 64);
  assert.equal(s.terminals.has('t0'), false);  // oldest evicted
  assert.equal(s.terminals.has('t79'), true);  // newest kept
});

test('steer ack/recalled/snapshot frames pass through for the shell to reconcile', () => {
  const { s, ws, frames } = makeSocket();
  s.connect();
  ws()._open();
  ws()._message({ type: 'turn.opened', requestId: 'r1', conversationId: 'c1', turnId: 't1', seq: 1 });
  ws()._message({ type: 'turn.steer_ack', conversationId: 'c1', turnId: 't1', seq: 2, payload: { inputId: 'i1' } });
  ws()._message({ type: 'turn.steer_recalled', conversationId: 'c1', turnId: 't1', seq: 3, payload: { inputId: 'i2' } });
  ws()._message({
    type: 'turn.resync_required', conversationId: 'c1', turnId: 't1', seq: 4,
    payload: { steering: [{ inputId: 'i1', state: 'persisted' }] },
  });
  assert.deepEqual(frames.map((f) => f.type),
    ['turn.opened', 'turn.steer_ack', 'turn.steer_recalled', 'turn.resync_required']);
  assert.equal(s.turns.get('t1').lastSeq, 4); // replay resumes past them
});
