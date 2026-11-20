// Transcript: persisted history and the live turn rendered through one
// item model (lib/turn.js). Prose renders as Markdown; reasoning and tool
// calls fold into collapsible "work" groups between prose.
import { html, useEffect, useLayoutEffect, useRef, useState } from '/vendor/preact/htm-preact.mjs';
import { renderInto, disposeTree, handleCopyClick } from '../lib/markdown.js';
import { historyItems, group, toolFailed } from '../lib/turn.js';
import { ToolRow } from './toolcard.js';
import { editMessage, isBusy, retry, loadEarlier, loadHistory } from '../lib/chat.js';
import { emit } from '../lib/store.js';
import { Icon } from './icons.js';
import { cx, Spinner, Button, ErrorBlock } from './ui.js';

// Expansion state survives re-renders and remounts, keyed by item id.
const opened = new Map();
function useOpen(id, initial = false) {
  const [open, set] = useState(opened.has(id) ? opened.get(id) : initial);
  return [open, (v) => { opened.set(id, v); set(v); }];
}

export function Markdown({ text, class: cls = '' }) {
  const ref = useRef();
  useLayoutEffect(() => { renderInto(ref.current, text); }, [text]);
  useEffect(() => () => ref.current && disposeTree(ref.current), []);
  return html`<div ref=${ref} class=${cx('md', cls)} onClick=${handleCopyClick}></div>`;
}

function CopyButton({ text, label = 'Copy' }) {
  const [done, setDone] = useState(false);
  return html`<button type="button" title=${label} aria-label=${label}
    onClick=${() => navigator.clipboard?.writeText(text).then(() => { setDone(true); setTimeout(() => setDone(false), 1200); })}
    class="inline-flex items-center justify-center size-7 rounded-md text-faint hover:text-fg hover:bg-hover">
    <${Icon} name=${done ? 'check' : 'copy'} size=${14} />
  </button>`;
}

function UserMessage({ room, msg, text, pending }) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(text);
  const canEdit = msg && !isBusy(room);
  if (editing) {
    const save = () => { if (editMessage(room, msg.id, draft)) setEditing(false); };
    return html`<div class="flex justify-end">
      <div class="w-full max-w-[85%] rounded-2xl border border-line bg-raised p-2">
        <textarea class="w-full min-h-20 resize-y bg-transparent outline-none px-2 py-1.5 text-[14px]" value=${draft} autofocus
          onInput=${(e) => setDraft(e.target.value)}
          onKeyDown=${(e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) save(); if (e.key === 'Escape') setEditing(false); }}></textarea>
        <div class="flex items-center justify-between gap-2 px-1">
          <span class="text-xs text-faint">Regenerates from here — later messages are replaced.</span>
          <div class="flex gap-2">
            <${Button} kind="ghost" onClick=${() => setEditing(false)}>Cancel</${Button}>
            <${Button} kind="primary" onClick=${save} disabled=${!draft.trim()}>Send</${Button}>
          </div>
        </div>
      </div>
    </div>`;
  }
  return html`<div class="group flex flex-col items-end gap-1">
    <div class=${cx('max-w-[85%] rounded-2xl bg-raised border border-line px-3.5 py-2 whitespace-pre-wrap break-words', pending && 'opacity-70')}>${text}</div>
    <div class="flex gap-0.5 opacity-0 group-hover:opacity-100 focus-within:opacity-100 transition-opacity">
      <${CopyButton} text=${text} label="Copy message" />
      ${canEdit && html`<button type="button" title="Edit and regenerate" aria-label="Edit and regenerate" onClick=${() => { setDraft(text); setEditing(true); }}
        class="inline-flex items-center justify-center size-7 rounded-md text-faint hover:text-fg hover:bg-hover"><${Icon} name="pencil" size=${14} /></button>`}
    </div>
  </div>`;
}

function Thoughts({ item, live }) {
  const [open, setOpen] = useOpen(item.id);
  return html`<div>
    <button type="button" onClick=${() => setOpen(!open)} aria-expanded=${open}
      class="flex items-center gap-1.5 py-1 text-[13px] text-muted hover:text-fg">
      <${Icon} name=${open ? 'chevronDown' : 'chevronRight'} size=${14} />
      <span class=${live ? 'shimmer' : ''}>${live ? 'Thinking' : 'Thoughts'}</span>
    </button>
    ${open && html`<div class="ml-[7px] pl-4 border-l border-line text-muted text-[13px] max-h-96 overflow-y-auto">
      <${Markdown} text=${item.text} />
    </div>`}
  </div>`;
}

