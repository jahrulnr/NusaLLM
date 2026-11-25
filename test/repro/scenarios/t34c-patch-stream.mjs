// T-34c — the read's tolerance extends to the patch stream and to unknown
// record kinds, and every skipped record leaves a diagnostic.
//
// Three shapes SA found still open:
//   1. a broken `tool_output` patch line must not fail the read; the call keeps
//      the output its message record already holds.
//   2. a line whose `Type` is unknown is ignored — but with a diagnostic, not
//      silently, so a reader of the log knows a record was dropped.
//   3. control: repair stays strict (repository level, BE). This kit has no
//      HTTP route that rewrites the stream, so that case is named here, not
//      faked.
//
// Until BE reports T-34c these checks are red; `expected.json` carries it.
import { appendFileSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { startServer } from '../lib/serve.mjs';

export default {
  ticket: 'T-34c',
  title: 'a corrupt patch line or an unknown record kind does not fail the read',
  async run({ startServer: boot }) {
    const server = await boot({ label: 't34c' });
    try {
      // 1. The call is stored with an output; a broken patch line follows it.
      const patchID = 'c_qa_t34c_patch';
      const patchDir = server.seedConversation({
        id: patchID,
        messages: [{
          id: 'm_patch', role: 'assistant', content: '', finishReason: 'tool_calls',
          createdAt: '2026-01-02T03:04:06Z',
          parts: [{ kind: 'tool_call', toolCallId: 'call_p' }],
          toolCalls: [{
            id: 'call_p', name: 'exec', input: '{}', output: 'first output', outputSet: true,
            type: 'origin', outcome: 'ok', callAt: '2026-01-02T03:04:05Z',
          }],
        }],
      });
      appendFileSync(path.join(patchDir, 'active.jsonl'), '{"type":"tool_output","callId":"call_p","output":\n');
      const patchRead = await server.request('GET', `/api/conversations/${patchID}/messages?start=0&limit=10`);
      const patchCall = (patchRead.json?.messages || []).flatMap((m) => m.toolCalls || []).find((c) => c.id === 'call_p');

      // 2. A line whose Type names nothing this version knows.
      const kindID = 'c_qa_t34c_kind';
      const kindDir = server.seedConversation({
        id: kindID,
        messages: [{ id: 'm_kind_ok', role: 'user', content: 'still readable', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });
      appendFileSync(path.join(kindDir, 'active.jsonl'), '{"type":"mystery","data":{"id":"m_mystery"}}\n');
      const kindRead = await server.request('GET', `/api/conversations/${kindID}/messages?start=0&limit=10`);
      const kindIDs = (kindRead.json?.messages || []).map((m) => m.id);
      const logs = server.logs.join('');

      // Control only: a normal stream still reads exactly.
      const okID = 'c_qa_t34c_ok';
      server.seedConversation({
        id: okID,
        messages: [{ id: 'm_ok', role: 'user', content: 'fine', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });
      const okRead = await server.request('GET', `/api/conversations/${okID}/messages?start=0&limit=10`);

      const checks = [
        {
          label: 'a broken patch line does not fail the read; the call keeps its recorded output',
          ok: patchRead.status === 200 && patchCall?.output === 'first output',
          observed: [
            `GET status: ${patchRead.status}`,
            `call_p: ${JSON.stringify(patchCall ? { status: patchCall.status, output: patchCall.output } : null)}`,
          ],
        },
        {
          label: 'an unknown record kind is skipped and the rest loads',
          ok: kindRead.status === 200 && JSON.stringify(kindIDs) === JSON.stringify(['m_kind_ok']),
          observed: [
            `GET status: ${kindRead.status}`,
            `messages: ${JSON.stringify(kindIDs)}`,
          ],
        },
        {
          label: 'the skipped unknown record leaves a diagnostic in the server log',
          ok: /mystery|unknown record|line skipped/i.test(logs),
          observed: [
            `log tail: ${JSON.stringify(logs.slice(-400))}`,
            'a silent drop is the defect: the reader of the log must see what was skipped',
          ],
        },
        {
          label: 'control: a normal stream still reads exactly',
          ok: okRead.status === 200 && JSON.stringify((okRead.json?.messages || []).map((m) => m.id)) === JSON.stringify(['m_ok']),
          observed: [`GET status: ${okRead.status}`],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'repair strictness (strictDecode in repair.go) is a repository-level assertion owned by BE; the kit has no HTTP route that rewrites the stream',
        ],
      };
    } finally {
      await server.close();
    }
  },
};
