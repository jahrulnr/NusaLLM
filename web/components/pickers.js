// Composer pickers: model (gateway → model → effort → upstream provider)
// and workspace (directory browser over /api/fs/browse).
import { html, useEffect, useRef, useState } from '/vendor/preact/htm-preact.mjs';
import { useStore } from '../lib/store.js';
import { api, errorText } from '../lib/api.js';
import { selection, setSelection, loadGateways, providersFor } from '../lib/chat.js';
import { Icon } from './icons.js';
import { cx, Popover, Spinner, ErrorBlock, Select, fmtTokens } from './ui.js';

const ALL_EFFORTS = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];

export function modelLabel(gateways, route, modelId) {
  const g = gateways.find((x) => x.id === route);
  const m = g?.models.find((x) => x.id === modelId);
  return m?.name || modelId || '';
}

export function ModelPicker({ room }) {
  const s = useStore();
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState('');
  const [providers, setProviders] = useState({ status: 'idle', list: [] });
  const sel = selection(room);
  const gws = s.gateways;
  const cur = gws.items.find((g) => g.id === sel.route)?.models.find((m) => m.id === sel.modelId);
  const efforts = cur?.supportedEfforts?.length ? cur.supportedEfforts : ALL_EFFORTS;

  useEffect(() => {
    if (!open || !sel.route || !sel.modelId) return;
    setProviders({ status: 'loading', list: [] });
    let live = true;
    // The controller keeps the answer and drops it whenever the gateway list is
    // re-read, so a provider saved on the settings page is visible here on the
    // next open without a page reload.
    providersFor(sel.route, sel.modelId).then((list) => {
      if (live) setProviders({ status: 'ready', list });
    }, () => live && setProviders({ status: 'error', list: [] }));
    return () => { live = false; };
  }, [open, sel.route, sel.modelId]);

  const needle = q.trim().toLowerCase();
  const groups = gws.items.map((g) => ({
    g,
    models: g.models.filter((m) => m.listed !== false && (!needle || (m.name || '').toLowerCase().includes(needle) || m.id.toLowerCase().includes(needle) || g.id.toLowerCase().includes(needle))),
  })).filter((x) => x.models.length);

  const label = cur ? (cur.name || cur.id) : sel.modelId || 'Choose model';
  return html`<div class="relative">
    <button type="button" onClick=${() => setOpen(!open)} aria-haspopup="dialog" aria-expanded=${open}
      class=${cx('flex items-center gap-1.5 h-8 px-2.5 rounded-lg text-[13px] hover:bg-hover max-w-[44vw] sm:max-w-64', sel.modelId ? 'text-fg' : 'text-warn')}>
      <span class="truncate">${label}</span>
      ${sel.effort && sel.effort !== 'default' && html`<span class="text-faint shrink-0">${sel.effort}</span>`}
      <${Icon} name="chevronDown" size=${14} class="text-faint" />
    </button>
    <${Popover} open=${open} onClose=${() => setOpen(false)} placement="top-end" label="Choose model" class="sm:w-[380px] flex flex-col">
      <div class="flex items-center gap-2 px-3 h-11 border-b border-line">
        <${Icon} name="search" class="text-faint" />
        <input autofocus value=${q} onInput=${(e) => setQ(e.target.value)} placeholder="Search models"
          class="flex-1 bg-transparent outline-none text-[13px]" />
      </div>
      <div class="flex-1 min-h-0 max-h-[min(18rem,34dvh)] overflow-y-auto py-1">
        ${gws.status === 'loading' && html`<div class="flex items-center gap-2 px-3 py-3 text-muted"><${Spinner} /> Loading providers…</div>`}
        ${gws.status === 'error' && html`<div class="p-2"><${ErrorBlock} title="Providers unavailable" message=${gws.error} onRetry=${loadGateways} /></div>`}
        ${gws.status === 'ready' && !gws.items.length && html`<div class="px-3 py-4 text-muted">
          No providers yet. <a class="text-accent underline" href="#/settings/providers/new" onClick=${() => setOpen(false)}>Add a provider</a> to start chatting.
        </div>`}
        ${gws.status === 'ready' && gws.items.length > 0 && !groups.length && html`<div class="px-3 py-4 text-muted">No model matches “${q}”.</div>`}
        ${groups.map(({ g, models }) => html`<div key=${g.id}>
          <div class="px-3 pt-2 pb-1 text-[11px] text-faint flex items-center gap-1.5">
            ${g.name || g.id}${g.broken && html`<span class="text-bad">· broken</span>`}
          </div>
          ${models.map((m) => {
            const active = g.id === sel.route && m.id === sel.modelId;
            return html`<button key=${m.id} type="button"
              onClick=${() => setSelection(room, { route: g.id, modelId: m.id, effort: 'default', provider: '' })}
              class=${cx('w-full flex items-center gap-2 px-3 h-8 text-left hover:bg-hover', active && 'bg-hover')}>
              <span class="flex-1 truncate">${m.name || m.id}</span>
              ${m.contextWindow > 0 && html`<span class="text-[11px] text-faint">${fmtTokens(m.contextWindow)}</span>`}
              ${active && html`<${Icon} name="check" size=${14} class="text-accent" />`}
            </button>`;
          })}
        </div>`)}
      </div>
      ${cur && html`<div class="border-t border-line px-3 py-2.5 flex flex-col gap-2">
        <div class="flex items-center justify-between gap-3">
          <span class="text-muted">Reasoning effort</span>
          <${Select} label="Reasoning effort" class="w-36" value=${sel.effort || 'default'}
            options=${[{ value: 'default', text: 'Default' }, ...efforts.map((e) => ({ value: e, text: e[0].toUpperCase() + e.slice(1) }))]}
            onChange=${(v) => setSelection(room, { effort: v })} />
        </div>
        ${providers.status === 'loading' && html`<div class="flex items-center gap-2 text-faint text-xs"><${Spinner} size=${12} /> Checking upstream routes…</div>`}
        ${providers.status === 'ready' && providers.list.length > 0 && html`<div class="flex items-center justify-between gap-3">
          <span class="text-muted">Upstream</span>
          <${Select} label="Upstream provider" class="w-36" search value=${sel.provider || ''}
            options=${[{ value: '', text: 'Auto' }, ...providers.list.map((p) => ({ value: p.slug, text: p.name }))]}
            onChange=${(v) => setSelection(room, { provider: v })} />
        </div>`}
      </div>`}
      <a href=${'#/settings/providers' + (sel.route ? '/' + encodeURIComponent(sel.route) : '')} onClick=${() => setOpen(false)}
        class="flex items-center gap-2 px-3 h-9 border-t border-line text-muted hover:text-fg hover:bg-hover">
        <${Icon} name="server" size=${14} /> Manage providers
      </a>
    </${Popover}>
  </div>`;
}

