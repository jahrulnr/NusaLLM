// Turn projection: websocket frames → ordered segments, and persisted
// history → the same segment shapes, so the transcript renders live and
// stored turns through one path.
//
// Segment kinds: text | reasoning | tool | steer | user.

export function newTurn({ turnId, requestId, conversationId, kind }) {
  return {
    turnId, requestId, conversationId, kind,
    lastSeq: -1,
    segments: [],
    phase: null,          // { phase, retryAfterMs, at }
    usage: null,
    startedAt: Date.now(),
    terminal: null,       // the terminal frame once it arrives
    n: 0,
    persistedMark: 0,     // segments the store already holds (a step or steer committed)
  };
}

const last = (t) => t.segments[t.segments.length - 1];

function settleTools(t, why) {
  for (const s of t.segments) if (s.kind === 'tool' && s.status !== 'settled') { s.status = 'settled'; s.settled = why; }
}

// applyFrame mutates `t`; replayed or doubled seq numbers are dropped.
export function applyFrame(t, f) {
  if (typeof f.seq === 'number') {
    if (f.seq <= t.lastSeq) return false;
    t.lastSeq = f.seq;
  }
  const p = f.payload || {};
  if (f.type !== 'turn.phase' && f.type !== 'turn.steer_ack' && f.type !== 'turn.steer_recalled') {
    if (t.phase && t.phase.phase !== 'reasoning') t.phase = null;
  }
  switch (f.type) {
    case 'turn.delta': {
      const s = last(t);
      if (s?.kind === 'text') s.text += p.text || '';
      else { settleTools(t, 'Model activity resumed'); t.segments.push({ id: `${t.turnId}:${++t.n}`, kind: 'text', text: p.text || '' }); }
      if (t.phase?.phase === 'reasoning') t.phase = null;
      break;
    }
    case 'turn.reasoning': {
      const s = last(t);
      if (s?.kind === 'reasoning') s.text += p.text || '';
      else t.segments.push({ id: `${t.turnId}:${++t.n}`, kind: 'reasoning', text: p.text || '' });
      break;
    }
    case 'turn.phase':
      t.phase = { phase: p.phase, retryAfterMs: p.retryAfterMs, at: Date.now() };
      // A retry re-runs a call whose output the store does not hold: every
      // segment after the last committed boundary — text, reasoning, tool rows
      // without a result — belongs to the attempt that just failed. Drop that
      // tail now, or the two attempts splice into one answer. The boundary is
      // the last turn.step_finished or folded steer (docs/audit/decisions.md
      // D-06), never a tool row's status: a row is settled when activity
      // resumes, whether or not a result ever arrived.
      if (p.phase === 'retry_wait') t.segments.length = Math.min(t.segments.length, t.persistedMark || 0);
      break;
    case 'turn.tool_call':
      t.segments.push({ id: `${t.turnId}:${++t.n}`, kind: 'tool', callId: p.callId, name: p.name, input: '', output: '', status: 'requested' });
      if (t.phase?.phase === 'reasoning') t.phase = null;
      break;
    case 'turn.tool_input': {
      const s = t.segments.findLast((x) => x.kind === 'tool' && x.callId === p.callId);
      if (s) s.input += p.text || '';
      break;
    }
    case 'turn.tool_output': {
      const s = t.segments.findLast((x) => x.kind === 'tool' && x.callId === p.callId);
      if (s) { s.output += p.text || ''; s.status = 'streaming'; }
      break;
    }
    case 'turn.steer': {
      const s = last(t);
      if (s?.kind === 'steer' && !s.final && s.inputId === p.inputId) s.text += p.text || '';
      else { settleTools(t, 'Model activity resumed'); t.segments.push({ id: `${t.turnId}:${++t.n}`, kind: 'steer', inputId: p.inputId, text: p.text || '' }); }
      if (p.final) last(t).final = true;
      // A folded steer is committed to history before its frame is emitted, so
      // it is a boundary a retry must not cut past.
      t.persistedMark = t.segments.length;
      break;
    }
    case 'turn.step_finished':
      t.usage = p.usage || t.usage;
      if (p.finishReason !== 'tool_calls') settleTools(t, 'Step ended');
      // The step's messages are appended and saved before the next provider
      // call, so this is the last position the store is known to hold.
      t.persistedMark = t.segments.length;
      break;
    case 'turn.completed':
    case 'turn.failed':
      settleTools(t, f.type === 'turn.completed' ? 'Turn completed' : 'Turn ended');
      t.terminal = f;
      t.phase = null;
      break;
  }
  return true;
}

// A tool's stored output signals failure by convention.
export function toolFailed(output) {
  const o = String(output || '');
  return /^error:/i.test(o) || /interrupted by user/i.test(o) || /\(exit [1-9]\d*\)\s*$/.test(o) || /\(signal: [^)]+\)\s*$/.test(o);
}

