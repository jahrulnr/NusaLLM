import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  createQueue, enqueue, steer, requeue, bindRoom, takeQueued,
  hasQueuedForRoom, hasHeldForRoom, findEntry, removeEntry,
  markInFlightUnknown, pauseQueue, unpauseQueue,
  markTurnUnknown, steerAcked, steerProjected, steerRecalled,
  reconcileSnapshot,
  QUEUED, SUBMITTING, STEERING, RECALLING, UNKNOWN,
} from '../lib/pending-queue.js';

const opts = { route: 'r', modelId: 'm', effort: '', provider: '', workspace: '' };

test('enqueue captures text, room, and submission opts in order', () => {
  const q = createQueue();
  const a = enqueue(q, { text: 'first', room: 'c1', opts });
  const b = enqueue(q, { text: 'second', room: 'c1', opts });
  assert.equal(q.items.length, 2);
  assert.equal(a.state, QUEUED);
  assert.ok(a.ordinal < b.ordinal);
  assert.deepEqual(a.opts, opts);
  // opts is a snapshot — mutating the source must not leak in.
  opts.route = 'changed';
  assert.equal(a.opts.route, 'r');
});

test('takeQueued drains only the addressed room, oldest first', () => {
  const q = createQueue();
  enqueue(q, { text: 'for-a', room: 'a', opts });
  enqueue(q, { text: 'for-b', room: 'b', opts });
  enqueue(q, { text: 'for-a-2', room: 'a', opts });
  const e = takeQueued(q, 'a');
  assert.equal(e.text, 'for-a');
  assert.equal(q.items.length, 2);
  // The other room's line is untouched; a's second line is next for a.
  assert.equal(takeQueued(q, 'a').text, 'for-a-2');
  assert.equal(takeQueued(q, 'a'), null);
  assert.equal(takeQueued(q, 'b').text, 'for-b');
  assert.equal(takeQueued(q, 'b'), null);
});

test('hasQueuedForRoom sees only queued entries for that room', () => {
  const q = createQueue();
  assert.equal(hasQueuedForRoom(q, 'a'), false);
  const e = enqueue(q, { text: 'x', room: 'a', opts });
  assert.equal(hasQueuedForRoom(q, 'a'), true);
  assert.equal(hasQueuedForRoom(q, 'b'), false);
  takeQueued(q, 'a');
  assert.equal(hasQueuedForRoom(q, 'a'), false);
  // A steering entry is not a queued send.
  steer(q, { text: 's', room: 'a', opts, turnId: 't1', inputId: 'i1' });
  assert.equal(hasQueuedForRoom(q, 'a'), false);
  removeEntry(q, e);
});

test('pre-open entries bind only their own fresh token to a minted room', () => {
  const q = createQueue();
  const e1 = enqueue(q, { text: 'one', room: 'fresh:1', opts });
  const e2 = enqueue(q, { text: 'two', room: 'fresh:1', opts });
  const otherFresh = enqueue(q, { text: 'other-fresh', room: 'fresh:2', opts });
  const other = enqueue(q, { text: 'other', room: 'c9', opts });
  bindRoom(q, 'fresh:1', 'minted');
  assert.equal(e1.room, 'minted');
  assert.equal(e2.room, 'minted');
  // A different fresh view's lines are never hijacked into this room.
  assert.equal(otherFresh.room, 'fresh:2');
  assert.equal(other.room, 'c9'); // an addressed room never rebinds
});

test('requeue rejoins at the original ordinal — newer lines never bypass', () => {
  const q = createQueue();
  const old = enqueue(q, { text: 'old', room: 'a', opts });
  enqueue(q, { text: 'mid', room: 'a', opts });
  enqueue(q, { text: 'new', room: 'a', opts });
  takeQueued(q, 'a'); // old dispatched, then fell back unconsumed
  requeue(q, old);
  const texts = q.items.map((e) => e.text);
  assert.deepEqual(texts, ['old', 'mid', 'new']);
  assert.equal(old.state, QUEUED);
  assert.equal(old.inputId, null);
  assert.equal(old.turnId, null);
});

