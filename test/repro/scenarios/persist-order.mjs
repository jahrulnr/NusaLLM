// Persist boundary (D-06) — `turn.step_finished` is only a safe client cut if a
// failed persist ends the turn.
//
// Scenario: step 1 runs a tool and is stored (the client sees step_finished);
// the store is then made read-only, so step 2's append must fail. The contract
// is that the turn ends failed right there — it must not start another provider
// call on top of a step the store rejected.
import { chmodSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { finish, text, toolCall } from '../lib/stub.mjs';
import { connect, messages, send, waitOpened, waitTerminal } from '../lib/turn.mjs';

export default {
  ticket: 'T-06/D-06',
  title: 'a failed append after step_finished ends the turn',
  async run({ startServer, startStub }) {
    const stub = await startStub(async function* (body, i) {
      if (i <= 1) {
        // Both steps ask for a tool: a third call would only happen if the
        // loop continued past the step whose append failed.
        yield toolCall(`call_step${i + 1}`, 'read_file', { path: 'probe.txt' });
        yield finish('tool_calls');
        return;
      }
      yield text('must never be reached');
      yield finish('stop');
    });
    const server = await startServer({ label: 'tp' });
    const ws = await connect(server);
    const dir = path.join(server.data, 'conversations', 'c_qa_tp');
    let mode = null;
    try {
      writeFileSync(path.join(server.workspace, 'probe.txt'), 'probe\n');
      server.addGateway({ endpoint: stub.endpoint });
      server.seedConversation({
        id: 'c_qa_tp',
        messages: [{ id: 'm_qa_tp_u0', role: 'user', content: 'work in two steps', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }],
      });

      const mark = ws.frames.length;
      send(ws, { type: 'turn.send', conversationId: 'c_qa_tp', payload: { content: 'two steps' } });
      const opened = await waitOpened(ws, { from: mark });
      const firstStep = await ws.next((f) => f.type === 'turn.step_finished' && f.turnId === opened.turnId,
        { label: 'first step_finished', from: mark });

      // Step 1 must be fully stored (its tool output included) before the
      // store is frozen: the failure this test wants is step 2's append, not
      // step 1's own.
      const until = Date.now() + 5000;
      for (;;) {
        const seen = await messages(server, 'c_qa_tp').catch(() => []);
        const call = seen.flatMap((m) => m.toolCalls || []).find((c) => c.id === 'call_step1');
        if (call?.status === 'completed') break;
        if (Date.now() > until) throw new Error('step 1 tool output never reached the store');
        await new Promise((r) => setTimeout(r, 25));
      }

      // Make the conversation directory and its files read-only for the
      // server's user (us). Every later append/save fails with EACCES.
      const { statSync, readdirSync } = await import('node:fs');
      mode = statSync(dir).mode;
      for (const name of readdirSync(dir)) {
        const p = path.join(dir, name);
        try { chmodSync(p, 0o444); } catch { /* the lock file may be gone */ }
      }
      chmodSync(dir, 0o555);

      const terminal = await waitTerminal(ws, { from: mark, timeoutMs: 30_000 });
      const rows = await messages(server, 'c_qa_tp').catch(() => []);

      const checks = [
        {
          label: 'step 1 was stored and announced before the failure',
          ok: firstStep.type === 'turn.step_finished',
          observed: [`first step frame: ${firstStep.type}#${firstStep.seq} ${JSON.stringify(firstStep.payload || {})}`],
        },
        {
          label: 'the turn ends failed instead of continuing past the failed append',
          ok: terminal.type === 'turn.failed' && stub.requests.length === 2,
          observed: [
            `terminal: ${terminal.type} ${JSON.stringify(terminal.payload || {})}`,
            `provider calls: ${stub.requests.length} (step 2's call ran; a third would prove the loop continued)`,
          ],
        },
        {
          label: 'the failure is self-describing (non-empty message, status >= 400)',
          ok: String(terminal.payload?.message || '').trim().length > 0 && Number(terminal.payload?.status) >= 400,
          observed: [`status=${terminal.payload?.status} code=${JSON.stringify(terminal.payload?.code)} message=${JSON.stringify(terminal.payload?.message || '')}`],
        },
        {
          label: 'no step-2 assistant line reached the store, so the last step_finished stays the cut boundary',
          ok: !rows.some((m) => (m.toolCalls || []).some((c) => c.id === 'call_step2')),
          observed: [
            `stored assistant rows: ${JSON.stringify(rows.filter((m) => m.role === 'assistant').map((m) => ({ calls: (m.toolCalls || []).map((c) => c.id), content: m.content })))}`,
          ],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'the store is made read-only (0555/0444) right after step 1 is announced; the harness restores it in teardown',
        ],
      };
    } finally {
      try {
        chmodSync(dir, mode ?? 0o755);
        const { readdirSync } = await import('node:fs');
        for (const name of readdirSync(dir)) {
          try { chmodSync(path.join(dir, name), 0o600); } catch { /* best effort */ }
        }
      } catch { /* teardown must not mask the verdict */ }
      ws.close();
      await stub.close();
      await server.close();
    }
  },
};
