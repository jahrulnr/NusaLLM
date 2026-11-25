// Projection tests for lib/turn.js — the one place that turns websocket
// frames and persisted messages into the transcript's item list.
//
// The T-08 and T-09 cases started as characterizations of the pre-fix
// projection; they now pin the rule that replaced it (a retried attempt's tail
// is dropped, neighbouring text is one Markdown document). Keep them named so
// the contract stays greppable.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { newTurn, applyFrame, historyItems, joinText, group, toolSummary } from '../lib/turn.js';

const turn = () => newTurn({ turnId: 't1', requestId: 'r1', conversationId: 'c1', kind: 'send' });
const frame = (type, payload, seq) => ({ type, payload, seq });
const kinds = (items) => items.map((i) => i.kind);

// ---------- applyFrame: the live turn ----------

test('consecutive deltas extend one text segment', () => {
  const t = turn();
  applyFrame(t, frame('turn.delta', { text: 'Hel' }, 0));
  applyFrame(t, frame('turn.delta', { text: 'lo' }, 1));
  assert.equal(t.segments.length, 1);
  assert.deepEqual([t.segments[0].kind, t.segments[0].text], ['text', 'Hello']);
  assert.equal(t.lastSeq, 1);
});

test('a tool call closes the text before it and a later delta starts a new one', () => {
  const t = turn();
  applyFrame(t, frame('turn.delta', { text: 'before' }, 0));
  applyFrame(t, frame('turn.tool_call', { callId: 'a', name: 'read_file' }, 1));
  applyFrame(t, frame('turn.delta', { text: 'after' }, 2));
  assert.deepEqual(t.segments.map((s) => [s.kind, s.text ?? s.name]), [
    ['text', 'before'],
    ['tool', 'read_file'],
    ['text', 'after'],
  ]);
  // The model resuming settles the tool row even before its own verdict.
  assert.deepEqual([t.segments[1].status, t.segments[1].settled], ['settled', 'Model activity resumed']);
});

test('a replayed or doubled seq is dropped and changes nothing', () => {
  const t = turn();
  assert.equal(applyFrame(t, frame('turn.delta', { text: 'a' }, 0)), true);
  assert.equal(applyFrame(t, frame('turn.delta', { text: 'a' }, 0)), false);
  assert.equal(applyFrame(t, frame('turn.delta', { text: 'b' }, 0)), false);
  assert.equal(t.segments[0].text, 'a');
  assert.equal(t.lastSeq, 0);
});

test('a frame without a seq is applied verbatim', () => {
  const t = turn();
  applyFrame(t, { type: 'turn.delta', payload: { text: 'x' } });
  applyFrame(t, { type: 'turn.delta', payload: { text: 'y' } });
  assert.equal(t.segments[0].text, 'xy');
  assert.equal(t.lastSeq, -1);
});

test('a tool streams its input and output into the segment it named', () => {
  const t = turn();
  applyFrame(t, frame('turn.tool_call', { callId: 'a', name: 'exec' }, 0));
  applyFrame(t, frame('turn.tool_input', { callId: 'a', text: '{"command":' }, 1));
  applyFrame(t, frame('turn.tool_input', { callId: 'a', text: '"ls"}' }, 2));
  applyFrame(t, frame('turn.tool_output', { callId: 'a', text: 'ok' }, 3));
  assert.equal(t.segments[0].input, '{"command":"ls"}');
  assert.equal(t.segments[0].output, 'ok');
  assert.equal(t.segments[0].status, 'streaming');
  // A frame naming no known call is dropped, not a new row.
  applyFrame(t, frame('turn.tool_output', { callId: 'ghost', text: 'x' }, 4));
  assert.equal(t.segments.length, 1);
});

test('a phase is a pause the next activity clears', () => {
  const t = turn();
  applyFrame(t, frame('turn.phase', { phase: 'retry_wait', retryAfterMs: 2000 }, 0));
  assert.deepEqual([t.phase.phase, t.phase.retryAfterMs], ['retry_wait', 2000]);
  assert.ok(t.phase.at > 0, 'the status line counts down from a timestamp');
  applyFrame(t, frame('turn.delta', { text: 'go' }, 1));
  assert.equal(t.phase, null);
});

