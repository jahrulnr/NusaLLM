// T-03 row 2 — `responses`: an in-stream `error` keeps the provider's wording
// and its `Retry-After`, so the turn fails once with the wait attached instead
// of retrying into a rate limit.
import { responses as resp } from '../lib/stub.mjs';
import { connect } from '../lib/turn.mjs';
import { runWireCase } from '../lib/wire.mjs';

export default {
  ticket: 'T-03 row 2',
  title: 'responses: an error event keeps the wording and `Retry-After`',
  async run({ startServer }) {
    const server = await startServer({ label: 't03b' });
    const ws = await connect(server);
    try {
      const c = await runWireCase(server, ws, {
        name: 't03b', style: 'responses',
        handler: async function* () { yield resp.error('rate_limit_exceeded', 'slow down'); },
        headers: () => ({ 'Retry-After': '120' }),
      });
      const p = c.terminal.payload || {};
      return {
        checks: [
          {
            label: 'the terminal keeps the provider message',
            ok: c.terminal.type === 'turn.failed' && String(p.message || '').includes('slow down'),
            observed: [`terminal: ${JSON.stringify(p)}`],
          },
          {
            label: 'the provider wait reaches the terminal and stops the retry',
            ok: p.status === 429 && p.retryAfterMs === 120000 && c.calls === 1,
            observed: [`status=${p.status} retryAfterMs=${p.retryAfterMs} provider calls=${c.calls}`],
          },
        ],
        notes: [`scratch root: ${server.root}`],
      };
    } finally {
      ws.close();
      await server.close();
    }
  },
};
