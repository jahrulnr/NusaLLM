// T-01 regressions — the path policy must not break the privileges it is not
// about.
//
// Five checks the round-1 change could quietly break:
//   1. a `~` workspace binding must fail cleanly (never create a literal `~`),
//   2. `read_media` on an absolute host path still works and its stored sha256
//      pin is the file's hash,
//   3. `apply_patch` on a CRLF file keeps the file's line endings,
//   4. `write_file` outside the workspace errors and names the root,
//   5. `exec` is the escape hatch: it may still write outside the workspace.
import { createHash } from 'node:crypto';
import { copyFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { finish, text, toolCall } from '../lib/stub.mjs';
import { connect, messages, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

const sha256 = (file) => createHash('sha256').update(readFileSync(file)).digest('hex');

export default {
  ticket: 'T-01 (regressions)',
  title: 'path policy keeps `~` clean, media pin, CRLF patch, write refusal and exec escape',
  async run({ startServer, startStub }) {
    const server = await startServer({ label: 't01b' });
    const ws = await connect(server);
    let ws2;
    let stub;
    try {
      // Files outside the workspace, inside the scratch root.
      const outside = path.join(server.root, 'outside');
      const escape = path.join(server.root, 'escape');
      mkdirSync(outside, { recursive: true });
      mkdirSync(escape, { recursive: true });
      const photo = path.join(outside, 'photo.png');
      copyFileSync(new URL('../../media/image.png', import.meta.url), photo);

      const crlfPath = path.join(server.workspace, 'crlf.txt');
      writeFileSync(crlfPath, 'alpha\r\nbeta\r\ndelta\r\n');

      stub = await startStub(async function* (body, i) {
        if (i === 0) {
          yield toolCall('call_out_write', 'write_file', { path: path.join(server.home, 'escape.txt'), content: 'nope' });
          yield finish('tool_calls');
        } else if (i === 1) {
          yield toolCall('call_media', 'read_media', { path: photo });
          yield finish('tool_calls');
        } else if (i === 2) {
          yield toolCall('call_patch', 'apply_patch', { path: 'crlf.txt', old_string: 'beta', new_string: 'GAMMA' });
          yield finish('tool_calls');
        } else if (i === 3) {
          yield toolCall('call_exec', 'exec', { command: `printf 'escaped' > ${JSON.stringify(path.join(escape, 'written.txt'))}` });
          yield finish('tool_calls');
        } else {
          yield text('done');
          yield finish('stop');
        }
      });
      server.addGateway({ endpoint: stub.endpoint });

      const cid = 'c_qa_t01b';
      server.seedConversation({
        id: cid,
        messages: [{ id: 'm_qa_t01b_u0', role: 'user', content: 'run the regressions', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });

      const mark = ws.frames.length;
      send(ws, { type: 'turn.send', conversationId: cid, payload: { content: 'check the path policy edges' } });
      await waitOpened(ws, { from: mark });
      const terminal = await waitTerminal(ws, { from: mark, timeoutMs: 40_000 });
      const rows = await messages(server, cid);
      const row = (id) => rows.flatMap((m) => m.toolCalls || []).find((c) => c.id === id);
      const output = (id) => String(row(id)?.output || '');
      const stored = server.readConversationFile(cid, 'active.jsonl') || '';
      const records = stored.split('\n').filter(Boolean).map((line) => JSON.parse(line));
      const mediaOutput = records.find((rec) => rec.type === 'tool_output' && rec.callId === 'call_media');
      const mediaPin = mediaOutput?.attachments?.[0];

      const checks = [
        {
          label: 'write_file outside the workspace errors and names the root',
          ok: output('call_out_write').startsWith('error:') && output('call_out_write').includes(server.workspace)
            && !existsSync(path.join(server.home, 'escape.txt')),
          observed: [
            `output: ${JSON.stringify(output('call_out_write').slice(0, 320))}`,
            `names the workspace root: ${output('call_out_write').includes(server.workspace)}`,
            `file created: ${existsSync(path.join(server.home, 'escape.txt'))}`,
          ],
        },
        {
          label: 'read_media on an absolute host path works and its stored sha256 is the file hash',
          ok: output('call_media').startsWith('loaded image') && mediaPin?.sha256 === sha256(photo),
          observed: [
            `output: ${JSON.stringify(output('call_media').slice(0, 200))}`,
            `stored pin: ${mediaPin?.sha256 ?? 'none'}`,
            `file hash: ${sha256(photo)}`,
            `uri: ${mediaPin?.uri ?? row('call_media')?.attachments?.[0]?.uri ?? 'none'}`,
          ],
        },
        {
          label: 'apply_patch on a CRLF file keeps every line ending',
          ok: output('call_patch').startsWith('patched crlf.txt')
            && readFileSync(crlfPath, 'utf8') === 'alpha\r\nGAMMA\r\ndelta\r\n',
          observed: [
            `output: ${JSON.stringify(output('call_patch'))}`,
            `bytes: ${JSON.stringify(readFileSync(crlfPath, 'latin1'))}`,
            `CRLF count: ${(readFileSync(crlfPath, 'latin1').match(/\r\n/g) || []).length}`,
          ],
        },
        {
          label: 'exec still writes outside the workspace (the escape hatch stays open)',
          ok: output('call_exec').trim() === '' && existsSync(path.join(escape, 'written.txt'))
            && readFileSync(path.join(escape, 'written.txt'), 'utf8') === 'escaped',
          observed: [
            `output: ${JSON.stringify(output('call_exec').slice(0, 200))}`,
            `file: ${existsSync(path.join(escape, 'written.txt'))}`,
            `content: ${JSON.stringify(existsSync(path.join(escape, 'written.txt')) ? readFileSync(path.join(escape, 'written.txt'), 'utf8') : '')}`,
          ],
        },
        {
          label: 'the turn completed',
          ok: terminal.type === 'turn.completed',
          observed: [`terminal: ${terminal.type} ${JSON.stringify(terminal.payload || {})}`, `provider calls: ${stub.requests.length}`],
        },
      ];

      // A `~` workspace binding is a directory binding, not a tool argument:
      // it must be refused, never turned into a literal `~` folder.
      ws2 = await connect(server);
      const cid2 = 'c_qa_t01b_binding';
      server.seedConversation({
        id: cid2,
        messages: [{ id: 'm_qa_t01b_u1', role: 'user', content: 'bind home', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });
      const mark2 = ws2.frames.length;
      send(ws2, {
        type: 'turn.send',
        conversationId: cid2,
        payload: { content: 'bind a tilde workspace', workspace: '~/ws' },
      });
      await waitOpened(ws2, { from: mark2 });
      const binding = await waitTerminal(ws2, { from: mark2, timeoutMs: 20_000 });
      checks.push({
        label: 'a `~` workspace binding is refused without creating a literal `~` folder',
        ok: binding.type === 'turn.failed' && String(binding.payload?.message || '').length > 0
          && !existsSync(path.join(server.root, '~')) && !existsSync(path.join(server.home, '~')),
        observed: [
          `terminal: ${binding.type} ${JSON.stringify(binding.payload || {})}`,
          `literal ~ under the launch dir: ${existsSync(path.join(server.root, '~'))}`,
          `literal ~ under home: ${existsSync(path.join(server.home, '~'))}`,
          'docs say payload.workspace is an optional absolute directory; `~` is not expanded for a binding',
        ],
      });

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'the sha256 pin is read from the stored record (the HTTP projection keeps it internal by design)',
        ],
      };
    } finally {
      ws.close();
      ws2?.close();
      await stub?.close();
      await server.close();
    }
  },
};
