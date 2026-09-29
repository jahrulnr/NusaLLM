import { test } from 'node:test';
import assert from 'node:assert/strict';
import { toolOrbPhase, toolOutputFailed } from '../lib/tool-phase.js';

test('in-progress tool calls stay live even when a chunk looks like a failure', () => {
  assert.equal(toolOrbPhase('requested', ''), 'live');
  assert.equal(toolOrbPhase('streaming', ''), 'live');
  assert.equal(toolOrbPhase('streaming', 'partial\n(exit 1)'), 'live');
});

test('a settled call is green unless the stored output is a failure marker', () => {
  assert.equal(toolOrbPhase('settled', 'hello\n'), 'done');
  assert.equal(toolOrbPhase('settled', ''), 'done');
  assert.equal(toolOrbPhase('settled', 'ok\n(exit 0)'), 'done');
  assert.equal(toolOrbPhase('settled', 'error: tool not permitted'), 'failed');
  assert.equal(toolOrbPhase('settled', 'interrupted by user'), 'failed');
  assert.equal(toolOrbPhase('settled', 'partial\n(exit 3)'), 'failed');
  assert.equal(toolOrbPhase('settled', 'partial\n(signal: killed)'), 'failed');
});

test('a call with no recorded outcome is idle', () => {
  assert.equal(toolOrbPhase('pending', ''), 'idle');
  assert.equal(toolOrbPhase('', ''), 'idle');
});

test('failure markers match the stored tool result, not a mention mid-log', () => {
  assert.equal(toolOutputFailed('build failed: see log\n(exit 0)'), false);
  assert.equal(toolOutputFailed('error: unknown tool exec'), true);
  assert.equal(toolOutputFailed('line\n(exit 1)'), true);
  assert.equal(toolOutputFailed('(exit 1)'), true);
  assert.equal(toolOutputFailed('(exit 0)'), false);
});
