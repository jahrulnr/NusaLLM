// T-03 row 6 — a provider 5xx is classified `upstream_error`.
//
// docs/api/websocket.md: "a failure that came from the provider is an upstream
// code — upstream_error for its 5xx (500, 503, and the non-standard 529
// overloaded included)". The status crosses as-is; the code classifies.
import { connect } from '../lib/turn.mjs';
import { runWireCase } from '../lib/wire.mjs';

export default {
  ticket: 'T-03 row 6 (T-16)',
  title: 'a provider 5xx reports `upstream_error`, never `internal`',
  async run({ startServer }) {
    const server = await startServer({ label: 't03f' });
    const ws = await connect(server);
    const stubs = [];
    try {
      const run = async (name, status, body) => {
        const c = await runWireCase(server, ws, {
          name, style: 'completion',
          handler: async function* () { /* rejected before the body */ },
          headers: () => ({ status, 'Content-Type': 'text/plain', body }),
        });
        stubs.push(c.stub);
        return c;
      };
      const plain = await run('t03f500', 500, '');
      const overloaded = await run('t03f529', 529, '{"type":"overloaded_error","message":"overloaded"}');
      const p1 = plain.terminal.payload || {};
      const p2 = overloaded.terminal.payload || {};
      return {
        checks: [
          {
            label: 'a provider 500 keeps its status and is classified `upstream_error`',
            ok: plain.terminal.type === 'turn.failed' && p1.status === 500 && p1.code === 'upstream_error',
            observed: [`terminal: ${JSON.stringify(p1)}`, `provider calls: ${plain.calls}`],
          },
          {
            label: 'a non-standard 529 crosses as 529, still upstream',
            ok: overloaded.terminal.type === 'turn.failed' && p2.status === 529 && p2.code === 'upstream_error',
            observed: [`terminal: ${JSON.stringify(p2)}`],
          },
        ],
        notes: [`scratch root: ${server.root}`],
      };
    } finally {
      ws.close();
      for (const s of stubs) await s.close();
      await server.close();
    }
  },
};
