// Composer: per-room draft, Enter to send. While a send turn runs, Enter
// steers that exact turn and Ctrl/⌘+Enter queues for after it; anything
// else that keeps the room busy queues. Stop is always its own control.
import { html, useEffect, useLayoutEffect, useRef, useState } from '/vendor/preact/htm-preact.mjs';
import { useStore, emit } from '../lib/store.js';
import { submit, steer, cancel, isBusy, recallSteer, removeQueued, restoreQueued, resumeQueue } from '../lib/chat.js';
import { Icon } from './icons.js';
import { cx, Popover, Kbd, Spinner } from './ui.js';
import { ModelPicker, WorkspacePicker, ContextMeter } from './pickers.js';

const isMac = /Mac|iPhone|iPad/.test(navigator.platform);
const MOD = isMac ? '⌘' : 'Ctrl';

function Tray({ room }) {
  const r = room;
  if (!r.steers.length && !r.queue.length) return null;
  const stateLabel = { sending: 'Sending', pending: 'Waiting for next step', unknown: 'Delivery unknown — check the thread' };
  return html`<div class="mx-3 mb-0 rounded-t-xl border border-b-0 border-line bg-panel/80 px-3 py-2 flex flex-col gap-1.5 max-h-48 overflow-y-auto text-[13px]">
    ${r.steers.map((s) => html`<div key=${s.inputId} class="flex items-center gap-2 min-w-0">
      <${Icon} name="steer" size=${14} class="text-accent" />
      <span class="flex-1 truncate">${s.text}</span>
      <span class=${cx('text-xs shrink-0', s.state === 'unknown' ? 'text-warn' : 'text-faint')}>${stateLabel[s.state]}</span>
      ${s.state === 'pending' && html`<button type="button" onClick=${() => recallSteer(r, s.inputId)} class="text-xs text-muted hover:text-fg px-1.5 h-6 rounded hover:bg-hover">Recall</button>`}
      ${s.state === 'unknown' && html`<button type="button" onClick=${() => { r.steers = r.steers.filter((x) => x !== s); emit(); }} class="text-xs text-muted hover:text-fg px-1.5 h-6 rounded hover:bg-hover">Dismiss</button>`}
    </div>`)}
    ${r.queue.length > 0 && html`<div class="flex items-center justify-between text-xs text-faint">
      <span>${r.queue.length} queued — ${r.held ? 'on hold after the turn stopped' : 'sends when the current turn ends'}</span>
      ${r.held && !isBusy(r) && html`<button type="button" onClick=${() => resumeQueue(r)} class="text-accent hover:underline">Send next</button>`}
    </div>`}
    ${r.queue.map((q) => html`<div key=${q.id} class="flex items-center gap-2 min-w-0">
      <${Icon} name="queue" size=${14} class="text-faint" />
      <span class="flex-1 truncate text-muted">${q.text}</span>
      <button type="button" title="Move back to the draft" onClick=${() => restoreQueued(r, q.id)} class="text-xs text-muted hover:text-fg px-1.5 h-6 rounded hover:bg-hover">Edit</button>
      <button type="button" aria-label="Remove from queue" onClick=${() => removeQueued(r, q.id)} class="text-faint hover:text-fg size-6 inline-flex items-center justify-center rounded hover:bg-hover"><${Icon} name="x" size=${13} /></button>
    </div>`)}
  </div>`;
}

