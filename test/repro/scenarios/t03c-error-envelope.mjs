// T-03 row 3 — a 200 whose body is a JSON error envelope.
//
// Gateways answer quota/overload failures with HTTP 200 and an error body. The
// stream path sees no SSE frame; the user must get the provider's message, not
// "upstream stream truncated".
import { connect } from '../lib/turn.mjs';
import { runWireCase } from '../lib/wire.mjs';

export default {
  ticket: 'T-03 row 3',
  title: 'completion: a 200 JSON error envelope surfaces the provider message',
  async run({ startServer }) {
    const server = await startServer({ label: 't03c' });
    const ws = await connect(server);
    let c;
    try {
      c = await runWireCase(server, ws, {
        name: 't03c', style: 'completion',
        handler: async function* () { /* unary body, not SSE */ },
        headers: () => ({
          'Content-Type': 'application/json',
          body: JSON.stringify({ error: { message: 'quota exceeded', type: 'insufficient_quota' } }),
        }),
      });
      const p = c.terminal.payload || {};
      return {
        checks: [{
          label: 'the provider message survives, and "truncated" does not replace it',
          ok: c.terminal.type === 'turn.failed'
            && String(p.message || '').includes('quota exceeded')
            && !String(p.message || '').includes('truncated'),
          observed: [
            `terminal: ${JSON.stringify(p)}`,
            `provider calls: ${c.calls}`,
          ],
        }],
        notes: [`scratch root: ${server.root}`],
      };
    } finally {
      ws.close();
      await c?.stub?.close();
      await server.close();
    }
  },
};
