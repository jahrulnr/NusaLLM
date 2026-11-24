// T-10 — a late watcher merges the journal with stored history, once.
//
// Contract: history is authoritative for every finished step, the journal
// supplies what history does not hold, and nothing is drawn twice or dropped.
// The trim must key on the turn, not on "the last user row" — a page loaded
// before the turn started must not lose the previous answer.
//
// The merge helpers (`liveHistory`, `liveSegments`) are the seam the FE fix
// lands in lib/turn.js; when they are absent the checks report the missing
// contract instead of guessing a shape.
import { writeFileSync } from 'node:fs';
import path from 'node:path';
import { delay, finish, text, toolCall } from '../lib/stub.mjs';
import { connect, send, waitOpened, waitTerminal, transcriptItems } from '../lib/turn.mjs';

const PREV_ANSWER = 'First answer.';
const STEP1 = 'Step one.\n\n';

export default {
  ticket: 'T-10',
  title: 'a late watcher draws history and journal once',
  async run({ startServer, startStub }) {
    const turn = await import('../../../web/lib/turn.js');
    const stub = await startStub(async function* (body, i) {
      if (i === 0) {
        yield text(STEP1);
        yield toolCall('call_step1', 'read_file', { path: 'probe.txt' });
        yield finish('tool_calls');
        return;
      }
      await delay(500); // the second tab attaches before step 2 ends
      for (const chunk of ['Step two ', 'streams ', 'here.']) {
        yield text(chunk);
        await delay(250);
      }
      yield finish('stop');
    });
    const server = await startServer({ label: 't10' });
    const a = await connect(server);
    let b;
    try {
      writeFileSync(path.join(server.workspace, 'probe.txt'), 'probe\n');
      server.addGateway({ endpoint: stub.endpoint });
      const cid = 'c_qa_t10';
      server.seedConversation({
        id: cid,
        messages: [
          { id: 'm_qa_t10_u0', role: 'user', content: 'first ask', userType: 'origin', createdAt: '2026-01-02T03:04:01Z' },
          { id: 'm_qa_t10_a0', role: 'assistant', content: PREV_ANSWER, finishReason: 'stop', createdAt: '2026-01-02T03:04:02Z',
            parts: [{ kind: 'text', text: PREV_ANSWER }] },
        ],
      });

      // Tab B opened this room before the turn existed.
      const stale = await transcriptItems(server, cid);

      b = await connect(server);
      const markA = a.frames.length;
      send(a, { type: 'turn.send', conversationId: cid, payload: { content: 'second ask' } });
      const opened = await waitOpened(a, { from: markA });
      await a.next((f) => f.type === 'turn.step_finished' && f.turnId === opened.turnId,
        { label: 'first step finished', from: markA });

      // Tab B reads history now (step 1 is stored) and attaches from -1.
      const midHistory = await transcriptItems(server, cid);
      const markB = b.frames.length;
      send(b, { type: 'turn.watch', conversationId: cid, turnId: opened.turnId, afterSeq: -1 });
      await b.next((f) => f.type === 'turn.completed' && f.turnId === opened.turnId,
        { label: 'terminal on the late watcher', timeoutMs: 30_000, from: markB });
      const replay = b.frames.slice(markB).filter((f) => f.turnId === opened.turnId && typeof f.seq === 'number');
      await waitTerminal(a, { from: markA });

      const live = turn.newTurn({ turnId: opened.turnId, conversationId: cid, kind: 'send' });
      for (const f of replay) turn.applyFrame(live, f);
      const liveText = live.segments.filter((s) => s.kind === 'text').map((s) => s.text).join('|');
      const histText = midHistory.filter((i) => i.kind === 'text').map((i) => i.text).join('|');
      const histCalls = midHistory.filter((i) => i.kind === 'tool').map((i) => i.callId);

      const hasMerge = typeof turn.liveHistory === 'function' && typeof turn.liveSegments === 'function';
      const lateLive = { kind: 'send' };
      const keptA = hasMerge ? turn.liveHistory(midHistory, lateLive) : midHistory;
      const tailA = hasMerge ? turn.liveSegments(live, midHistory) : live.segments;
      const keptB = hasMerge ? turn.liveHistory(stale, lateLive) : stale;
      const screenText = [...keptA.filter((i) => i.kind === 'text').map((i) => i.text),
        ...tailA.filter((s) => s.kind === 'text').map((s) => s.text)].join('|');

      const occurrences = (haystack, needle) => haystack.split(needle).length - 1;

      const checks = [
        {
          label: 'lib/turn.js exports the merge seam (liveHistory + liveSegments)',
          ok: hasMerge,
          observed: [
            `liveHistory: ${typeof turn.liveHistory}`,
            `liveSegments: ${typeof turn.liveSegments}`,
          ],
        },
        {
          label: 'A: a plain send trims nothing from history',
          ok: hasMerge && JSON.stringify(keptA.map((i) => i.id)) === JSON.stringify(midHistory.map((i) => i.id)),
          observed: [
            `history ids: ${midHistory.map((i) => i.id).join(', ')}`,
            `kept ids: ${keptA.map((i) => i.id).join(', ')}`,
          ],
        },
        {
          label: 'A: the replayed prefix history already holds is not drawn again',
          ok: hasMerge && tailA.filter((s) => s.kind === 'text').map((s) => s.text).join('') === 'Step two streams here.'
            && !tailA.some((s) => s.callId === 'call_step1'),
          observed: [
            `journal text: ${JSON.stringify(liveText)}`,
            `history text: ${JSON.stringify(histText)}`,
            `live tail after the merge: ${tailA.map((s) => s.kind + (s.text ? ':' + JSON.stringify(s.text) : s.callId ? ':' + s.callId : '')).join(' | ')}`,
          ],
        },
        {
          label: 'A: the composed screen carries step 1 prose exactly once and its tool row once',
          ok: hasMerge && occurrences(screenText, STEP1) === 1 && midHistory.filter((i) => i.callId === 'call_step1').length === 1
            && !tailA.some((s) => s.callId === 'call_step1') && histCalls.includes('call_step1'),
          observed: [
            `composed text: ${JSON.stringify(screenText)}`,
            `"${STEP1.trim()}" occurrences: ${occurrences(screenText, STEP1)}`,
            `call_step1 in history: ${histCalls.includes('call_step1')}, in live tail: ${tailA.some((s) => s.callId === 'call_step1')}`,
          ],
        },
        {
          label: 'B: a page loaded before the turn keeps the previous answer (the trim does not cut it)',
          ok: hasMerge && keptB.map((i) => i.id).join(',') === stale.map((i) => i.id).join(','),
          observed: [
            `stale page ids: ${stale.map((i) => i.id).join(', ')}`,
            `after liveHistory: ${keptB.map((i) => i.id).join(', ')}`,
            `previous answer still drawn: ${keptB.some((i) => i.text === PREV_ANSWER)}`,
          ],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'the compose step mirrors the transcript: history (after liveHistory) then the live tail (liveSegments)',
        ],
      };
    } finally {
      a.close();
      b?.close();
      await stub.close();
      await server.close();
    }
  },
};