test('a reasoning phase survives reasoning frames and ends with the first delta', () => {
  const t = turn();
  applyFrame(t, frame('turn.phase', { phase: 'reasoning' }, 0));
  applyFrame(t, frame('turn.reasoning', { text: 'think' }, 1));
  assert.equal(t.phase.phase, 'reasoning', 'reasoning frames are the phase, not activity after it');
  applyFrame(t, frame('turn.delta', { text: 'answer' }, 2));
  assert.equal(t.phase, null);
});

test('a retry pause drops what the interrupted attempt streamed', () => {
  const t = turn();
  applyFrame(t, frame('turn.delta', { text: 'half' }, 0));
  applyFrame(t, frame('turn.phase', { phase: 'retry_wait', retryAfterMs: 500 }, 1));
  assert.deepEqual(t.segments, [], 'no step has committed, so the store cannot hold this attempt');
  assert.equal(t.phase.phase, 'retry_wait', 'the status line still counts the pause');
  applyFrame(t, frame('turn.delta', { text: 'the answer' }, 2));
  assert.deepEqual(t.segments.map((s) => [s.kind, s.text]), [['text', 'the answer']],
    'the retry starts a fresh segment instead of appending to the dropped text');
});

test('steering folds chunk by chunk until its final chunk', () => {
  const t = turn();
  applyFrame(t, frame('turn.tool_call', { callId: 'a', name: 'exec' }, 0));
  applyFrame(t, frame('turn.steer', { inputId: 'i1', text: 'use ' }, 1));
  applyFrame(t, frame('turn.steer', { inputId: 'i1', text: 'go', final: true }, 2));
  applyFrame(t, frame('turn.steer', { inputId: 'i2', text: 'next', final: true }, 3));
  assert.deepEqual(t.segments.map((s) => [s.kind, s.text]), [
    ['tool', undefined],
    ['steer', 'use go'],
    ['steer', 'next'],
  ]);
  assert.equal(t.segments[1].final, true);
  // A steer is user activity: the tool row before it is settled.
  assert.equal(t.segments[0].status, 'settled');
  assert.equal(t.segments[0].settled, 'Model activity resumed');
});

test('a step reports usage and settles tools unless the model asked for more', () => {
  const usage = { inputTokens: 10, outputTokens: 2 };
  const t = turn();
  applyFrame(t, frame('turn.tool_call', { callId: 'a', name: 'exec' }, 0));
  applyFrame(t, frame('turn.step_finished', { finishReason: 'tool_calls' }, 1));
  assert.deepEqual([t.segments[0].status, t.usage], ['requested', null]);
  applyFrame(t, frame('turn.step_finished', { finishReason: 'stop', usage }, 2));
  assert.equal(t.usage, usage);
  assert.equal(t.segments[0].status, 'settled');
});

test('a terminal frame settles what is open and records itself', () => {
  for (const type of ['turn.completed', 'turn.failed']) {
    const t = turn();
    applyFrame(t, frame('turn.phase', { phase: 'retry_wait', retryAfterMs: 1 }, 0));
    applyFrame(t, frame('turn.tool_call', { callId: 'a', name: 'exec' }, 1));
    applyFrame(t, frame(type, { status: 500 }, 2));
    assert.equal(t.segments[0].status, 'settled');
    assert.equal(t.terminal.type, type);
    assert.equal(t.phase, null, 'a finished turn shows no countdown');
  }
});

// T-09: a retried call persisted nothing, so the attempt's streamed text, its
// reasoning and any tool row without a result belong to the attempt that
// failed. The reset stops at the last committed boundary — the last
// turn.step_finished or folded steer — because that is what the store holds.
test('T-09: a retried attempt leaves no tail behind', () => {
  const t = turn();
  applyFrame(t, frame('turn.delta', { text: 'first ' }, 0));
  applyFrame(t, frame('turn.reasoning', { text: 'thinking' }, 1));
  applyFrame(t, frame('turn.tool_call', { callId: 'a', name: 'exec' }, 2));
  applyFrame(t, frame('turn.phase', { phase: 'retry_wait', retryAfterMs: 1000 }, 3));
  assert.deepEqual(t.segments, [], 'the abandoned text, reasoning and never-answered row go');
  applyFrame(t, frame('turn.delta', { text: 'second' }, 4));
  assert.deepEqual(t.segments.map((s) => [s.kind, s.text]), [['text', 'second']],
    'the retry does not splice into the abandoned sentence');
});

