// T-03 row 4 — a later partial usage object merges into the complete one.
//
// Providers repeat a partial `usage` object mid-stream; replacing the previous
// counters with it drops the prompt tokens the turn really spent.
import { finish, text, usage } from '../lib/stub.mjs';
import { connect } from '../lib/turn.mjs';
import { runWireCase } from '../lib/wire.mjs';

// A usage chunk that carries only completion tokens (no prompt object at all).
const partial = (completion) => ({
  id: 'chatcmpl-qa', object: 'chat.completion.chunk', created: 1, model: 'qa-model',
  choices: [], usage: { completion_tokens: completion },
});

export default {
  ticket: 'T-03 row 4',
  title: 'usage: a later partial object merges, it does not zero the counters',
  async run({ startServer }) {
    const server = await startServer({ label: 't03d' });
    const ws = await connect(server);
    let c;
    try {
      c = await runWireCase(server, ws, {
        name: 't03d', style: 'completion',
        handler: async function* () {
          yield text('counted answer');
          yield usage(100, 5);
          yield partial(7);
          yield finish('stop');
        },
      });
      return {
        checks: [{
          label: 'the prompt count from the complete object survives the partial one',
          ok: c.usage?.inputTokens === 100 && c.usage?.outputTokens === 7,
          observed: [
            `conversation usage: ${JSON.stringify(c.usage)}`,
            `expected inputTokens=100 outputTokens=7`,
            `terminal: ${c.terminal.type}`,
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
