// T-01 — file-tool path policy (ADR-0002).
//
// Contract: the workspace repository owns what a path argument means.
// `~` and `file:` URLs are decoded against the host, an absolute path inside
// the workspace resolves relative to it, a read outside the workspace is a
// host read, and a write outside it is an error naming the root — never a
// literal `~` folder. One turn asks for exactly those calls; the stored tool
// rows and the filesystem are the evidence.
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { finish, text, toolCall } from '../lib/stub.mjs';
import { connect, send, waitOpened, waitTerminal, toolRows } from '../lib/turn.mjs';

const hostname = (() => {
  try { return readFileSync('/etc/hostname', 'utf8').trim(); } catch { return null; }
})();

export default {
  ticket: 'T-01',
  title: 'file tools: `~`, `file:` URL, absolute and relative paths',
  async run({ startServer, startStub }) {
    const stub = await startStub(async function* (body, i) {
      if (i === 0) {
        yield toolCall('call_tilde', 'write_file', { path: '~/probe.txt', content: 'qa-probe' });
        yield finish('tool_calls');
      } else if (i === 1) {
        yield toolCall('call_fileurl', 'read_file', { path: 'file:///etc/hostname' });
        yield finish('tool_calls');
      } else if (i === 2) {
        yield toolCall('call_abs', 'read_file', { path: '/etc/hostname' });
        yield finish('tool_calls');
      } else if (i === 3) {
        yield toolCall('call_rel', 'read_file', { path: 'probe.txt' });
        yield finish('tool_calls');
      } else {
        yield text('done');
        yield finish('stop');
      }
    });

    const server = await startServer({ label: 't01' });
    let ws;
    try {
      server.addGateway({ endpoint: stub.endpoint });
      writeFileSync(path.join(server.workspace, 'probe.txt'), 'relative probe\n');
      const cid = 'c_qa_t01';
      server.seedConversation({
        id: cid,
        messages: [{ id: 'm_qa_t01_u0', role: 'user', content: 'run the path probes', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });

      ws = await connect(server);
      send(ws, { type: 'turn.send', conversationId: cid, payload: { content: 'probe the path policy' } });
      await waitOpened(ws);
      const terminal = await waitTerminal(ws);
      const rows = await toolRows(server, cid);
      const row = (id) => rows.find((r) => r.id === id);
      const output = (id) => String(row(id)?.output || '');

      const tildeFile = path.join(server.home, 'probe.txt');
      const literalTilde = path.join(server.workspace, '~');
      const checks = [];

      checks.push({
        label: 'a write through `~` is refused outside the workspace and creates no literal `~` folder',
        ok: output('call_tilde').startsWith('error:') && !existsSync(tildeFile) && !existsSync(literalTilde),
        observed: [
          `output: ${JSON.stringify(output('call_tilde').slice(0, 300))}`,
          `${tildeFile}: ${existsSync(tildeFile) ? 'created (contract violated)' : 'missing'}`,
          `${literalTilde}: ${existsSync(literalTilde) ? 'created (contract violated)' : 'missing'}`,
        ],
      });

      checks.push({
        label: '`file:` URL names a host file and is read',
        ok: hostname !== null && output('call_fileurl').includes(hostname),
        observed: [
          `input: ${JSON.stringify(row('call_fileurl')?.input ?? null)}`,
          `output head: ${JSON.stringify(output('call_fileurl').slice(0, 200))}`,
          `hostname expected in the output: ${hostname !== null}`,
        ],
      });

      checks.push({
        label: 'an absolute host path is a read, not a sandbox escape error',
        ok: hostname !== null && output('call_abs').includes(hostname),
        observed: [
          `input: ${JSON.stringify(row('call_abs')?.input ?? null)}`,
          `output head: ${JSON.stringify(output('call_abs').slice(0, 200))}`,
        ],
      });

      checks.push({
        label: 'a relative path still resolves inside the workspace (control)',
        ok: output('call_rel').includes('relative probe'),
        observed: [`output head: ${JSON.stringify(output('call_rel').slice(0, 200))}`],
      });

      // D-12/T-27 replaced the old status vocabulary: a refused write is
      // `failed`, a successful read is `ok` (the old word was `completed`).
      // This is the contract, not a relaxed assertion.
      checks.push({
        label: 'the turn completes and persists the four probe rows (status per D-12: refused write=failed, reads=ok)',
        ok: terminal.type === 'turn.completed'
          && row('call_tilde')?.status === 'failed'
          && ['call_fileurl', 'call_abs', 'call_rel'].every((id) => row(id)?.status === 'ok'),
        observed: [
          `terminal: ${terminal.type} status=${terminal.payload?.status ?? '-'}`,
          `probe rows: ${['call_tilde', 'call_fileurl', 'call_abs', 'call_rel'].map((id) => `${id}:${row(id)?.name}:${row(id)?.status}`).join(', ')}`,
          `provider calls: ${stub.requests.length}`,
        ],
      });

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'the write is expected to fail: ADR-0002 keeps writes inside the workspace and makes `file:`/absolute paths a read-only privilege',
        ],
      };
    } finally {
      ws?.close();
      await stub.close();
      await server.close();
    }
  },
};
