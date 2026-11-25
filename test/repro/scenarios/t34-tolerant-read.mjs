// T-34 — one unreadable record must not fail the whole history read.
//
// The write path stays strict, but a read tolerates per record: a tool call
// without its `type` projects `unknown` while its neighbours stay normal, and a
// record that cannot be decoded at all is skipped with a diagnostic instead of
// turning the whole `GET …/messages` into a 500. This scenario pins the read
// side; the strict write side is a repository-level assertion (there is no
// black-box way to append a malformed record without a production seam).
//
// Until the fix lands the checks are red; `expected.json` carries T-34.
import { finish, text, toolCall } from '../lib/stub.mjs';
import { appendFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { connect, messages, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

// read records the status instead of throwing: today the whole read is the bug,
// and a scenario must report that as a red check, not a harness error.
const read = async (server, cid) => {
  const res = await server.request('GET', `/api/conversations/${cid}/messages?start=0&limit=100`);
  return { status: res.status, messages: res.json?.messages || [] };
};

export default {
  ticket: 'T-34',
  title: 'an unreadable record degrades one call, not the history read',
  async run({ startServer, startStub }) {
    const server = await startServer({ label: 't34' });
    const ws = await connect(server);
    let stub;
    try {
      // ---- case 1: one tool call without `type`, one healthy call ----
      const missingID = 'c_qa_t34_type';
      server.seedConversation({
        id: missingID,
        messages: [{
          id: 'm_qa_t34_type', role: 'assistant', content: '', finishReason: 'tool_calls',
          createdAt: '2026-01-02T03:04:06Z',
          parts: [
            { kind: 'tool_call', toolCallId: 'call_good' },
            { kind: 'tool_call', toolCallId: 'call_notype' },
            { kind: 'tool_call', toolCallId: 'call_badoutcome' },
          ],
          toolCalls: [
            { id: 'call_good', name: 'exec', input: '{}', output: 'ok', outputSet: true, type: 'origin', outcome: 'ok', callAt: '2026-01-02T03:04:05Z' },
            { id: 'call_notype', name: 'exec', input: '{}', output: 'ok', outputSet: true, callAt: '2026-01-02T03:04:05Z' },
            { id: 'call_badoutcome', name: 'exec', input: '{}', output: 'ran', outputSet: true, type: 'origin', outcome: 'bogus', callAt: '2026-01-02T03:04:05Z' },
          ],
        }],
      });
      const typeRead = await read(server, missingID);

      // ---- case 2: a record that cannot be decoded at all ----
      const lineID = 'c_qa_t34_badline';
      const lineDir = server.seedConversation({
        id: lineID,
        messages: [{ id: 'm_ok', role: 'user', content: 'still readable', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });
      // A line that is not even JSON: skipped, the rest of the file loads.
      // Repairs stay strict (repository level, BE): this kit has no HTTP route
      // that rewrites the stream.
      appendFileSync(path.join(lineDir, 'active.jsonl'), '{not a json line}\n');
      const lineRead = await read(server, lineID);

      // ---- control: a plain turn still works after a tolerant read ----
      stub = await startStub(async function* (body, i) {
        if (i === 0) {
          yield toolCall('call_t34', 'read_file', { path: 'probe.txt' });
          yield finish('tool_calls');
        } else {
          yield text('done');
          yield finish('stop');
        }
      });
      server.addGateway({ endpoint: stub.endpoint });
      writeFileSync(path.join(server.workspace, 'probe.txt'), 'probe\n');
      const liveID = 'c_qa_t34_live';
      server.seedConversation({
        id: liveID,
        messages: [{ id: 'm_live_u0', role: 'user', content: 'go', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });
      const mark = ws.frames.length;
      send(ws, { type: 'turn.send', conversationId: liveID, payload: { content: 'read it' } });
      await waitOpened(ws, { from: mark });
      const terminal = await waitTerminal(ws, { from: mark, timeoutMs: 30_000 });

      const typeCall = (id) => typeRead.messages.flatMap((m) => m.toolCalls || []).find((c) => c.id === id);

      const checks = [
        {
          label: 'a call without `type` projects type/status `unknown`, its neighbour stays normal',
          ok: typeRead.status === 200
            && typeCall('call_notype')?.type === 'unknown' && typeCall('call_notype')?.status === 'unknown'
            && typeCall('call_good')?.type === 'origin' && typeCall('call_good')?.status === 'ok',
          observed: [
            `GET status: ${typeRead.status}`,
            `call_notype: ${JSON.stringify({ type: typeCall('call_notype')?.type ?? null, status: typeCall('call_notype')?.status })}`,
            `call_good: ${JSON.stringify({ type: typeCall('call_good')?.type, status: typeCall('call_good')?.status })}`,
          ],
        },
        {
          label: 'an unknown outcome degrades to `unknown` too, and the readable calls survive',
          ok: typeRead.status === 200 && typeCall('call_badoutcome')?.status === 'unknown'
            && typeCall('call_badoutcome')?.type === 'unknown',
          observed: [
            `call_badoutcome: ${JSON.stringify({ type: typeCall('call_badoutcome')?.type ?? null, status: typeCall('call_badoutcome')?.status })}`,
            `call_good still ok: ${typeCall('call_good')?.status === 'ok'}`,
          ],
        },
        {
          label: 'a line that is not JSON is skipped and the rest of the history loads',
          ok: lineRead.status === 200 && lineRead.messages.some((m) => m.id === 'm_ok'),
          observed: [
            `GET status: ${lineRead.status}`,
            `messages read: ${JSON.stringify(lineRead.messages.map((m) => m.id))}`,
          ],
        },
        {
          label: 'a normal turn still persists and completes (the tolerant read did not weaken writes)',
          ok: terminal.type === 'turn.completed'
            && (await messages(server, liveID)).flatMap((m) => m.toolCalls || []).some((c) => c.id === 'call_t34' && c.status === 'ok'),
          observed: [
            `terminal: ${terminal.type}`,
            `stored call: ${JSON.stringify((await messages(server, liveID)).flatMap((m) => m.toolCalls || []).find((c) => c.id === 'call_t34') || null)}`,
          ],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'write-path strictness for the same malformed payload is asserted at the repository level; this kit only drives HTTP/WS',
        ],
      };
    } finally {
      ws.close();
      await stub?.close();
      await server.close();
    }
  },
};
