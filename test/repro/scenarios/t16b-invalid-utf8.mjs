// T-16b — invalid UTF-8 in `read_file`.
//
// A byte sequence that is not UTF-8 reaches JSON unchanged and is mangled on
// encode (U+FFFD), so the model sees text that is not in the file. The fix
// reports the encoding instead of silently replacing bytes; either way the file
// must not change and the turn must not fail.
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { finish, text, toolCall } from '../lib/stub.mjs';
import { connect, messages, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

export default {
  ticket: 'T-16b',
  title: 'read_file reports invalid UTF-8 instead of mangling it',
  async run({ startServer, startStub }) {
    const server = await startServer({ label: 't16b' });
    const ws = await connect(server);
    const file = path.join(server.workspace, 'binary.dat');
    const raw = Buffer.from([0x61, 0x6c, 0x70, 0x68, 0x61, 0x20, 0xff, 0xfe, 0x20, 0x62, 0x65, 0x74, 0x61, 0x0a]);
    let stub;
    try {
      writeFileSync(file, raw);
      stub = await startStub(async function* (body, i) {
        if (i === 0) {
          yield toolCall('call_utf8', 'read_file', { path: 'binary.dat' });
          yield finish('tool_calls');
        } else {
          yield text('done');
          yield finish('stop');
        }
      });
      server.addGateway({ endpoint: stub.endpoint });
      const cid = 'c_qa_t16b';
      server.seedConversation({
        id: cid,
        messages: [{ id: 'm_qa_t16b_u0', role: 'user', content: 'read the binary file', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });
      const mark = ws.frames.length;
      send(ws, { type: 'turn.send', conversationId: cid, payload: { content: 'read it' } });
      await waitOpened(ws, { from: mark });
      const terminal = await waitTerminal(ws, { from: mark, timeoutMs: 30_000 });
      const rows = await messages(server, cid);
      const call = rows.flatMap((m) => m.toolCalls || []).find((c) => c.id === 'call_utf8');
      const output = String(call?.output || '');
      const unchanged = Buffer.compare(readFileSync(file), raw) === 0;
      return {
        checks: [
          {
            label: 'the tool does not hand the model replacement characters for real bytes',
            ok: !output.includes('\ufffd'),
            observed: [`tool output: ${JSON.stringify(output.slice(0, 240))}`, `contains U+FFFD: ${output.includes('\ufffd')}`],
          },
          {
            label: 'the file is untouched and the turn completes',
            ok: unchanged && terminal.type === 'turn.completed',
            observed: [`file unchanged: ${unchanged}`, `terminal: ${terminal.type} ${JSON.stringify(terminal.payload || {})}`],
          },
        ],
        notes: [`scratch root: ${server.root}`, 'an error naming the encoding is an acceptable fix; silently replacing bytes is not'],
      };
    } finally {
      ws.close();
      await stub?.close();
      await server.close();
    }
  },
};
