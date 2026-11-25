// T-33 — `exit.signal` is the signal's name, and the text keeps its label.
//
// The wire value is bare (`"killed"`), because the Go error's `"signal: "`
// prefix is an artifact; renderers add their own label. The tool's output text
// still ends with `(signal: killed)` — moving the value must not silently
// delete the label a reader depends on. Both are asserted together so neither
// side can be "fixed" alone.
import { finish, text, toolCall } from '../lib/stub.mjs';
import { connect, messages, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

const SIGNAL_TEXT = /\(signal: killed\)\s*$/;

export default {
  ticket: 'T-33',
  title: 'a signal death reports `killed`, and the text keeps `(signal: killed)`',
  async run({ startServer, startStub }) {
    const server = await startServer({ label: 't33' });
    const ws = await connect(server);
    let stub;
    try {
      stub = await startStub(async function* (body, i) {
        if (i === 0) {
          yield toolCall('call_exec_signal', 'exec', { command: 'kill -9 $$' });
          yield finish('tool_calls');
        } else if (i === 1) {
          yield toolCall('call_session_signal', 'exec_session', { command: 'kill -9 $$', yield_time_ms: 5000 });
          yield finish('tool_calls');
        } else {
          yield text('done');
          yield finish('stop');
        }
      });
      server.addGateway({ endpoint: stub.endpoint });
      const cid = 'c_qa_t33';
      server.seedConversation({
        id: cid,
        messages: [{ id: 'm_qa_t33_u0', role: 'user', content: 'die by signal', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });

      const mark = ws.frames.length;
      send(ws, { type: 'turn.send', conversationId: cid, payload: { content: 'kill both' } });
      await waitOpened(ws, { from: mark });
      await waitTerminal(ws, { from: mark, timeoutMs: 30_000 });

      const rows = await messages(server, cid);
      const call = (id) => rows.flatMap((m) => m.toolCalls || []).find((c) => c.id === id);
      const frames = ws.frames.slice(mark);
      const frame = (id) => frames.find((f) => f.type === 'turn.tool_result' && f.payload?.callId === id);

      const checks = [
        {
          label: 'exec: the wire value is `killed`, with no `signal: ` prefix',
          ok: call('call_exec_signal')?.exit?.signal === 'killed',
          observed: [
            `stored exit: ${JSON.stringify(call('call_exec_signal')?.exit ?? null)}`,
            `live frame: ${JSON.stringify(frame('call_exec_signal')?.payload ?? null)}`,
            `has prefix: ${String(call('call_exec_signal')?.exit?.signal || '').startsWith('signal:')}`,
          ],
        },
        {
          label: 'exec: the output text still ends with `(signal: killed)`',
          ok: SIGNAL_TEXT.test(String(call('call_exec_signal')?.output || '')),
          observed: [`output tail: ${JSON.stringify(String(call('call_exec_signal')?.output || '').slice(-80))}`],
        },
        {
          label: 'exec_session: the same bare value and the same text label',
          ok: call('call_session_signal')?.exit?.signal === 'killed'
            && SIGNAL_TEXT.test(String(call('call_session_signal')?.output || '')),
          observed: [
            `stored exit: ${JSON.stringify(call('call_session_signal')?.exit ?? null)}`,
            `output tail: ${JSON.stringify(String(call('call_session_signal')?.output || '').slice(-80))}`,
          ],
        },
        {
          label: 'the turn completed with both calls stored',
          ok: call('call_exec_signal') !== undefined && call('call_session_signal') !== undefined,
          observed: [`provider calls: ${stub.requests.length}`],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'the signal name is bare on the wire and labelled in the text; both assertions live here so neither side moves alone',
        ],
      };
    } finally {
      ws.close();
      await stub?.close();
      await server.close();
    }
  },
};
