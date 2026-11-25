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
    // A call's verdict, recorded once after its result (D-12). It is a fact
    // about the call, not about the row's life: the row stays streaming until
    // activity resumes or the turn ends, and a row can be settled and failed.
    case 'turn.tool_result': {
      const s = t.segments.findLast((x) => x.kind === 'tool' && x.callId === p.callId);
      if (s) { s.outcome = p.status; s.exit = p.exit; }
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

// exitText is the reader's label for a call's recorded exit status — the fact
// the server stored, never one read out of the output. A process exit is a
// number; a signal is shown exactly as the server names it, with no prefix
// added here (the wire value is the name: T-33 changes it from
// "signal: killed" to "killed", and this formatter renders either as-is).
export function exitText(exit) {
  if (!exit) return '';
  if (exit.signal) return exit.signal;
  return `Exit code ${exit.code ?? 0}`;
}

// historyItems flattens persisted messages into transcript items:
// { kind: 'user', msg } | { kind: 'note', msg } | segment-like items.
export function historyItems(messages, cwd = '') {
  const out = [];
  for (const m of messages) {
    if (m.role === 'user') {
      // inputId is the steer command a line was folded from (D-06.1/T-18): the
      // only identity a steered row has, and never matched by its text.
      out.push({ id: m.id, kind: m.userType === 'system' ? 'note' : 'user', msg: m, inputId: m.inputId || '' });
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
      if (part.kind === 'text' && part.text) out.push({ id, kind: 'text', text: part.text, messageId: m.id });
      else if (part.kind === 'reasoning' && part.text) out.push({ id, kind: 'reasoning', text: part.text, done: true, messageId: m.id });
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
          // Two facts, kept apart (D-12): `status` is the row's life in the
          // transcript (pending until it has a result, settled after), and
          // `outcome` is the call's verdict, straight from the record —
          // dto.status → item.outcome, never derived from the text. A record
          // written before the verdict existed carries no status and reads as
          // `unknown`: neutral, no badge, its text left alone.
          status: c.status ? (c.status === 'pending' ? 'pending' : 'settled') : (c.output === undefined || c.output === null ? 'pending' : 'settled'),
          outcome: c.status || 'unknown',
          exit: c.exit,
          calledAt: c.calledAt, stored: true, cwd, messageId: m.id,
        });
      }
    }
    if (m.finishReason === 'interrupted') out.push({ id: `${m.id}:int`, kind: 'interrupted' });
  }
  return out;
}

// liveHistory trims the transcript list to what the live turn replaces in the
// record (decisions.md D-06.1). The record is authoritative: a running turn's
// finished steps are drawn from history, never from the journal, so nothing is
// trimmed for a plain send. An edit replaces the message it names, so history
// stops ahead of it; a resume this tab sent re-answers the newest prompt, so
// history stops after it. A turn adopted from another tab or a chat channel
// cannot tell the turn's prompt from an older message a stale page still ends
// on, so it trims nothing.
export function liveHistory(items, live) {
  const list = items || [];
  if (!live) return list;
  if (live.editOf) {
    const at = list.findIndex((x) => x.id === live.editOf);
    return at < 0 ? list : list.slice(0, at);
  }
  if (live.kind === 'resume' && !live.adopted) {
    const at = list.findLastIndex((x) => x.kind === 'user');
    return at < 0 ? list : list.slice(0, at + 1);
  }
  return list;
}

// stepKinds answers which prose the record holds for the step that owns `row`:
// the kinds of text and reasoning items in the same message. Without a message
// id (fixtures, or a record shape that predates it) the run immediately before
// the row is the same step — walking over its tool rows and reasoning back to
// the prose that precedes them, and stopping at the first item that belongs to
// the owner.
function stepKinds(items, row) {
  const kinds = new Set();
  if (!row) return kinds;
  if (row.messageId) {
    for (const it of items) {
      if (it.messageId === row.messageId && (it.kind === 'text' || it.kind === 'reasoning')) kinds.add(it.kind);
    }
    return kinds;
  }
  for (let i = items.indexOf(row) - 1; i >= 0; i--) {
    const kind = items[i]?.kind;
    if (kind === 'tool') continue;
    if (kind === 'text' || kind === 'reasoning') { kinds.add(kind); continue; }
    break;
  }
  return kinds;
}

// liveSegments answers the live turn's segments as the transcript should draw
// them, given what the record already holds (D-06.1).
//
// A tab that attaches mid-turn replays the journal from seq 0, and everything
// the store committed is already in history — a step's text, reasoning and its
// tool rows sealed into one message. Drawing the replay whole would draw those
// steps twice, so the cut is found by identity, never by position: the last
// live tool row whose callId the record carries ends the persisted prefix.
// Tool rows in it are dropped by that identity alone; the text and reasoning
// before them are dropped only when the record's own message holds that kind
// of content — a step whose record has no text keeps its replayed text, because
// dropping what the store does not have would erase the answer. A resync marker
// inside the dropped range is kept: it is the only evidence that output is
// missing.
//
// A folded steer is matched the same way, by the inputId its row carries
// (T-18); a record without one leaves the replayed bubble alone — it is never
// matched by text.
//
// With no record, or no identity match, nothing is dropped: the merge never
// invents a cut.
export function liveSegments(live, items) {
  const segments = live?.segments || [];
  const list = items || [];
  if (!segments.length) return segments;
  const calls = new Set();
  for (const it of list) if (it.kind === 'tool' && it.callId) calls.add(it.callId);
  const steers = new Set();
  for (const it of list) if (it.kind === 'user' && it.inputId) steers.add(it.inputId);
  // Nothing in the record carries an identity, so there is nothing to prove a
  // replayed segment is already stored: draw the journal whole.
  if (!calls.size && !steers.size) return segments;
  // A steer row is committed before its frame is emitted (turn.go fold), so a
  // replayed bubble whose inputId the record holds is the same input — the
  // record draws it, and the journal copy goes wherever it sits.
  const dropSteer = (s) => s?.kind === 'steer' && !!s.inputId && steers.has(s.inputId);
  let cut = -1;
  for (let i = 0; i < segments.length; i++) {
    const s = segments[i];
    if (s?.kind === 'tool' && s.callId && calls.has(s.callId)) cut = i;
  }
  // No replayed row the record identifies as a step boundary: only the steers
  // it holds are dropped, and no prose is cut.
  if (cut < 0) return segments.filter((s) => !dropSteer(s));
  const recorded = stepKinds(list, list.findLast((x) => x.kind === 'tool' && x.callId === segments[cut].callId));
  const kept = [];
  for (let i = 0; i < cut; i++) {
    const s = segments[i];
    if (s?.kind === 'resync') kept.push(s);
    else if (s?.kind === 'steer') { if (!dropSteer(s)) kept.push(s); }
    else if (s?.kind === 'tool') { if (!(s.callId && calls.has(s.callId))) kept.push(s); }
    else if ((s?.kind === 'text' || s?.kind === 'reasoning') && !recorded.has(s.kind)) kept.push(s);
  }
  return [...kept, ...segments.slice(cut + 1).filter((s) => !dropSteer(s))];
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