function Activity({ item, live, cwd }) {
  const [open, setOpen] = useOpen(item.id);
  const tools = item.items.filter((x) => x.kind === 'tool');
  if (!tools.length) return item.items.map((x) => html`<${Thoughts} key=${x.id} item=${x} live=${live} />`);
  const failed = tools.filter((t) => t.status === 'settled' && toolFailed(t.output)).length;
  const label = live ? `Working · ${tools.length} ${tools.length === 1 ? 'step' : 'steps'}` : `Used ${tools.length} ${tools.length === 1 ? 'tool' : 'tools'}`;
  const tail = !open && live ? item.items.slice(-1) : [];
  return html`<div>
    <button type="button" onClick=${() => setOpen(!open)} aria-expanded=${open}
      class="flex items-center gap-1.5 py-1 text-[13px] text-muted hover:text-fg">
      <${Icon} name=${open ? 'chevronDown' : 'chevronRight'} size=${14} />
      <span class=${live ? 'shimmer' : ''}>${label}</span>
      ${failed > 0 && html`<span class="text-bad text-xs">· ${failed} failed</span>`}
    </button>
    ${(open || tail.length > 0) && html`<div class="ml-[7px] pl-4 border-l border-line flex flex-col">
      ${(open ? item.items : tail).map((x) => x.kind === 'tool'
        ? html`<${ToolRow} key=${x.id} item=${x} cwd=${x.cwd || cwd} />`
        : html`<${Thoughts} key=${x.id} item=${x} live=${live && x === item.items[item.items.length - 1]} />`)}
    </div>`}
  </div>`;
}

function Status({ room }) {
  const [, tick] = useState(0);
  useEffect(() => { const t = setInterval(() => tick((n) => n + 1), 1000); return () => clearInterval(t); }, []);
  const live = room.live;
  let text = 'Working';
  if (room.pendingOpen) text = 'Sending';
  else if (room.settling) text = 'Finishing';
  else if (live?.canceling) text = 'Stopping';
  else if (live?.phase?.phase === 'retry_wait') {
    const left = Math.max(0, Math.ceil(((live.phase.at + (live.phase.retryAfterMs || 0)) - Date.now()) / 1000));
    text = `Provider busy — retrying in ${left}s`;
  } else if (live?.phase?.phase === 'compacting') text = 'Compacting history to fit the context window';
  else if (live?.phase?.phase === 'reasoning') text = 'Thinking';
  const secs = live ? Math.floor((Date.now() - live.startedAt) / 1000) : 0;
  return html`<div class="flex items-center gap-2 text-[13px] text-muted py-1" role="status">
    <span class=${live?.phase?.phase === 'retry_wait' ? 'orb live' : 'orb live'} style=${live?.phase?.phase === 'retry_wait' ? 'background:var(--bad)' : ''}></span>
    <span class="shimmer">${text}…</span>
    ${secs > 2 && html`<span class="text-faint text-xs">${secs >= 60 ? Math.floor(secs / 60) + 'm ' : ''}${secs % 60}s</span>`}
  </div>`;
}

function FailedCard({ room }) {
  const f = room.failed;
  const lastUser = [...room.messages].reverse().find((m) => m.role === 'user');
  return html`<div role="alert" class="rounded-xl border border-bad/30 bg-bad/10 px-4 py-3 text-[13px]">
    <div class="flex items-start gap-2.5">
      <${Icon} name="alert" class="text-bad mt-0.5" />
      <div class="flex-1 min-w-0">
        <div class="font-medium">Turn failed${f.status ? ` (${f.status})` : ''}</div>
        <div class="text-muted mt-0.5 break-words">${f.message}</div>
        ${room.queue.length > 0 && html`<div class="text-faint mt-1">${room.queue.length} queued ${room.queue.length === 1 ? 'message is' : 'messages are'} on hold.</div>`}
      </div>
    </div>
    <div class="flex justify-end gap-2 mt-2">
      <${Button} kind="ghost" onClick=${() => { room.failed = null; emit(); }}>Dismiss</${Button}>
      ${lastUser && html`<${Button} onClick=${() => retry(room)}><${Icon} name="refresh" size=${14} /> Retry</${Button}>`}
    </div>
  </div>`;
}

