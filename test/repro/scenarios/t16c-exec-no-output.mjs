// T-16c — a silent `exec`.
//
// A command that succeeds without printing anything returns an empty tool
// result, indistinguishable from a tool that produced nothing at all. The
// contract: the result says the command exited 0.
import { finish, text, toolCall } from '../lib/stub.mjs';
import { connect, messages, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

export default {
  ticket: 'T-16c',
  title: 'a silent exec reports its exit status',
  async run({ startServer, startStub }) {
    const server = await startServer({ label: 't16c' });
    const ws = await connect(server);
    let stub;
    try {
      stub = await startStub(async function* (body, i) {
        if (i === 0) {
          yield toolCall('call_silent', 'exec', { command: 'true' });
          yield finish('tool_calls');
        } else {
          yield text('done');
          yield finish('stop');
        }
      });
      server.addGateway({ endpoint: stub.endpoint });
      const cid = 'c_qa_t16c';
      server.seedConversation({
        id: cid,
        messages: [{ id: 'm_qa_t16c_u0', role: 'user', content: 'run a silent command', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });
      const mark = ws.frames.length;
      send(ws, { type: 'turn.send', conversationId: cid, payload: { content: 'run true' } });
      await waitOpened(ws, { from: mark });
      const terminal = await waitTerminal(ws, { from: mark, timeoutMs: 30_000 });
      const rows = await messages(server, cid);
      const call = rows.flatMap((m) => m.toolCalls || []).find((c) => c.id === 'call_silent');
      const output = String(call?.output || '');
      return {
        checks: [{
          label: 'the silent command reports that it exited 0',
          ok: terminal.type === 'turn.completed' && /exit(ed)?\s*0/i.test(output),
          observed: [
            `tool output: ${JSON.stringify(output)}`,
            `status: ${call?.status}`,
            `terminal: ${terminal.type}`,
          ],
        }],
        notes: [`scratch root: ${server.root}`, 'an empty result and "I ran it, exit 0" are different facts to a model'],
      };
    } finally {
      ws.close();
      await stub?.close();
      await server.close();
    }
  },
};
