// Replay exact-fit — a window that fits the queue exactly must still leave the
// socket alive for the next live frame.
//
// `turn.watch` replays as many frames as the socket has free slots. A window of
// exactly that size fills the queue; the next live delta is the one that used
// to hit `enqueue`'s full-queue kill.
//
// The stub is gated: it emits WARMUP deltas, then waits for this test to open
// the gate, so the server's journal is frozen while the exact window is
// computed (an ungated stream advances seq during the round trip and turns the
// watch into a resync). The client pauses its own socket for the replay and for
// the first live frames — the only lever a black-box test has.
//
// Caveat, measured: a loopback socket buffers several MiB, and the whole
// journal is capped at 4 MiB, so the kernel can absorb the replay before the
// server queue ever backs up. A green run means "did not reproduce on this
// host"; the headroom the server must leave is still the contract.
import { delay, finish, text } from '../lib/stub.mjs';
import { connect, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

const CHUNK = 'x'.repeat(48 * 1024);
const WARMUP = 70;
const BUDGET = 64; // free slots on an idle socket (outboundQueueCap)

export default {
  ticket: 'T-17 (exact fit)',
  title: 'a replay of exactly the free budget survives the next live frame',
  async run({ startServer, startStub }) {
    let openGate;
    const gate = new Promise((resolve) => { openGate = resolve; });
    const stub = await startStub(async function* () {
      for (let n = 0; n < WARMUP; n++) yield text(CHUNK);
      await gate; // frozen until the watch has been answered
      for (let n = 0; n < 40; n++) {
        yield text(`${CHUNK}${n}`);
        await delay(20);
      }
      yield finish('stop');
    });
    const server = await startServer({ label: 'tr' });
    const a = await connect(server);
    let b;
    try {
      server.addGateway({ endpoint: stub.endpoint });
      const cid = 'c_qa_tr';
      server.seedConversation({
        id: cid,
        messages: [{ id: 'm_qa_tr_u0', role: 'user', content: 'exact fit', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });
      b = await connect(server);

      const mark = a.frames.length;
      send(a, { type: 'turn.send', conversationId: cid, payload: { content: 'flood exactly' } });
      const opened = await waitOpened(a, { from: mark });
      await a.next((f) => f.type === 'turn.delta' && f.seq >= WARMUP - 1, { label: 'warmup deltas', from: mark });
      await delay(120); // let the last in-flight frames land on the owner
      const lastSeq = a.frames.filter((f) => f.turnId === opened.turnId && typeof f.seq === 'number').at(-1).seq;
      const afterSeq = lastSeq - BUDGET;

      b.pause();
      send(b, { type: 'turn.watch', conversationId: cid, turnId: opened.turnId, afterSeq });
      await delay(150); // the server answers and replays into a paused socket
      openGate();       // live frames start arriving with the queue under pressure
      await delay(500);
      b.resume();

      const closedDuringPause = b.closed;
      let terminalB = null;
      try {
        terminalB = await waitTerminal(b, { from: 0, timeoutMs: 25_000 });
      } catch { /* the assertion below reports the miss */ }
      const terminalA = await waitTerminal(a, { from: mark, timeoutMs: 25_000 }).catch(() => null);

      const framesOnB = b.frames.filter((f) => f.turnId === opened.turnId && typeof f.seq === 'number');
      const replayed = framesOnB.filter((f) => f.seq <= lastSeq).length;

      const checks = [
        {
          label: 'the watch asks for exactly the socket budget, computed while the journal is frozen',
          ok: lastSeq - afterSeq === BUDGET,
          observed: [
            `journal last seq at watch: ${lastSeq}`,
            `afterSeq: ${afterSeq} → window ${lastSeq - afterSeq} (budget ${BUDGET})`,
            `owner frames: ${a.frames.filter((f) => f.turnId === opened.turnId).length}`,
          ],
        },
        {
          label: 'the socket survives the replay plus live frames while paused',
          ok: !closedDuringPause && !b.closed,
          observed: [
            `closed while paused: ${closedDuringPause}`,
            `closed at verdict: ${b.closed}`,
            `client pressure: pause() across the replay and the first 500 ms of live frames`,
          ],
        },
        {
          label: 'the watcher still receives the terminal',
          ok: terminalB?.type === 'turn.completed',
          observed: [
            `terminal on the watcher: ${terminalB ? terminalB.type : 'none'}`,
            `frames on the watcher: ${framesOnB.length} (${replayed} with seq <= ${lastSeq})`,
            `terminal on the owner: ${terminalA ? terminalA.type : 'none'}`,
          ],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'a resync (0 replayed) means the server budget was already below the window — survival is the contract either way',
          'loopback buffers hold the 4 MiB journal, so this scenario can pass without proving headroom',
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
