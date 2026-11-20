import { html, useEffect, useLayoutEffect, useRef, useState } from '/vendor/preact/htm-preact.mjs';
import { useStore, state, emit } from '../lib/store.js';
import { room, freshRoom, rename, removeRoom, compact, isBusy, openRoom, navigate } from '../lib/chat.js';
import { Icon, Logo } from './icons.js';
import { cx, Popover, IconButton, Dialog, Button, ErrorBlock, Spinner } from './ui.js';
import { Transcript } from './transcript.js';
import { Composer } from './composer.js';

export function MenuButton() {
  return html`<${IconButton} icon="sidebar" label="Open threads" class="md:hidden" onClick=${() => { state.sidebarOpen = true; emit(); }} />`;
}

function Header({ room: r }) {
  const [menu, setMenu] = useState(false);
  const [editing, setEditing] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const title = r.info?.title || 'Untitled thread';
  const ws = r.info?.workspace;

  const saveTitle = (v) => { setEditing(false); if (v.trim() !== (r.info?.title || '')) rename(r, v.trim()); };
  return html`<header class="flex items-center gap-1 h-12 px-2 sm:px-3 border-b border-line shrink-0">
    <${MenuButton} />
    <div class="flex-1 min-w-0 flex items-center gap-3">
      ${editing
        ? html`<input autofocus value=${r.info?.title || ''} aria-label="Thread title"
            onKeyDown=${(e) => { if (e.key === 'Enter') saveTitle(e.target.value); if (e.key === 'Escape') setEditing(false); }}
            onBlur=${(e) => saveTitle(e.target.value)}
            class="flex-1 min-w-0 h-8 rounded-lg bg-raised border border-accent px-2 outline-none" />`
        : html`<button type="button" onClick=${() => setEditing(true)} title="Rename" class="min-w-0 truncate text-left font-medium hover:text-fg px-1 rounded">${title}</button>`}
      ${ws && html`<span class="hidden sm:flex items-center gap-1.5 text-[13px] text-faint min-w-0" title=${ws}>
        <${Icon} name="folder" size=${14} /><span class="truncate max-w-56">${ws.split('/').pop()}</span>
      </span>`}
    </div>
    ${r.compacting && html`<span class="flex items-center gap-1.5 text-xs text-muted"><${Spinner} size=${12} /> Compacting</span>`}
    <div class="relative">
      <${IconButton} icon="more" label="Thread actions" onClick=${() => setMenu(!menu)} aria-expanded=${menu} />
      <${Popover} open=${menu} onClose=${() => setMenu(false)} placement="bottom-end" label="Thread actions" class="sm:w-56 py-1">
        <button type="button" class="w-full flex items-center gap-2.5 px-3 h-9 hover:bg-hover" onClick=${() => { setMenu(false); setEditing(true); }}>
          <${Icon} name="pencil" size=${15} /> Rename
        </button>
        <button type="button" disabled=${!r.info?.canCompact || isBusy(r) || r.compacting}
          title=${r.info?.canCompact ? 'Summarize older history to free context' : 'Nothing to compact yet'}
          class="w-full flex items-center gap-2.5 px-3 h-9 hover:bg-hover disabled:opacity-40 disabled:hover:bg-transparent"
          onClick=${() => { setMenu(false); compact(r); }}>
          <${Icon} name="compress" size=${15} /> Compact history
        </button>
        <div class="my-1 border-t border-line"></div>
        <button type="button" class="w-full flex items-center gap-2.5 px-3 h-9 hover:bg-hover text-bad" onClick=${() => { setMenu(false); setConfirm(true); }}>
          <${Icon} name="trash" size=${15} /> Delete thread
        </button>
      </${Popover}>
    </div>
    <${Dialog} open=${confirm} onClose=${() => setConfirm(false)} title="Delete this thread?"
      footer=${html`<${Button} kind="ghost" autofocus onClick=${() => setConfirm(false)}>Keep</${Button}>
        <${Button} kind="danger" onClick=${() => { setConfirm(false); removeRoom(r); }}>Delete</${Button}>`}>
      <p class="text-muted">“${title}” and its history are removed permanently. Running or queued turns in it stop.</p>
    </${Dialog}>
  </header>`;
}

export function ChatView({ id }) {
  useStore();
  const r = room(id);
  const scroller = useRef();
  const stick = useRef(true);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  });
  useEffect(() => { stick.current = true; }, [id]);

  if (r.infoStatus === 'notfound') {
    return html`<div class="flex flex-col h-full">
      <header class="flex items-center h-12 px-2 border-b border-line"><${MenuButton} /></header>
      <div class="flex-1 grid place-items-center p-6 text-center">
        <div>
          <p class="font-medium">Thread not found</p>
          <p class="text-muted text-[13px] mt-1">It may have been deleted in another tab.</p>
          <${Button} class="mt-4" onClick=${() => navigate('#/')}>Start a new thread</${Button}>
        </div>
      </div>
    </div>`;
  }

  return html`<div class="flex flex-col h-full min-w-0">
    <${Header} room=${r} />
    ${r.infoStatus === 'error' && html`<div class="px-4 pt-3"><${ErrorBlock} title="Could not load thread details" message=${r.infoError} onRetry=${() => openRoom(r.id)} /></div>`}
    <div ref=${scroller} class="flex-1 min-h-0 overflow-y-auto" onScroll=${(e) => { const el = e.target; stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80; }}>
      <div class="mx-auto w-full max-w-3xl px-4 sm:px-6">
        <${Transcript} room=${r} scroller=${scroller} />
      </div>
    </div>
    <div class="mx-auto w-full max-w-3xl px-2 sm:px-4">
      <${Composer} room=${r} />
    </div>
  </div>`;
}

export function HomeView() {
  const s = useStore();
  const r = freshRoom();
  const recent = s.convs.items.slice(0, 4);
  return html`<div class="flex flex-col h-full">
    <header class="flex items-center h-12 px-2 md:hidden"><${MenuButton} /></header>
    <div class="flex-1 min-h-0 overflow-y-auto">
      <div class="min-h-full flex flex-col justify-center mx-auto w-full max-w-2xl px-3 sm:px-4 py-10">
        <div class="flex items-center gap-2.5 mb-5 px-1">
          <${Logo} size=${28} />
          <h1 class="text-xl font-semibold tracking-tight">What are we building?</h1>
        </div>
        ${r.live || r.pendingOpen ? html`<div class="flex items-center gap-2 text-muted text-[13px] px-1 mb-3"><${Spinner} /> Starting thread…</div>` : null}
        <${Composer} room=${r} hero />
        ${recent.length > 0 && html`<div class="mt-8 px-1">
          <div class="text-xs text-faint mb-2">Recent</div>
          <div class="grid sm:grid-cols-2 gap-2">
            ${recent.map((c) => html`<a key=${c.id} href=${'#/c/' + encodeURIComponent(c.id)}
              class="flex items-center gap-2 rounded-xl border border-line px-3 py-2.5 text-[13px] hover:bg-hover min-w-0">
              ${c.activeTurnId ? html`<span class="orb live"></span>` : html`<${Icon} name="chevronRight" size=${14} class="text-faint" />`}
              <span class="truncate">${c.title || 'Untitled thread'}</span>
            </a>`)}
          </div>
        </div>`}
      </div>
    </div>
  </div>`;
}