const base = (p) => (p ? p.replace(/[\\/]+$/, '').split(/[\\/]/).pop() || p : '');

export function WorkspacePicker({ room }) {
  const [open, setOpen] = useState(false);
  const [dir, setDir] = useState({ status: 'idle' });
  const [showHidden, setShowHidden] = useState(false);
  const sel = selection(room);
  const req = useRef(0);

  const browse = async (path) => {
    const n = ++req.current;
    setDir((d) => ({ ...d, status: 'loading' }));
    try {
      const r = await api.browse(path);
      if (n === req.current) setDir({ status: 'ready', ...r });
    } catch (err) {
      if (n === req.current) setDir((d) => ({ ...d, status: 'error', error: errorText(err), notFound: err.status === 404 }));
    }
  };
  useEffect(() => { if (open) browse(sel.workspace || ''); }, [open]);

  const entries = (dir.entries || []).filter((e) => showHidden || !e.hidden);
  return html`<div class="relative">
    <button type="button" onClick=${() => setOpen(!open)} aria-haspopup="dialog" aria-expanded=${open} title=${sel.workspace || 'Choose a working directory'}
      class="flex items-center gap-1.5 h-8 px-2.5 rounded-lg text-[13px] text-muted hover:text-fg hover:bg-hover max-w-[40vw] sm:max-w-56">
      <${Icon} name="folder" size=${15} />
      <span class="truncate">${base(sel.workspace) || 'Workspace'}</span>
    </button>
    <${Popover} open=${open} onClose=${() => setOpen(false)} placement="top-start" label="Working directory" class="sm:w-[420px] flex flex-col">
      <div class="px-3 pt-2.5 pb-2 border-b border-line flex flex-col gap-2">
        <div class="flex items-center justify-between">
          <span class="text-faint text-xs">Working directory</span>
          <label class="flex items-center gap-1.5 text-xs text-faint cursor-pointer">
            <input type="checkbox" checked=${showHidden} onChange=${(e) => setShowHidden(e.target.checked)} class="accent-[var(--accent)]" /> Hidden
          </label>
        </div>
        <div class="flex items-center gap-1">
          <button type="button" disabled=${!dir.parent || dir.status === 'loading'} onClick=${() => browse(dir.parent)} title="Parent folder" aria-label="Parent folder"
            class="size-7 inline-flex items-center justify-center rounded-md text-muted hover:bg-hover hover:text-fg disabled:opacity-30"><${Icon} name="folderUp" size=${15} /></button>
          <span class="flex-1 truncate font-mono text-xs" title=${dir.path}>${dir.path || '…'}</span>
          ${(dir.roots || []).map((r) => html`<button key=${r.path} type="button" onClick=${() => browse(r.path)} class="px-1.5 h-6 rounded text-xs text-muted hover:bg-hover">${r.name}</button>`)}
        </div>
      </div>
      <div class="flex-1 min-h-0 max-h-[min(18rem,34dvh)] overflow-y-auto py-1">
        ${dir.status === 'loading' && !dir.entries && html`<div class="flex items-center gap-2 px-3 py-3 text-muted"><${Spinner} /> Reading folder…</div>`}
        ${dir.status === 'error' && html`<div class="p-2"><${ErrorBlock} title=${dir.notFound ? 'Folder not found' : 'Cannot open folder'} message=${dir.error} onRetry=${() => browse(dir.notFound ? '' : dir.path || '')} /></div>`}
        ${dir.status !== 'error' && dir.entries && !entries.length && html`<div class="px-3 py-3 text-faint">No subfolders here.</div>`}
        ${dir.status !== 'error' && entries.map((e) => html`<button key=${e.path} type="button" onClick=${() => browse(e.path)}
          class=${cx('w-full flex items-center gap-2 px-3 h-8 text-left hover:bg-hover', dir.status === 'loading' && 'opacity-60', e.hidden && 'text-muted')}>
          <${Icon} name="folder" size=${15} class="text-faint" /><span class="truncate">${e.name}</span>
        </button>`)}
        ${dir.truncated && html`<div class="px-3 py-2 text-xs text-faint">Showing the first 1000 folders.</div>`}
      </div>
      <div class="flex items-center justify-end gap-2 px-3 py-2 border-t border-line">
        <button type="button" onClick=${() => setOpen(false)} class="h-8 px-3 rounded-lg text-muted hover:bg-hover">Cancel</button>
        <button type="button" disabled=${!dir.path || dir.status !== 'ready'} onClick=${() => { setSelection(room, { workspace: dir.path }); setOpen(false); }}
          class="h-8 px-3 rounded-lg bg-accent text-accent-fg font-medium disabled:opacity-50">Use this folder</button>
      </div>
    </${Popover}>
  </div>`;
}

