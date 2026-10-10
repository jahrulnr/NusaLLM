import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  createTurnProjection, applyTurnFrame, settleTurnProjection,
} from '../lib/turn-projection.js';

let mono = 0;
let wall = 1000;
const clock = () => ({ wall: (wall += 100), mono: (mono += 50) });
const F = (type, payload, seq) => ({ type, payload, seq });

const segTypes = (p) => p.segments.map((s) => s.type);
const steers = (p) => p.segments.filter((s) => s.type === 'steer');

test('a steer fold splits markdown and renders ordered user text', () => {
  const p = createTurnProjection('req-1');
  applyTurnFrame(p, F('turn.delta', { text: 'partial answer' }), clock());
  applyTurnFrame(p, F('turn.steer', { text: 'focus on tests', inputId: 'i1', final: true }), clock());
  applyTurnFrame(p, F('turn.delta', { text: 'continued' }), clock());
  assert.deepEqual(segTypes(p), ['markdown', 'steer', 'markdown']);
  assert.equal(steers(p)[0].text, 'focus on tests');
  assert.equal(steers(p)[0].final, true);
  // The markdown never absorbs the steer — a new segment follows it.
  assert.equal(p.segments[0].text, 'partial answer');
  assert.equal(p.segments[2].text, 'continued');
});

test('a steer fold finishes thinking and settles unresolved tools', () => {
  const p = createTurnProjection('req-1');
  applyTurnFrame(p, F('turn.reasoning', { text: 'planning' }), clock());
  applyTurnFrame(p, F('turn.tool_call', { callId: 'c1', name: 'read' }), clock());
  applyTurnFrame(p, F('turn.steer', { text: 'stop that', inputId: 'i1', final: true }), clock());
  const thinking = p.segments.find((s) => s.type === 'thinking');
  const tool = p.segments.find((s) => s.type === 'tool');
  assert.equal(thinking.status, 'done');     // thinking never resumes past a fold
  assert.equal(tool.tool.status, 'settled'); // honest boundary, not tool content
  assert.deepEqual(segTypes(p), ['thinking', 'tool', 'steer']);
});

test('chunked folds reassemble by inputId — identical inputs stay separate', () => {
  const p = createTurnProjection('req-1');
  applyTurnFrame(p, F('turn.steer', { text: 'go ', inputId: 'i1', final: false }), clock());
  applyTurnFrame(p, F('turn.steer', { text: 'left', inputId: 'i1', final: true }), clock());
  applyTurnFrame(p, F('turn.steer', { text: 'go left', inputId: 'i2', final: true }), clock());
  const s = steers(p);
  assert.equal(s.length, 2);
  assert.equal(s[0].text, 'go left');
  assert.equal(s[0].inputId, 'i1');
  assert.equal(s[1].text, 'go left');
  assert.equal(s[1].inputId, 'i2');
});

test('receipt-free folds (CLI origin) group chunks until the final marker', () => {
  const p = createTurnProjection('req-1');
  applyTurnFrame(p, F('turn.steer', { text: 'same ', final: false }), clock());
  applyTurnFrame(p, F('turn.steer', { text: 'text', final: true }), clock());
  applyTurnFrame(p, F('turn.steer', { text: 'same text', final: true }), clock());
  const s = steers(p);
  assert.equal(s.length, 2); // identical consecutive inputs never concatenate
  assert.equal(s[0].text, 'same text');
  assert.equal(s[1].text, 'same text');
  assert.equal(s[0].inputId, null);
});

test('replayed seq numbers dedupe before any mutation', () => {
  const p = createTurnProjection('req-1');
  applyTurnFrame(p, F('turn.delta', { text: 'a', }, 5), clock());
  applyTurnFrame(p, F('turn.steer', { text: 'x', inputId: 'i1', final: true }, 6), clock());
  // A reconnect re-watch replays frames 5 and 6 — nothing doubles.
  applyTurnFrame(p, F('turn.delta', { text: 'a', }, 5), clock());
  applyTurnFrame(p, F('turn.steer', { text: 'x', inputId: 'i1', final: true }, 6), clock());
  assert.equal(p.segments.length, 2);
  assert.equal(p.segments[0].text, 'a');
  assert.equal(steers(p).length, 1);
  assert.equal(steers(p)[0].text, 'x');
  // A genuinely new frame still lands.
  applyTurnFrame(p, F('turn.delta', { text: 'b' }, 7), clock());
  assert.equal(p.segments[2].text, 'b');
});

test('unsequenced frames still apply; seq tracking starts on first number', () => {
  const p = createTurnProjection('req-1');
  applyTurnFrame(p, F('turn.delta', { text: 'no seq' }), clock());
  applyTurnFrame(p, F('turn.delta', { text: 's1' }, 3), clock());
  applyTurnFrame(p, F('turn.delta', { text: 'replay' }, 3), clock()); // dropped
  applyTurnFrame(p, F('turn.delta', { text: 'old' }, 2), clock());   // dropped
  assert.equal(p.segments[0].text, 'no seqs1');
});

test('steer announcements name the fold, never the content', () => {
  const p = createTurnProjection('req-1');
  applyTurnFrame(p, F('turn.steer', { text: 'secret instruction', inputId: 'i1', final: true }), clock());
  const last = p.announcements[p.announcements.length - 1];
  assert.equal(last, 'Your input folded into the turn');
  assert.ok(!last.includes('secret'));
});

test('terminal settle leaves steer segments as honest user text', () => {
  const p = createTurnProjection('req-1');
  applyTurnFrame(p, F('turn.delta', { text: 'work' }), clock());
  applyTurnFrame(p, F('turn.steer', { text: 'mid-turn input', inputId: 'i1', final: true }), clock());
  settleTurnProjection(p);
  assert.deepEqual(segTypes(p), ['markdown', 'steer']);
  assert.equal(p.active, false);
});