test('T-09: a retry keeps the finished step it continues from', () => {
  const t = turn();
  applyFrame(t, frame('turn.delta', { text: 'step one' }, 0));
  applyFrame(t, frame('turn.tool_call', { callId: 'a', name: 'exec' }, 1));
  applyFrame(t, frame('turn.tool_output', { callId: 'a', text: 'ok' }, 2));
  applyFrame(t, frame('turn.step_finished', { finishReason: 'tool_calls' }, 3));
  applyFrame(t, frame('turn.delta', { text: 'abandoned ' }, 4));
  applyFrame(t, frame('turn.phase', { phase: 'retry_wait', retryAfterMs: 1000 }, 5));
  assert.deepEqual(t.segments.map((s) => [s.kind, s.text ?? s.output]), [
    ['text', 'step one'],
    ['tool', 'ok'],
  ], 'the committed step and its result stay on screen; only the attempt after it goes');
});

test('T-09: a folded steer survives a retry in the next step', () => {
  const t = turn();
  applyFrame(t, frame('turn.delta', { text: 'step one' }, 0));
  applyFrame(t, frame('turn.step_finished', { finishReason: 'tool_calls' }, 1));
  applyFrame(t, frame('turn.steer', { inputId: 'i1', text: 'also check X', final: true }, 2));
  applyFrame(t, frame('turn.delta', { text: 'abandoned' }, 3));
  applyFrame(t, frame('turn.phase', { phase: 'retry_wait', retryAfterMs: 1 }, 4));
  assert.deepEqual(t.segments.map((s) => s.kind), ['text', 'steer'],
    'a folded steer is committed history, not part of the attempt that failed');
});

test('T-09: a retry keeps the usage a finished step reported', () => {
  const usage = { inputTokens: 7, outputTokens: 1 };
  const t = turn();
  applyFrame(t, frame('turn.step_finished', { finishReason: 'tool_calls', usage }, 0));
  applyFrame(t, frame('turn.delta', { text: 'x' }, 1));
  applyFrame(t, frame('turn.phase', { phase: 'retry_wait', retryAfterMs: 1 }, 2));
  assert.deepEqual(t.usage, usage, 'accounting belongs to the step, not the attempt');
});

test('T-09: a replayed journal rebuilds the reset boundary', () => {
  // A tab that attaches mid-turn replays the turn from seq 0. The boundary is
  // state the tab never had, so it has to come back from the frames — a cut
  // one step too far would erase work the store already holds.
  const t = turn();
  const journal = [
    frame('turn.opened', {}, 0),
    frame('turn.delta', { text: 'step one' }, 1),
    frame('turn.tool_call', { callId: 'a', name: 'exec' }, 2),
    frame('turn.step_finished', { finishReason: 'tool_calls' }, 3),
    frame('turn.steer', { inputId: 'i1', text: 'check X', final: true }, 4),
    frame('turn.delta', { text: 'attempt two ' }, 5),
    frame('turn.tool_call', { callId: 'b', name: 'read_file' }, 6),
    frame('turn.phase', { phase: 'retry_wait', retryAfterMs: 1000 }, 7),
  ];
  for (const f of journal) applyFrame(t, f);
  assert.deepEqual(t.segments.map((s) => s.kind), ['text', 'tool', 'steer'],
    'the replayed step and steer are the boundary; the failed attempt after them goes');
  applyFrame(t, frame('turn.delta', { text: 'the answer' }, 8));
  assert.deepEqual(t.segments.map((s) => [s.kind, s.text]), [
    ['text', 'step one'],
    ['tool', undefined],
    ['steer', 'check X'],
    ['text', 'the answer'],
  ]);
});

