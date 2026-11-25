// T-30 — `exec_session` must end a finished poll with the same exit marker
// `exec` writes, or a nonzero exit reads as success.
//
// The text marker is the model-facing evidence: a finished poll must end with
// the same "(exit N)" / "(signal: X)" that `exec` writes, or a failed command
// reads as successful. The verdict itself is data since D-12 — `status` and
// `exit` on the stored call — so both are asserted here, and nothing reads the
// header wording.
import { finish, text, toolCall } from '../lib/stub.mjs';
import { connect, messages, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

const EXIT3 = /\(exit 3\)\s*$/;

export default {
  ticket: 'T-30',
  title: 'exec_session ends a finished poll with the exec exit marker',
  async run({ startServer, startStub }) {
    const server = await startServer({ label: 't30' });
    const ws = await connect(server);
    let stub;
    try {
      stub = await startStub(async function* (body, i) {
        if (i === 0) {
          yield toolCall('call_session_exit3', 'exec_session', { command: 'exit 3', yield_time_ms: 5000 });
          yield finish('tool_calls');
        } else if (i === 1) {
          yield toolCall('call_exec_exit3', 'exec', { command: 'exit 3' });
          yield finish('tool_calls');
        } else if (i === 2) {
          yield toolCall('call_session_ok', 'exec_session', { command: 'true', yield_time_ms: 5000 });
          yield finish('tool_calls');
        } else {
          yield text('done');
          yield finish('stop');
        }
      });
      server.addGateway({ endpoint: stub.endpoint });
      const cid = 'c_qa_t30';
      server.seedConversation({
        id: cid,
        messages: [{ id: 'm_qa_t30_u0', role: 'user', content: 'run the exit probes', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });

      const mark = ws.frames.length;
      send(ws, { type: 'turn.send', conversationId: cid, payload: { content: 'probe exit markers' } });
      await waitOpened(ws, { from: mark });
      const terminal = await waitTerminal(ws, { from: mark, timeoutMs: 40_000 });
      const rows = await messages(server, cid);
      const row = (id) => rows.flatMap((m) => m.toolCalls || []).find((c) => c.id === id);
      const output = (id) => String(row(id)?.output || '');
      const session3 = output('call_session_exit3');
      const exec3 = output('call_exec_exit3');
      const sessionOK = output('call_session_ok');

      const checks = [
        {
          label: 'a finished exec_session poll ends with the exec exit marker',
          ok: EXIT3.test(session3),
          observed: [
            `exec_session tail: ${JSON.stringify(session3.slice(-120))}`,
            `ends with "(exit 3)": ${EXIT3.test(session3)}`,
          ],
        },
        {
          label: 'the same command through exec carries the same marker',
          ok: EXIT3.test(exec3),
          observed: [`exec tail: ${JSON.stringify(exec3.slice(-120))}`],
        },
        {
          label: 'the stored verdict says failed with exit.code=3, and the success stays ok',
          ok: row('call_session_exit3')?.status === 'failed' && row('call_session_exit3')?.exit?.code === 3
            && row('call_exec_exit3')?.status === 'failed' && row('call_exec_exit3')?.exit?.code === 3
            && row('call_session_ok')?.status === 'ok',
          observed: [
            `exec_session exit 3: ${JSON.stringify({ status: row('call_session_exit3')?.status, exit: row('call_session_exit3')?.exit ?? null })}`,
            `exec exit 3: ${JSON.stringify({ status: row('call_exec_exit3')?.status, exit: row('call_exec_exit3')?.exit ?? null })}`,
            `exec_session true: ${JSON.stringify({ status: row('call_session_ok')?.status, exit: row('call_session_ok')?.exit ?? null })} — tail ${JSON.stringify(sessionOK.slice(-60))}`,
          ],
        },
        {
          label: 'the turn completed with all three calls recorded',
          ok: terminal.type === 'turn.completed' && ['call_session_exit3', 'call_exec_exit3', 'call_session_ok']
            .every((id) => rows.flatMap((m) => m.toolCalls || []).some((c) => c.id === id)),
          observed: [
            `terminal: ${terminal.type}`,
            `provider calls: ${stub.requests.length}`,
            `stored call ids: ${JSON.stringify(rows.flatMap((m) => (m.toolCalls || []).map((c) => c.id)))}`,
          ],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'the header wording ("Process exited with code 3") is not asserted; the trailing marker and the failure verdict are',
          'when T-27 lands this scenario gains status/exit assertions and stops reading the text',
        ],
      };
    } finally {
      ws.close();
      await stub?.close();
      await server.close();
    }
  },
};
