// T-08 — one answer, one Markdown document.
//
// Contract: a persistence boundary is not a presentation boundary. Text the
// store (or an old record) split across neighbouring parts is joined before
// grouping, so the transcript renders one <Markdown> per answer; a real event
// between two texts — a tool, reasoning, a steer — stays a boundary.
//
// The seed is the legacy shape (two adjacent text parts, a fence opened in the
// first and closed in the second). New writes merge those parts at the domain
// layer, so this is the shape a pre-fix record keeps; the render path is the
// same either way.
import { marked } from '../../../web/vendor/marked/marked.esm.js';
import { transcriptItems } from '../lib/turn.mjs';

const FIRST = 'Here is the code:\n\n```js\nconst a = 1;\n';
const SECOND = 'const b = 2;\n```\n\nDone.';

const count = (tokens, type) => tokens.filter((t) => t === type).length;

export default {
  ticket: 'T-08',
  title: 'neighbouring text parts render as one Markdown document',
  async run({ startServer }) {
    const turn = await import('../../../web/lib/turn.js');
    const server = await startServer({ label: 't08' });
    try {
      const cid = 'c_qa_t08';
      server.seedConversation({
        id: cid,
        title: 'split markdown',
        messages: [
          { id: 'm_qa_t08_u0', role: 'user', content: 'show me the diff', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' },
          {
            id: 'm_qa_t08_a0', role: 'assistant', content: FIRST + SECOND, finishReason: 'stop',
            createdAt: '2026-01-02T03:04:06Z',
            parts: [{ kind: 'text', text: FIRST }, { kind: 'text', text: SECOND }],
          },
        ],
      });

      const items = await transcriptItems(server, cid);
      const texts = items.filter((i) => i.kind === 'text');
      const hasJoin = typeof turn.joinText === 'function';
      const joined = hasJoin ? turn.joinText(items) : items;
      const joinedTexts = joined.filter((i) => i.kind === 'text');
      const tokens = joinedTexts.length === 1 ? marked.lexer(joinedTexts[0].text).map((t) => t.type) : [];

      // A tool between two texts is a real event: joinText must not fold them.
      const around = hasJoin ? turn.joinText([
        { id: 'a', kind: 'text', text: 'before' },
        { id: 'b', kind: 'tool', callId: 'c1', name: 'exec' },
        { id: 'c', kind: 'text', text: 'after' },
      ]) : [];

      // The same composition the transcript performs, through the public
      // projection API: a late-watch tail whose replayed step history already
      // holds is dropped, and the two texts around a real event stay apart.
      const replayTurn = turn.newTurn({ turnId: 't_live', conversationId: 'c_live', kind: 'send' });
      const liveHistoryItems = [
        { id: 'u1', kind: 'user', msg: { content: 'go' } },
        { id: 'h1', kind: 'text', text: 'one' },
        { id: 'h2', kind: 'tool', callId: 'call_step1', name: 'exec' },
      ];
      turn.applyFrame(replayTurn, { type: 'turn.delta', payload: { text: 'one' }, seq: 1 });
      turn.applyFrame(replayTurn, { type: 'turn.tool_call', payload: { callId: 'call_step1', name: 'exec' }, seq: 2 });
      turn.applyFrame(replayTurn, { type: 'turn.delta', payload: { text: 'two' }, seq: 3 });
      turn.applyFrame(replayTurn, { type: 'turn.delta', payload: { text: 'three' }, seq: 4 });
      const composed = typeof turn.liveSegments === 'function'
        ? turn.joinText(turn.liveSegments(replayTurn, liveHistoryItems))
        : null;

      const checks = [
        {
          label: 'lib/turn.js exports joinText (the projection seam the fix needs)',
          ok: hasJoin,
          observed: [`joinText: ${typeof turn.joinText}`],
        },
        {
          label: 'the split answer reaches the transcript as one text item',
          ok: hasJoin && joinedTexts.length === 1,
          observed: [
            `history items before join: ${items.map((i) => i.kind).join(' → ')}`,
            `after join: ${joined.map((i) => i.kind + (i.text ? ':' + JSON.stringify(i.text.slice(0, 40)) + '…' : '')).join(' | ')}`,
            `lengths: before ${texts.map((i) => i.text.length).join('+')}, after ${joinedTexts.map((i) => i.text.length).join('+')}`,
          ],
        },
        {
          label: 'the joined text keeps the first id and concatenates without a separator',
          ok: hasJoin && joinedTexts[0]?.id === texts[0]?.id && joinedTexts[0]?.text === FIRST + SECOND,
          observed: [
            `id: ${joinedTexts[0]?.id} (first item was ${texts[0]?.id})`,
            `text equals the stored concatenation: ${joinedTexts[0]?.text === FIRST + SECOND}`,
          ],
        },
        {
          label: 'the joined text parses as one code block, not two',
          ok: tokens.length > 0 && count(tokens, 'code') === 1,
          observed: [
            `marked tokens: ${JSON.stringify(tokens)} → ${count(tokens, 'code')} code block(s)`,
            `rendered head: ${JSON.stringify(joinedTexts[0] ? marked.parse(joinedTexts[0].text).slice(0, 160) : '')}`,
          ],
        },
        {
          label: 'a real event between two texts keeps them apart',
          ok: hasJoin && around.map((i) => i.kind).join(',') === 'text,tool,text',
          observed: [`joinText([text, tool, text]): ${around.map((i) => i.kind).join(' → ')}`],
        },
        {
          label: 'the live path folds the same way: replayed step dropped, its tail one block',
          ok: composed !== null
            && composed.map((i) => i.kind).join(',') === 'text'
            && composed[0].text === 'twothree'
            && !composed.some((i) => i.callId === 'call_step1'),
          observed: [
            `joinText(liveSegments(live, history)): ${composed === null ? 'liveSegments missing' : composed.map((i) => i.kind + (i.text ? ':' + JSON.stringify(i.text) : '')).join(' | ')}`,
            `history tool call present: ${liveHistoryItems.some((i) => i.callId === 'call_step1')}`,
          ],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'the seed is a pre-fix record shape; new messages merge adjacent text parts in SetParts, so the store no longer writes one',
        ],
      };
    } finally {
      await server.close();
    }
  },
};
