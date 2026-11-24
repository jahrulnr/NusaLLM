// Shared turn helpers: a socket on the scratch server, one command sent, and
// the HTTP reads that turn the server's own output into evidence.
import { RawSocket } from './ws.mjs';

export async function connect(server) {
  return RawSocket.connect({ host: '127.0.0.1', port: server.port, origin: server.origin });
}

let seq = 0;
export const requestId = (tag) => `qa-${tag}-${++seq}-${Date.now().toString(36)}`;

export function send(ws, command) {
  const id = command.requestId || requestId(command.type.replace('turn.', ''));
  ws.send({ ...command, requestId: id });
  return id;
}

export const isTerminal = (f) => f.type === 'turn.completed' || f.type === 'turn.failed';
export const isOpened = (f) => f.type === 'turn.opened';

export async function waitOpened(ws, { timeoutMs = 10_000, from = 0 } = {}) {
  return ws.next((f) => f.type === 'turn.opened', { timeoutMs, from, label: 'turn.opened' });
}

export async function waitTerminal(ws, { timeoutMs = 30_000, from = 0 } = {}) {
  return ws.next((f) => f.type === 'turn.completed' || f.type === 'turn.failed', { timeoutMs, from, label: 'terminal frame' });
}

// text joins the deltas one turn streamed, in order.
export function text(frames, type = 'turn.delta') {
  return frames.filter((f) => f.type === type).map((f) => f.payload?.text || '').join('');
}

// toolRows reads the persisted message projection and flattens each call with
// its input and stored output — the authoritative record of what a tool did.
export async function toolRows(server, conversationId) {
  const res = await server.request('GET', `/api/conversations/${conversationId}/messages?start=0&limit=100`);
  if (res.status !== 200) throw new Error(`messages read failed: ${res.status} ${res.text}`);
  const rows = [];
  for (const m of res.json.messages) {
    for (const c of m.toolCalls || []) rows.push({ id: c.id, name: c.name, input: c.input, output: c.output, status: c.status });
  }
  return rows;
}

// transcriptItems runs the WebUI's own projection over the persisted
// messages, so a reproduction reports the same items the screen draws.
export async function transcriptItems(server, conversationId) {
  const { historyItems } = await import('../../../web/lib/turn.js');
  const res = await server.request('GET', `/api/conversations/${conversationId}/messages?start=0&limit=100`);
  if (res.status !== 200) throw new Error(`messages read failed: ${res.status} ${res.text}`);
  return historyItems(res.json.messages);
}

// messages reads the raw projection, for scenarios that need byte counts.
export async function messages(server, conversationId) {
  const res = await server.request('GET', `/api/conversations/${conversationId}/messages?start=0&limit=100`);
  if (res.status !== 200) throw new Error(`messages read failed: ${res.status} ${res.text}`);
  return res.json.messages;
}

// frameLine is the one-line form of a frame used in every evidence block.
export function frameLine(f) {
  const p = f.payload || {};
  const bits = [`${f.type}#${f.seq ?? '-'}`];
  if (p.text !== undefined) bits.push(JSON.stringify(p.text));
  if (p.phase) bits.push(`phase=${p.phase}`);
  if (p.callId) bits.push(`call=${p.callId}`);
  if (p.status !== undefined) bits.push(`status=${p.status}`);
  if (p.code) bits.push(`code=${p.code}`);
  return bits.join(' ');
}
