// T-10 — the late-watch merge, projection level.
//
// Contract (findings-FE T-10): history is authoritative for the steps the
// turn finished; the journal supplies what history does not hold; a page that
// predates the turn keeps the previous answer. The merge seam is
// `liveHistory(items, live)` + `liveSegments(live, items)` in lib/turn.js.
//
// Until that seam lands these tests skip with the ticket name — a skip is not
// a pass, and `node --test` reports it as skipped. They activate by
// themselves the moment the functions are exported.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as turn from '../lib/turn.js';

const ready = typeof turn.liveHistory === 'function' && typeof turn.liveSegments === 'function';
const pending = ready ? {} : { skip: 'pending: liveHistory/liveSegments (T-10, FE round 1)' };

const item = (id, kind, extra = {}) => ({ id, kind, ...extra });
const history = [
  item('m1', 'user', { msg: { content: 'first ask' } }),
  item('m2', 'text', { text: 'First answer.' }),
  item('m3', 'user', { msg: { content: 'second ask' } }),
  item('m4', 'text', { text: 'Step one.' }),
  item('m5', 'tool', { callId: 'c1', name: 'read_file' }),
];
const live = {
  segments: [
    item('t:1', 'text', { text: 'Step one.' }),
    item('t:2', 'tool', { callId: 'c1', name: 'read_file' }),
    item('t:3', 'text', { text: 'Step two streams here.' }),
  ],
};

test('a plain send trims nothing: history is authoritative for every finished step', pending, () => {
  const kept = turn.liveHistory(history, { kind: 'send' });
  assert.deepEqual(kept.map((i) => i.id), ['m1', 'm2', 'm3', 'm4', 'm5']);
});

test('an edit replaces its target: history stops ahead of the edited message', pending, () => {
  const kept = turn.liveHistory(history, { kind: 'edit', editOf: 'm3' });
  assert.deepEqual(kept.map((i) => i.id), ['m1', 'm2']);
});

test('a resume this tab sent re-answers its prompt: history stops at that prompt', pending, () => {
  const kept = turn.liveHistory(history, { kind: 'resume' });
  assert.deepEqual(kept.map((i) => i.id), ['m1', 'm2', 'm3']);
});

test('a resume adopted from elsewhere cannot identify its prompt, so it trims nothing', pending, () => {
  const kept = turn.liveHistory(history, { kind: 'resume', adopted: true });
  assert.deepEqual(kept.map((i) => i.id), ['m1', 'm2', 'm3', 'm4', 'm5']);
});

test('the replayed prefix history already holds is not drawn again', pending, () => {
  const tail = turn.liveSegments(live, history);
  assert.deepEqual(tail.map((i) => i.id), ['t:3'], 'the stored step is drawn from history, the rest from the journal');
  assert.equal(tail[0].text, 'Step two streams here.');
});

test('nothing of this turn in history: the whole live tail is drawn', pending, () => {
  const tail = turn.liveSegments(live, [item('m1', 'user', { msg: { content: 'first ask' } })]);
  assert.deepEqual(tail.map((i) => i.id), ['t:1', 't:2', 't:3']);
});

test('a live tail that history does not fully hold keeps its own opening text', pending, () => {
  const partial = [item('m1', 'user'), item('m4', 'tool', { callId: 'c1' })];
  const tail = turn.liveSegments(live, partial);
  assert.deepEqual(tail.map((i) => i.id), ['t:1', 't:3'], 'only the replayed call history knows is dropped');
});

test('an empty live turn draws nothing', pending, () => {
  assert.deepEqual(turn.liveSegments(null, history), []);
});

// ---------- edges the stage above does not name ----------

test('a resync marker inside the dropped range is kept', () => {
  const marked = {
    segments: [
      item('t:0', 'resync', { text: 'earlier output was trimmed' }),
      item('t:1', 'text', { text: 'Step one.' }),
      item('t:2', 'tool', { callId: 'c1', name: 'read_file' }),
      item('t:3', 'text', { text: 'Step two streams here.' }),
    ],
  };
  const tail = turn.liveSegments(marked, history);
  assert.deepEqual(tail.map((i) => i.id), ['t:0', 't:3'],
    'the marker is the only evidence output is missing, so the cut keeps it');
});

