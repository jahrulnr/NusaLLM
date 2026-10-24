// Multiplexed turn socket for /api/ws: one native WebSocket carries
// commands and stream frames. Reconnects with bounded backoff, queues
// commands until open, and re-watches in-flight turns from their last
// observed seq after a reconnect. Nothing is persisted to storage.

const BASE_DELAY_MS = 500;
const MAX_DELAY_MS = 15000;

export function requestId() {
  if (crypto.randomUUID) return crypto.randomUUID();
  return 'req_' + Math.random().toString(36).slice(2) + Date.now().toString(36);
}

export class TurnSocket extends EventTarget {
  constructor() {
    super();
    this.state = 'closed';
    this.ws = null;
    this.queue = [];
    this.retries = 0;
    this.intentionalClose = false;
    // turnId -> { conversationId, lastSeq } for turns still in flight.
    this.turns = new Map();
    // turnId -> terminal seq for finished turns — bounded so a replayed
    // or re-watched terminal frame is dropped like any duplicate.
    this.terminals = new Map();
  }

  url() {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    return `${proto}://${location.host}/api/ws`;
  }

  connect() {
    if (this.ws && (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)) {
      return;
    }
    this.intentionalClose = false;
    this._state('connecting');
    const ws = new WebSocket(this.url());
    this.ws = ws;
    ws.onopen = () => {
      this.retries = 0;
      this._state('open');
      for (const raw of this.queue.splice(0)) ws.send(raw);
      // Resynchronize any turn that was mid-flight when the socket dropped.
      for (const [turnId, info] of this.turns) {
        this._send({
          type: 'turn.watch',
          requestId: requestId(),
          conversationId: info.conversationId,
          turnId,
          afterSeq: info.lastSeq,
        });
      }
    };
    ws.onmessage = (ev) => this._onmessage(ev);
    ws.onclose = () => {
      this.ws = null;
      // Never auto-resend: commands queued but never acknowledged are
      // dropped — delivery was unknowable and the UI already warned.
      this.queue.length = 0;
      this._state('closed');
      if (!this.intentionalClose) this._retry();
    };
    ws.onerror = () => { /* onclose follows and drives retry */ };
  }

  close() {
    this.intentionalClose = true;
    if (this.ws) this.ws.close();
    this.ws = null;
    this._state('closed');
  }

  sendTurn({ conversationId = null, route = '', modelId = '', content,
    effort = '', provider = '', workspace = '' }) {
    const payload = { route, modelId, content };
    if (effort) payload.effort = effort;
    if (provider) payload.provider = provider;
    if (workspace) payload.workspace = workspace;
    return this._command({
      type: 'turn.send',
      requestId: requestId(),
      conversationId,
      payload,
    });
  }

  // turn.edit carries the same optional per-turn selections as sendTurn —
  // a retry re-runs under the picker's current choice, and a successful
  // turn re-binds them to the room. '' keeps the stored binding.
  editTurn({ conversationId, messageId, content,
    route = '', modelId = '', effort = '', provider = '', workspace = '' }) {
    const payload = { messageId, content, route, modelId };
    if (effort) payload.effort = effort;
    if (provider) payload.provider = provider;
    if (workspace) payload.workspace = workspace;
    return this._command({
      type: 'turn.edit',
      requestId: requestId(),
      conversationId,
      payload,
    });
  }

  // watchTurn attaches to a turn this tab did not open — a reload or a
  // room selection that found one still running. The cursor starts at
  // afterSeq and reconnect re-watches from the last frame actually seen.
  watchTurn({ conversationId, turnId, afterSeq = -1 }) {
    let info = this.turns.get(turnId);
    if (!info) {
      info = { conversationId, lastSeq: afterSeq };
      this.turns.set(turnId, info);
    }
    return this._command({
      type: 'turn.watch',
      requestId: requestId(),
      conversationId,
      turnId,
      afterSeq: info.lastSeq,
    });
  }

  cancelTurn({ conversationId, turnId }) {
    return this._command({
      type: 'turn.cancel',
      requestId: requestId(),
      conversationId,
      turnId,
    });
  }