test('T-09: a second retry in the same step does not cut past the record', () => {
  const t = turn();
  applyFrame(t, frame('turn.step_finished', { finishReason: 'tool_calls' }, 0));
  applyFrame(t, frame('turn.delta', { text: 'attempt a' }, 1));
  applyFrame(t, frame('turn.phase', { phase: 'retry_wait', retryAfterMs: 1 }, 2));
  applyFrame(t, frame('turn.delta', { text: 'attempt b' }, 3));
  applyFrame(t, frame('turn.phase', { phase: 'retry_wait', retryAfterMs: 1 }, 4));
  assert.deepEqual(t.segments, [], 'each attempt is dropped back to the same committed boundary');
  assert.equal(t.persistedMark, 0);
});

test('T-09: a replay that starts mid-attempt cuts the attempt, not the record', () => {
  // afterSeq past the last step: only the attempt's own frames are replayed,
  // so the tab holds no committed segment of this turn and everything goes.
  const t = turn();
  applyFrame(t, frame('turn.delta', { text: 'attempt only' }, 9));
  applyFrame(t, frame('turn.phase', { phase: 'retry_wait', retryAfterMs: 1 }, 10));
  assert.deepEqual(t.segments, []);
});

// ---------- historyItems: the stored turn ----------

test('history flattens messages into the segment shapes the live path uses', () => {
  const items = historyItems([
    { id: 'm1', role: 'user', content: 'hi' },
    { id: 'm2', role: 'user', userType: 'system', content: 'note' },
    {
      id: 'm3', role: 'assistant', finishReason: 'interrupted',
      parts: [
        { kind: 'reasoning', text: 'think' },
        { kind: 'tool_call', toolCallId: 'c1' },
        { kind: 'text', text: 'answer' },
      ],
      toolCalls: [{ id: 'c1', name: 'exec', input: '{}', output: 'ok', status: 'ok' }],
    },
  ]);
  assert.deepEqual(kinds(items), ['user', 'note', 'reasoning', 'tool', 'text', 'interrupted']);
  assert.equal(items[0].msg.content, 'hi');
  assert.equal(items[2].done, true);
  assert.equal(items[3].status, 'settled');
  assert.equal(items[3].stored, true);
  assert.equal(items[3].id, 'm3:c1');
});

test('history orders text and tool rows by the parts, not by the tool list', () => {
  const items = historyItems([{
    id: 'm', role: 'assistant', content: 'fallback content',
    parts: [{ kind: 'text', text: 'A' }, { kind: 'tool_call', toolCallId: 'x' }, { kind: 'text', text: 'B' }],
    toolCalls: [{ id: 'x', name: 'exec', input: '{}', output: 'ok', status: 'ok' }],
  }]);
  assert.deepEqual(items.map((i) => i.text ?? i.name), ['A', 'exec', 'B']);
});

test('a message without parts falls back to content followed by its calls', () => {
  const items = historyItems([{
    id: 'm', role: 'assistant', content: 'text',
    // The DTO omits `output` (a pointer) when the call recorded none.
    toolCalls: [{ id: 'c', name: 'ls', input: '{}' }],
  }]);
  assert.deepEqual(kinds(items), ['text', 'tool']);
  assert.equal(items[1].status, 'pending', 'a call with no result is not settled');
  assert.equal(items[1].outcome, 'unknown', 'and it carries no verdict');
});

test('the workspace injection is a cwd, never a row', () => {
  const items = historyItems([{
    id: 'm', role: 'assistant',
    parts: [{ kind: 'tool_call', toolCallId: 'w' }, { kind: 'tool_call', toolCallId: 'c' }],
    toolCalls: [
      { id: 'w', type: 'injection', name: 'workspace', input: '{"path":"/proj"}', output: '' },
      { id: 'c', name: 'exec', input: '{}', output: '', status: 'pending' },
    ],
  }]);
  assert.equal(items.length, 1);
  assert.equal(items[0].kind, 'tool');
  assert.equal(items[0].cwd, '/proj');
  // A malformed injection keeps the cwd it had instead of throwing.
  const kept = historyItems([{
    id: 'm', role: 'assistant',
    parts: [{ kind: 'tool_call', toolCallId: 'w' }, { kind: 'tool_call', toolCallId: 'c' }],
    toolCalls: [
      { id: 'w', type: 'injection', name: 'workspace', input: 'not json' },
      { id: 'c', name: 'exec', input: '{}', output: '', status: 'pending' },
    ],
  }], '/elsewhere');
  assert.equal(kept[0].cwd, '/elsewhere');
});