test('every replayed row the record identifies is dropped, not just the last', () => {
  const twoSteps = [
    item('m1', 'user'),
    item('m2', 'text', { text: 'One.' }),
    item('m3', 'tool', { callId: 'c1' }),
    item('m4', 'text', { text: 'Two.' }),
    item('m5', 'tool', { callId: 'c2' }),
  ];
  const replay = {
    segments: [
      item('t:1', 'text', { text: 'One.' }),
      item('t:2', 'tool', { callId: 'c1' }),
      item('t:3', 'text', { text: 'Two.' }),
      item('t:4', 'tool', { callId: 'c2' }),
      item('t:5', 'text', { text: 'Three streams.' }),
    ],
  };
  assert.deepEqual(turn.liveSegments(replay, twoSteps).map((i) => i.id), ['t:5'],
    'both committed steps come from the record; only the running one is live');
});

test('a record with no tool row at all cuts nothing', () => {
  const tail = turn.liveSegments(live, [item('m1', 'user'), item('m2', 'text', { text: 'First answer.' })]);
  assert.deepEqual(tail.map((i) => i.id), ['t:1', 't:2', 't:3'], 'no identity, no cut');
});

// ---------- the steer path (T-18: the row carries its inputId) ----------

const steered = [
  item('m1', 'user', { msg: { content: 'first ask' } }),
  item('m2', 'text', { text: 'Step one.' }),
  item('m3', 'tool', { callId: 'c1' }),
  item('m4', 'user', { msg: { content: 'also check X' }, inputId: 'i1' }),
];
const withSteer = (suffix) => ({
  segments: [
    item('t:1', 'text', { text: 'Step one.' }),
    item('t:2', 'tool', { callId: 'c1' }),
    item('t:3', 'steer', { inputId: 'i1', text: 'also check X' }),
    item('t:4', 'text', { text: 'Step two streams.' + suffix }),
  ],
});

test('a replayed steer the record holds is drawn once, from history', () => {
  const tail = turn.liveSegments(withSteer(''), steered);
  assert.deepEqual(tail.map((i) => i.id), ['t:4'], 'the record row is the copy the reader sees');
});

test('a steer the record does not hold stays in the live tail', () => {
  const notStored = [item('m1', 'user', { msg: { content: 'first ask' } })];
  const tail = turn.liveSegments(withSteer(''), notStored);
  assert.deepEqual(tail.map((i) => i.id), ['t:1', 't:2', 't:3', 't:4'], 'no record, no cut — and no identity, no drop');
});

test('a row without inputId never matches a steer, even with the same text', () => {
  const byText = [
    item('m1', 'user', { msg: { content: 'first ask' } }),
    item('m2', 'tool', { callId: 'c1' }),
    item('m3', 'user', { msg: { content: 'also check X' } }),
  ];
  const tail = turn.liveSegments(withSteer(''), byText);
  assert.deepEqual(tail.map((i) => i.id), ['t:1', 't:3', 't:4'],
    'the text is identical and it is still not identity: only the tool row is cut');
});

test('a mismatched inputId keeps the bubble', () => {
  const other = [
    item('m1', 'user', { msg: { content: 'first ask' } }),
    item('m2', 'tool', { callId: 'c1' }),
    item('m3', 'user', { msg: { content: 'other input' }, inputId: 'i2' }),
  ];
  assert.deepEqual(turn.liveSegments(withSteer(''), other).map((i) => i.id), ['t:1', 't:3', 't:4']);
});

test('a steer row is matched even when the record holds no tool row yet', () => {
  // A fold commits the row before the next provider call, so a turn can hold a
  // steer and no finished step. The steer is still drawn once.
  const onlySteer = [item('m1', 'user', { msg: { content: 'first ask' } }), item('m2', 'user', { msg: { content: 'also check X' }, inputId: 'i1' })];
  const replay = {
    segments: [
      item('t:1', 'steer', { inputId: 'i1', text: 'also check X' }),
      item('t:2', 'text', { text: 'the answer so far' }),
    ],
  };
  assert.deepEqual(turn.liveSegments(replay, onlySteer).map((i) => i.id), ['t:2']);
});
