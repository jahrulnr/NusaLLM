// T-03 row 5 — a truncated tool call is an error, not a silent drop.
//
// The provider closed the stream while the call's arguments were still
// incomplete. Committing the text and dropping the call hides a planned action
// from history; with nothing committed the step must fail as a retryable 502.
import { finish, text, toolCall } from '../lib/stub.mjs';
import { connect } from '../lib/turn.mjs';
import { assistant, runWireCase } from '../lib/wire.mjs';

export default {
  ticket: 'T-03 row 5',
  title: 'completion: a truncated tool call fails the step as a retryable 502',
  async run({ startServer }) {
    const server = await startServer({ label: 't03e' });
    const ws = await connect(server);
    try {
      const c = await runWireCase(server, ws, {
        name: 't03e', style: 'completion',
        handler: async function* () {
          yield text('let me check');
          yield toolCall('call_cut', 'exec', '{"command":');
          yield finish('tool_calls');
        },
      });
      const p = c.terminal.payload || {};
      const rows = assistant(c.rows);
      return {
        checks: [
          {
            label: 'the turn fails with 502 instead of completing',
            ok: c.terminal.type === 'turn.failed' && p.status === 502,
            observed: [`terminal: ${JSON.stringify(p)}`],
          },
          {
            label: 'the 502 is retried (more than one provider call)',
            ok: c.calls > 1,
            observed: [`provider calls: ${c.calls}`, `rows: ${JSON.stringify(rows.map((m) => ({ content: m.content, calls: (m.toolCalls || []).map((x) => x.id) })))}`],
          },
          {
            label: 'no assistant row claims the truncated call',
            ok: !rows.some((m) => (m.toolCalls || []).some((x) => x.id === 'call_cut')),
            observed: [`stored call ids: ${JSON.stringify(rows.flatMap((m) => (m.toolCalls || []).map((x) => x.id)))}`],
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