test('a part naming a missing tool call is skipped, and empty text is no row', () => {
  const items = historyItems([{
    id: 'm', role: 'assistant',
    parts: [
      { kind: 'text', text: '' },
      { kind: 'tool_call', toolCallId: 'missing' },
      { kind: 'tool_call', toolCallId: 'c' },
    ],
    toolCalls: [{ id: 'c', name: 'exec', input: '{}', status: 'pending' }],
  }]);
  assert.deepEqual(kinds(items), ['tool']);
});

// T-08: a text part is a persistence boundary, not a presentation one. The
// projection joins neighbouring text before grouping, so one answer renders as
// one Markdown document; a real event between two texts still separates them.
test('T-08: neighbouring text parts render as one Markdown block', () => {
  const items = historyItems([{
    id: 'm', role: 'assistant',
    parts: [
      { kind: 'text', text: 'see this:\n\n```js\nconst a = 1;\n' },
      { kind: 'text', text: 'const b = 2;\n```\n\ndone' },
    ],
  }]);
  assert.deepEqual(kinds(items), ['text', 'text'], 'the store still reports two parts');
  const joined = joinText(items);
  assert.deepEqual(kinds(joined), ['text'], 'one answer, one Markdown document');
  assert.equal(joined[0].text, 'see this:\n\n```js\nconst a = 1;\nconst b = 2;\n```\n\ndone',
    'parts are slices of one stream, so the join adds no separator of its own');
  assert.equal(joined[0].id, 'm:0', 'the first part keys the joined item, so its DOM survives the join');
});

test('T-08: a real event between two texts keeps them apart', () => {
  const around = (kind) => joinText([
    { id: 'a', kind: 'text', text: 'one ' },
    { id: 'b', kind },
    { id: 'c', kind: 'text', text: 'two' },
  ]);
  // Every kind but text is an event the reader must see. `note` is the
  // userType:"system" record, `interrupted` the cut-answer marker, `steer` the
  // owner's mid-turn input, `activity` a folded reasoning/tool run.
  for (const kind of ['reasoning', 'tool', 'steer', 'note', 'interrupted', 'user', 'activity', 'plan', 'media', 'approval', 'elicitation']) {
    const out = around(kind);
    assert.deepEqual(kinds(out), ['text', kind, 'text'], `${kind} is a boundary`);
    assert.equal(out[0].text, 'one ', `${kind} leaves the first text alone`);
    assert.equal(out[2].text, 'two', `${kind} leaves the second text alone`);
  }
  // A note reaches the projection as a user-role message with userType system;
  // it must split an answer just like a steer does.
  const noted = joinText(historyItems([
    { id: 'm1', role: 'assistant', parts: [{ kind: 'text', text: 'before' }] },
    { id: 'n1', role: 'user', userType: 'system', content: 'context fixed' },
    { id: 'm2', role: 'assistant', parts: [{ kind: 'text', text: 'after' }] },
  ]));
  assert.deepEqual(kinds(noted), ['text', 'note', 'text']);
  // The live path respects the same rules: a tool the model called is not text.
  const live = turn();
  applyFrame(live, frame('turn.delta', { text: 'see this:' }, 0));
  applyFrame(live, frame('turn.tool_call', { callId: 'a', name: 'exec' }, 1));
  applyFrame(live, frame('turn.delta', { text: 'done' }, 2));
  assert.deepEqual(kinds(joinText(live.segments)), ['text', 'tool', 'text']);
});

// Two assistant messages can be neighbours when the record between them draws
// no row (a system-role tool-context record). The prose is continuous, so the
// projection joins it — and the joined block keeps the first item's id, which
// is only a render key. A real boundary between two messages still splits them.
test('T-08: two messages with no drawable record between them read as one answer', () => {
  const items = historyItems([
    { id: 'm1', role: 'assistant', parts: [{ kind: 'text', text: 'The cache has one ' }] },
    { id: 's1', role: 'system', content: 'workspace: /w' },
    { id: 'm2', role: 'assistant', parts: [{ kind: 'text', text: 'write path, and it is wrong.' }] },
  ]);
  assert.deepEqual(items.map((i) => [i.id, i.kind]), [['m1:0', 'text'], ['m2:0', 'text']]);
  const joined = joinText(items);
  assert.deepEqual(kinds(joined), ['text']);
  assert.equal(joined[0].id, 'm1:0', 'the block is keyed by its first item');
  assert.equal(joined[0].text, 'The cache has one write path, and it is wrong.');
});

