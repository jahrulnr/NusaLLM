// T-18 (round 2) — a folded steer's stored row must carry the `inputId` that
// named it on the wire.
//
// The server writes a steered input as an ordinary user message and journals a
// `turn.steer` frame with the input's `inputId` (the command's requestId).
// Without that id on the stored row, a client that reloads mid-turn cannot tell
// which history row is the steer the journal replays, so it either doubles the
// bubble or drops it. This is the invariant T-10's merge needs; it is not
// implemented yet, so the assertions are expected to be red.
import { delay, finish, text, toolCall } from '../lib/stub.mjs';
import { connect, messages, send, waitOpened, waitTerminal } from '../lib/turn.mjs';
import { writeFileSync } from 'node:fs';
import path from 'node:path';

export default {
  ticket: 'T-18',
  title: 'the stored fold row of a steer carries its inputId',
  async run({ startServer, startStub }) {
    const stub = await startStub(async function* (body, i) {
      if (i === 0) {
        // Step 1 stays open long enough for the steer to be accepted, then
        // asks for a tool; the steer folds at the top of step 2.
        yield text('working ');
        await delay(500);
        yield toolCall('call_t18', 'read_file', { path: 'probe.txt' });
        yield finish('tool_calls');
        return;
      }
      yield text('done');
      yield finish('stop');
    });
    const server = await startServer({ label: 't18' });
    const ws = await connect(server);
    try {
      writeFileSync(path.join(server.workspace, 'probe.txt'), 'probe\n');
      server.addGateway({ endpoint: stub.endpoint });
      const cid = 'c_qa_t18';
      server.seedConversation({
        id: cid,
        messages: [{ id: 'm_qa_t18_u0', role: 'user', content: 'start', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });

      const mark = ws.frames.length;
      send(ws, { type: 'turn.send', conversationId: cid, payload: { content: 'work slowly' } });
      const opened = await waitOpened(ws, { from: mark });
      await ws.next((f) => f.type === 'turn.delta' && f.turnId === opened.turnId, { label: 'first delta', from: mark });

      const steerId = send(ws, {
        type: 'turn.steer',
        conversationId: cid,
        turnId: opened.turnId,
        payload: { content: 'steered note' },
      });
      const ack = await ws.next((f) => (f.type === 'turn.steer_ack' || f.type === 'turn.steer') && f.payload?.inputId === steerId,
        { label: 'steer ack', from: ws.frames.length - 1 });
      const terminal = await waitTerminal(ws, { from: mark, timeoutMs: 30_000 });
      const rows = await messages(server, cid);

      const steerRow = rows.find((m) => m.role === 'user' && m.content === 'steered note');
      const { historyItems } = await import('../../../web/lib/turn.js');
      const items = historyItems(rows);
      const steerItem = items.find((i) => i.kind === 'user' && i.msg?.content === 'steered note');

      const checks = [
        {
          label: 'the steer was accepted and folded into the turn',
          ok: ack !== undefined && terminal.type === 'turn.completed' && rows.some((m) => m.content === 'steered note'),
          observed: [
            `steer command id: ${steerId}`,
            `ack: ${ack ? ack.type : 'none'}`,
            `terminal: ${terminal.type}`,
            `stored user rows: ${JSON.stringify(rows.filter((m) => m.role === 'user').map((m) => m.content))}`,
          ],
        },
        {
          label: 'the stored fold row carries `inputId` equal to the steer command',
          ok: steerRow !== undefined && steerRow.inputId === steerId,
          observed: [
            `stored row: ${JSON.stringify(steerRow ? { content: steerRow.content, inputId: steerRow.inputId ?? null, userType: steerRow.userType } : null)}`,
            `expected inputId: ${steerId}`,
          ],
        },
        {
          label: 'the projection exposes it (`steerInput`) so the merge can match the journal',
          ok: steerItem !== undefined && steerItem.steerInput === steerId,
          observed: [
            `projected item: ${JSON.stringify(steerItem ? { kind: steerItem.kind, steerInput: steerItem.steerInput ?? null } : null)}`,
          ],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'round 2 work: the field does not exist yet; the scenario is the acceptance test for it',
        ],
      };
    } finally {
      ws.close();
      await stub.close();
      await server.close();
    }
  },
};
