// T-32 — a tool call's outcome is data (D-12, decisions.md:1068-1207).
//
// T-32 landed with the producer: `toolCallDTO.status` and the
// `turn.tool_result` frame carry the verdict, and the text heuristics
// (`toolFailed`) are gone. These tests read fields only, so T-33 (the wire
// value `"signal: killed"` → `"killed"`) cannot turn them red.
//
// The rules they lock (PA decisions, T-32):
//   · the item carries `outcome` (+ `exit`); `status` stays the settle state,
//     so one row can be settled AND failed at once;
//   · a verdict comes from the field only — text never decides, in either
//     direction (`(exit 2)` in the body does not make a call failed, and a
//     failed call needs no marker in its text);
//   · `pending` (no output yet) and `unknown` (older record, no verdict) are
//     both neutral: no failed badge, not counted as failed.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import './register.mjs';

const turn = await import('../lib/turn.js');

// One stored assistant message with the tool calls given; the message carries
// output, so the derived row is settled whatever the verdict says.
const storedTurn = (calls) => [{
  id: 'a1', role: 'assistant', content: '',
  parts: calls.map((c) => ({ kind: 'tool_call', toolCallId: c.id })),
  toolCalls: calls.map((c) => ({ name: 'exec', input: '{}', output: '', ...c })),
}];

const tools = (items) => items.filter((x) => x.kind === 'tool');
const failedCount = (items) => tools(items).filter((x) => x.outcome === 'failed').length;

test('T-32: a stored call maps toolCallDTO.status to the item outcome and keeps the settle state separate', () => {
  const [row] = tools(turn.historyItems(storedTurn([
    { id: 't1', output: 'boom', status: 'failed', exit: { code: 3 } },
    { id: 't2', output: 'fine', status: 'ok', exit: { code: 0 } },
  ])));
  assert.equal(row.status, 'settled', 'a row with output is settled — that is the UI state');
  assert.equal(row.outcome, 'failed', 'the verdict is its own fact');
  assert.deepEqual(row.exit, { code: 3 }, 'the exit status travels with it');
});

test('T-32: a failed call whose text carries no marker at all still reads as failed', () => {
  const items = turn.historyItems(storedTurn([
    { id: 't1', output: 'Process exited with code 3', status: 'failed' },
  ]));
  assert.equal(tools(items)[0].outcome, 'failed', 'no regex needed: the field is the verdict');
  assert.equal(failedCount(items), 1);
});

test('T-32: text never decides — an ok call whose output ends with (exit 2) stays ok', () => {
  const items = turn.historyItems(storedTurn([
    { id: 't1', output: 'printed a literal\n(exit 2)', status: 'ok' },
  ]));
  assert.equal(tools(items)[0].outcome, 'ok');
  assert.equal(failedCount(items), 0, 'the marker in the body is evidence, not a verdict');
});

test('T-32: a record written before the field exists is unknown, and its text is left alone', () => {
  const items = turn.historyItems(storedTurn([
    { id: 't1', output: 'ran\n(exit 2)' },
    { id: 't2', output: 'error: nope' },
  ]));
  assert.deepEqual(tools(items).map((x) => x.outcome), ['unknown', 'unknown'], 'no heuristic replaces the missing field');
  assert.match(tools(items)[0].output, /\(exit 2\)\s*$/, 'the model-facing text is untouched');
  assert.equal(failedCount(items), 0, 'an unknown verdict draws no failure');
});

test('T-32: pending and unknown are equally neutral', () => {
  const items = turn.historyItems(storedTurn([
    { id: 't1', output: '', status: 'pending' },
    { id: 't2', output: 'done', status: 'unknown' },
  ]));
  assert.deepEqual(tools(items).map((x) => x.outcome), ['pending', 'unknown']);
  assert.equal(tools(items)[0].status, 'pending', 'no output yet: the row is not settled');
  assert.equal(failedCount(items), 0);
});

test('T-32: canceled is neutral in the count but distinct in the item', () => {
  const items = turn.historyItems(storedTurn([
    { id: 't1', output: 'interrupted by user', status: 'canceled' },
  ]));
  assert.equal(tools(items)[0].outcome, 'canceled');
  assert.equal(failedCount(items), 0, 'a stopped call is not a failure the reader should count');
});

test('T-32: the exit label shows the recorded value as-is, and adds no prefix of its own', () => {
  assert.equal(turn.exitText({ code: 0 }), 'Exit code 0');
  assert.equal(turn.exitText({ code: 3 }), 'Exit code 3');
  // The signal is the server's name, printed unchanged: T-33 changes the wire
  // value from "signal: killed" to "killed", and neither spelling gains a label.
  assert.equal(turn.exitText({ signal: 'killed' }), 'killed');
  assert.equal(turn.exitText({ code: 0, signal: 'killed' }), 'killed');
  assert.equal(turn.exitText(undefined), '');
});

test('T-32: turn.tool_result sets the live verdict without settling the row', () => {
  const live = turn.newTurn({ turnId: 't1', conversationId: 'c1', kind: 'send' });
  turn.applyFrame(live, { type: 'turn.tool_call', seq: 1, payload: { callId: 'call_1', name: 'exec' } });
  turn.applyFrame(live, { type: 'turn.tool_output', seq: 2, payload: { callId: 'call_1', text: 'Process exited with code 3' } });
  assert.equal(live.segments[0].status, 'streaming', 'output is still streaming');

  assert.equal(turn.applyFrame(live, { type: 'turn.tool_result', seq: 3, payload: { callId: 'call_1', status: 'failed', exit: { code: 3 } } }), true);
  assert.equal(live.segments[0].outcome, 'failed', 'the frame carries the call-level verdict');
  assert.deepEqual(live.segments[0].exit, { code: 3 });
  assert.equal(live.segments[0].status, 'streaming', 'the verdict does not settle the row; activity or the terminal does');
});

test('T-32: a turn.tool_result is matched by callId, never by position', () => {
  const live = turn.newTurn({ turnId: 't1', conversationId: 'c1', kind: 'send' });
  turn.applyFrame(live, { type: 'turn.tool_call', seq: 1, payload: { callId: 'call_1', name: 'exec' } });
  turn.applyFrame(live, { type: 'turn.tool_call', seq: 2, payload: { callId: 'call_2', name: 'exec' } });
  turn.applyFrame(live, { type: 'turn.tool_result', seq: 3, payload: { callId: 'call_2', status: 'failed' } });
  const [first, second] = live.segments;
  assert.equal(first.outcome, undefined, 'the first row is not touched by its neighbour’s verdict');
  assert.equal(second.outcome, 'failed', 'the verdict finds its own call');
});