test('steer submits tracked input; ack flips submitting to steering', () => {
  const q = createQueue();
  const e = steer(q, { text: 'steer me', room: 'a', opts, turnId: 't1', inputId: 'req-1' });
  assert.equal(e.state, SUBMITTING);
  assert.equal(e.inputId, 'req-1');
  assert.equal(steerAcked(q, 't1', 'req-1'), e);
  assert.equal(e.state, STEERING);
  // Wrong turn or unknown input never flips.
  assert.equal(steerAcked(q, 't2', 'req-1'), null);
  assert.equal(steerAcked(q, 't1', 'nope'), null);
});

test('steerProjected drops the folded input — the receipt persisted', () => {
  const q = createQueue();
  const e = steer(q, { text: 's', room: 'a', opts, turnId: 't1', inputId: 'i1' });
  steerAcked(q, 't1', 'i1');
  assert.equal(steerProjected(q, 't1', 'i1'), e);
  assert.equal(findEntry(q, e.id), null);
});

test('steerRecalled drops the input so the caller restores its text', () => {
  const q = createQueue();
  const e = steer(q, { text: 'restore me', room: 'a', opts, turnId: 't1', inputId: 'i1' });
  assert.equal(steerRecalled(q, 't1', 'i1'), e);
  assert.equal(e.text, 'restore me');
  assert.equal(findEntry(q, e.id), null);
});

test('markInFlightUnknown only touches transmitted, unanswered controls', () => {
  const q = createQueue();
  const local = enqueue(q, { text: 'local', room: 'a', opts });
  const sub = steer(q, { text: 'sub', room: 'a', opts, turnId: 't1', inputId: 'i1' });
  const acked = steer(q, { text: 'acked', room: 'a', opts, turnId: 't1', inputId: 'i2' });
  steerAcked(q, 't1', 'i2');
  const recall = steer(q, { text: 'rec', room: 'a', opts, turnId: 't1', inputId: 'i3' });
  steerAcked(q, 't1', 'i3');
  recall.state = RECALLING;
  assert.equal(markInFlightUnknown(q), true);
  assert.equal(local.state, QUEUED);   // never transmitted — stays local
  assert.equal(sub.state, UNKNOWN);    // ack possibly lost either way
  assert.equal(acked.state, STEERING); // acked — replay reconciles
  assert.equal(recall.state, UNKNOWN);
});

test('markTurnUnknown flags every non-queued entry bound to the turn', () => {
  const q = createQueue();
  enqueue(q, { text: 'local', room: 'a', opts });
  const a = steer(q, { text: 'a', room: 'a', opts, turnId: 't1', inputId: 'i1' });
  const b = steer(q, { text: 'b', room: 'a', opts, turnId: 't2', inputId: 'i2' });
  markTurnUnknown(q, 't1');
  assert.equal(a.state, UNKNOWN);
  assert.equal(b.state, SUBMITTING); // another turn is unaffected
});

test('reconcileSnapshot: consumed drops, recalled restores, pending requeues, absent goes unknown', () => {
  const q = createQueue();
  const consumed = steer(q, { text: 'c', room: 'a', opts, turnId: 't1', inputId: 'i-c' });
  const recalled = steer(q, { text: 'r', room: 'a', opts, turnId: 't1', inputId: 'i-r' });
  const pending = steer(q, { text: 'p', room: 'a', opts, turnId: 't1', inputId: 'i-p' });
  const claimed = steer(q, { text: 'cl', room: 'a', opts, turnId: 't1', inputId: 'i-cl' });
  const absent = steer(q, { text: 'x', room: 'a', opts, turnId: 't1', inputId: 'i-x' });
  const otherTurn = steer(q, { text: 'o', room: 'a', opts, turnId: 't2', inputId: 'i-o' });
  const out = reconcileSnapshot(q, 't1', [
    { inputId: 'i-c', state: 'persisted' },
    { inputId: 'i-r', state: 'recalled' },
    { inputId: 'i-p', state: 'pending' },
    { inputId: 'i-cl', state: 'claimed' },
  ]);
  assert.deepEqual(out.consumed, [consumed]);
  assert.deepEqual(out.restored, [recalled]);
  assert.deepEqual(out.requeued, [pending, claimed]);
  assert.deepEqual(out.unknown, [absent]);
  assert.equal(findEntry(q, consumed.id), null);
  assert.equal(findEntry(q, recalled.id), null);
  assert.equal(pending.state, QUEUED);   // fallback to the local queue
  assert.equal(claimed.state, QUEUED);   // claimed-but-uncommitted too
  assert.equal(absent.state, UNKNOWN);   // expired/restart — explicit, never retried
  assert.equal(otherTurn.state, SUBMITTING); // a foreign turn's snapshot leaves it alone
});

