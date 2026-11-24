// T-16 — the provider/server split of `turn.failed.code`.
//
// Contract (docs/api/websocket.md, terminal frames): "a failure that came from
// the provider is an upstream code — `upstream_error` for its 5xx (500, 503,
// and the non-standard 529 overloaded included) and for its 429 … A
// provider-originated 5xx therefore never reports `internal` or `unavailable`."
// The status crosses as-is; the code classifies.
import { connect, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

export default {
  ticket: 'T-16',
  title: 'a provider 5xx reports `upstream_error`, never `internal`',
  async run({ startServer, startStub }) {
    const server = await startServer({ label: 't16' });
    const ws = await connect(server);
    const stubs = [];
    let n = 0;
    try {
      const runCase = async ({ name, status, body }) => {
        const stub = await startStub(async function* () { /* rejected before the body */ }, {
          headers: () => ({ status, 'Content-Type': 'text/plain', body }),
        });
        stubs.push(stub);
        const gw = `qa_t16_${name}`;
        server.addGateway({ id: gw, endpoint: stub.endpoint });
        const cid = `c_qa_t16_${++n}`;
        server.seedConversation({
          id: cid,
          route: gw,
          messages: [{ id: `m_qa_t16_${n}_u0`, role: 'user', content: name, userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
        });
        const mark = ws.frames.length;
        send(ws, { type: 'turn.send', conversationId: cid, payload: { content: `case ${name}` } });
        await waitOpened(ws, { from: mark });
        const terminal = await waitTerminal(ws, { from: mark, timeoutMs: 40_000 });
        return { terminal, calls: stub.requests.length };
      };

      const plain = await runCase({ name: 'provider500', status: 500, body: '' });
      const overloaded = await runCase({ name: 'provider529', status: 529, body: '{"type":"overloaded_error","message":"overloaded"}' });

      const p1 = plain.terminal.payload || {};
      const p2 = overloaded.terminal.payload || {};

      const checks = [
        {
          label: 'a provider 500 keeps its status and is classified `upstream_error`',
          ok: plain.terminal.type === 'turn.failed' && p1.status === 500 && p1.code === 'upstream_error',
          observed: [
            `terminal: ${JSON.stringify(p1)}`,
            `provider calls: ${plain.calls} (500 is transient, so it retries first)`,
          ],
        },
        {
          label: 'a non-standard 529 crosses as 529, not remapped, still upstream',
          ok: overloaded.terminal.type === 'turn.failed' && p2.status === 529 && p2.code === 'upstream_error',
          observed: [`terminal: ${JSON.stringify(p2)}`],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'round 2 candidate: today a provider 500 reports code "internal", which the terminal-frame clause forbids',
        ],
      };
    } finally {
      ws.close();
      for (const s of stubs) await s.close();
      await server.close();
    }
  },
};
