// A raw RFC 6455 client — no dependency, and the one thing a browser's
// WebSocket cannot give a test: custom request headers. The server's
// upgrade needs an exact Origin, so every reproduction talks to it through
// this class instead.
//
// It exposes JSON text frames only: the conversations socket has no binary
// payloads, and ping/pong is answered internally.
import net from 'node:net';
import crypto from 'node:crypto';
import { EventEmitter } from 'node:events';

const GUID = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';

export class RawSocket extends EventEmitter {
  constructor() {
    super();
    this.frames = [];       // every JSON frame received, in order
    this.raw = [];          // {opcode, payload} for frames that are not JSON
    this.closed = false;
  }

  // connect opens the socket and resolves once the server answered 101 with
  // a valid accept hash. A non-101 answer rejects with the status line and
  // body, which is how a rejected Origin shows up as evidence.
  static connect({ host = '127.0.0.1', port, path = '/api/ws', origin } = {}) {
    const sock = new RawSocket();
    return new Promise((resolve, reject) => {
      const key = crypto.randomBytes(16).toString('base64');
      const expected = crypto.createHash('sha1').update(key + GUID).digest('base64');
      const net_ = net.connect({ host, port });
      let buf = Buffer.alloc(0);
      let upgraded = false;
      const fail = (err) => { net_.destroy(); reject(err); };
      const timer = setTimeout(() => fail(new Error('websocket handshake timed out')), 5000);
      net_.on('error', (err) => { clearTimeout(timer); if (!upgraded) reject(err); else sock.emit('error', err); });
      net_.on('close', () => { clearTimeout(timer); sock.closed = true; sock.emit('close'); });
      net_.on('connect', () => {
        const lines = [
          `GET ${path} HTTP/1.1`,
          `Host: ${host}:${port}`,
          'Upgrade: websocket',
          'Connection: Upgrade',
          `Sec-WebSocket-Key: ${key}`,
          'Sec-WebSocket-Version: 13',
        ];
        if (origin) lines.push(`Origin: ${origin}`);
        net_.write(lines.join('\r\n') + '\r\n\r\n');
      });
      net_.on('data', (chunk) => {
        buf = Buffer.concat([buf, chunk]);
        if (!upgraded) {
          const end = buf.indexOf('\r\n\r\n');
          if (end < 0) return;
          const head = buf.subarray(0, end).toString('latin1');
          buf = buf.subarray(end + 4);
          const [statusLine, ...headerLines] = head.split('\r\n');
          const status = Number(statusLine.split(' ')[1]);
          const headers = {};
          for (const line of headerLines) {
            const i = line.indexOf(':');
            if (i > 0) headers[line.slice(0, i).trim().toLowerCase()] = line.slice(i + 1).trim();
          }
          if (status !== 101) {
            clearTimeout(timer);
            fail(new Error(`handshake rejected: ${statusLine} ${buf.toString('utf8').slice(0, 300)}`));
            return;
          }
          if (headers['sec-websocket-accept'] !== expected) {
            clearTimeout(timer);
            fail(new Error(`bad accept hash: ${headers['sec-websocket-accept']}`));
            return;
          }
          upgraded = true;
          clearTimeout(timer);
          sock.net = net_;
          resolve(sock);
        }
        sock.#drain();
      });
      sock._buffer = () => buf;
      sock._setBuffer = (b) => { buf = b; };
    });
  }

