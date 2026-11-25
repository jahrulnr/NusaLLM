// T-34 extension — a decodable line whose message cannot hydrate.
//
// T-34 fixed three shapes: a tool call without `type`, an unknown outcome, and
// a line that is not JSON. This is the remaining one: the JSON parses, but the
// message itself cannot be built (an unknown role). One such record must not
// turn the whole history read into a 500 either — the same rule, one level up.
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';

export default {
  ticket: 'T-34 extension',
  title: 'an unreadable message record is skipped, not fatal to the read',
  async run({ startServer: boot }) {
    const server = await boot({ label: 't34b' });
    try {
      const cid = 'c_qa_t34b';
      server.seedConversation({
        id: cid,
        messages: [
          { id: 'm_ok', role: 'user', content: 'still readable', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' },
          { id: 'm_bad', role: 'wizard', content: 'not a role', createdAt: '2026-01-02T03:04:06Z' },
        ],
      });
      // A broken line *between* two healthy records: skipping must not cut the
      // tail, and the order of what remains must be untouched.
      const midID = 'c_qa_t34b_mid';
      const midDir = server.seedConversation({
        id: midID,
        messages: [
          { id: 'm_mid1', role: 'user', content: 'one', userType: 'origin', createdAt: '2026-01-02T03:04:01Z' },
          { id: 'm_mid2', role: 'user', content: 'two', userType: 'origin', createdAt: '2026-01-02T03:04:02Z' },
          { id: 'm_mid3', role: 'user', content: 'three', userType: 'origin', createdAt: '2026-01-02T03:04:03Z' },
        ],
      });
      const stream = readFileSync(path.join(midDir, 'active.jsonl'), 'utf8').split('\n').filter(Boolean);
      stream.splice(1, 0, '{broken in the middle');
      writeFileSync(path.join(midDir, 'active.jsonl'), stream.join('\n') + '\n');
      const mid = await server.request('GET', `/api/conversations/${midID}/messages?start=0&limit=10`);
      const midIDs = (mid.json?.messages || []).map((m) => m.id);

      const res = await server.request('GET', `/api/conversations/${cid}/messages?start=0&limit=10`);
      const ids = (res.json?.messages || []).map((m) => m.id);
      return {
        checks: [
          {
            label: 'the read answers 200 with the good record and without the bad one',
            ok: res.status === 200 && ids.includes('m_ok') && !ids.includes('m_bad'),
            observed: [
              `GET status: ${res.status}`,
              `messages: ${JSON.stringify(ids)}`,
              `body: ${JSON.stringify((res.json || res.text).toString().slice(0, 160))}`,
            ],
          },
          {
            label: 'a broken line in the middle skips only itself: both neighbours load, in order',
            ok: mid.status === 200 && JSON.stringify(midIDs) === JSON.stringify(['m_mid1', 'm_mid2', 'm_mid3']),
            observed: [
              `GET status: ${mid.status}`,
              `messages: ${JSON.stringify(midIDs)}`,
              'a skip that truncates the tail would report only m_mid1',
            ],
          },
        ],
        notes: [
          `scratch root: ${server.root}`,
          'the three named T-34 shapes live in t34; this is the message-level shape left open',
        ],
      };
    } finally {
      await server.close();
    }
  },
};