export function ContextMeter({ room }) {
  const c = room.ctx;
  if (!c || !c.total) return null;
  const used = c.live ?? c.estimate ?? 0;
  const pct = Math.min(100, Math.round((used / c.total) * 100));
  const r = 7, len = 2 * Math.PI * r;
  const color = pct >= 90 ? 'var(--bad)' : pct >= 70 ? 'var(--warn)' : 'var(--accent)';
  const text = `${pct}% (${fmtTokens(used)} / ${fmtTokens(c.total)}) context used${c.live != null ? ' · live' : ' · estimated'}`;
  return html`<span class="group relative inline-flex items-center justify-center size-8" tabindex="0" aria-label=${text}>
    <svg width="18" height="18" viewBox="0 0 18 18" aria-hidden="true">
      <circle cx="9" cy="9" r=${r} fill="none" stroke="var(--line)" stroke-width="2.5" />
      <circle cx="9" cy="9" r=${r} fill="none" stroke=${color} stroke-width="2.5" stroke-linecap="round"
        stroke-dasharray=${`${(pct / 100) * len} ${len}`} transform="rotate(-90 9 9)" />
    </svg>
    <span role="tooltip" class="pointer-events-none absolute bottom-full right-0 mb-2 whitespace-nowrap rounded-lg border border-line bg-raised px-2.5 py-1.5 text-xs shadow-pop opacity-0 group-hover:opacity-100 group-focus:opacity-100 transition-opacity">${text}</span>
  </span>`;
}