  #drain() {
    let buf = this._buffer();
    for (;;) {
      if (buf.length < 2) break;
      const fin = (buf[0] & 0x80) !== 0;
      const opcode = buf[0] & 0x0f;
      const masked = (buf[1] & 0x80) !== 0;
      let len = buf[1] & 0x7f;
      let off = 2;
      if (len === 126) {
        if (buf.length < 4) break;
        len = buf.readUInt16BE(2);
        off = 4;
      } else if (len === 127) {
        if (buf.length < 10) break;
        len = Number(buf.readBigUInt64BE(2));
        off = 10;
      }
      const maskBytes = masked ? 4 : 0;
      if (buf.length < off + maskBytes + len) break;
      let payload = buf.subarray(off + maskBytes, off + maskBytes + len);
      if (masked) {
        const mask = buf.subarray(off, off + 4);
        payload = Buffer.from(payload.map((b, i) => b ^ mask[i % 4]));
      }
      buf = buf.subarray(off + maskBytes + len);
      this._setBuffer(buf);
      if (opcode === 0x8) { this.close(); break; }
      if (opcode === 0x9) { this.#sendFrame(0xA, payload); continue; }
      if (opcode === 0xA) continue;
      if (!fin) continue; // the server sends JSON frames whole
      const text = payload.toString('utf8');
      let frame;
      try { frame = JSON.parse(text); } catch { this.raw.push({ opcode, text }); continue; }
      this.frames.push(frame);
      this.emit('frame', frame, this.frames.length - 1);
    }
  }

  #sendFrame(opcode, payload) {
    if (!this.net || this.closed) return;
    const mask = crypto.randomBytes(4);
    const len = payload.length;
    let header;
    if (len < 126) {
      header = Buffer.from([0x80 | opcode, 0x80 | len]);
    } else if (len < 65536) {
      header = Buffer.alloc(4);
      header[0] = 0x80 | opcode;
      header[1] = 0x80 | 126;
      header.writeUInt16BE(len, 2);
    } else {
      header = Buffer.alloc(10);
      header[0] = 0x80 | opcode;
      header[1] = 0x80 | 127;
      header.writeBigUInt64BE(BigInt(len), 2);
    }
    const body = Buffer.from(payload.map((b, i) => b ^ mask[i % 4]));
    this.net.write(Buffer.concat([header, mask, body]));
  }

  send(command) {
    this.#sendFrame(0x1, Buffer.from(JSON.stringify(command), 'utf8'));
  }

  // next resolves with the first frame that satisfies `predicate`, counting
  // frames already received — unless `from` names an index to start at, which
  // is how a second turn ignores the first turn's frames of the same type.
  // A timeout is a failure with the frames seen so far in the message, so a
  // reproduction never hangs on a frame the server never sends.
  next(predicate, { timeoutMs = 5000, label = 'frame', from = 0 } = {}) {
    for (let i = from; i < this.frames.length; i++) {
      if (predicate(this.frames[i], i)) return Promise.resolve(this.frames[i]);
    }
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.off('frame', onFrame);
        reject(new Error(`timed out waiting for ${label}; saw: ${this.frames.map((f) => f.type + '#' + f.seq).join(', ') || 'nothing'}`));
      }, timeoutMs);
      const onFrame = (frame, index) => {
        if (!predicate(frame, index)) return;
        clearTimeout(timer);
        this.off('frame', onFrame);
        resolve(frame);
      };
      this.on('frame', onFrame);
      this.once('close', () => { clearTimeout(timer); this.off('frame', onFrame); reject(new Error(`socket closed while waiting for ${label}`)); });
    });
  }

  // pause/resume stop reading the socket. A test that needs the server's
  // outbound queue to stay full pauses here instead of reading: whatever the
  // server writes waits in the kernel until resume().
  pause() { this.net?.pause(); }

  resume() { this.net?.resume(); }

  // settle resolves when the socket closes, or after `graceMs` — the server
  // holds an idle socket open, so a test that only watched frames must not
  // wait for a close that will not come.
  settle(graceMs = 200) {
    if (this.closed) return Promise.resolve();
    return new Promise((resolve) => {
      const timer = setTimeout(resolve, graceMs);
      this.once('close', () => { clearTimeout(timer); resolve(); });
    });
  }

  close() {
    if (this.closed) return;
    try { this.#sendFrame(0x8, Buffer.alloc(0)); } catch { /* already gone */ }
    this.net?.end();
    this.closed = true;
  }
}
