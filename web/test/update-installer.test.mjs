import { test } from 'node:test';
import assert from 'node:assert/strict';
import { UpdateInstaller } from '../lib/update-installer.js';

// Timers the test drives by hand: flush() runs whatever is scheduled, then the
// promises the run started settle before the test looks again.
function manualTimers() {
  let next = 0;
  const pending = new Map();
  return {
    setTimeout(fn) {
      const id = ++next;
      pending.set(id, fn);
      return id;
    },
    clearTimeout(id) {
      pending.delete(id);
    },
    count() {
      return pending.size;
    },
    async flush() {
      const fns = [...pending.values()];
      pending.clear();
      fns.forEach((fn) => fn());
      await new Promise((resolve) => setImmediate(resolve));
    },
  };
}

// A script answers each call in order. An Error value rejects the call.
function script(list) {
  const queue = [...list];
  return async () => {
    const step = queue.shift();
    if (step === undefined) throw new Error('no scripted answer left');
    if (step instanceof Error) throw step;
    return step;
  };
}

function harness({ start = [], progress = [], status = [], deadlineMs = 90_000, clock = { t: 0 } } = {}) {
  const timers = manualTimers();
  const views = [];
  const api = {
    startUpdate: script(start),
    getUpdateProgress: script(progress),
    getUpdate: script(status),
  };
  const installer = new UpdateInstaller({
    api,
    onChange: (v) => views.push(v),
    timers,
    now: () => clock.t,
    pollMs: 1,
    deadlineMs,
  });
  return { installer, timers, views, clock };
}

const running = (phase, extra = {}) => ({ state: 'running', phase, version: '0.1.0', ...extra });

test('a service restart drops the connection and resolves once the new version answers', async () => {
  const h = harness({
    start: [running('download')],
    progress: [
      running('verify'),
      running('install'),
      { state: 'succeeded', phase: 'restart', version: '0.1.0', restart: 'service' },
      new Error('connection reset'),
    ],
    status: [{ current: '0.1.0' }],
  });
  await h.installer.begin('0.1.0');
  for (let i = 0; i < 6 && h.installer.view.state === 'running'; i += 1) await h.timers.flush();

  assert.equal(h.installer.view.state, 'succeeded');
  assert.equal(h.installer.view.version, '0.1.0');
  assert.equal(h.installer.view.restart, 'service');
  assert.equal(h.timers.count(), 0, 'no poll left running after success');
});

test('an install the server fails is reported with its error and polling stops', async () => {
  const h = harness({
    start: [running('download')],
    progress: [{ state: 'failed', phase: 'verify', version: '0.1.0', error: 'checksum mismatch for nusallm-linux-amd64' }],
  });
  await h.installer.begin('0.1.0');
  await h.timers.flush();
  assert.equal(h.installer.view.state, 'failed');
  assert.equal(h.installer.view.error, 'checksum mismatch for nusallm-linux-amd64');
  assert.equal(h.timers.count(), 0);
});

test('a refused start names the reason and does not poll', async () => {
  const refusal = Object.assign(new Error('x'), { code: 'update_not_available', detail: 'no newer release is available' });
  const h = harness({ start: [refusal] });
  await h.installer.begin('0.1.0');
  assert.equal(h.installer.view.state, 'failed');
  assert.equal(h.installer.view.error, 'no newer release is available');
  assert.equal(h.timers.count(), 0);
});

test('a start refused as busy follows the install already running', async () => {
  const busy = Object.assign(new Error('x'), { code: 'update_busy', detail: 'an update is already running' });
  const h = harness({
    start: [busy],
    progress: [running('install')],
  });
  await h.installer.begin('0.1.0');
  assert.equal(h.installer.view.state, 'running');
  assert.equal(h.installer.view.phase, 'install');
});

test('resume stays quiet when no install is running', async () => {
  const h = harness({ progress: [{ state: 'idle' }] });
  await h.installer.resume();
  assert.equal(h.installer.view.state, 'idle');
  assert.equal(h.timers.count(), 0);
});

test('resume ignores a reply that names no install or an old failed job', async () => {
  const h = harness({ progress: [{}, { state: 'failed', phase: 'verify', version: '0.1.0', error: 'old' }] });
  await h.installer.resume();
  assert.equal(h.installer.view.state, 'idle');
  await h.installer.resume();
  assert.equal(h.installer.view.state, 'idle');
  assert.equal(h.timers.count(), 0);
});

test('resume follows an install that a reload interrupted', async () => {
  const h = harness({ progress: [running('download', { bytesDone: 10, bytesTotal: 100 })] });
  await h.installer.resume();
  assert.equal(h.installer.view.state, 'running');
  assert.equal(h.installer.view.bytesTotal, 100);
  assert.equal(h.timers.count(), 1);
});

test('a slow download is not a restart failure, however long it takes', async () => {
  const clock = { t: 0 };
  const h = harness({
    start: [running('download')],
    progress: Array.from({ length: 3 }, () => running('download')),
    deadlineMs: 1000,
    clock,
  });
  await h.installer.begin('0.1.0');
  clock.t = 10 * 60 * 1000;
  await h.timers.flush();
  assert.equal(h.installer.view.state, 'running');
});

test('a restart that never brings the target version back fails after the deadline', async () => {
  const clock = { t: 0 };
  const down = () => new Error('connection refused');
  const h = harness({
    start: [running('download')],
    progress: [{ state: 'succeeded', phase: 'restart', version: '0.1.0' }, down(), down()],
    status: [{ current: '0.0.0' }, { current: '0.0.0' }, down()],
    deadlineMs: 1000,
    clock,
  });
  await h.installer.begin('0.1.0');
  await h.timers.flush(); // the restart is queued
  clock.t = 5000;
  await h.timers.flush(); // the connection drops: the restart window opens here
  assert.equal(h.installer.view.state, 'running', 'the window has just opened');
  clock.t = 7000;
  await h.timers.flush(); // still no target version, two seconds past the 1s deadline
  assert.equal(h.installer.view.state, 'failed');
  assert.match(h.installer.view.error, /did not come back on 0\.1\.0/);
});

test('an idle server after the restart waits for the target version', async () => {
  const h = harness({
    start: [running('download')],
    progress: [
      { state: 'succeeded', phase: 'restart', version: '0.1.0', restart: 'process' },
      { state: 'idle' },
      { state: 'idle' },
    ],
    status: [{ current: '0.0.0' }, { current: '0.1.0' }],
  });
  await h.installer.begin('0.1.0');
  await h.timers.flush();
  assert.equal(h.installer.view.state, 'running', 'restart queued');
  await h.timers.flush();
  assert.equal(h.installer.view.state, 'running', 'still waiting while the old version answers');
  await h.timers.flush();
  assert.equal(h.installer.view.state, 'succeeded');
  assert.equal(h.installer.view.restart, 'process');
});
