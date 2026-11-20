// The one multiplexed turn socket. Reconnects with capped backoff; a
// disconnect never cancels a turn server-side, so the controller re-watches
// live turns from their last seq on every (re)open.

let seq = 0;
export const newRequestId = () => `r${Date.now().toString(36)}${(++seq).toString(36)}`;

export class TurnSocket {
  constructor({ onFrame, onState, onOpen }) {
    this.onFrame = onFrame;
    this.onState = onState;
    this.onOpen = onOpen;
    this.ws = null;
    this.delay = 500;
    this.connect();
  }

  get open() { return this.ws?.readyState === WebSocket.OPEN; }

  connect() {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(`${proto}//${location.host}/api/ws`);
    this.ws = ws;
    this.onState('connecting');
    ws.onopen = () => {
      this.delay = 500;
      this.onState('open');
      this.onOpen();
    };
    ws.onmessage = (ev) => {
      let frame;
      try { frame = JSON.parse(ev.data); } catch { return; }
      this.onFrame(frame);
    };
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.onState('closed');
      setTimeout(() => this.connect(), this.delay);
      this.delay = Math.min(this.delay * 2, 8000);
    };
  }

  // send returns false when the socket can't carry the command right now;
  // callers keep the user's text instead of guessing delivery.
  send(cmd) {
    if (!this.open) return false;
    this.ws.send(JSON.stringify(cmd));
    return true;
  }
}
