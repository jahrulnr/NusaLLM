// T-25 — the application status has one writer (the controller), the install
// flow runs one loop with its waits and reload injected, and a discovered
// upstream-provider answer cannot outlive the gateway document it came from.
//
// Everything here drives `web/lib/chat.js` against the mock API: no DOM, no
// browser, no timers left running (env.mjs unrefs what the code schedules).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import './register.mjs';
import './env.mjs';
import { calls, mockApi, sleep } from './env.mjs';

const store = await import('../lib/store.js');
const chat = await import('../lib/chat.js');

const reads = (path) => calls.filter((c) => c.method === 'GET' && c.url.startsWith(path)).length;

function reset() {
  store.state.update = null;
  store.state.gateways = { items: [], status: 'ready', error: null };
  chat.cancelInstall();
}

test('a status read lands in the store, a forced check asks upstream, and a failed read keeps the badge it had', async () => {
  reset();
  mockApi({
    'POST /api/update/check': { current: '1.0.0', latest: '1.2.0', available: true },
    'GET /api/update': { current: '1.0.0', latest: '1.1.0', available: true },
  });
  await chat.refreshUpdate();
  assert.equal(store.state.update.latest, '1.1.0', 'the plain read answers from the server cache');
  await chat.refreshUpdate(true);
  assert.equal(store.state.update.latest, '1.2.0', 'force routes to the check endpoint');

  mockApi({ 'GET /api/update': [500, { code: 'internal', detail: 'boom' }] });
  await assert.rejects(chat.refreshUpdate(), /boom/, 'the caller decides how to report it');
  assert.equal(store.state.update.latest, '1.2.0', 'the sidebar badge survives a failed background read');
});

test('the install flow reports every progress answer, stops on the terminal one, and does not reload an unchanged version', async () => {
  reset();
  store.state.update = { current: '1.0.0' };
  let polls = 0;
  mockApi({
    'GET /api/update/install': () => {
      polls++;
      return polls === 1 ? { state: 'running', phase: 'download', bytesDone: 5, bytesTotal: 10 } : { state: 'succeeded' };
    },
    'POST /api/update/install': { state: 'running', phase: 'start', bytesDone: 0, bytesTotal: 10 },
    'GET /api/update': { current: '1.0.0' },
  });
  const seen = [];
  let reloads = 0;
  const res = await chat.installUpdate({
    onProgress: (p) => seen.push(p.bytesDone === 5 ? 'running+bytes' : p.state),
    reload: () => reloads++,
    wait: async () => {},
    pollMs: 1,
    graceMs: 0,
  });
  assert.deepEqual(seen, ['running', 'running+bytes', 'succeeded'], 'each answer reaches the page in order');
  assert.equal(res.state, 'succeeded');
  assert.equal(polls, 2, 'polling stops at the terminal answer');
  assert.equal(reloads, 0, 'the same version does not reload');
});

test('a terminal progress answer is final: a later running answer never reaches the page', async () => {
  reset();
  store.state.update = { current: '1.0.0' };
  let polls = 0;
  mockApi({
    // Every poll after the first would say "running" again if the loop kept going.
    'GET /api/update/install': () => (++polls === 1 ? { state: 'succeeded' } : { state: 'running' }),
    'POST /api/update/install': { state: 'running' },
    'GET /api/update': { current: '1.0.0' },
  });
  const seen = [];
  await chat.installUpdate({ onProgress: (p) => seen.push(p.state), wait: async () => {}, pollMs: 1, graceMs: 0 });
  assert.deepEqual(seen, ['running', 'succeeded']);
  assert.equal(polls, 1, 'one poll after the start, then the flow stopped');
});

test('a restarted server with a new version reloads exactly once', async () => {
  reset();
  store.state.update = { current: '1.0.0' };
  mockApi({
    'GET /api/update/install': { state: 'succeeded' },
    'POST /api/update/install': { state: 'running' },
    'GET /api/update': { current: '1.1.0' },
  });
  let reloads = 0;
  await chat.installUpdate({ onProgress: () => {}, reload: () => reloads++, wait: async () => {}, pollMs: 1, graceMs: 50 });
  assert.equal(reloads, 1, 'the page reloads once the server answers with the new version');
});