test('T-08: an interrupted reply and its continuation stay apart', () => {
  const joined = joinText(historyItems([
    { id: 'm1', role: 'assistant', finishReason: 'interrupted', parts: [{ kind: 'text', text: 'half an answer' }] },
    { id: 'm2', role: 'assistant', parts: [{ kind: 'text', text: 'the continuation' }] },
  ]));
  assert.deepEqual(kinds(joined), ['text', 'interrupted', 'text'], 'the marker is what says the answer was cut');
  assert.deepEqual(joined.map((i) => i.text ?? i.kind), ['half an answer', 'interrupted', 'the continuation']);
});

test('T-08: an interrupted reply joins its text and keeps its marker', () => {
  // interruptedReply drops the tool-call part, so the two halves of the
  // answer sit next to each other in the record; the marker follows both.
  const items = joinText(historyItems([{
    id: 'm', role: 'assistant', finishReason: 'interrupted',
    parts: [{ kind: 'text', text: 'Here is the fix.' }, { kind: 'text', text: 'The cause is invalidate().' }],
  }]));
  assert.deepEqual(kinds(items), ['text', 'interrupted']);
  assert.equal(items[0].text, 'Here is the fix.The cause is invalidate().');
});

// ---------- group ----------

test('group folds consecutive reasoning and tool items into one activity item', () => {
  const out = group([
    { id: 'a', kind: 'text', text: 'one' },
    { id: 'b', kind: 'reasoning', text: 'hmm' },
    { id: 'c', kind: 'tool' },
    { id: 'd', kind: 'text', text: 'two' },
    { id: 'e', kind: 'tool' },
  ]);
  assert.deepEqual(kinds(out), ['text', 'activity', 'text', 'activity']);
  assert.deepEqual(out[1].items.map((i) => i.id), ['b', 'c']);
  assert.equal(out[1].id, 'g:b', 'the first folded item keys the group');
  assert.equal(out[3].items.length, 1);
});

test('group keeps a user, note, steer or interrupted item as its own row', () => {
  const out = group([
    { id: 'u', kind: 'user' },
    { id: 't', kind: 'tool' },
    { id: 's', kind: 'steer' },
    { id: 'n', kind: 'note' },
    { id: 'i', kind: 'interrupted' },
  ]);
  assert.deepEqual(kinds(out), ['user', 'activity', 'steer', 'note', 'interrupted']);
});

test('group of an empty list is empty, and a lone activity closes at the end', () => {
  assert.deepEqual(group([]), []);
  assert.deepEqual(kinds(group([{ id: 'r', kind: 'reasoning' }])), ['activity']);
});

// ---------- the two small readers ----------

test('toolSummary names the action and its subject, and survives partial JSON', () => {
  assert.deepEqual(toolSummary('exec', '{"command":"ls -la\\nmore"}'), { verb: 'Ran', detail: 'ls -la' });
  assert.deepEqual(toolSummary('read_file', '{"path":"a.go"}'), { verb: 'Read', detail: 'a.go' });
  assert.deepEqual(toolSummary('web_search', '{"query":"x"}'), { verb: 'Searched web', detail: 'x' });
  assert.deepEqual(toolSummary('exec', '{"comm'), { verb: 'Ran', detail: '' });
  assert.deepEqual(toolSummary('custom_tool', '{"thing":"v"}'), { verb: 'custom_tool', detail: 'v' });
});

test('a stored steer row carries its inputId, a plain row does not', () => {
  // D-06.1/T-18: the fold's identity is the only thing the merge may match a
  // live steer bubble against — never its text.
  const items = historyItems([
    { id: 'u1', role: 'user', content: 'plain', createdAt: '' },
    { id: 'u2', role: 'user', content: 'steered', inputId: 'req-7', createdAt: '' },
  ]);
  assert.deepEqual(items.map((i) => [i.kind, i.inputId]), [['user', ''], ['user', 'req-7']]);
});
