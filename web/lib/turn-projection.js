// Pure transport projection of a live turn: an ordered segment timeline
// built from websocket frames. No DOM access — testable under Node.
//
// Protocol honesty rules (see docs/api/websocket.md):
// - A tool_call is a *request*, not running/completed work.
// - turn.tool_input chunks reassemble the request's argument text.
// - turn.tool_output is progress only — it marks `streaming`, never
//   `settled`, and never fabricates result content.
// - Model activity (reasoning phase, reasoning summary, or output
//   delta) after a tool call is proof the earlier tool invocation
//   ended → its segment settles.
// - Raw and summarized provider reasoning cross as ordered turn.reasoning
//   chunks and accumulate on the thinking segment's `text`.
// - Segments are append-ordered: output text never concatenates across
//   a tool or thinking boundary.
//
// Every mutation bumps the segment's `version` so renderers can skip
// reassigning frozen segments. `announcements` holds short, safe strings
// for the live region — never arguments, output, or reasoning text.

const STREAM_FRAMES = new Set([
  'turn.phase', 'turn.reasoning', 'turn.delta', 'turn.tool_call',
  'turn.tool_input', 'turn.tool_output', 'turn.step_finished',
]);

export function createTurnProjection(id) {
  return {
    id,
    active: true,
    segments: [],
    finishReason: null,
    nextSegment: 0,
    announcements: [],
  };
}

function pushSegment(p, seg) {
  seg.id = 's' + p.nextSegment++;
  seg.version = 0;
  p.segments.push(seg);
  return seg;
}

function bump(seg) { seg.version++; }

function announce(p, text) { p.announcements.push(text); }

// The active thinking segment — at most one can exist at a time because
// a new one is only appended after prior activity finished the last.
function finishThinking(p, clock) {
  const seg = p.segments.find((s) => s.type === 'thinking' && s.status === 'active');
  if (seg) {
    seg.status = 'done';
    seg.elapsedMs = clock.mono - (seg.startedAt ?? clock.mono);
    bump(seg);
  }
}

// unresolved = requested|streaming. Evidence the call ended (a later
// model step or the terminal) flips it to `settled`.
function settleTools(p) {
  for (const seg of p.segments) {
    if (seg.type !== 'tool') continue;
    const t = seg.tool;
    if (t.status === 'requested' || t.status === 'streaming') {
      t.status = 'settled';
      bump(seg);
      announce(p, `${t.name || 'Tool'} settled`);
    }
  }
}

// A reasoning stretch needs an active thinking segment at the tail —
// phase markers and summary chunks alike open one when the last
// segment isn't it. 'Thinking' announces once, only when the segment
// opens. Reasoning is model activity: unresolved tools settle.
function ensureThinking(p, clock) {
  settleTools(p);
  let last = p.segments[p.segments.length - 1];
  if (!(last && last.type === 'thinking' && last.status === 'active')) {
    last = pushSegment(p, {
      type: 'thinking',
      status: 'active', startedAt: clock.mono, elapsedMs: null, text: '',
    });
    announce(p, 'Thinking');
  }
  return last;
}

// Applies one stream frame. Terminal frames are NOT handled here —
// call settleTurnProjection so the caller picks the honest label.
export function applyTurnFrame(p, frame, clock = { wall: Date.now(), mono: performance.now() }) {
  if (!p || !p.active) return p;
  const payload = (frame && frame.payload) || {};
  if (!STREAM_FRAMES.has(frame && frame.type)) return p;

  switch (frame.type) {
    case 'turn.phase':
      // Phase is timing metadata; wait for actual provider text before
      // showing a reasoning card.
      break;

    case 'turn.reasoning': {
      // Ordered provider reasoning chunks accumulate on the thinking
      // segment and are NEVER announced.
      const text = payload.text || '';
      if (!text) break;
      const seg = ensureThinking(p, clock);
      seg.text = (seg.text || '') + text;
      bump(seg);
      break;
    }

    case 'turn.delta': {
      const text = payload.text || '';
      if (!text) break;
      finishThinking(p, clock);
      settleTools(p);
      const last = p.segments[p.segments.length - 1];
      if (last && last.type === 'markdown') {
        last.text += text;
        bump(last);
      } else {
        pushSegment(p, { type: 'markdown', text });
      }
      announce(p, text);
      break;
    }

    case 'turn.tool_call': {
      finishThinking(p, clock);
      pushSegment(p, {
        type: 'tool',
        tool: {
          callId: payload.callId,
          name: payload.name,
          status: 'requested',
          // Exact argument text arrives as ordered turn.tool_input chunks.
          input: '',
          // Live output chunks accumulate in order; settled/persisted
          // content lands via resultStored/output from the HTTP record.
          output: '',
          resultStored: false,
        },
      });
      announce(p, `${payload.name || 'Tool'} requested`);
      break;
    }

    case 'turn.tool_input': {
      // Ordered argument chunks — reassembled before the tool runs.
      const seg = p.segments.find((s) => s.type === 'tool' && s.tool.callId === payload.callId);
      if (!seg) break;
      seg.tool.input += payload.text || '';
      bump(seg);
      break;
    }

    case 'turn.tool_output': {
      // Progress only — thinking stays active, nothing settles. Every
      // chunk appends to the tool's live output; the announcement is
      // deduped but content is never dropped. `streaming`, never
      // `settled`. Announcements stay metadata-only — never the text.
      const seg = p.segments.find((s) => s.type === 'tool' && s.tool.callId === payload.callId);
      if (!seg) break;
      const t = seg.tool;
      if (t.status === 'requested' || t.status === 'streaming') {
        t.status = 'streaming';
        t.output += payload.text || '';
        if (!t.outputAnnounced) {
          t.outputAnnounced = true;
          announce(p, `${t.name || 'Tool'} output activity`);
        }
        bump(seg);
      }
      break;
    }

    case 'turn.step_finished': {
      finishThinking(p, clock);
      p.finishReason = payload.finishReason || null;
      // 'tool_calls' means the step ended by requesting tools — they
      // execute next, so nothing settles. Any other reason means the
      // model moved on; unresolved tools are honestly over.
      if (p.finishReason !== 'tool_calls') {
        settleTools(p);
      }
      break;
    }
  }
  return p;
}

// Terminal frames (completed/failed) settle the projection.
export function settleTurnProjection(p, clock = { wall: Date.now(), mono: performance.now() }) {
  if (!p || !p.active) return p;
  finishThinking(p, clock);
  settleTools(p);
  p.active = false;
  return p;
}
