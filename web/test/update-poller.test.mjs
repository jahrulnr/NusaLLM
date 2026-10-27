import { test } from 'node:test';
import assert from 'node:assert/strict';
import { UpdatePoller, POLL_INTERVAL_MS } from '../lib/update-poller.js';

// Timers are recorded instead of run, so each test fires the interval by hand.
function fakeTimers() {
  const intervals = [];
  return {
    intervals,
    setInterval(fn, ms) {
      const handle = { fn, ms, unref() { handle.unrefd = true; } };
      intervals.push(handle);
      return handle;
    },
    clearInterval(handle) { handle.cleared = true; },
  };
}

const settle = () => new Promise((resolve) => setImmediate(resolve));

test('start refreshes at once, then polls on the release interval without holding the process', async () => {
  const timers = fakeTimers();
  const seen = [];
  const poller = new UpdatePoller({
    fetchStatus: async () => ({ current: '0.10.3', available: false }),
    checkNow: async () => ({ current: 'never-used' }),
    onStatus: (status) => seen.push(status),
    timers,
  });
  await poller.start();
  assert.deepEqual(seen, [{ current: '0.10.3', available: false }]);
  assert.equal(timers.intervals.length, 1);
  assert.equal(timers.intervals[0].ms, POLL_INTERVAL_MS);
  assert.equal(timers.intervals[0].unrefd, true);

  poller.stop();
  assert.equal(timers.intervals[0].cleared, true);
});

test('an interval tick reads the server cache, not a fresh release lookup', async () => {
  const timers = fakeTimers();
  let fetches = 0;
  let checks = 0;
  const poller = new UpdatePoller({
    fetchStatus: async () => { fetches += 1; return { current: '0.10.3' }; },
    checkNow: async () => { checks += 1; return { current: '0.10.3' }; },
    onStatus() {},
    timers,
  });
  await poller.start();
  timers.intervals[0].fn();
  await settle();
  assert.equal(fetches, 2);
  assert.equal(checks, 0);
});

test('a manual check asks for a fresh lookup and reports its answer', async () => {
  const seen = [];
  let fetches = 0;
  const poller = new UpdatePoller({
    fetchStatus: async () => { fetches += 1; return { latest: '0.10.3', checkedAt: 'old' }; },
    checkNow: async () => ({ latest: '0.10.4', checkedAt: 'new' }),
    onStatus: (status) => seen.push(status),
    timers: fakeTimers(),
  });
  const next = await poller.check();
  assert.equal(next.latest, '0.10.4');
  assert.equal(fetches, 0);
  assert.deepEqual(seen, [next]);
});

test('a failed poll keeps the last status and records the error beside it', async () => {
  const seen = [];
  let offline = false;
  const poller = new UpdatePoller({
    fetchStatus: async () => {
      if (offline) throw new Error('offline');
      return { latest: '0.10.4', available: true };
    },
    checkNow: async () => { throw new Error('offline'); },
    onStatus: (status) => seen.push(status),
    timers: fakeTimers(),
  });
  await poller.refresh();
  offline = true;
  await poller.refresh();
  assert.equal(seen[1].latest, '0.10.4');
  assert.equal(seen[1].available, true);
  assert.equal(seen[1].error, 'offline');

  await poller.check();
  assert.equal(seen[2].latest, '0.10.4', 'a failed manual check keeps the known update too');
  assert.equal(seen[2].error, 'offline');
});

test('a first failure reports only the error, with no status to keep', async () => {
  const seen = [];
  const poller = new UpdatePoller({
    fetchStatus: async () => { throw new Error('no route'); },
    checkNow: async () => ({}),
    onStatus: (status) => seen.push(status),
    timers: fakeTimers(),
  });
  await poller.start();
  assert.deepEqual(seen, [{ error: 'no route' }]);
});
