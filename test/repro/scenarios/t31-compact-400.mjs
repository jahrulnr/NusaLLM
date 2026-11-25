// T-31 — "can compact" has one answer, and it is 400, not 500.
//
// A room with no new foldable history cannot be compacted. The HTTP route
// queues the same job the CLI does, and the job is the single decider: this
// scenario proves the status a client sees (400 with a problem code) and that
// the projection agrees (`canCompact: false`), so the two surfaces cannot drift.
import { connect } from '../lib/turn.mjs';

export default {
  ticket: 'T-31',
  title: 'compacting a room with nothing to fold answers 400 on every path',
  async run({ startServer }) {
    const server = await startServer({ label: 't31' });
    const ws = await connect(server);
    try {
      const cid = 'c_qa_t31';
      server.seedConversation({
        id: cid,
        messages: [{ id: 'm_qa_t31_u0', role: 'user', content: 'one short message', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });

      const detail = await server.request('GET', `/api/conversations/${cid}`);
      const first = await server.request('POST', `/api/conversations/${cid}/compactions`);
      const second = await server.request('POST', `/api/conversations/${cid}/compactions`);

      const checks = [
        {
          label: 'the projection already says the room cannot be compacted',
          ok: detail.status === 200 && detail.json?.canCompact === false,
          observed: [`GET canCompact: ${JSON.stringify(detail.json?.canCompact)}`, `GET status: ${detail.status}`],
        },
        {
          label: 'compaction answers 400 with a problem code (not 500)',
          ok: first.status === 400 && typeof first.json?.code === 'string' && first.json.code.length > 0,
          observed: [
            `POST status: ${first.status}`,
            `body: ${JSON.stringify(first.json || first.text.slice(0, 200))}`,
          ],
        },
        {
          label: 'the answer is stable: a second request answers the same way',
          ok: second.status === first.status && second.json?.code === first.json?.code,
          observed: [`second POST: ${second.status} ${JSON.stringify(second.json?.code)}`],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'the HTTP route queues the same job the CLI runs, so this status is the job path answering; there is no separate handler verdict to compare',
        ],
      };
    } finally {
      ws.close();
      await server.close();
    }
  },
};
