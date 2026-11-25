// Tool call row + terminal-style card. The row is a one-line summary
// ("Ran <command>"); expanding it shows the input (head first) and the
// output (tail first), each clipped with a fade and expandable in place.
import { html, useLayoutEffect, useRef, useState } from '/vendor/preact/htm-preact.mjs';
import hljs from '/vendor/highlight/highlight.min.mjs';
import { toolSummary, exitText } from '../lib/turn.js';
import { Icon } from './icons.js';
import { cx, Spinner } from './ui.js';

const CLIP_LINES = 12;
const SHELL_TOOLS = new Set(['exec', 'exec_session', 'shell', 'bash']);

const opened = new Map();
function useOpen(id) {
  const [open, set] = useState(opened.get(id) ?? false);
  return [open, (v) => { opened.set(id, v); set(v); }];
}

function parseArgs(input) {
  try { return JSON.parse(input || '{}'); } catch { return null; }
}

// splitExit peels the runner's trailing "(exit N)" / "(signal: X)" marker off
// the stored output so it can render as a footer instead of text. It is
// formatting only and is applied only when the record carries an `exit` field:
// the text never decides anything (D-12), and a record without the field keeps
// its output exactly as stored.
function splitExit(output) {
  const m = /\n?\((exit (\d+)|signal: ([^)]+))\)\s*$/.exec(output || '');
  if (!m) return { body: output || '', exit: null };
  return { body: output.slice(0, m.index), exit: m[2] != null ? { code: Number(m[2]) } : { signal: m[3] } };
}

function CopyIcon({ text }) {
  const [done, setDone] = useState(false);
  return html`<button type="button" title="Copy" aria-label="Copy"
    onClick=${(e) => { e.stopPropagation(); navigator.clipboard?.writeText(text).then(() => { setDone(true); setTimeout(() => setDone(false), 1200); }); }}
    class="absolute top-1.5 right-1.5 z-10 size-7 inline-flex items-center justify-center rounded-md border border-line bg-raised text-faint hover:text-fg opacity-0 group-hover/clip:opacity-100 focus:opacity-100 transition-opacity">
    <${Icon} name=${done ? 'check' : 'copy'} size=${13} />
  </button>`;
}

// Clip shows the first (head) or last (tail) lines of long text with a fade
// toward the hidden part; a chevron expands it. Tail mode follows appended
// text while the reader stays at the bottom.
function Clip({ id, text, mode, lang, live }) {
  const [full, setFull] = useOpen(id + ':full');
  const box = useRef();
  const stick = useRef(true);
  const lines = text.split('\n');
  const long = lines.length > CLIP_LINES;
  const shown = !long || full ? text : mode === 'head' ? lines.slice(0, CLIP_LINES).join('\n') : lines.slice(-CLIP_LINES).join('\n');

  let content = shown;
  if (lang && hljs.getLanguage(lang)) {
    try { content = html`<code dangerouslySetInnerHTML=${{ __html: hljs.highlight(shown, { language: lang, ignoreIllegals: true }).value }}></code>`; } catch { /* plain */ }
  }
  useLayoutEffect(() => {
    const el = box.current;
    if (el && mode === 'tail' && stick.current) el.scrollTop = el.scrollHeight;
  }, [text, full]);

  return html`<div class="group/clip relative">
    <${CopyIcon} text=${text} />
    <pre ref=${box} onScroll=${(e) => { const el = e.target; stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24; }}
      class=${cx('m-0 font-mono text-[12.5px] leading-[1.6] whitespace-pre-wrap break-words', full && long && 'max-h-[60vh] overflow-y-auto')}>${content}</pre>
    ${long && !full && html`<div aria-hidden="true" class=${cx('pointer-events-none absolute inset-x-0 h-20 from-raised to-transparent',
      mode === 'head' ? 'bottom-0 bg-linear-to-t' : 'top-0 bg-linear-to-b')}></div>`}
    ${long && html`<div class=${cx('flex justify-center', full ? 'pt-2' : mode === 'head' ? 'absolute inset-x-0 -bottom-1' : 'absolute inset-x-0 -top-1')}>
      <button type="button" onClick=${() => setFull(!full)} aria-expanded=${full}
        title=${full ? 'Collapse' : `Show all ${lines.length} lines`} aria-label=${full ? 'Collapse' : `Show all ${lines.length} lines`}
        class="size-7 inline-flex items-center justify-center rounded-full border border-line bg-hover text-fg hover:brightness-125 shadow-pop">
        <${Icon} name=${(mode === 'head') !== full ? 'chevronDown' : 'chevronUp'} size=${14} />
      </button>
    </div>`}
    ${live && !full && mode === 'tail' && long && html`<span class="sr-only">Showing the latest output</span>`}
  </div>`;
}