// historyItems flattens persisted messages into transcript items:
// { kind: 'user', msg } | { kind: 'note', msg } | segment-like items.
export function historyItems(messages, cwd = '') {
  const out = [];
  for (const m of messages) {
    if (m.role === 'user') {
      out.push({ id: m.id, kind: m.userType === 'system' ? 'note' : 'user', msg: m });
      continue;
    }
    if (m.role !== 'assistant') continue;
    const calls = new Map((m.toolCalls || []).map((c) => [c.id, c]));
    const parts = m.parts?.length ? m.parts : [
      ...(m.content ? [{ kind: 'text', text: m.content }] : []),
      ...(m.toolCalls || []).map((c) => ({ kind: 'tool_call', toolCallId: c.id })),
    ];
    let i = 0;
    for (const part of parts) {
      const id = `${m.id}:${i++}`;
      if (part.kind === 'text' && part.text) out.push({ id, kind: 'text', text: part.text });
      else if (part.kind === 'reasoning' && part.text) out.push({ id, kind: 'reasoning', text: part.text, done: true });
      else if (part.kind === 'tool_call') {
        const c = calls.get(part.toolCallId);
        if (!c) continue;
        // The workspace injection records where later tools run.
        if (c.type === 'injection') {
          if (c.name === 'workspace') { try { cwd = JSON.parse(c.input).path || cwd; } catch { /* keep */ } }
          continue;
        }
        out.push({
          id: `${m.id}:${c.id}`, kind: 'tool', callId: c.id, name: c.name, input: c.input || '', output: c.output || '',
          status: c.status === 'completed' ? 'settled' : 'pending', calledAt: c.calledAt, stored: true, cwd,
        });
      }
    }
    if (m.finishReason === 'interrupted') out.push({ id: `${m.id}:int`, kind: 'interrupted' });
  }
  return out;
}

// joinText folds neighbouring text items into one block. A part is a
// persistence boundary — the server seals a step's text, and an interrupted
// reply drops the tool call that split it — not a presentation one: drawn as
// separate Markdown documents, one sentence split at that boundary becomes two
// paragraphs, a fence split there is parsed twice and comes out broken, and
// the copy of the answer carries the seam. Joining keeps the first item's id,
// so the DOM key stays stable while the text grows, and it runs before
// group(), so the live and the stored path render one <Markdown> either way.
//
// Two texts can also be neighbours because the record between them draws no
// row at all: the job writes a system-role tool-context record between two
// assistant messages, and historyItems has no item for a record that is not
// conversation. Joining those is right — the reader would otherwise see one
// answer split by a seam only the store can see. Every boundary a reader cares
// about is an item (a tool ran, the owner steered, a note landed, the turn was
// interrupted), so it still stops the join. What a joined block loses is the
// per-message id: it keeps the first item's, which is a render key and nothing
// else — no caller reads a text item's id as a message id.
export function joinText(items) {
  const out = [];
  for (const it of items || []) {
    const prev = out[out.length - 1];
    if (it?.kind === 'text' && prev?.kind === 'text') {
      out[out.length - 1] = { ...prev, text: prev.text + it.text };
      continue;
    }
    out.push(it);
  }
  return out;
}

// group folds consecutive reasoning/tool items into one activity item so
// the transcript reads as prose with collapsible "work" between.
export function group(items) {
  const out = [];
  let cur = null;
  for (const it of items) {
    if (it.kind === 'reasoning' || it.kind === 'tool') {
      if (!cur) { cur = { id: 'g:' + it.id, kind: 'activity', items: [] }; out.push(cur); }
      cur.items.push(it);
    } else {
      cur = null;
      out.push(it);
    }
  }
  return out;
}

// toolSummary renders a one-line label for a tool call from its arguments.
export function toolSummary(name, input) {
  let args = null;
  try { args = JSON.parse(input || '{}'); } catch { /* partial while streaming */ }
  const pick = (...keys) => { for (const k of keys) if (args && typeof args[k] === 'string' && args[k]) return args[k]; return ''; };
  const first = (s) => String(s).split('\n')[0];
  switch (name) {
    case 'exec': case 'exec_session': case 'shell': case 'bash': return { verb: 'Ran', detail: first(pick('command', 'cmd')) };
    case 'write_stdin': return { verb: 'Sent input', detail: first(pick('chars')) };
    case 'read_skill': return { verb: 'Read skill', detail: pick('name') };
    case 'read_file': case 'read_media': case 'read': return { verb: 'Read', detail: pick('path', 'file') };
    case 'write_file': case 'write': return { verb: 'Wrote', detail: pick('path', 'file') };
    case 'apply_patch': case 'edit_file': return { verb: 'Edited', detail: pick('path', 'file') };
    case 'list_dir': case 'ls': case 'glob': return { verb: 'Listed', detail: pick('path', 'pattern') };
    case 'grep': case 'search': return { verb: 'Searched', detail: pick('pattern', 'query') };
    case 'web_search': return { verb: 'Searched web', detail: pick('query') };
    case 'web_fetch': case 'fetch': return { verb: 'Fetched', detail: pick('url') };
    default: {
      const detail = args ? pick(...Object.keys(args)) : '';
      return { verb: name, detail: first(detail) };
    }
  }
}
