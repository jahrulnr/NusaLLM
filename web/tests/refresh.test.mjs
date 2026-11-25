// T-15 (D-08) — a refresh compares before it writes.
//
// The contract (docs/audit/decisions.md D-08): `store.applyRecord(id, info)`
// keeps the last applied `updatedAt` per id and applies a strictly newer
// answer, discards a strictly older one, and treats an equal version as a
// no-op unless the surface's view key changed; `store.recordKey(surface,
// record)` names exactly the fields that surface renders.
//
// These are acceptance tests written before the seam exists. Until FE round 2
// exports `applyRecord`/`recordKey` they skip with the ticket name — a skip is
// reported, never counted as a pass. When the seam lands they run; a mismatch
// in signature is a conversation with FE, not a silent rewrite of the rule.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import './register.mjs';
import './env.mjs';

const store = await import('../lib/store.js');
const ready = typeof store.applyRecord === 'function' && typeof store.recordKey === 'function';
const pending = ready ? {} : { skip: 'pending: store.applyRecord/recordKey (T-15, FE round 2)' };

const conv = (over = {}) => ({
  id: 'c_1', title: 't', updatedAt: '2026-01-02T03:04:05Z',
  route: 'r', modelId: 'm', workspace: '', canCompact: true,
  usage: { inputTokens: 1 }, activeCount: 1, sealedSegments: 0, ...over,
});

const reset = () => {
  store.state.convs = { items: [], total: 0, hasMore: false, status: 'ready', error: null, more: false };
  store.state.rooms = {};
};

test('a strictly newer answer is applied', pending, () => {
  reset();
  store.state.convs.items = [conv({ updatedAt: '2026-01-02T03:04:05Z', title: 'old' })];
  store.applyRecord('c_1', conv({ updatedAt: '2026-01-02T03:05:00Z', title: 'new' }));
  assert.equal(store.state.convs.items[0].title, 'new');
});

test('a strictly older answer is discarded, so a slow read cannot undo a rename', pending, () => {
  reset();
  store.state.convs.items = [conv({ updatedAt: '2026-01-02T03:05:00Z', title: 'renamed' })];
  store.applyRecord('c_1', conv({ updatedAt: '2026-01-02T03:04:05Z', title: 'stale' }));
  assert.equal(store.state.convs.items[0].title, 'renamed', 'the older response must not win');
});

test('an equal version with an equal view key writes nothing and keeps the row identity', pending, () => {
  reset();
  const row = conv({ updatedAt: '2026-01-02T03:05:00Z', usage: { inputTokens: 1 } });
  store.state.convs.items = [row];
  let emitted = 0;
  const off = store.onEmit?.(() => { emitted++; });
  store.applyRecord('c_1', conv({ updatedAt: '2026-01-02T03:05:00Z', usage: { inputTokens: 999 }, activeCount: 9 }));
  assert.equal(store.state.convs.items[0], row, 'the same object stays in place');
  if (store.onEmit) assert.equal(emitted, 0, 'no render is requested for an unchanged view');
  off?.();
});

test('an equal version with a different view key is applied (a turn started, meta unchanged)', pending, () => {
  reset();
  store.state.convs.items = [conv({ updatedAt: '2026-01-02T03:05:00Z' })];
  store.applyRecord('c_1', conv({ updatedAt: '2026-01-02T03:05:00Z', activeTurnId: 't_9', activeTurnKind: 'send' }));
  assert.equal(store.state.convs.items[0].activeTurnId, 't_9');
});

test('the view key names what a surface renders and ignores churn it does not', pending, () => {
  const base = conv({ updatedAt: '2026-01-02T03:05:00Z' });
  const churn = conv({ updatedAt: '2026-01-02T03:05:00Z', usage: { inputTokens: 42 }, activeCount: 7, sealedSegments: 3 });
  const renamed = conv({ updatedAt: '2026-01-02T03:05:00Z', title: 'other' });
  const active = conv({ updatedAt: '2026-01-02T03:05:00Z', activeTurnId: 't_9' });
  assert.equal(store.recordKey('sidebar', base), store.recordKey('sidebar', churn));
  assert.notEqual(store.recordKey('sidebar', base), store.recordKey('sidebar', renamed));
  assert.notEqual(store.recordKey('sidebar', base), store.recordKey('sidebar', active), 'activeTurnId is rendered');
});

test('applying a record never touches room-local state', pending, () => {
  reset();
  const room = {
    key: 'c_1', id: 'c_1', info: conv(),
    draft: 'half-typed', queue: [{ id: 'q1', text: 'next' }], steers: [{ inputId: 's1', text: 'steer' }],
    live: { turnId: 't_1', segments: [{ id: 'x', kind: 'text', text: 'live' }] },
    pendingOpen: null, failed: null, sel: { modelId: 'picked' }, hist: { start: 0, total: 2 }, messages: [{ id: 'm1' }],
  };
  store.state.rooms.c_1 = room;
  store.applyRecord('c_1', conv({ updatedAt: '2026-01-02T06:00:00Z', title: 'from refresh' }));
  assert.equal(room.draft, 'half-typed');
  assert.deepEqual(room.queue.map((q) => q.id), ['q1']);
  assert.deepEqual(room.steers.map((s) => s.inputId), ['s1']);
  assert.equal(room.live.segments[0].text, 'live');
  assert.equal(room.sel.modelId, 'picked', 'the unsent picker choice outranks the refresh');
  assert.deepEqual(room.messages.map((m) => m.id), ['m1']);
});
