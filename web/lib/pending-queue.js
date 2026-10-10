// Room-bound pending submissions — the composer's local queue plus the
// receipt-tracking side of submitted steering. Pure state: the shell
// renders it and drives every transition from socket frames. Nothing
// persists to storage; a reload forgets pending items by design.
//
// Entry shape:
//   { id, text, room, opts, ordinal, state, inputId, turnId, recallId }
// - room: the target conversation id captured at submission, or the
//   fresh:<n> token of the un-minted view — rebound by bindRoom when
//   that token's send mints a room.
// - opts: the submission-time send binding (route/modelId/effort/
//   provider/workspace) — dispatch replays it verbatim.
// - ordinal: global submission order; a requeued entry never lets newer
//   submissions bypass it.
// - inputId/turnId: set once submitted as turn.steer — the command's
//   requestId is the receipt key every snapshot names.
// - recallId: the in-flight turn.steer_recall requestId.
//
// States:
//   queued      — local text waiting for a free turn
//   submitting  — turn.steer transmitted, ack not yet seen
//   steering    — acked; waiting to be consumed at a safe point
//   recalling   — turn.steer_recall in flight
//   unknown     — transmitted, outcome lost (never auto-retried)
export const QUEUED = 'queued';
export const SUBMITTING = 'submitting';
export const STEERING = 'steering';
export const RECALLING = 'recalling';
export const UNKNOWN = 'unknown';

export function createQueue() {
  // notice: one inline status line the panel shows instead of toasts
  // for routine queue/recall/connect outcomes.
  return { items: [], paused: false, next: 1, notice: '' };
}

export function findEntry(q, id) {
  return q.items.find((e) => e.id === id) || null;
}

export function removeEntry(q, entry) {
  const i = q.items.indexOf(entry);
  if (i >= 0) q.items.splice(i, 1);
}

// enqueue captures one local submission. The queue owns the text the
// moment it lands — the draft is free to clear.
export function enqueue(q, { text, room, opts }) {
  const e = {
    id: `p${q.next}`, ordinal: q.next++,
    text, room: room ?? null, opts: { ...(opts || {}) },
    state: QUEUED, inputId: null, turnId: null, recallId: null,
  };
  q.items.push(e);
  return e;
}

// steer submits draft text into an exact turn — tracked until its
// receipt resolves (projected, snapshot, recalled, or unknown).
export function steer(q, { text, room, opts, turnId, inputId }) {
  const e = {
    id: `p${q.next}`, ordinal: q.next++,
    text, room: room ?? null, opts: { ...(opts || {}) },
    state: SUBMITTING, inputId, turnId, recallId: null,
  };
  q.items.push(e);
  return e;
}

// takeQueued removes and returns the oldest queued entry targeting
// room — the only path a queued line becomes a turn.send, so newer
// submissions never bypass older ones.
export function takeQueued(q, room) {
  const e = q.items.find((x) => x.state === QUEUED && x.room === room);
  if (!e) return null;
  removeEntry(q, e);
  return e;
}

export function hasQueuedForRoom(q, room) {
  return q.items.some((e) => e.state === QUEUED && e.room === room);
}

// requeue returns a dispatched or unconsumed entry at its original
// ordinal — it rejoins the queue where it was submitted, never behind
// later arrivals. An entry still seated keeps its place; only its
// state and submitted identity reset.
export function requeue(q, e) {
  e.state = QUEUED;
  e.inputId = null;
  e.turnId = null;
  e.recallId = null;
  if (q.items.includes(e)) return;
  const at = q.items.findIndex((x) => x.ordinal > e.ordinal);
  q.items.splice(at < 0 ? q.items.length : at, 0, e);
}

// bindRoom attaches queued entries from ONE fresh-view token to the
// room its send just minted — entries captured on a different fresh
// token or a real room are never rebound.
export function bindRoom(q, token, room) {
  for (const e of q.items) {
    // Every entry the origin fresh view owns follows the minted room —
    // queued, steering, and unknown alike — never a foreign token's.
    if (e.room === token) e.room = room;
  }
}