test('one install loop at a time, and a finished flow can start again', async () => {
  reset();
  let starts = 0;
  mockApi({
    'GET /api/update/install': { state: 'succeeded' },
    'POST /api/update/install': () => { starts++; return { state: 'succeeded' }; },
    'GET /api/update': { current: '1.0.0' },
  });
  const first = chat.installUpdate({ onProgress: () => {}, wait: async () => {}, graceMs: 0 });
  const second = chat.installUpdate({ onProgress: () => {}, wait: async () => {}, graceMs: 0 });
  assert.equal(second, first, 'a second click rides the running flow instead of starting another');
  await first;
  assert.equal(starts, 1);
  await chat.installUpdate({ onProgress: () => {}, wait: async () => {}, graceMs: 0 });
  assert.equal(starts, 2, 'the finished flow leaves nothing behind that blocks a new one');
});

test('cancelling stops the loop: no further wait, no further fetch, and nothing left to cancel', async () => {
  reset();
  mockApi({
    'GET /api/update/install': { state: 'running' },
    'POST /api/update/install': { state: 'running' },
  });
  let waits = 0;
  const run = chat.installUpdate({
    onProgress: () => {},
    wait: async () => { waits++; await sleep(1); },
    pollMs: 1,
    graceMs: 0,
  });
  await sleep(10);
  assert.ok(waits >= 1, 'the flow is polling');
  assert.equal(chat.cancelInstall(), true);
  const seenWaits = waits;
  const seenPolls = reads('/api/update/install');
  await sleep(15);
  assert.equal(waits, seenWaits, 'no new pause is scheduled after a cancel');
  assert.equal(reads('/api/update/install'), seenPolls, 'no new poll is sent after a cancel');
  assert.equal(chat.cancelInstall(), false, 'the second cancel has nothing to stop');
  await run;
});

test('a cancel resolves the pending pause, so unmounting leaves no timer behind', async () => {
  reset();
  mockApi({
    'GET /api/update/install': { state: 'running' },
    'POST /api/update/install': { state: 'running' },
  });
  const run = chat.installUpdate({ onProgress: () => {}, pollMs: 60_000, graceMs: 0 });
  await sleep(10);
  assert.equal(chat.cancelInstall(), true);
  const started = Date.now();
  await run;
  assert.ok(Date.now() - started < 1000, 'the 60s pause was cleared, not waited out');
});

test('a discovered provider list is cached, dropped when the gateways are re-read, and a failure is not cached', async () => {
  reset();
  let listed = 0;
  mockApi({
    // Specific path first: the mock matches by prefix.
    'GET /api/gateways/g1/providers': () => ({ data: [{ slug: 'upstream-' + (++listed), name: 'U' }] }),
    'GET /api/gateways': { data: [], page: { total: 0 } },
  });
  const first = await chat.providersFor('g1', 'm1');
  const again = await chat.providersFor('g1', 'm1');
  assert.deepEqual(again, first, 'the second open answers from the cache');
  assert.equal(listed, 1, 'one discovery per gateway+model');
  await chat.loadGateways();
  const afterSave = await chat.providersFor('g1', 'm1');
  assert.equal(listed, 2, 'saving a provider drops the answer it invalidated');
  assert.notDeepEqual(afterSave, first, 'the picker sees what the new document offers');

  mockApi({ 'GET /api/gateways/g2/providers': [500, { code: 'internal', detail: 'catalog down' }] });
  await assert.rejects(chat.providersFor('g2', 'm2'), /catalog down/);
  await assert.rejects(chat.providersFor('g2', 'm2'), /catalog down/, 'a failure is retried, not remembered');
  assert.equal(reads('/api/gateways/g2/providers'), 2);
});
