// T-03 row 1 — `messages`: `stop_reason` is terminal evidence.
//
// The Anthropic wire may cut the tail after `message_delta.stop_reason` and
// never send `message_stop`. The verdict is the semantic terminal: the answer
// commits instead of being discarded as a truncated stream.
import { messages as msg } from '../lib/stub.mjs';
import { connect } from '../lib/turn.mjs';
import { assistant, runWireCase } from '../lib/wire.mjs';

export default {
  ticket: 'T-03 row 1',
  title: 'messages: `stop_reason` without `message_stop` commits the answer',
  async run({ startServer }) {
    const server = await startServer({ label: 't03a' });
    const ws = await connect(server);
    try {
      const c = await runWireCase(server, ws, {
        name: 't03a', style: 'messages',
        handler: async function* () {
          yield msg.start(12);
          yield msg.textStart(0);
          yield msg.text('committed answer');
          yield msg.verdict('end_turn', 5);
          // no message_stop: the provider cut the tail after the verdict
        },
      });
      const rows = assistant(c.rows);
      return {
        checks: [{
          label: 'the verdict commits the answer without `message_stop`',
          ok: c.terminal.type === 'turn.completed' && rows.length === 1 && rows[0].content === 'committed answer',
          observed: [
            `terminal: ${c.terminal.type} ${JSON.stringify(c.terminal.payload || {})}`,
            `assistant rows: ${JSON.stringify(rows.map((m) => ({ content: m.content, finishReason: m.finishReason })))}`,
            `provider calls: ${c.calls}`,
          ],
        }],
        notes: [`scratch root: ${server.root}`],
      };
    } finally {
      ws.close();
      await server.close();
    }
  },
};
