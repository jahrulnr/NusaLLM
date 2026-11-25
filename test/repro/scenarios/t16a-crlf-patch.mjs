// T-16a — `apply_patch` on a CRLF file.
//
// A model writes an anchor with LF endings; the file uses CRLF. Comparing raw
// strings never matches, so the edit fails on a file the model just read. The
// contract: an LF anchor matches its CRLF counterpart and the file keeps its
// line endings.
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { finish, text, toolCall } from '../lib/stub.mjs';
import { connect, messages, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

export default {
  ticket: 'T-16a',
  title: 'apply_patch matches an LF anchor in a CRLF file',
  async run({ startServer, startStub }) {
    const server = await startServer({ label: 't16a' });
    const ws = await connect(server);
    const file = path.join(server.workspace, 'crlf.txt');
    let stub;
    try {
      writeFileSync(file, 'alpha\r\nbeta\r\ndelta\r\n');
      stub = await startStub(async function* (body, i) {
        if (i === 0) {
          yield toolCall('call_crlf', 'apply_patch', { path: 'crlf.txt', old_string: 'beta\n', new_string: 'GAMMA\n' });
          yield finish('tool_calls');
        } else {
          yield text('done');
          yield finish('stop');
        }
      });
      server.addGateway({ endpoint: stub.endpoint });
      const cid = 'c_qa_t16a';
      server.seedConversation({
        id: cid,
        messages: [{ id: 'm_qa_t16a_u0', role: 'user', content: 'patch the CRLF file', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });
      const mark = ws.frames.length;
      send(ws, { type: 'turn.send', conversationId: cid, payload: { content: 'patch it' } });
      await waitOpened(ws, { from: mark });
      const terminal = await waitTerminal(ws, { from: mark, timeoutMs: 30_000 });
      const rows = await messages(server, cid);
      const call = rows.flatMap((m) => m.toolCalls || []).find((c) => c.id === 'call_crlf');
      const bytes = readFileSync(file, 'latin1');
      return {
        checks: [{
          label: 'the LF anchor patches the CRLF file and every ending survives',
          ok: terminal.type === 'turn.completed' && String(call?.output || '').startsWith('patched crlf.txt') && bytes === 'alpha\r\nGAMMA\r\ndelta\r\n',
          observed: [
            `tool output: ${JSON.stringify(String(call?.output || '').slice(0, 200))}`,
            `file bytes: ${JSON.stringify(bytes)}`,
            `terminal: ${terminal.type}`,
          ],
        }],
        notes: [`scratch root: ${server.root}`, 'the anchor is LF while the file is CRLF — the raw-string compare is what this pins'],
      };
    } finally {
      ws.close();
      await stub?.close();
      await server.close();
    }
  },
};
