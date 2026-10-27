import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  stepStates, percent, sizeLabel, headline, restartNote,
} from '../lib/update-install.js';

const marks = (view) => stepStates(view).map((s) => s.state);

test('a running download marks download active and the rest pending', () => {
  assert.deepEqual(marks({ state: 'running', phase: 'download' }), ['active', 'pending', 'pending', 'pending']);
});

test('later phases mark earlier steps done', () => {
  assert.deepEqual(marks({ state: 'running', phase: 'verify' }), ['done', 'active', 'pending', 'pending']);
  assert.deepEqual(marks({ state: 'running', phase: 'install' }), ['done', 'done', 'active', 'pending']);
});

test('a dropped connection during the restart keeps the restart step active', () => {
  assert.deepEqual(
    marks({ state: 'running', phase: 'restart', reconnecting: true }),
    ['done', 'done', 'done', 'active'],
  );
});

test('success marks every step done', () => {
  assert.deepEqual(marks({ state: 'succeeded', phase: 'restart' }), ['done', 'done', 'done', 'done']);
});

test('a failure marks the failing step and leaves the later ones pending', () => {
  assert.deepEqual(marks({ state: 'failed', phase: 'verify' }), ['done', 'failed', 'pending', 'pending']);
  assert.deepEqual(marks({ state: 'failed', phase: 'restart' }), ['done', 'done', 'done', 'failed']);
});

test('an idle view has no active step', () => {
  assert.deepEqual(marks({ state: 'idle' }), ['pending', 'pending', 'pending', 'pending']);
});

test('percent reports download progress only, clamped to 0..100', () => {
  assert.equal(percent({ phase: 'download', bytesDone: 0, bytesTotal: 200 }), 0);
  assert.equal(percent({ phase: 'download', bytesDone: 50, bytesTotal: 200 }), 25);
  assert.equal(percent({ phase: 'download', bytesDone: 999, bytesTotal: 200 }), 100);
  assert.equal(percent({ phase: 'verify', bytesDone: 200, bytesTotal: 200 }), null);
  assert.equal(percent({ phase: 'download', bytesDone: 10, bytesTotal: 0 }), null);
});

test('sizeLabel reads bytes at a human scale', () => {
  assert.equal(sizeLabel(0), '0 B');
  assert.equal(sizeLabel(512), '512 B');
  assert.equal(sizeLabel(2048), '2 KB');
  assert.equal(sizeLabel(29_700_000), '28.3 MB');
  assert.equal(sizeLabel(undefined), '0 B');
});

test('headlines follow the phase and the restart path', () => {
  assert.equal(headline({ state: 'running', phase: 'download', version: '0.1.0' }), 'Downloading NusaLLM 0.1.0');
  assert.equal(headline({ state: 'running', phase: 'verify', version: '0.1.0' }), 'Verifying NusaLLM 0.1.0');
  assert.equal(headline({ state: 'running', phase: 'install', version: '0.1.0' }), 'Installing NusaLLM 0.1.0');
  assert.equal(
    headline({ state: 'running', phase: 'restart', restart: 'service' }),
    'Restarting the service',
  );
  assert.equal(
    headline({ state: 'running', phase: 'restart', restart: 'process', reconnecting: true }),
    'Restarting NusaLLM',
  );
  assert.equal(headline({ state: 'succeeded', version: '0.1.0' }), 'NusaLLM 0.1.0 is running');
  assert.equal(headline({ state: 'failed', version: '0.1.0' }), 'The update to 0.1.0 did not finish');
});

test('restartNote says which restart is happening', () => {
  assert.equal(
    restartNote({ restart: 'service' }),
    'Restarting the service. This window reconnects when it is back.',
  );
  assert.equal(
    restartNote({ restart: 'process' }),
    'Restarting NusaLLM. This window reconnects when it is back.',
  );
});