test('active snapshot keeps server-owned pending/claimed bound to the turn', () => {
  const q = createQueue();
  const pending = steer(q, { text: 'p', room: 'a', opts, turnId: 't1', inputId: 'i-p' });
  const claimed = steer(q, { text: 'cl', room: 'a', opts, turnId: 't1', inputId: 'i-cl' });
  const unknown = steer(q, { text: 'u', room: 'a', opts, turnId: 't1', inputId: 'i-u' });
  steerAcked(q, 't1', 'i-p');
  unknown.state = UNKNOWN;
  const out = reconcileSnapshot(q, 't1', [
    { inputId: 'i-p', state: 'pending' },
    { inputId: 'i-cl', state: 'claimed' },
    { inputId: 'i-u', state: 'pending' },
  ], { terminal: false });
  // Nothing requeues while the turn still runs — the server owns the
  // mailbox; requeueing would duplicate the input on the next turn.
  assert.deepEqual(out.requeued, []);
  assert.equal(pending.state, STEERING);
  assert.equal(claimed.state, STEERING);
  assert.equal(unknown.state, STEERING); // identity recovered
  assert.equal(pending.turnId, 't1');
  assert.equal(pending.inputId, 'i-p');
});

test('active snapshot resolves persisted/recalled; absent stays unknown', () => {
  const q = createQueue();
  const consumed = steer(q, { text: 'c', room: 'a', opts, turnId: 't1', inputId: 'i-c' });
  const recalled = steer(q, { text: 'r', room: 'a', opts, turnId: 't1', inputId: 'i-r' });
  const absent = steer(q, { text: 'x', room: 'a', opts, turnId: 't1', inputId: 'i-x' });
  const out = reconcileSnapshot(q, 't1', [
    { inputId: 'i-c', state: 'persisted' },
    { inputId: 'i-r', state: 'recalled' },
  ], { terminal: false });
  assert.deepEqual(out.consumed, [consumed]);
  assert.deepEqual(out.restored, [recalled]);
  assert.deepEqual(out.unknown, [absent]);
  assert.equal(findEntry(q, consumed.id), null);
  assert.equal(findEntry(q, recalled.id), null);
  assert.equal(absent.state, UNKNOWN); // no receipt — not recoverable yet
});

test('active snapshot: a recall in flight survives a still-pending receipt', () => {
  const q = createQueue();
  const e = steer(q, { text: 's', room: 'a', opts, turnId: 't1', inputId: 'i1' });
  steerAcked(q, 't1', 'i1');
  e.recallId = 'req-r';
  e.state = RECALLING;
  reconcileSnapshot(q, 't1', [{ inputId: 'i1', state: 'pending' }], { terminal: false });
  // The recall hasn't answered — don't regress to steering.
  assert.equal(e.state, RECALLING);
  assert.equal(e.turnId, 't1');
  assert.equal(e.inputId, 'i1');
});

test('active snapshot: replayed ack while recall in flight stays RECALLING', () => {
  const q = createQueue();
  const e = steer(q, { text: 's', room: 'a', opts, turnId: 't1', inputId: 'i1' });
  e.state = UNKNOWN;
  e.recallId = 'req-r'; // recall transmitted before the drop
  const out = reconcileSnapshot(q, 't1', [{ inputId: 'i1', state: 'pending' }], { terminal: false });
  assert.equal(e.state, RECALLING);
  assert.deepEqual(out.restored, []);
});

test('replayed ack restores UNKNOWN to STEERING only with no recall in flight', () => {
  const q = createQueue();
  const a = steer(q, { text: 'a', room: 'a', opts, turnId: 't1', inputId: 'i-a' });
  a.state = UNKNOWN;
  assert.equal(steerAcked(q, 't1', 'i-a'), a);
  assert.equal(a.state, STEERING);
  // With an outstanding recall the same ack must not resurrect steering.
  const b = steer(q, { text: 'b', room: 'a', opts, turnId: 't1', inputId: 'i-b' });
  b.state = UNKNOWN;
  b.recallId = 'req-b';
  assert.equal(steerAcked(q, 't1', 'i-b'), b);
  assert.equal(b.state, RECALLING);
});

