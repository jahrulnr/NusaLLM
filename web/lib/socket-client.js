// Multiplexed turn socket for /api/ws: one native WebSocket carries
// commands and stream frames. Reconnects with bounded backoff, queues
// commands until open, and re-watches in-flight turns from their last
// observed seq after a reconnect. Nothing is persisted to storage.

const BASE_DELAY_MS = 500;
const MAX_DELAY_MS = 15000;

function requestId() {
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

  editTurn({ conversationId, messageId, content }) {
    return this._command({
      type: 'turn.edit',
      requestId: requestId(),
      conversationId,
      payload: { messageId, content },
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
    // Track in-flight turns by seq so reconnect can watch from the last
    // known position; terminal frames retire the entry.
    if (frame.turnId) {
      if (frame.type === 'turn.opened') {
        this.turns.set(frame.turnId, { conversationId: frame.conversationId, lastSeq: -1 });
      }
      const tracked = this.turns.get(frame.turnId);
      if (tracked && typeof frame.seq === 'number') tracked.lastSeq = frame.seq;
      if (frame.type === 'turn.completed' || frame.type === 'turn.failed') {
        this.turns.delete(frame.turnId);
      }
    }
    this.dispatchEvent(new CustomEvent('frame', { detail: { frame } }));
  }

  _retry() {
    const delay = Math.min(BASE_DELAY_MS * Math.pow(2, this.retries++), MAX_DELAY_MS);
    this._state('connecting');
    setTimeout(() => { if (!this.intentionalClose) this.connect(); }, delay);
  }
}