// markInFlightUnknown flips every transmitted-but-unanswered control
// entry to unknown — a dropped socket means the ack may have been lost
// either way, so nothing retries and nothing pretends.
export function markInFlightUnknown(q) {
  let touched = false;
  for (const e of q.items) {
    if (e.state === SUBMITTING || e.state === RECALLING) {
      e.state = UNKNOWN;
      touched = true;
    }
  }
  return touched;
}

function entryForInput(q, turnId, inputId) {
  return q.items.find((e) => e.turnId === turnId && e.inputId === inputId) || null;
}

// steerAcked flips a submitted input to steering — the journaled
// turn.steer_ack confirms the server recorded it, not that it consumed.
// An ack replayed after a disconnect also recovers an unknown entry —
// unless a recall is in flight, whose own outcome decides.
export function steerAcked(q, turnId, inputId) {
  const e = entryForInput(q, turnId, inputId);
  if (!e) return null;
  if (e.state === SUBMITTING) e.state = STEERING;
  else if (e.state === UNKNOWN && e.recallId) e.state = RECALLING;
  else if (e.state === UNKNOWN) e.state = STEERING;
  return e;
}

// steerProjected drops a folded input — the turn.steer chunk carrying
// its inputId is proof the receipt persisted before it rendered.
export function steerProjected(q, turnId, inputId) {
  const e = entryForInput(q, turnId, inputId);
  if (!e) return null;
  removeEntry(q, e);
  return e;
}

// steerRecalled withdraws a confirmed recall — the caller restores the
// entry's text to the draft.
export function steerRecalled(q, turnId, inputId) {
  const e = entryForInput(q, turnId, inputId);
  if (!e) return null;
  removeEntry(q, e);
  return e;
}

// markTurnUnknown flags every entry bound to a turn whose outcome the
// server can no longer report (evicted journal, restart).
export function markTurnUnknown(q, turnId) {
  let touched = false;
  for (const e of q.items) {
    if (e.turnId === turnId && e.state !== QUEUED) {
      e.state = UNKNOWN;
      touched = true;
    }
  }
  return touched;
}

// reconcileSnapshot resolves every entry bound to turnId against the
// receipt snapshot [{inputId,state}] a terminal or resync frame carries.
// `terminal` matters: on a live turn's ACTIVE snapshot pending/claimed
// means the server still owns the input — the entry keeps its
// turnId/inputId and returns to steering (or stays recalling while its
// recall is in flight), never requeued — requeueing here would let the
// client send a copy the server may still fold. On a TERMINAL snapshot
// pending/claimed means the turn ended without committing — the entry
// falls back to the local queue at its original ordinal. Absent from
// the snapshot is unknown in both modes, identity preserved.
// Returns the categorized fallout — the caller restores texts:
//   consumed → persisted input, dropped from the queue
//   restored → recalled input, text returns to the owner
//   requeued → terminal pending/claimed fallback
//   steering → active pending/claimed retained against the turn
//   unknown  → submitted but absent from the snapshot
export function reconcileSnapshot(q, turnId, snapshot, { terminal = true } = {}) {
  const byInput = new Map((snapshot || []).map((s) => [s.inputId, s.state]));
  const out = { consumed: [], restored: [], requeued: [], steering: [], unknown: [] };
  for (const e of [...q.items]) {
    if (e.turnId !== turnId || e.state === QUEUED) continue;
    const state = byInput.get(e.inputId);
    switch (state) {
      case 'persisted':
        removeEntry(q, e);
        out.consumed.push(e);
        break;
      case 'recalled':
        removeEntry(q, e);
        out.restored.push(e);
        break;
      case 'pending':
      case 'claimed':
        if (terminal) {
          requeue(q, e);
          out.requeued.push(e);
        } else {
          e.state = e.recallId ? RECALLING : STEERING;
          out.steering.push(e);
        }
        break;
      default:
        e.state = UNKNOWN;
        out.unknown.push(e);
    }
  }
  return out;
}
