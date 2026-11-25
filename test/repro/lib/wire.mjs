// One wire case: its own stub, gateway and conversation on a running scratch
// server, one turn, then the server's own record of what happened. Shared by
// the per-row T-03 scenarios so each row is a scenario that can go red alone.
import { startStub } from './stub.mjs';
import { messages, send, waitOpened, waitTerminal } from './turn.mjs';

let seq = 0;

export async function runWireCase(server, ws, { name, style = 'completion', handler, headers, content = 'x' }) {
  const stub = await startStub(handler, { headers });
  const gw = `qa_${name}`;
  server.addGateway({ id: gw, endpoint: stub.endpoint, style });
  const cid = `c_qa_${name}_${++seq}`;
  server.seedConversation({
    id: cid,
    route: gw,
    messages: [{ id: `m_${name}_${seq}_u0`, role: 'user', content, userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
  });
  const mark = ws.frames.length;
  send(ws, { type: 'turn.send', conversationId: cid, payload: { content } });
  await waitOpened(ws, { from: mark });
  const terminal = await waitTerminal(ws, { from: mark, timeoutMs: 40_000 });
  const rows = await messages(server, cid);
  const detail = await server.request('GET', `/api/conversations/${cid}`);
  return { stub, cid, terminal, rows, usage: detail.json?.usage, calls: stub.requests.length };
}

export const assistant = (rows) => rows.filter((m) => m.role === 'assistant');
