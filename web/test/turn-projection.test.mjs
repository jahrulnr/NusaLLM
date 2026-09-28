import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  createTurnProjection, applyTurnFrame, settleTurnProjection,
} from '../lib/turn-projection.js';

let mono = 0;
let wall = 1000;
const clock = () => ({ wall: (wall += 100), mono: (mono += 50) });
const F = (type, payload) => ({ type, payload });

const segTypes = (p) => p.segments.map((s) => s.type);
const tools = (p) => p.segments.filter((s) => s.type === 'tool').map((s) => s.tool);

test('full lifecycle: thinking→delta→tool_call→step(tool_calls)→tool_output→delta→terminal', () => {
  const p = createTurnProjection('req-1');
  applyTurnFrame(p, F('turn.phase', { phase: 'reasoning' }), clock());
  applyTurnFrame(p, F('turn.reasoning', { text: 'hmm' }), clock()); // reasoning text opens the card
  applyTurnFrame(p, F('turn.delta', { text: 'Before tool — ' }), clock());
  applyTurnFrame(p, F('turn.delta', { text: 'grows' }), clock());
  applyTurnFrame(p, F('turn.tool_call', { callId: 'c1', name: 'read' }), clock());
  applyTurnFrame(p, F('turn.tool_input', { callId: 'c1', text: '{"path":' }), clock());
  applyTurnFrame(p, F('turn.tool_input', { callId: 'c1', text: '"README.md"}' }), clock());
  applyTurnFrame(p, F('turn.step_finished', { finishReason: 'tool_calls' }), clock());
  applyTurnFrame(p, F('turn.tool_output', { callId: 'c1', text: 'line 1\n' }), clock());
  applyTurnFrame(p, F('turn.tool_output', { callId: 'c1', text: 'line 2\n' }), clock());

  // tool_output is progress only — streaming, not settled; chunks
  // accumulate in order, announcement deduped.
  let t = tools(p)[0];
  assert.equal(t.status, 'streaming');
  assert.equal(t.input, '{"path":"README.md"}');
  assert.equal(t.output, 'line 1\nline 2\n');
  assert.equal(p.announcements.filter((a) => a === 'read output activity').length, 1);

  applyTurnFrame(p, F('turn.delta', { text: 'after tool' }), clock());
  // Model activity proves the tool settled.
  t = tools(p)[0];
  assert.equal(t.status, 'settled');

  assert.deepEqual(segTypes(p), ['thinking', 'markdown', 'tool', 'markdown']);
  assert.equal(p.segments[1].text, 'Before tool — grows');
  assert.equal(p.segments[3].text, 'after tool');

  settleTurnProjection(p, clock());
  assert.equal(p.active, false);
  assert.equal(p.finishReason, 'tool_calls');
  assert.equal(p.segments[0].status, 'done');
});

test('two reasoning stretches stay two segments; phase frames alone open nothing', () => {
  const p = createTurnProjection('r2');
  applyTurnFrame(p, F('turn.phase', { phase: 'reasoning' }), clock());
  applyTurnFrame(p, F('turn.phase', { phase: 'reasoning' }), clock());
  assert.equal(p.segments.length, 0); // timing metadata only — no card without text
  applyTurnFrame(p, F('turn.reasoning', { text: 'hmm' }), clock());
  applyTurnFrame(p, F('turn.delta', { text: 'x' }), clock());
  applyTurnFrame(p, F('turn.reasoning', { text: 'again' }), clock());
  assert.deepEqual(segTypes(p), ['thinking', 'markdown', 'thinking']);
  assert.equal(p.segments[0].status, 'done');
  assert.equal(p.segments[2].status, 'active');
});

