// T-02 — a transient failure after streamed output must not kill the turn.
//
// Contract (ADR-0003 §1-2): the retry gate is what the store holds, not what
// the screen shows. An attempt that streamed a delta and then failed with a
// 429 is retried, `turn.phase {phase:"retry_wait", retryAfterMs}` announces the
// pause, and only the surviving attempt is persisted — one assistant line, one
// user line.
import { delay, finish, streamError, text } from '../lib/stub.mjs';
import { connect, messages, send, toolRows, waitOpened, waitTerminal } from '../lib/turn.mjs';

export default {
  ticket: 'T-02',
  title: 'a 429 after the first delta is retried',
  async run({ startServer, startStub }) {
    const stub = await startStub(async function* (body, i) {
      if (i === 0) {
        yield text('partial answer ');
        await delay(50);
        yield streamError('slow down', 'rate_limit_error');
        return;
      }
      yield text('full answer');
      yield finish('stop');
    }, {
      // The provider asks for a short pause; the job honours it and retries.
      headers: (body, i) => (i === 0 ? { 'Retry-After': '1' } : {}),
    });

    const server = await startServer({ label: 't02' });
    let ws;
    try {
      server.addGateway({ endpoint: stub.endpoint });
      const cid = 'c_qa_t02';
      server.seedConversation({
        id: cid,
        messages: [{ id: 'm_qa_t02_u0', role: 'user', content: 'first prompt', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });

      ws = await connect(server);
      const mark = ws.frames.length;
      send(ws, { type: 'turn.send', conversationId: cid, payload: { content: 'answer even if it is slow' } });
      const opened = await waitOpened(ws, { from: mark });
      const terminal = await waitTerminal(ws, { from: mark, timeoutMs: 30_000 });
      const phase = ws.frames.find((f) => f.type === 'turn.phase' && f.turnId === opened.turnId && f.payload?.phase === 'retry_wait');
      const rows = await messages(server, cid);
      const assistant = rows.filter((m) => m.role === 'assistant');
      const users = rows.filter((m) => m.role === 'user');

      const checks = [
        {
          label: 'the failed attempt is retried (two provider calls)',
          ok: stub.requests.length === 2,
          observed: [
            `provider calls: ${stub.requests.length}`,
            `first request stream flag: ${JSON.stringify(stub.requests[0]?.body?.stream)}`,
            `second request's message count: ${stub.requests[1]?.body?.messages?.length ?? '-'}`,
          ],
        },
        {
          label: '`retry_wait` is announced with the provider-asked pause',
          ok: phase !== undefined && phase.payload.retryAfterMs === 1000,
          observed: [
            `phase frame: ${phase ? JSON.stringify(phase.payload) : 'missing'}`,
            `frames in order: ${ws.frames.filter((f) => f.turnId === opened.turnId).map((f) => f.type).join(' → ')}`,
          ],
        },
        {
          label: 'exactly one assistant line is persisted, and it is the surviving attempt',
          ok: assistant.length === 1 && assistant[0].content === 'full answer',
          observed: [
            `assistant messages: ${assistant.length}`,
            `contents: ${JSON.stringify(assistant.map((m) => ({ content: m.content, finishReason: m.finishReason })))}`,
          ],
        },
        {
          label: 'no duplicate user line and no abandoned tail in the store',
          ok: users.length === 2 && !rows.some((m) => String(m.content).includes('partial answer')),
          observed: [
            `user messages: ${users.length} (${users.map((m) => JSON.stringify(m.content)).join(', ')})`,
            `any stored "partial answer": ${rows.some((m) => String(m.content).includes('partial answer'))}`,
          ],
        },
        {
          label: 'the turn completes',
          ok: terminal.type === 'turn.completed',
          observed: [`terminal: ${terminal.type} ${JSON.stringify(terminal.payload || {})}`],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'the 429 rides an in-stream error event after the first delta, which is how a provider cuts a live stream',
        ],
      };
    } finally {
      ws?.close();
      await stub.close();
      await server.close();
    }
  },
};
