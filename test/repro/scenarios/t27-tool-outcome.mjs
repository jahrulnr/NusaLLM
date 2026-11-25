// T-27 (D-12) — a tool's outcome is data, not a text convention.
//
// Three cases the decision names explicitly:
//   1. `exec_session` exit 3 records `status=failed` and `exit.code=3` — on the
//      live frame (`turn.tool_result`) and in the stored projection — and a
//      client can decide from the fields without reading the text.
//   2. A record written before the field exists has no outcome: it projects as
//      `unknown`, and nothing pretends to know better.
//   3. Text left in the output (`(exit 2)`) does not decide the status: a call
//      recorded with outcome `ok` stays `ok` whatever its text says.
//
// Until the producer lands these checks are red; `expected.json` carries the
// ticket so the run reports PENDING, not a regression.
import { finish, text, toolCall } from '../lib/stub.mjs';
import { connect, messages, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

export default {
  ticket: 'T-27',
  title: 'tool outcome and exit are fields, not text',
  async run({ startServer, startStub }) {
    const server = await startServer({ label: 't27' });
    const ws = await connect(server);
    let stub;
    try {
      // ---- case 1: a live exec_session that exits 3 ----
      stub = await startStub(async function* (body, i) {
        if (i === 0) {
          yield toolCall('call_t27_exit3', 'exec_session', { command: 'exit 3', yield_time_ms: 5000 });
          yield finish('tool_calls');
        } else {
          yield text('done');
          yield finish('stop');
        }
      });
      server.addGateway({ endpoint: stub.endpoint, id: 'stub' });
      const liveID = 'c_qa_t27_live';
      server.seedConversation({
        id: liveID,
        messages: [{ id: 'm_qa_t27_u0', role: 'user', content: 'run exit 3', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });
      const mark = ws.frames.length;
      send(ws, { type: 'turn.send', conversationId: liveID, payload: { content: 'run it' } });
      await waitOpened(ws, { from: mark });
      let resultFrame = null;
      try {
        resultFrame = await ws.next((f) => f.type === 'turn.tool_result' && f.payload?.callId === 'call_t27_exit3',
          { label: 'turn.tool_result', from: mark, timeoutMs: 5000 });
      } catch { /* red until the frame exists */ }
      await waitTerminal(ws, { from: mark, timeoutMs: 30_000 });
      const liveRows = await messages(server, liveID);
      const liveCall = liveRows.flatMap((m) => m.toolCalls || []).find((c) => c.id === 'call_t27_exit3');

      // ---- case 2: a legacy row with no outcome ----
      const legacyID = 'c_qa_t27_legacy';
      server.seedConversation({
        id: legacyID,
        messages: [{
          id: 'm_qa_t27_legacy', role: 'assistant', content: '', finishReason: 'tool_calls', createdAt: '2026-01-02T03:04:06Z',
          parts: [{ kind: 'tool_call', toolCallId: 'call_legacy' }],
          toolCalls: [{ id: 'call_legacy', name: 'exec', input: '{}', output: 'error: boom\n(exit 2)', outputSet: true, callAt: '2026-01-02T03:04:05Z' }],
        }],
      });
      const legacyRows = await messages(server, legacyID);
      const legacyCall = legacyRows.flatMap((m) => m.toolCalls || []).find((c) => c.id === 'call_legacy');

      // ---- case 3: outcome ok, text still says (exit 2) ----
      const textID = 'c_qa_t27_text';
      server.seedConversation({
        id: textID,
        messages: [{
          id: 'm_qa_t27_text', role: 'assistant', content: '', finishReason: 'tool_calls', createdAt: '2026-01-02T03:04:07Z',
          parts: [{ kind: 'tool_call', toolCallId: 'call_text' }],
          toolCalls: [{
            id: 'call_text', name: 'exec', input: '{}', output: 'ran\n(exit 2)', outputSet: true,
            type: 'origin', outcome: 'ok', callAt: '2026-01-02T03:04:05Z',
          }],
        }],
      });
      const textRows = await messages(server, textID);
      const textCall = textRows.flatMap((m) => m.toolCalls || []).find((c) => c.id === 'call_text');

      const checks = [
        {
          label: 'case 1a: the live frame carries status=failed and exit.code=3',
          ok: resultFrame?.payload?.status === 'failed' && resultFrame?.payload?.exit?.code === 3,
          observed: [
            resultFrame ? `frame: ${JSON.stringify(resultFrame.payload)}` : 'turn.tool_result: missing',
          ],
        },
        {
          label: 'case 1b: the stored projection carries the same verdict (no text read)',
          ok: liveCall?.status === 'failed' && liveCall?.exit?.code === 3,
          observed: [
            `stored call: ${JSON.stringify({ status: liveCall?.status, exit: liveCall?.exit ?? null })}`,
            `output tail (evidence only): ${JSON.stringify(String(liveCall?.output || '').slice(-40))}`,
          ],
        },
        {
          label: 'case 2: a pre-D-12 record projects as `unknown`, not a guessed verdict',
          ok: legacyCall?.status === 'unknown',
          observed: [
            `stored call: ${JSON.stringify({ status: legacyCall?.status, exit: legacyCall?.exit ?? null })}`,
            `text still visible: ${JSON.stringify(String(legacyCall?.output || ''))}`,
          ],
        },
        {
          label: 'case 3: text saying (exit 2) does not override a recorded outcome=ok',
          ok: textCall?.status === 'ok',
          observed: [
            `stored call: ${JSON.stringify({ status: textCall?.status, exit: textCall?.exit ?? null, output: textCall?.output })}`,
          ],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'the UI half (toolFailed/splitExit deleted, transcript reads status) is FE; this scenario pins the producer and the projection',
        ],
      };
    } finally {
      ws.close();
      await stub?.close();
      await server.close();
    }
  },
};