export function ToolRow({ item, cwd }) {
  const [open, setOpen] = useOpen(item.id);
  const { verb, detail } = toolSummary(item.name, item.input);
  const running = item.status === 'requested' || item.status === 'streaming';
  const shell = SHELL_TOOLS.has(item.name);
  const args = parseArgs(item.input);
  // The verdict is the record's own fact (D-12); the marker in the text is
  // only moved into the footer, and only when the record carries `exit`.
  const exit = item.exit || null;
  const failed = item.outcome === 'failed';
  const body = exit ? splitExit(item.output).body : item.output || '';

  let inputText = item.input || '';
  let inputLang = 'json';
  let inputLabel = 'Input';
  if (shell && args?.command) {
    inputText = args.command + (args.stdin ? `\n# stdin:\n${args.stdin}` : '');
    inputLang = 'bash';
    inputLabel = cwd ? html`Command in <span class="font-mono">${cwd}</span>` : 'Command';
  } else if (args) {
    inputText = JSON.stringify(args, null, 2);
  }

  return html`<div data-tool-call=${item.callId || item.id}>
    <button type="button" onClick=${() => setOpen(!open)} aria-expanded=${open}
      class="group w-full flex items-center gap-2 py-1 text-left text-[13px] text-muted hover:text-fg min-w-0">
      ${running && html`<span class="orb live" title="Running"></span>`}
      ${failed && html`<span class="orb bad" title="Failed"></span>`}
      ${item.status === 'pending' && html`<span class="orb idle" title="No result recorded"></span>`}
      <span class="min-w-0 truncate"><span class=${running ? 'shimmer' : ''}>${verb}</span> <span class="text-faint group-hover:text-muted">${detail}</span></span>
      <${Icon} name=${open ? 'chevronDown' : 'chevronRight'} size=${14} class="shrink-0 ml-auto text-faint" />
    </button>
    ${open && html`<div class="mt-1 mb-2 rounded-xl border border-line bg-raised overflow-hidden">
      <section class="px-3 pt-2 pb-3">
        <div class="text-xs text-faint mb-1.5 truncate">${inputLabel}${!shell && html` · <span class="font-mono">${item.name}</span>`}</div>
        <${Clip} id=${item.id + ':in'} text=${inputText || '(no arguments)'} mode="head" lang=${inputLang} />
      </section>
      <section class="px-3 pt-2 pb-3 border-t border-line">
        <div class="text-xs text-faint mb-1.5">Output</div>
        ${body
          ? html`<${Clip} id=${item.id + ':out'} text=${body} mode="tail" live=${running} />`
          : html`<div class="text-[12.5px] text-faint">${running ? 'Waiting for output…' : item.status === 'pending' ? 'No result recorded.' : '(no output)'}</div>`}
      </section>
      ${(running || exit || failed) && html`<footer class="flex items-center gap-2 px-3 py-2 border-t border-line text-xs">
        ${running && html`<${Spinner} size=${12} class="text-muted" /><span class="text-muted">Running</span>`}
        ${!running && exit && html`<${Icon} name=${!exit.signal && exit.code === 0 ? 'check' : 'xCircle'} size=${14} class=${!exit.signal && exit.code === 0 ? 'text-ok' : 'text-bad'} />
          <span class=${!exit.signal && exit.code === 0 ? 'text-muted' : 'text-bad'}>${exitText(exit)}</span>`}
        ${!running && !exit && failed && html`<${Icon} name="xCircle" size=${14} class="text-bad" /><span class="text-bad">Failed</span>`}
      </footer>`}
    </div>`}
  </div>`;
}
