// T-17 — a journal replay that does not fit the socket's queue must not kill
// the socket.
//
// `enqueue` kills a socket whose 64-frame outbound queue fills, so a
// `turn.watch afterSeq:-1` over a longer journal used to answer, flood, and
// disconnect the watcher — which reconnects into the same death. Contract: a
// replay larger than the free budget answers `turn.resync_required` with no
// replayed frames, then attaches while the turn is live.
import { delay, finish, text } from '../lib/stub.mjs';
import { connect, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

const CHUNKS = 90; // journal > outboundQueueCap (64) before the watch

export default {
  ticket: 'T-17',
  title: '`turn.watch` replay is bounded by the outbound queue',
  async run({ startServer, startStub }) {
    const stub = await startStub(async function* (body, i) {
      for (let n = 0; n < CHUNKS; n++) yield text(`chunk-${n} `);
      await delay(1500); // the watcher attaches while the turn is still live
      yield text('tail-one ');
      await delay(200);
      yield text('tail-two');
      yield finish('stop');
    });
    const server = await startServer({ label: 't17' });
    const a = await connect(server);
    let b;
    try {
      server.addGateway({ endpoint: stub.endpoint });
      const cid = 'c_qa_t17';
      server.seedConversation({
        id: cid,
        messages: [{ id: 'm_qa_t17_u0', role: 'user', content: 'flood the journal', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });
      b = await connect(server);

      const mark = a.frames.length;
      send(a, { type: 'turn.send', conversationId: cid, payload: { content: 'answer in many chunks' } });
      const opened = await waitOpened(a, { from: mark });
      // The journal must be well past the queue cap before the watch.
      await a.next((f) => f.type === 'turn.delta' && f.seq >= CHUNKS - 5, { label: `delta seq ${CHUNKS - 5}`, from: mark });
      const journaled = a.frames.filter((f) => f.turnId === opened.turnId).length;

      send(b, { type: 'turn.watch', conversationId: cid, turnId: opened.turnId, afterSeq: -1 });
      const resync = await b.next((f) => f.type === 'turn.resync_required', { label: 'resync for the oversized replay' });
      const bMark = b.frames.length;
      const liveAfter = await b.next(
        (f) => (f.type === 'turn.delta' || f.type === 'turn.step_finished') && f.turnId === opened.turnId,
        { label: 'live frame after the resync', from: bMark },
      );
      const terminalB = await waitTerminal(b, { from: bMark, timeoutMs: 20_000 });
      const terminalA = await waitTerminal(a, { from: mark });
      const sentToB = b.frames.filter((f) => f.turnId === opened.turnId && f.seq !== undefined);

      const checks = [
        {
          label: 'the journal outgrew the queue cap before the watch',
          ok: journaled > 64,
          observed: [`frames journaled for the turn: ${journaled} (cap 64)`, `provider calls: ${stub.requests.length}`],
        },
        {
          label: 'the oversized replay answers `turn.resync_required` instead of replaying',
          ok: resync.type === 'turn.resync_required',
          observed: [`payload: ${JSON.stringify(resync.payload)}`],
        },
        {
          label: 'no journal frame is replayed into the socket',
          ok: sentToB.every((f) => f.seq > CHUNKS - 5),
          observed: [
            `frames for the turn after the watch: ${sentToB.length}`,
            `first seq after the watch: ${sentToB[0]?.seq}`,
            `replayed (seq <= ${CHUNKS - 5}): ${sentToB.filter((f) => f.seq <= CHUNKS - 5).length}`,
          ],
        },
        {
          label: 'the socket survives and reaches the terminal',
          ok: terminalB.type === 'turn.completed' && !b.closed && liveAfter !== undefined,
          observed: [
            `live frame: ${liveAfter.type}#${liveAfter.seq} ${JSON.stringify(liveAfter.payload?.text || '')}`,
            `terminal: ${terminalB.type} status=${terminalB.payload?.status ?? '-'}`,
            `watcher socket open: ${!b.closed}`,
            `owning socket terminal: ${terminalA.type}`,
          ],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'a kill would be visible as a closed socket without a terminal frame — the check reads both',
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
