// T-15 (server precondition) — D-08's client CAS is only sound if the server
// keeps two promises: every meta write bumps `updatedAt`, and a read carries
// the hub-owned `activeTurnId` even though it is not persisted meta.
//
// These are the guarantees the client's version compare orders answers by. The
// client-side rule itself (applyRecord, view keys, one re-read) is tested in
// web/tests/refresh.test.mjs, pending FE round 2.
import { delay, finish, text } from '../lib/stub.mjs';
import { connect, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

export default {
  ticket: 'T-15 (precondition)',
  title: 'every write bumps `updatedAt`; reads carry hub state',
  async run({ startServer, startStub }) {
    const stub = await startStub(async function* () {
      yield text('slow ');
      await delay(400);
      yield text('answer');
      yield finish('stop');
    });
    const server = await startServer({ label: 't15' });
    const ws = await connect(server);
    try {
      server.addGateway({ endpoint: stub.endpoint });
      const cid = 'c_qa_t15';
      server.seedConversation({
        id: cid,
        title: 'before',
        messages: [{ id: 'm_qa_t15_u0', role: 'user', content: 'start', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });

      const before = await server.request('GET', `/api/conversations/${cid}`);
      const t0 = before.json?.updatedAt;

      const renamed = await server.request('PATCH', `/api/conversations/${cid}`, { title: 'after' });
      const t1 = renamed.json?.updatedAt;
      const afterRead = await server.request('GET', `/api/conversations/${cid}`);

      // A turn overriding the binding: the room re-binds on success, which is
      // a meta write and must bump the version the client compares.
      const mark = ws.frames.length;
      send(ws, {
        type: 'turn.send',
        conversationId: cid,
        payload: { content: 'answer slowly', route: 'stub', modelId: 'qa-model' },
      });
      const opened = await waitOpened(ws, { from: mark });
      const during = await server.request('GET', `/api/conversations/${cid}`);
      const terminal = await waitTerminal(ws, { from: mark, timeoutMs: 30_000 });
      const afterTurn = await server.request('GET', `/api/conversations/${cid}`);

      const version = (r) => Date.parse(r?.json?.updatedAt || '') || 0;
      return {
        checks: [
          {
            label: 'a rename bumps `updatedAt` and the new title is what a read returns',
            ok: renamed.status === 200 && afterRead.json?.title === 'after' && version(afterRead) > version(before),
            observed: [
              `before: title=${JSON.stringify(before.json?.title)} updatedAt=${before.json?.updatedAt}`,
              `rename response: status=${renamed.status} title=${JSON.stringify(renamed.json?.title)} updatedAt=${renamed.json?.updatedAt}`,
              `read after rename: title=${JSON.stringify(afterRead.json?.title)} updatedAt=${afterRead.json?.updatedAt}`,
            ],
          },
          {
            label: 'a live turn is visible in the read (`activeTurnId`) though it is not persisted meta',
            ok: during.json?.activeTurnId === opened.turnId && during.json?.activeTurnKind === 'send',
            observed: [
              `during the turn: activeTurnId=${JSON.stringify(during.json?.activeTurnId)} activeTurnKind=${JSON.stringify(during.json?.activeTurnKind)}`,
              `opened turn: ${opened.turnId}`,
              `updatedAt during the turn: ${during.json?.updatedAt}`,
            ],
          },
          {
            label: 'the successful turn re-binds the room and bumps `updatedAt` again',
            ok: terminal.type === 'turn.completed'
              && afterTurn.json?.route === 'stub' && afterTurn.json?.modelId === 'qa-model'
              && version(afterTurn) > version(afterRead),
            observed: [
              `terminal: ${terminal.type}`,
              `route/model: ${JSON.stringify([afterTurn.json?.route, afterTurn.json?.modelId])}`,
              `updatedAt: ${afterRead.json?.updatedAt} → ${afterTurn.json?.updatedAt}`,
            ],
          },
        ],
        notes: [
          `scratch root: ${server.root}`,
          'no client code runs here: this pins the server facts D-08 orders answers by',
        ],
      };
    } finally {
      ws.close();
      await stub.close();
      await server.close();
    }
  },
};
