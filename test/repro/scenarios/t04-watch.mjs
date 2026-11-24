// T-04 — an unsatisfiable `turn.watch` on a live turn attaches.
//
// Contract (ADR-0003 §4): an `afterSeq` ahead of the journal answers
// `turn.resync_required` and then joins the subscriber set under the same
// lock, so the watching socket still receives the rest of the stream and the
// terminal. The control run watches with an honest `afterSeq` and must still
// replay + attach.
import { delay, finish, text } from '../lib/stub.mjs';
import { connect, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

const streamOf = async function* (label) {
  for (const chunk of [`${label}-one `, `${label}-two `, `${label}-three`]) {
    yield text(chunk);
    await delay(250);
  }
  yield finish('stop');
};

export default {
  ticket: 'T-04',
  title: '`turn.watch` with an impossible afterSeq keeps the socket attached',
  async run({ startServer, startStub }) {
    const stub = await startStub(async function* (body, i) {
      yield* streamOf(i === 0 ? 'first' : 'second');
    });
    const server = await startServer({ label: 't04' });
    const a = await connect(server);
    let b;
    try {
      server.addGateway({ endpoint: stub.endpoint });
      const cid = 'c_qa_t04';
      server.seedConversation({
        id: cid,
        messages: [{ id: 'm_qa_t04_u0', role: 'user', content: 'start the turn', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });
      b = await connect(server);

      // ---- turn 1: the impossible watch ----
      let mark = a.frames.length;
      send(a, { type: 'turn.send', conversationId: cid, payload: { content: 'stream slowly' } });
      const opened1 = await waitOpened(a, { from: mark });
      await a.next((f) => f.type === 'turn.delta' && f.turnId === opened1.turnId, { label: 'first delta on the owning socket', from: mark });

      send(b, { type: 'turn.watch', conversationId: cid, turnId: opened1.turnId, afterSeq: 10_000 });
      const resync = await b.next((f) => f.type === 'turn.resync_required', { label: 'resync for the impossible watch' });
      const bMark = b.frames.length;
      const afterResync = await b.next(
        (f) => f.type === 'turn.delta' && f.turnId === opened1.turnId,
        { label: 'live delta after the resync', from: bMark },
      );
      const terminal1 = await waitTerminal(b, { from: bMark });
      const liveOnB = b.frames.slice(bMark).filter((f) => f.turnId === opened1.turnId && f.seq !== undefined);
      await waitTerminal(a, { from: mark });

      // ---- control: the same shape, an honest afterSeq ----
      mark = a.frames.length;
      send(a, { type: 'turn.send', conversationId: cid, payload: { content: 'stream again' } });
      const opened2 = await waitOpened(a, { from: mark });
      if (opened2.turnId === opened1.turnId) throw new Error('second turn.send reused the first turnId: the harness matched a stale frame');
      await a.next((f) => f.type === 'turn.delta' && f.turnId === opened2.turnId, { label: 'first delta of turn 2', from: mark });
      const bCountBeforeControl = b.frames.length;
      send(b, { type: 'turn.watch', conversationId: cid, turnId: opened2.turnId, afterSeq: 0 });
      await b.next((f) => f.type === 'turn.delta' && f.turnId === opened2.turnId, { label: 'replayed delta on the control watch', from: bCountBeforeControl });
      const controlTerminal = await b.next(
        (f) => f.type === 'turn.completed' && f.turnId === opened2.turnId,
        { label: 'live terminal after the control watch', from: bCountBeforeControl },
      );
      await waitTerminal(a, { from: mark });

      // A finished turn: the journal either replays through the terminal or
      // answers resync — it never attaches to a turn that is over.
      const finishedMark = b.frames.length;
      send(b, { type: 'turn.watch', conversationId: cid, turnId: opened2.turnId, afterSeq: -1 });
      const finishedReply = await b.next(
        (f) => (f.type === 'turn.completed' && f.turnId === opened2.turnId) || f.type === 'turn.resync_required',
        { label: 'reply for a finished turn', from: finishedMark },
      );

      // An unknown turn cannot be replayed: resync with the unknown verdict.
      const unknownMark = b.frames.length;
      send(b, { type: 'turn.watch', conversationId: cid, turnId: 't_qa_never_existed', afterSeq: 0 });
      const unknown = await b.next((f) => f.type === 'turn.resync_required', { label: 'resync for an unknown turn', from: unknownMark });

      const checks = [
        {
          label: 'an afterSeq past the turn answers `turn.resync_required`',
          ok: resync.type === 'turn.resync_required',
          observed: [`payload: ${JSON.stringify(resync.payload)}`, `turnId: ${resync.turnId}`],
        },
        {
          label: 'the watcher keeps receiving the live turn after the snapshot',
          ok: afterResync.type === 'turn.delta' && liveOnB.length > 0,
          observed: [
            `first live frame after the resync: ${afterResync.type}#${afterResync.seq} ${JSON.stringify(afterResync.payload?.text || '')}`,
            `frames for the turn after the snapshot: ${liveOnB.length} (${liveOnB.map((f) => f.type + '#' + f.seq).join(', ')})`,
          ],
        },
        {
          label: 'the watcher reaches the terminal frame on the same socket',
          ok: terminal1.type === 'turn.completed' && !b.closed,
          observed: [
            `terminal: ${terminal1.type} status=${terminal1.payload?.status ?? '-'}`,
            `socket still open: ${!b.closed}`,
          ],
        },
        {
          label: 'control: an honest afterSeq replays and attaches (the same socket hears the live terminal)',
          ok: controlTerminal.type === 'turn.completed',
          observed: [
            `live terminal after the control watch: ${controlTerminal.type}#${controlTerminal.seq}`,
            `provider calls: ${stub.requests.length}`,
          ],
        },
        {
          label: 'watching a finished turn replays through its terminal (or answers resync), never a silent attach',
          ok: finishedReply.type === 'turn.completed' || finishedReply.type === 'turn.resync_required',
          observed: [`reply: ${finishedReply.type}${finishedReply.seq !== undefined ? '#' + finishedReply.seq : ''}`],
        },
        {
          label: 'an unknown turn answers `turn.resync_required` with the unknown verdict',
          ok: unknown.type === 'turn.resync_required' && unknown.payload?.turn === 'unknown',
          observed: [`payload: ${JSON.stringify(unknown.payload)}`],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'frames were counted after the resync frame, so a replayed journal cannot be mistaken for live delivery',
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
