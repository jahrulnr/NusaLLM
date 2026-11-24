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

      // The renderer must call it on both lists, not only on history.
      const transcriptSrc = (await import('node:fs')).readFileSync(new URL('../../../web/components/transcript.js', import.meta.url), 'utf8');
      const wired = /group\(\s*joinText\(hist\)/.test(transcriptSrc) && /group\(\s*joinText\(liveItems\)/.test(transcriptSrc);

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
          label: 'the transcript calls joinText on history and on the live segments',
          ok: wired,
          observed: [
            `group(joinText(hist)): ${/group\(\s*joinText\(hist\)/.test(transcriptSrc)}`,
            `group(joinText(liveItems)): ${/group\(\s*joinText\(liveItems\)/.test(transcriptSrc)}`,
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
