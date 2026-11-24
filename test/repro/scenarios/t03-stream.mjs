// T-03 — provider stream robustness, one case per wire.
//
// Contract per row (docs/audit/findings-BE.md T-03):
//   messages  — `stop_reason` is terminal evidence without `message_stop`.
//   responses — an in-stream `error` event keeps the provider's wording and
//               its `Retry-After`.
//   completion— a 200 whose body is a JSON error envelope surfaces that
//               message, not "upstream stream truncated".
//   usage     — a later partial usage object merges, it does not zero the
//               counters a complete one reported.
//   tool call — a truncated call with nothing committed fails the step as a
//               retryable 502 instead of silently dropping the call.
import { delay, finish, messages as msg, responses as resp, streamError, text, toolCall, usage } from '../lib/stub.mjs';
import { connect, messages as history, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

const assistant = (rows) => rows.filter((m) => m.role === 'assistant');

export default {
  ticket: 'T-03',
  title: 'wire-specific terminal evidence, error text, usage merge and truncated calls',
  async run({ startServer, startStub }) {
    const server = await startServer({ label: 't03' });
    const ws = await connect(server);
    const stubs = [];
    let n = 0;
    try {
      // runCase wires one gateway + conversation, sends one turn and returns
      // the terminal frame, the persisted messages and the conversation usage.
      const runCase = async ({ name, style, handler, headers }) => {
        const stub = await startStub(handler, { headers });
        stubs.push(stub);
        const gw = `qa_${name}`;
        server.addGateway({ id: gw, endpoint: stub.endpoint, style });
        const cid = `c_qa_t03_${name}`;
        server.seedConversation({
          id: cid,
          route: gw,
          messages: [{ id: `m_qa_t03_${name}_u0`, role: 'user', content: name, userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
        });
        const mark = ws.frames.length;
        send(ws, { type: 'turn.send', conversationId: cid, payload: { content: `case ${name}` } });
        const opened = await waitOpened(ws, { from: mark });
        const terminal = await waitTerminal(ws, { from: mark, timeoutMs: 40_000 });
        const rows = await history(server, cid);
        const detail = await server.request('GET', `/api/conversations/${cid}`);
        return { stub, terminal, rows, usage: detail.json?.usage, calls: stub.requests.length, opened };
      };

      // 1. messages: stop_reason without message_stop.
      const case1 = await runCase({
        name: 'stop_reason',
        style: 'messages',
        handler: async function* () {
          yield msg.start(12);
          yield msg.textStart(0);
          yield msg.text('committed answer');
          yield msg.verdict('end_turn', 5);
          // no message_stop: the provider cut the tail after the verdict
        },
      });

      // 2. responses: an error event carrying a provider wait.
      const case2 = await runCase({
        name: 'responses_error',
        style: 'responses',
        handler: async function* () { yield resp.error('rate_limit_exceeded', 'slow down'); },
        headers: () => ({ 'Retry-After': '120' }),
      });

      // 3. completion: HTTP 200 whose body is a JSON error envelope.
      const case3 = await runCase({
        name: 'error_envelope',
        style: 'completion',
        handler: async function* () { /* never reached: the body is unary */ },
        headers: () => ({
          'Content-Type': 'application/json',
          body: JSON.stringify({ error: { message: 'quota exceeded', type: 'insufficient_quota' } }),
        }),
      });

      // 4. usage: a partial object after a complete one.
      const case4 = await runCase({
        name: 'usage_merge',
        style: 'completion',
        handler: async function* () {
          yield text('counted answer');
          yield usage(100, 5);
          await delay(30);
          yield usage(undefined, 7); // providers repeat a partial object mid-stream
          yield finish('stop');
        },
      });

      // 5. truncated tool call with text already streamed.
      const case5 = await runCase({
        name: 'truncated_call',
        style: 'completion',
        handler: async function* () {
          yield text('let me check');
          yield toolCall('call_cut', 'exec', '{"command":');
          yield finish('tool_calls');
        },
      });

      const checks = [
        {
          label: 'messages: `stop_reason` commits the answer without `message_stop`',
          ok: case1.terminal.type === 'turn.completed' && assistant(case1.rows).length === 1
            && assistant(case1.rows)[0].content === 'committed answer',
          observed: [
            `terminal: ${case1.terminal.type} ${JSON.stringify(case1.terminal.payload || {})}`,
            `assistant rows: ${JSON.stringify(assistant(case1.rows).map((m) => ({ content: m.content, finishReason: m.finishReason })))}`,
            `provider calls: ${case1.calls}`,
          ],
        },
        {
          label: 'responses: an error event keeps the provider wording and the Retry-After',
          ok: case2.terminal.type === 'turn.failed'
            && String(case2.terminal.payload?.message || '').includes('slow down')
            && case2.terminal.payload?.retryAfterMs === 120000,
          observed: [
            `terminal: ${JSON.stringify(case2.terminal.payload || {})}`,
            `provider calls: ${case2.calls}`,
          ],
        },
        {
          label: 'completion: a 200 JSON error envelope surfaces the provider message, not "truncated"',
          ok: case3.terminal.type === 'turn.failed'
            && String(case3.terminal.payload?.message || '').includes('quota exceeded'),
          observed: [
            `terminal: ${JSON.stringify(case3.terminal.payload || {})}`,
            `provider calls: ${case3.calls}`,
          ],
        },
        {
          label: 'usage: a later partial object merges instead of zeroing the complete one',
          ok: case4.usage?.inputTokens === 100 && case4.usage?.outputTokens === 7,
          observed: [
            `conversation usage: ${JSON.stringify(case4.usage)}`,
            `expected inputTokens=100 outputTokens=7`,
            `terminal: ${case4.terminal.type}`,
          ],
        },
        {
          label: 'tool call: a truncated call fails the step as a retryable 502',
          ok: case5.terminal.type === 'turn.failed' && case5.terminal.payload?.status === 502 && case5.calls > 1
            && String(case5.terminal.payload?.message || '').length > 0,
          observed: [
            `terminal: ${JSON.stringify(case5.terminal.payload || {})}`,
            `provider calls: ${case5.calls}`,
            `assistant rows: ${JSON.stringify(assistant(case5.rows).map((m) => ({ content: m.content, toolCalls: (m.toolCalls || []).length })))}`,
          ],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'each case has its own gateway and conversation, so one wire cannot mask another',
        ],
      };
    } finally {
      ws.close();
      for (const s of stubs) await s.close();
      await server.close();
    }
  },
};
