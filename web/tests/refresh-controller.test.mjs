// T-15 (D-08) — the controller half: one read per id in flight, a local write
// mid-flight re-reads once, a slow list answer cannot undo a rename, and an
// adopted turn reads its prompt from the record without touching local state.
//
// The store's funnel has its own tests (refresh.test.mjs). These drive
// `web/lib/chat.js` against the mock API and the fake socket in env.mjs.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import './register.mjs';
import './env.mjs';
import { calls, mockApi, sleep, sockets } from './env.mjs';

const store = await import('../lib/store.js');
const chat = await import('../lib/chat.js');

const conv = (over = {}) => ({
  id: 'c_1', title: 't', updatedAt: '2026-01-02T03:04:05Z',
  route: 'r', modelId: 'm', workspace: '', canCompact: true, ...over,
});
const page = (messages, total = messages.length) => ({ messages, effectiveStart: 0, total });
const msg = (id, role, extra = {}) => ({ id, role, content: id, createdAt: '', ...extra });

function reset() {
  store.state.convs = { items: [], total: 0, hasMore: false, status: 'ready', error: null, more: false };
  store.state.rooms = {};
  store.state.route = { view: 'home' };
  store.state.prefs = { theme: 'system', lastModel: null };
}

test('a list read that lands after a rename keeps the new title in the row and the room', async () => {
  reset();
  mockApi({
    'GET /api/conversations/c_1/messages': page([msg('u0', 'user')]),
    'GET /api/conversations/c_1': async () => { await sleep(15); return conv(); },
    'GET /api/conversations': async () => { await sleep(40); return { data: [conv({ title: 'old' })], page: { total: 1 } }; },
    'PATCH /api/conversations/c_1': conv({ title: 'new', updatedAt: '2026-01-02T03:06:00Z' }),
  });
  const listRead = chat.loadConversations();
  const opening = chat.openRoom('c_1');
  await opening;
  const r = store.state.rooms.c_1;
  await chat.rename(r, 'new');
  await listRead;
  assert.equal(store.state.convs.items[0].title, 'new', 'the older list answer does not undo the rename');
  assert.equal(r.info.title, 'new', 'the header and the row agree');
});

test('a read in flight across a local write re-reads once and applies the newer version', async () => {
  reset();
  let gets = 0;
  mockApi({
    'GET /api/conversations/c_1/messages': page([msg('u0', 'user')]),
    'GET /api/conversations/c_1': async () => {
      gets++;
      if (gets === 1) { await sleep(30); return conv({ title: 'old' }); }
      return conv({ title: 'new', updatedAt: '2026-01-02T03:06:00Z' });
    },
    'PATCH /api/conversations/c_1': conv({ title: 'new', updatedAt: '2026-01-02T03:06:00Z' }),
  });
  const r = chat.room('c_1');
  const inflight = chat.loadInfo(r);
  await sleep(5);
  await chat.rename(r, 'new');
  await inflight;
  assert.equal(r.info.title, 'new', 'the answer that predates the write never wins');
  assert.equal(gets, 2, 'the record is read exactly once more');
});

test('two needs for one record share one read', async () => {
  reset();
  let gets = 0;
  mockApi({
    'GET /api/conversations/c_1': async () => { gets++; await sleep(20); return conv(); },
  });
  const r = chat.room('c_1');
  await Promise.all([chat.loadInfo(r), chat.loadInfo(r), chat.loadInfo(r)]);
  assert.equal(gets, 1, 'a need that arrives during the flight rides it');
});

test('an adopted turn reads its prompt from the record once, and leaves local state alone', async () => {
  reset();
  let historyReads = 0;
  mockApi({
    // Specific paths first: the mock matches by prefix.
    'GET /api/conversations/c_1/messages': async () => {
      historyReads++;
      await sleep(5);
      return page([msg('u0', 'user'), msg('a1', 'assistant'), msg('u1', 'user')]);
    },
    'GET /api/conversations/c_1': conv({ activeTurnId: 't_9', activeTurnKind: 'send' }),
    'GET /api/conversations': { data: [conv()], page: { total: 1 } },
    'GET /api/gateways': { data: [], page: { total: 0 } },
    'GET /api/update': { current: '0.1.0', latest: '0.1.0', available: false, notes: '' },
  });
  chat.start();
  const socket = sockets[sockets.length - 1];
  socket.open();
  const r = chat.room('c_1');
  r.messages = [msg('u0', 'user'), msg('a1', 'assistant')];
  r.draft = 'half-typed';
  r.sel = { modelId: 'picked' };
  store.state.route = { view: 'chat', id: 'c_1' };
  await sleep(10);
  socket.serve({
    type: 'conversation.activity', conversationId: 'c_1', turnId: 't_9',
    payload: { state: 'opened', kind: 'send' },
  });
  await sleep(40);
  assert.equal(r.live?.adopted, true, 'the turn is adopted, not one this tab opened');
  assert.deepEqual(r.messages.map((m) => m.id), ['u0', 'a1', 'u1'], 'the prompt the journal never carries is drawn from the record');
  assert.equal(r.draft, 'half-typed', 'a refresh never touches the draft');
  assert.equal(r.sel.modelId, 'picked', 'the unsent picker choice outranks the refresh');
  assert.equal(historyReads, 1, 'one read, not a second fetch');
  assert.equal(calls.filter((c) => c.method === 'GET' && c.url.startsWith('/api/conversations/c_1/messages')).length, 1);
});

test('a rename of a running room keeps the live marker (T-19: the write reply is the read projection)', async () => {
  reset();
  const live = { activeTurnId: 't_live', activeTurnKind: 'send' };
  mockApi({
    'GET /api/conversations/c_1': conv({ title: 'old', ...live }),
    'GET /api/conversations': { data: [conv({ title: 'old', ...live })], page: { total: 1 } },
    'PATCH /api/conversations/c_1': conv({ title: 'new', updatedAt: '2026-01-02T03:06:00Z', ...live }),
  });
  const r = chat.room('c_1');
  await chat.loadInfo(r);
  await chat.loadConversations();
  await chat.rename(r, 'new');
  assert.equal(r.info.title, 'new', 'the write reply replaces the record');
  assert.equal(r.info.activeTurnId, 't_live', 'the reply carries the live turn, so the FE merges nothing');
  assert.equal(r.info.activeTurnKind, 'send');
  assert.equal(store.state.convs.items[0].title, 'new');
  assert.equal(store.state.convs.items[0].activeTurnId, 't_live', 'the row keeps its live dot');
});