test('turn.reasoning opens/extends the thinking segment; text accumulates unannounced', () => {
  const p = createTurnProjection('r2b');
  // A summary chunk can arrive without a preceding phase frame (e.g.
  // a mid-stretch watch) — the segment still opens.
  applyTurnFrame(p, F('turn.reasoning', { text: 'first ' }), clock());
  assert.equal(p.segments.length, 1);
  assert.equal(p.segments[0].type, 'thinking');
  assert.equal(p.segments[0].status, 'active');
  assert.equal(p.segments[0].text, 'first ');

  applyTurnFrame(p, F('turn.reasoning', { text: 'second' }), clock());
  assert.equal(p.segments.length, 1); // same active segment, not a new one
  assert.equal(p.segments[0].text, 'first second');
  assert.equal(p.segments[0].version, 2); // bumped once per append

  // Summary text is model activity — an unresolved tool settles.
  const q = createTurnProjection('r2c');
  applyTurnFrame(q, F('turn.tool_call', { callId: 't', name: 'read' }), clock());
  applyTurnFrame(q, F('turn.reasoning', { text: 'hmm' }), clock());
  assert.equal(tools(q)[0].status, 'settled');
  assert.equal(q.segments.at(-1).type, 'thinking');

  // Announcements: 'Thinking' once on open, never the chunk text.
  assert.deepEqual(p.announcements, ['Thinking']);
  assert.ok(!p.announcements.some((a) => a.includes('first') || a.includes('second')));
});

test('parallel tools settle independently; later step_finished(stop) settles both', () => {
  const p = createTurnProjection('r3');
  applyTurnFrame(p, F('turn.tool_call', { callId: 'a', name: 'read' }), clock());
  applyTurnFrame(p, F('turn.tool_call', { callId: 'b', name: 'grep' }), clock());
  applyTurnFrame(p, F('turn.step_finished', { finishReason: 'tool_calls' }), clock());
  assert.deepEqual(tools(p).map((t) => t.status), ['requested', 'requested']);
  applyTurnFrame(p, F('turn.tool_output', { callId: 'a' }), clock());
  assert.deepEqual(tools(p).map((t) => t.status), ['streaming', 'requested']);
  applyTurnFrame(p, F('turn.step_finished', { finishReason: 'stop' }), clock());
  assert.deepEqual(tools(p).map((t) => t.status), ['settled', 'settled']);
});

test('terminal failure settles tools without fabricating output markers', () => {
  const p = createTurnProjection('r4');
  applyTurnFrame(p, F('turn.tool_call', { callId: 'x', name: 'exec' }), clock());
  settleTurnProjection(p, clock());
  const t = tools(p)[0];
  assert.equal(t.status, 'settled');
  assert.equal(t.output, '');
  assert.equal(p.active, false);
});

test('announcements carry safe metadata only', () => {
  const p = createTurnProjection('r5');
  applyTurnFrame(p, F('turn.phase', { phase: 'reasoning' }), clock());
  applyTurnFrame(p, F('turn.delta', { text: 'hello ' }), clock());
  applyTurnFrame(p, F('turn.tool_call', { callId: 'c', name: 'read' }), clock());
  applyTurnFrame(p, F('turn.tool_input', { callId: 'c', text: '{"s":"secret"}' }), clock());
  applyTurnFrame(p, F('turn.tool_output', { callId: 'c', text: 'chunk-text-not-announced' }), clock());
  applyTurnFrame(p, F('turn.delta', { text: 'world' }), clock());
  settleTurnProjection(p, clock());
  // turn.phase is timing metadata — 'Thinking' announces only when
  // actual reasoning text opens a segment.
  assert.deepEqual(p.announcements, [
    'hello ', 'read requested', 'read output activity',
    'read settled', 'world',
  ]);
  // Chunk content and input never enter the announcement stream.
  assert.ok(!p.announcements.some((a) => a.includes('chunk-text-not-announced') || a.includes('secret')));
});

test('version bumps on mutation; frozen segments keep their version', () => {
  const p = createTurnProjection('r6');
  applyTurnFrame(p, F('turn.delta', { text: 'a' }), clock());
  const md = p.segments[0];
  assert.equal(md.version, 0);
  applyTurnFrame(p, F('turn.delta', { text: 'b' }), clock());
  assert.equal(md.version, 1);
  applyTurnFrame(p, F('turn.tool_call', { callId: 'c', name: 't' }), clock());
  applyTurnFrame(p, F('turn.delta', { text: 'c' }), clock());
  assert.equal(md.version, 1); // frozen — never reparsed
  assert.equal(p.segments[2].version, 0);
});

test('unknown frames and inactive projections are no-ops', () => {
  const p = createTurnProjection('r7');
  applyTurnFrame(p, F('turn.opened', {}), clock());
  assert.equal(p.segments.length, 0);
  settleTurnProjection(p, clock());
  applyTurnFrame(p, F('turn.delta', { text: 'late' }), clock());
  assert.equal(p.segments.length, 0);
});