test('replayed recall restores exactly once', () => {
  const q = createQueue();
  const e = steer(q, { text: 'dup', room: 'a', opts, turnId: 't1', inputId: 'i1' });
  assert.equal(steerRecalled(q, 't1', 'i1'), e);
  // The entry is gone — the same replayed frame cannot restore twice.
  assert.equal(steerRecalled(q, 't1', 'i1'), null);
});

test('requeue preserves ordinal when an unconsumed steer falls back', () => {
  const q = createQueue();
  const first = steer(q, { text: 'steer-1', room: 'a', opts, turnId: 't1', inputId: 'i1' });
  enqueue(q, { text: 'queued-2', room: 'a', opts });
  enqueue(q, { text: 'queued-3', room: 'a', opts });
  const out = reconcileSnapshot(q, 't1', [{ inputId: 'i1', state: 'pending' }]);
  assert.deepEqual(out.requeued, [first]);
  // Its original submission order is kept ahead of the later lines.
  assert.deepEqual(q.items.map((e) => e.text), ['steer-1', 'queued-2', 'queued-3']);
});

test('removeEntry/findEntry round-trip a known entry', () => {
  const q = createQueue();
  const e = enqueue(q, { text: 'x', room: 'a', opts });
  assert.equal(findEntry(q, e.id), e);
  removeEntry(q, e);
  assert.equal(findEntry(q, e.id), null);
  assert.equal(q.items.length, 0);
});

test('a pause holds the existing backlog; a held head blocks the room lane', () => {
  const q = createQueue();
  enqueue(q, { text: 'backlog', room: 'a', opts });
  pauseQueue(q);
  // Lines enqueued after the pause are fresh intent — but a held head
  // still blocks the room's lane ahead of them.
  enqueue(q, { text: 'fresh', room: 'a', opts });
  assert.equal(takeQueued(q, 'a'), null);
  assert.equal(hasHeldForRoom(q, 'a'), true);
  // Another room's unheld lane flows while the queue stays paused.
  enqueue(q, { text: 'other', room: 'b', opts });
  assert.equal(takeQueued(q, 'b').text, 'other');
  // Resume releases the hold — the backlog drains in order.
  unpauseQueue(q);
  assert.equal(hasHeldForRoom(q, 'a'), false);
  assert.equal(takeQueued(q, 'a').text, 'backlog');
  assert.equal(takeQueued(q, 'a').text, 'fresh');
});

test('a room-scoped pause never touches another room\'s lane', () => {
  const q = createQueue();
  enqueue(q, { text: 'mine', room: 'a', opts });
  enqueue(q, { text: 'theirs', room: 'b', opts });
  pauseQueue(q, 'a');
  assert.equal(hasHeldForRoom(q, 'a'), true);
  assert.equal(hasHeldForRoom(q, 'b'), false);
  assert.equal(takeQueued(q, 'b').text, 'theirs');
  assert.equal(takeQueued(q, 'a'), null);
  // Release is scoped the same way — another room's holds survive.
  enqueue(q, { text: 'b-backlog', room: 'b', opts });
  pauseQueue(q, 'b');
  unpauseQueue(q, 'a');
  assert.equal(hasHeldForRoom(q, 'a'), false);
  assert.equal(hasHeldForRoom(q, 'b'), true);
  assert.equal(q.paused, true);
  unpauseQueue(q);
  assert.equal(q.paused, false);
});

test('a requeue inherits the hold only inside a held lane', () => {
  const q = createQueue();
  const e = enqueue(q, { text: 'in-flight', room: 'a', opts });
  removeEntry(q, e); // simulate dispatch
  enqueue(q, { text: 'waiting', room: 'a', opts });
  pauseQueue(q, 'a');
  requeue(q, e);
  assert.equal(e.held, true);
  assert.equal(takeQueued(q, 'a'), null);
  // A different room's failed dispatch rejoins unheld — it drains.
  const other = enqueue(q, { text: 'elsewhere', room: 'b', opts });
  removeEntry(q, other);
  requeue(q, other);
  assert.equal(other.held, false);
  assert.equal(takeQueued(q, 'b').text, 'elsewhere');
});
