// Terminal contract (ADR-0003 §5) — `turn.failed` must be self-describing.
//
// Two cases, both about the frame a user sees when nothing else arrives:
//   1. the provider fails without a message of its own — the terminal must
//      still carry a non-empty, display-safe `message`;
//   2. the provider asks for a wait longer than the harness will sit out —
//      the terminal must carry `retryAfterMs` so the client can disable Retry
//      until the window passes instead of hammering a dead route.
import { connect, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

export default {
  ticket: 'T-06/T-02',
  title: '`turn.failed` carries a message and the provider-asked wait',
  async run({ startServer, startStub }) {
    const server = await startServer({ label: 'tc' });
    const ws = await connect(server);
    const stubs = [];
    let n = 0;
    try {
      const runCase = async ({ name, headers }) => {
        const stub = await startStub(async function* () { /* rejected before the body */ }, { headers });
        stubs.push(stub);
        const gw = `qa_tc_${name}`;
        server.addGateway({ id: gw, endpoint: stub.endpoint });
        const cid = `c_qa_tc_${++n}_${name}`;
        server.seedConversation({
          id: cid,
          route: gw,
          messages: [{ id: `m_qa_tc_${n}_u0`, role: 'user', content: name, userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
        });
        const mark = ws.frames.length;
        send(ws, { type: 'turn.send', conversationId: cid, payload: { content: `case ${name}` } });
        await waitOpened(ws, { from: mark });
        const terminal = await waitTerminal(ws, { from: mark, timeoutMs: 40_000 });
        return { terminal, calls: stub.requests.length };
      };

      // 1. HTTP 500 with an empty body: the provider says nothing at all.
      const emptyReason = await runCase({
        name: 'no_message',
        headers: () => ({ status: 500, 'Content-Type': 'text/plain', body: '' }),
      });

      // 2. HTTP 429 with Retry-After beyond maxRateLimitWait (60 s).
      const longWait = await runCase({
        name: 'long_retry_after',
        headers: () => ({
          status: 429,
          'Retry-After': '120',
          'Content-Type': 'application/json',
          body: JSON.stringify({ error: { message: 'rate limited by provider', type: 'rate_limit_error' } }),
        }),
      });

      const p1 = emptyReason.terminal.payload || {};
      const p2 = longWait.terminal.payload || {};

      const checks = [
        {
          label: 'a provider failure with no message still carries a non-empty terminal message',
          ok: emptyReason.terminal.type === 'turn.failed' && String(p1.message || '').trim().length > 0,
          observed: [
            `terminal: ${emptyReason.terminal.type}`,
            `payload: ${JSON.stringify(p1)}`,
            `provider calls: ${emptyReason.calls} (the 500 is transient, so it retries first)`,
          ],
        },
        {
          label: 'that terminal names a status and a code from the catalog',
          ok: typeof p1.status === 'number' && p1.status >= 400 && String(p1.code || '').length > 0,
          observed: [`status=${p1.status} code=${JSON.stringify(p1.code)}`],
        },
        {
          label: 'the failed send terminal is a self-describing receipt (resyncRequired + userMessagePersisted)',
          ok: p1.resyncRequired === true && p1.userMessagePersisted === true,
          observed: [
            `resyncRequired: ${p1.resyncRequired}`,
            `userMessagePersisted: ${p1.userMessagePersisted}`,
            `keys: ${Object.keys(p1).join(', ')}`,
          ],
        },
        {
          label: 'a `Retry-After` beyond the harness wait is surfaced as `retryAfterMs`',
          ok: longWait.terminal.type === 'turn.failed' && p2.status === 429 && p2.retryAfterMs === 120000,
          observed: [
            `terminal: ${JSON.stringify(p2)}`,
            `provider calls: ${longWait.calls}`,
          ],
        },
        {
          label: 'the long-wait terminal keeps the provider wording',
          ok: String(p2.message || '').toLowerCase().includes('rate limited by provider'),
          observed: [`message: ${JSON.stringify(p2.message || '')}`],
        },
        {
          label: 'no provider wait means no `retryAfterMs` on the terminal',
          ok: !('retryAfterMs' in p1),
          observed: [
            `case 1 payload keys: ${Object.keys(p1).join(', ') || '(none)'}`,
            `retryAfterMs present: ${'retryAfterMs' in p1}`,
          ],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'each case has its own stub, so a retry in one cannot answer the other',
        ],
      };
    } finally {
      ws.close();
      for (const s of stubs) await s.close();
      await server.close();
    }
  },
};