// Stick to the bottom while the reader is there; keep the reading anchor
// when earlier messages are prepended.
export function Transcript({ room, scroller }) {
  const r = room;
  let hist = historyItems(r.messages);
  const lastTool = hist.findLast((x) => x.kind === 'tool');
  const cwd = r.sel.workspace || r.info?.workspace || lastTool?.cwd || '';
  if (r.cutHistory && r.live) {
    const at = hist.findLastIndex((x) => x.kind === 'user');
    hist = hist.slice(0, at + 1);
  }
  if (r.live?.editOf) {
    const at = hist.findIndex((x) => x.id === r.live.editOf);
    if (at >= 0) hist = hist.slice(0, at);
  }
  const liveItems = r.live ? r.live.segments : [];
  const items = group(hist);
  const liveGroups = group(liveItems);

  // Older pages load as the reader nears the top. The distance from the
  // bottom is tracked on every scroll and re-applied after a prepend, so
  // the line being read stays put.
  const fromBottom = useRef(0);
  const sentinel = useRef();
  const first = r.messages[0]?.id;
  useEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const track = () => { fromBottom.current = el.scrollHeight - el.scrollTop; };
    track();
    el.addEventListener('scroll', track, { passive: true });
    return () => el.removeEventListener('scroll', track);
  }, []);
  const prevFirst = useRef(first);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && prevFirst.current && first !== prevFirst.current) el.scrollTop = el.scrollHeight - fromBottom.current;
    prevFirst.current = first;
  }, [first]);
  const canLoad = r.hist.more && !r.hist.earlierError && r.hist.status === 'ready';
  useEffect(() => {
    const el = sentinel.current;
    if (!el || !canLoad) return;
    // Re-observing after each page fires again if the sentinel is still in
    // view, so a short page keeps filling until the screen is covered.
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) loadEarlier(r);
    }, { root: scroller.current, rootMargin: '600px 0px 0px 0px' });
    io.observe(el);
    return () => io.disconnect();
  }, [canLoad, first, r.hist.loadingEarlier]);

  if (r.hist.status === 'loading') {
    return html`<div class="flex flex-col gap-5 pt-6" aria-busy="true" aria-label="Loading messages">
      <div class="skeleton h-9 w-1/2 self-end rounded-2xl"></div>
      <div class="flex flex-col gap-2"><div class="skeleton h-4 w-4/5"></div><div class="skeleton h-4 w-3/5"></div><div class="skeleton h-4 w-2/3"></div></div>
      <div class="skeleton h-9 w-1/3 self-end rounded-2xl"></div>
      <div class="flex flex-col gap-2"><div class="skeleton h-4 w-3/4"></div><div class="skeleton h-4 w-1/2"></div></div>
    </div>`;
  }
  if (r.hist.status === 'error') {
    return html`<${ErrorBlock} class="mt-6" title="Could not load this thread" message=${r.hist.error} onRetry=${() => loadHistory(r)} />`;
  }

  const renderItem = (it, live) => {
    switch (it.kind) {
      case 'user': return html`<${UserMessage} key=${it.id} room=${r} msg=${it.msg} text=${it.msg.content} />`;
      case 'steer': return html`<div key=${it.id} class="flex flex-col items-end gap-1">
        <span class="text-[11px] text-faint">steered</span>
        <div class="max-w-[85%] rounded-2xl bg-raised border border-dashed border-line px-3.5 py-2 whitespace-pre-wrap break-words">${it.text}</div>
      </div>`;
      case 'note': return html`<details key=${it.id} class="text-[13px] text-muted">
        <summary class="cursor-pointer py-1">Context note</summary>
        <pre class="rawtext mt-1">${it.msg.content}</pre>
      </details>`;
      case 'text': return html`<${Markdown} key=${it.id} text=${it.text} />`;
      case 'activity': return html`<${Activity} key=${it.id} item=${it} live=${live} cwd=${cwd} />`;
      case 'interrupted': return html`<div key=${it.id} class="text-xs text-faint">— interrupted</div>`;
      default: return null;
    }
  };

  const empty = !hist.length && !r.live && !r.pendingOpen;
  return html`<div class="flex flex-col gap-4 py-6">
    ${r.hist.more && html`<div ref=${sentinel} aria-busy=${r.hist.loadingEarlier ? 'true' : 'false'} aria-label="Earlier messages">
      ${r.hist.earlierError
        ? html`<div role="alert" class="flex items-center justify-center gap-3 py-3 text-[13px] text-muted">
            <span>Could not load earlier messages.</span>
            <${Button} kind="ghost" onClick=${() => loadEarlier(r)}>Retry</${Button}>
          </div>`
        : html`<div class="flex flex-col gap-4">
            <div class="flex flex-col gap-2"><div class="skeleton h-4 w-3/4"></div><div class="skeleton h-4 w-1/2"></div></div>
            <div class="skeleton h-9 w-2/5 self-end rounded-2xl"></div>
            <div class="flex flex-col gap-2"><div class="skeleton h-4 w-4/5"></div><div class="skeleton h-4 w-3/5"></div></div>
          </div>`}
    </div>`}
    ${empty && r.infoStatus === 'ready' && html`<p class="text-center text-faint text-[13px] py-10">This thread has no messages yet.</p>`}
    ${items.map((it) => renderItem(it, false))}
    ${r.pendingOpen?.text && r.pendingOpen.kind === 'send' && html`<${UserMessage} room=${r} text=${r.pendingOpen.text} pending />`}
    ${r.live?.userText && html`<${UserMessage} room=${r} text=${r.live.userText} pending=${!r.live.terminal} />`}
    ${liveGroups.map((it, i) => renderItem(it, !r.live.terminal && i === liveGroups.length - 1))}
    ${(r.live && !r.live.terminal) || r.pendingOpen || r.settling ? html`<${Status} room=${r} />` : null}
    ${r.failed && !isBusy(r) && html`<${FailedCard} room=${r} />`}
  </div>`;
}
