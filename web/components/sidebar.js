import { html, useState } from '/vendor/preact/htm-preact.mjs';
import { useStore } from '../lib/store.js';
import { loadConversations } from '../lib/chat.js';
import { Icon, Logo } from './icons.js';
import { cx, Spinner, ErrorBlock, IconButton, relTime } from './ui.js';

export function Sidebar() {
  const s = useStore();
  const [q, setQ] = useState('');
  const [searching, setSearching] = useState(false);
  const c = s.convs;
  const items = q ? c.items.filter((x) => (x.title || 'Untitled').toLowerCase().includes(q.toLowerCase())) : c.items;
  const activeId = s.route.view === 'chat' ? s.route.id : null;

  return html`<nav aria-label="Threads" class="flex flex-col h-full w-full bg-panel">
    <div class="flex items-center gap-2 px-3 h-12 shrink-0">
      <${Logo} size=${22} />
      <span class="font-semibold tracking-tight">NusaLLM</span>
      <span class=${cx('ml-1 size-1.5 rounded-full', s.conn === 'open' ? 'bg-ok' : s.conn === 'connecting' ? 'bg-warn' : 'bg-bad')}
        title=${s.conn === 'open' ? 'Connected' : s.conn === 'connecting' ? 'Connecting…' : 'Disconnected — retrying'}></span>
      <div class="ml-auto flex">
        <${IconButton} icon="search" label="Search threads" active=${searching} onClick=${() => { setSearching(!searching); setQ(''); }} />
      </div>
    </div>

    <div class="px-2 flex flex-col gap-0.5">
      <a href="#/" class=${cx('flex items-center gap-2.5 h-8 px-2.5 rounded-lg hover:bg-hover', s.route.view === 'home' && 'bg-hover')}>
        <${Icon} name="plus" /> New thread
      </a>
      <a href="#/settings" class=${cx('flex items-center gap-2.5 h-8 px-2.5 rounded-lg hover:bg-hover text-muted hover:text-fg', s.route.view === 'settings' && 'bg-hover text-fg')}>
        <${Icon} name="settings" /> Settings
      </a>
    </div>

    ${searching && html`<div class="px-3 pt-3">
      <input autofocus value=${q} onInput=${(e) => setQ(e.target.value)} placeholder="Filter loaded threads…"
        class="w-full h-8 rounded-lg bg-raised border border-line px-2.5 text-[13px] outline-none focus:border-accent" />
    </div>`}

    <div class="flex items-center justify-between px-4 pt-5 pb-1.5 text-xs text-faint">
      <span>Threads</span>
      ${c.total > 0 && html`<span title="Loaded threads of total">${c.items.length}/${c.total}</span>`}
    </div>

    <div class="flex-1 min-h-0 overflow-y-auto px-2 pb-2">
      ${c.status === 'loading' && html`<div class="flex flex-col gap-2 px-2 pt-1">
        ${[70, 85, 60, 78, 66].map((w) => html`<div class="skeleton h-4" style=${{ width: w + '%' }}></div>`)}
      </div>`}
      ${c.status === 'error' && html`<${ErrorBlock} class="mx-1" title="Threads unavailable" message=${c.error} onRetry=${() => loadConversations()} />`}
      ${c.status === 'ready' && !c.items.length && html`<p class="px-2.5 py-2 text-[13px] text-faint">No threads yet. Start one from New thread.</p>`}
      ${c.status === 'ready' && q && !items.length && html`<p class="px-2.5 py-2 text-[13px] text-faint">No loaded thread matches “${q}”.</p>`}
      <ul class="flex flex-col gap-px">
        ${items.map((x) => html`<li key=${x.id}>
          <a href=${'#/c/' + encodeURIComponent(x.id)} aria-current=${x.id === activeId ? 'page' : undefined}
            class=${cx('group flex items-center gap-2 h-8 px-2.5 rounded-lg text-[13px] hover:bg-hover', x.id === activeId ? 'bg-hover text-fg' : 'text-muted hover:text-fg')}>
            ${x.activeTurnId ? html`<span class="orb live" title="Working"></span>` : html`<span class="size-2 shrink-0"></span>`}
            <span class="flex-1 truncate">${x.title || 'Untitled thread'}</span>
            <span class="text-[11px] text-faint shrink-0">${relTime(x.updatedAt)}</span>
          </a>
        </li>`)}
      </ul>
      ${c.hasMore && !q && html`<button onClick=${() => loadConversations(true)} disabled=${c.more}
        class="mt-1 w-full flex items-center justify-center gap-2 h-8 rounded-lg text-[13px] text-muted hover:bg-hover hover:text-fg disabled:opacity-60">
        ${c.more && html`<${Spinner} />`} Load more
      </button>`}
    </div>

    ${s.update?.available && html`<a href="#/settings/about" class="flex items-center gap-2 mx-2 mb-2 px-3 h-9 rounded-lg text-[13px] text-accent hover:bg-hover">
      <${Icon} name="download" /> Update available · ${s.update.latest}
    </a>`}
  </nav>`;
}