export function Composer({ room, hero = false }) {
  useStore();
  const r = room;
  const ta = useRef();
  const [, bump] = useState(0);
  const [menu, setMenu] = useState(false);
  const live = r.live && !r.live.terminal;
  const canSteer = live && r.live.kind === 'send';
  const busy = isBusy(r);
  const hasText = !!r.draft.trim();

  // Grow with the draft up to a cap; the textarea scrolls past it.
  useLayoutEffect(() => {
    const el = ta.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = Math.min(el.scrollHeight, Math.round(window.innerHeight * 0.4)) + 'px';
  }, [r.draft, r.key]);
  useEffect(() => { if (window.matchMedia('(pointer:fine)').matches) ta.current?.focus(); }, [r.key]);

  const clear = () => { r.draft = ''; bump((n) => n + 1); emit(); };
  const doSteer = () => { if (steer(r, r.draft)) clear(); };
  const doQueue = () => { if (submit(r, r.draft)) clear(); };
  const primary = () => (canSteer ? doSteer() : doQueue());

  const onKey = (e) => {
    if (e.key !== 'Enter' || e.isComposing || e.keyCode === 229) return;
    if (e.shiftKey) return;
    e.preventDefault();
    if ((e.ctrlKey || e.metaKey) && canSteer) doQueue();
    else primary();
  };

  const placeholder = canSteer ? 'Steer the running turn…' : busy ? 'Queue a follow-up…' : hero ? 'What should we work on?' : 'Reply…';
  const primaryLabel = canSteer ? 'Steer now' : busy ? 'Add to queue' : 'Send';

  return html`<div class=${cx('w-full', hero ? '' : 'pb-[max(12px,env(safe-area-inset-bottom))]')}>
    <${Tray} room=${r} />
    <div class=${cx('rounded-2xl border border-line bg-raised shadow-sm focus-within:border-[color-mix(in_srgb,var(--accent)_45%,var(--line))] transition-colors', (r.steers.length || r.queue.length) && 'rounded-t-xl')}>
      <textarea ref=${ta} rows=${hero ? 3 : 1} value=${r.draft} placeholder=${placeholder} aria-label="Message"
        onInput=${(e) => { r.draft = e.target.value; bump((n) => n + 1); }}
        onKeyDown=${onKey}
        class="composer-input"></textarea>
      <div class="flex items-center gap-1 px-2 pb-2 pt-1">
        <${WorkspacePicker} room=${r} />
        <div class="flex-1"></div>
        <${ModelPicker} room=${r} />
        <${ContextMeter} room=${r} />
        ${live && html`<button type="button" onClick=${() => cancel(r)} disabled=${r.live.canceling} title="Stop this turn" aria-label="Stop this turn"
          class="size-8 inline-flex items-center justify-center rounded-full border border-line text-fg hover:bg-hover disabled:opacity-50">
          ${r.live.canceling ? html`<${Spinner} size=${13} />` : html`<${Icon} name="stop" size=${12} stroke=${3} />`}
        </button>`}
        <div class="relative flex items-center">
          <button type="button" onClick=${primary} disabled=${!hasText} title=${primaryLabel + ' (Enter)'} aria-label=${primaryLabel}
            class=${cx('h-8 inline-flex items-center justify-center bg-fg text-bg hover:opacity-90 disabled:opacity-30 transition',
              canSteer ? 'w-8 rounded-l-full pl-1' : 'size-8 rounded-full')}>
            <${Icon} name=${busy && !canSteer ? 'queue' : 'arrowUp'} size=${16} stroke=${2.25} />
          </button>
          ${canSteer && html`<button type="button" onClick=${() => setMenu(!menu)} aria-label="More send options" aria-expanded=${menu}
            class="h-8 w-6 inline-flex items-center justify-center rounded-r-full bg-fg text-bg border-l border-bg/20 hover:opacity-90">
            <${Icon} name="chevronDown" size=${13} stroke=${2.25} />
          </button>`}
          <${Popover} open=${menu} onClose=${() => setMenu(false)} placement="top-end" label="Send options" class="sm:w-60 py-1">
            <button type="button" onClick=${() => { setMenu(false); doSteer(); }} disabled=${!hasText}
              class="w-full flex items-center justify-between gap-3 px-3 h-9 hover:bg-hover disabled:opacity-50">
              <span>Steer now</span><${Kbd}>↵</${Kbd}>
            </button>
            <button type="button" onClick=${() => { setMenu(false); doQueue(); }} disabled=${!hasText}
              class="w-full flex items-center justify-between gap-3 px-3 h-9 hover:bg-hover disabled:opacity-50">
              <span>Add to queue</span><span class="flex gap-1"><${Kbd}>${MOD}</${Kbd}><${Kbd}>↵</${Kbd}></span>
            </button>
          </${Popover}>
        </div>
      </div>
    </div>
  </div>`;
}
