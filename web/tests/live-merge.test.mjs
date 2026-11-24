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