  // turn.steer submits input into the exact turn's mailbox. The
  // command's requestId becomes the input's inputId — returned so the
  // caller can track the receipt's state (accepted ≠ consumed; the
  // terminal snapshot reconciles). Same requestId + same content re-acks
  // idempotently server-side.
  steerTurn({ conversationId, turnId, content }) {
    const inputId = requestId();
    this._command({
      type: 'turn.steer',
      requestId: inputId,
      conversationId,
      turnId,
      payload: { content },
    });
    return inputId;
  }

  // turn.steer_recall withdraws one submitted input while it is still
  // pending — claimed/persisted answers not_found server-side.
  steerRecall({ conversationId, turnId, inputId }) {
    return this._command({
      type: 'turn.steer_recall',
      requestId: requestId(),
      conversationId,
      turnId,
      payload: { inputId },
    });
  }

  _command(cmd) {
    this._send(cmd);
    return cmd.requestId;
  }

  _send(cmd) {
    const raw = JSON.stringify(cmd);
    if (this.ws && this.ws.readyState === WebSocket.OPEN) this.ws.send(raw);
    else this.queue.push(raw);
  }

  _state(state) {
    this.state = state;
    this.dispatchEvent(new CustomEvent('connection', { detail: { state } }));
  }

  _onmessage(ev) {
    let frame;
    try {
      frame = JSON.parse(ev.data);
    } catch {
      return; // unparseable frame — nothing safe to surface
    }
    if (frame.turnId && !this._accept(frame)) return;
    this.dispatchEvent(new CustomEvent('frame', { detail: { frame } }));
  }

  // One dedup point for every sequenced frame: the projection AND the
  // queue/shell both rely on it, so dropping must happen before the
  // frame event, not inside each consumer. Returns false for
  // duplicates/replays — a replayed turn.opened must never reset the
  // tracked cursor, and an old frame must never regress it.
  // Unsequenced frames that still carry a turnId — the room-level
  // conversation.activity broadcast — match none of the type checks
  // below: they pass through to the shell without moving the cursor,
  // minting a tracked turn, or retiring one.
  _accept(frame) {
    const t = this.turns.get(frame.turnId);
    const terminalSeq = this.terminals.get(frame.turnId);
    const seen = t ? t.lastSeq : terminalSeq;
    if (typeof frame.seq === 'number') {
      if (typeof seen === 'number' && frame.seq <= seen) return false;
      if (t) t.lastSeq = frame.seq;
    }
    if (frame.type === 'turn.opened') {
      if (!t) {
        this.turns.set(frame.turnId, {
          conversationId: frame.conversationId,
          lastSeq: typeof frame.seq === 'number' ? frame.seq : -1,
        });
        this.terminals.delete(frame.turnId); // a live turn outranks a stale marker
      }
      return true;
    }
    if (frame.type === 'turn.completed' || frame.type === 'turn.failed') {
      this.turns.delete(frame.turnId);
      this._markTerminal(frame.turnId, typeof frame.seq === 'number' ? frame.seq : seen);
    } else if (frame.type === 'turn.resync_required') {
      // The hub's verdict retires tracking too: a finished or unknown
      // turn is never re-watched, and its seq marker keeps deduping.
      const turn = frame.payload && frame.payload.turn;
      if (turn && turn !== 'active') {
        this.turns.delete(frame.turnId);
        this._markTerminal(frame.turnId, typeof frame.seq === 'number' ? frame.seq : seen);
      }
    }
    return true;
  }

  _markTerminal(turnId, seq) {
    this.terminals.delete(turnId);
    this.terminals.set(turnId, typeof seq === 'number' ? seq : -1);
    while (this.terminals.size > 64) {
      this.terminals.delete(this.terminals.keys().next().value);
    }
  }

  _retry() {
    const delay = Math.min(BASE_DELAY_MS * Math.pow(2, this.retries++), MAX_DELAY_MS);
    this._state('connecting');
    setTimeout(() => { if (!this.intentionalClose) this.connect(); }, delay);
  }
}
