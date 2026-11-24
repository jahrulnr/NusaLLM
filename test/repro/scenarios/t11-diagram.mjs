// T-11 — a diagram block without its renderer structure must not abort the
// transcript.
//
// The payload is renderer-shaped HTML the sanitizer allows (div, class,
// data-diagram) but the renderer never produced: `decorate` used to read
// `details pre` off it and throw, so the answer after the block never rendered.
// This is a DOM check: the page is loaded in headless Chrome and the dumped DOM
// is the evidence. When no browser is present the check reports that instead of
// pretending.
import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const PAYLOAD = [
  'Here you go:',
  '',
  '<div data-diagram><div class="canvas"></div></div>',
  '',
  'and the rest of the answer.',
].join('\n');

const CANDIDATES = [process.env.CHROME_BIN, 'google-chrome', 'google-chrome-stable', 'chromium', 'chromium-browser', 'brave-browser'].filter(Boolean);

function browser() {
  for (const bin of CANDIDATES) {
    const probe = spawnSync(bin, ['--version'], { encoding: 'utf8', timeout: 15_000 });
    if (probe.status === 0) return { bin, version: probe.stdout.trim() || probe.stderr.trim() };
  }
  return null;
}

function dumpDom(bin, url) {
  const profile = mkdtempSync(path.join(os.tmpdir(), 'qa-chrome-'));
  try {
    const out = spawnSync(bin, [
      '--headless=new', '--disable-gpu', '--no-sandbox', '--disable-dev-shm-usage',
      '--no-first-run', '--no-default-browser-check', '--disable-extensions',
      `--user-data-dir=${profile}`,
      '--virtual-time-budget=10000',
      '--dump-dom', url,
    ], { encoding: 'utf8', timeout: 40_000, maxBuffer: 32 * 1024 * 1024 });
    return { dom: out.stdout || '', stderr: out.stderr || '', status: out.status };
  } finally {
    rmSync(profile, { recursive: true, force: true });
  }
}

export default {
  ticket: 'T-11',
  title: 'an invalid `<div data-diagram>` block does not truncate the answer',
  async run({ startServer }) {
    const server = await startServer({ label: 't11' });
    try {
      const cid = 'c_qa_t11';
      server.seedConversation({
        id: cid,
        title: 'diagram crash',
        messages: [
          { id: 'm_qa_t11_u0', role: 'user', content: 'render this', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' },
          {
            id: 'm_qa_t11_a0', role: 'assistant', content: PAYLOAD, finishReason: 'stop',
            createdAt: '2026-01-02T03:04:06Z', parts: [{ kind: 'text', text: PAYLOAD }],
          },
        ],
      });

      const bin = browser();
      if (!bin) {
        return {
          checks: [{
            label: 'a headless browser is available for the DOM check',
            ok: false,
            observed: [`looked for: ${CANDIDATES.join(', ')}`, 'set CHROME_BIN to point at one'],
          }],
          notes: ['this scenario is a DOM check; without a browser the crash path cannot be exercised'],
        };
      }

      const url = `${server.base}/#/c/${encodeURIComponent(cid)}`;
      const { dom, stderr, status } = dumpDom(bin.bin, url);
      const text = dom.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ');

      const checks = [
        {
          label: 'the page loads and the transcript renders the text before the block',
          ok: status === 0 && text.includes('Here you go:'),
          observed: [
            `browser: ${bin.bin} (${bin.version})`,
            `exit: ${status}`,
            `dom bytes: ${dom.length}`,
            stderr ? `stderr tail: ${JSON.stringify(stderr.slice(-300))}` : 'stderr: empty',
          ],
        },
        {
          label: 'the answer continues after the invalid diagram block',
          ok: text.includes('and the rest of the answer.'),
          observed: [
            `contains "and the rest of the answer.": ${text.includes('and the rest of the answer.')}`,
            `text around the block: ${JSON.stringify(text.slice(Math.max(0, text.indexOf('Here you go:')), text.indexOf('Here you go:') + 260))}`,
          ],
        },
        {
          label: 'the invalid block is neutralized, not treated as a diagram',
          ok: !dom.includes('data-diagram') || text.includes('Diagram source is missing'),
          observed: [
            `data-diagram left in the DOM: ${dom.includes('data-diagram')}`,
            `"Diagram source is missing" shown: ${text.includes('Diagram source is missing')}`,
          ],
        },
        {
          label: 'no failure surface replaced the answer',
          ok: !text.includes('Turn failed') && !text.includes('Could not load this thread'),
          observed: [
            `"Turn failed" present: ${text.includes('Turn failed')}`,
            `"Could not load this thread" present: ${text.includes('Could not load this thread')}`,
          ],
        },
      ];

      return {
        checks,
        notes: [
          `scratch root: ${server.root}`,
          'the DOM is dumped after a 10 s virtual-time budget, enough for the room fetch and render',
        ],
      };
    } finally {
      await server.close();
    }
  },
};
