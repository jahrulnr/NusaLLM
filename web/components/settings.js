// Settings workspace: providers (gateways), application settings, tool
// catalog, and about/update. Global scope only — per-thread choices live in
// the composer.
import { html, useEffect, useState } from '/vendor/preact/htm-preact.mjs';
import { useStore, setPref, toast, emit, state } from '../lib/store.js';
import { api, errorText } from '../lib/api.js';
import { loadGateways, navigate } from '../lib/chat.js';
import { Icon } from './icons.js';
import { cx, Button, Dialog, Select, Toggle, ErrorBlock, Spinner, fmtTokens } from './ui.js';
import { MenuButton } from './chat.js';

const SECTIONS = [
  { id: 'providers', label: 'Providers', icon: 'server' },
  { id: 'general', label: 'General', icon: 'sliders' },
  { id: 'tools', label: 'Tools', icon: 'tool' },
  { id: 'about', label: 'About', icon: 'info' },
];

export function SettingsView() {
  const s = useStore();
  const section = SECTIONS.find((x) => x.id === s.route.section) ? s.route.section : 'providers';
  return html`<div class="flex flex-col h-full">
    <header class="flex items-center gap-1 h-12 px-2 sm:px-4 border-b border-line shrink-0">
      <${MenuButton} />
      <span class="text-muted">Settings</span><span class="text-faint mx-1">/</span>
      <span class="font-medium">${SECTIONS.find((x) => x.id === section).label}</span>
    </header>
    <div class="border-b border-line px-2 sm:px-4 shrink-0 overflow-x-auto">
      <div class="flex gap-1">
        ${SECTIONS.map((x) => html`<a key=${x.id} href=${'#/settings/' + x.id} aria-current=${x.id === section ? 'page' : undefined}
          class=${cx('flex items-center gap-2 h-10 px-3 text-[13px] border-b-2 -mb-px whitespace-nowrap', x.id === section ? 'border-accent text-fg' : 'border-transparent text-muted hover:text-fg')}>
          <${Icon} name=${x.icon} size=${15} />${x.label}
        </a>`)}
      </div>
    </div>
    <div class="flex-1 min-h-0 overflow-y-auto">
      <div class="mx-auto w-full max-w-3xl px-4 sm:px-6 py-6 sm:py-8">
        ${section === 'providers' && html`<${Providers} arg=${s.route.arg} />`}
        ${section === 'general' && html`<${General} />`}
        ${section === 'tools' && html`<${Tools} />`}
        ${section === 'about' && html`<${About} />`}
      </div>
    </div>
  </div>`;
}

function SectionTitle({ title, desc, action }) {
  return html`<div class="flex items-end justify-between gap-4 mb-4">
    <div><h2 class="text-[17px] font-semibold">${title}</h2>${desc && html`<p class="text-muted text-[13px] mt-0.5">${desc}</p>`}</div>
    ${action}
  </div>`;
}

function Card({ children, class: cls = '' }) {
  return html`<div class=${cx('rounded-xl border border-line bg-panel divide-y divide-line', cls)}>${children}</div>`;
}

function Row({ title, desc, children }) {
  return html`<div class="flex flex-col sm:flex-row sm:items-center gap-2 sm:gap-6 px-4 py-3.5">
    <div class="flex-1 min-w-0"><div class="text-[13px] font-medium">${title}</div>${desc && html`<div class="text-xs text-muted mt-0.5">${desc}</div>`}</div>
    <div class="sm:shrink-0 flex items-center gap-2">${children}</div>
  </div>`;
}

const inputCls = 'h-8 rounded-lg bg-raised border border-line px-2.5 text-[13px] outline-none focus:border-accent w-full';

// ---------------- Providers ----------------

const KEY_STATE = {
  unset: ['No key', 'text-faint'],
  env: ['Env variable', 'text-ok'],
  vaulted: ['Key stored', 'text-ok'],
  broken: ['Key unresolved', 'text-bad'],
};

function Providers({ arg }) {
  const s = useStore();
  const g = s.gateways;
  const editing = arg === 'new' ? { isNew: true } : g.items.find((x) => x.id === arg);
  const close = () => navigate('#/settings/providers');
  return html`<div>
    <${SectionTitle} title="Providers" desc="Model gateways the composer can route to. Keys are write-only — they never come back to the browser."
      action=${html`<${Button} kind="primary" onClick=${() => navigate('#/settings/providers/new')}><${Icon} name="plus" size=${14} /> Add provider</${Button}>`} />
    ${g.status === 'loading' && html`<div class="flex flex-col gap-2">${[1, 2].map(() => html`<div class="skeleton h-[72px] rounded-xl"></div>`)}</div>`}
    ${g.status === 'error' && html`<${ErrorBlock} title="Could not load providers" message=${g.error} onRetry=${loadGateways} />`}
    ${g.status === 'ready' && !g.items.length && html`<div class="rounded-xl border border-dashed border-line p-8 text-center">
      <p class="font-medium">No providers yet</p>
      <p class="text-muted text-[13px] mt-1">Add an OpenAI-compatible, Responses, Anthropic Messages, or Gemini endpoint to start chatting.</p>
      <${Button} class="mt-4" kind="primary" onClick=${() => navigate('#/settings/providers/new')}>Add provider</${Button}>
    </div>`}
    <div class="flex flex-col gap-2">
      ${g.items.map((x) => {
        const [kl, kc] = KEY_STATE[x.keyState] || ['', ''];
        const listed = x.models.filter((m) => m.listed !== false).length;
        return html`<button key=${x.id} type="button" onClick=${() => navigate('#/settings/providers/' + encodeURIComponent(x.id))}
          class="text-left rounded-xl border border-line bg-panel hover:bg-hover px-4 py-3 flex items-center gap-4">
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2"><span class="font-medium truncate">${x.name || x.id}</span>
              ${x.name && x.name !== x.id && html`<span class="text-xs text-faint font-mono">${x.id}</span>`}
              ${x.broken && html`<span class="text-xs text-bad">broken</span>`}</div>
            <div class="text-xs text-muted truncate mt-0.5">${x.style} · ${x.endpoint}</div>
          </div>
          <div class="hidden sm:flex flex-col items-end text-xs gap-0.5">
            <span class=${kc}>${kl}</span><span class="text-faint">${listed} models</span>
          </div>
          <${Icon} name="chevronRight" class="text-faint" />
        </button>`;
      })}
    </div>
    ${arg && g.status === 'ready' && !editing && html`<${ErrorBlock} class="mt-4" title="Provider not found" message=${`No provider named “${arg}”.`} />`}
    <${GatewayDialog} key=${(arg || '') + (editing ? ':1' : ':0')} gateway=${editing} onClose=${close} />
  </div>`;
}

const STYLES = [
  { value: 'completion', text: 'Chat Completions (OpenAI-compatible)' },
  { value: 'responses', text: 'Responses (OpenAI)' },
  { value: 'messages', text: 'Messages (Anthropic)' },
  { value: 'generatecontent', text: 'generateContent (Gemini)' },
];

function stripModel(m) {
  const { listed, ...rest } = m;
  return rest;
}

function GatewayDialog({ gateway, onClose }) {
  const open = !!gateway;
  const isNew = !!gateway?.isNew;
  const [f, setF] = useState(() => ({
    id: gateway?.id || '', name: gateway?.name || '', style: gateway?.style || 'completion',
    endpoint: gateway?.endpoint || '', stream: gateway?.stream ?? true, apiKey: '', clearKey: false,
  }));
  const [models, setModels] = useState(gateway?.models || []);
  const [q, setQ] = useState('');
  const [busy, setBusy] = useState('');
  const [err, setErr] = useState('');
  const [note, setNote] = useState('');
  const [confirmDelete, setConfirmDelete] = useState(false);
  const set = (k) => (v) => setF((x) => ({ ...x, [k]: v }));

  const idOk = /^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(f.id);
  const valid = idOk && /^https?:\/\/\S+$/.test(f.endpoint);

  const save = async () => {
    setBusy('save'); setErr(''); setNote('');
    const doc = { name: f.name, style: f.style, endpoint: f.endpoint.trim(), stream: f.stream, models: models.map(stripModel) };
    if (f.clearKey) doc.apiKey = '';
    else if (f.apiKey) doc.apiKey = f.apiKey;
    try {
      const res = await api.saveGateway(f.id, doc);
      await loadGateways();
      const added = res.added?.length || 0;
      toast(`Saved ${res.name || res.id}${added ? ` · imported ${added} model${added === 1 ? '' : 's'}` : ''}.`);
      if (res.catalogError) toast('Saved, but the model catalog could not be fetched: ' + res.catalogError, 'error');
      onClose();
    } catch (e) { setErr(errorText(e)); }
    setBusy('');
  };

  const refresh = async () => {
    setBusy('import'); setErr(''); setNote('');
    try {
      const res = await api.importModels(f.id);
      setModels(res.models);
      await loadGateways();
      setNote(res.catalogError ? 'Catalog fetch failed: ' + res.catalogError : `Imported ${res.added.length} new model${res.added.length === 1 ? '' : 's'}.`);
    } catch (e) { setErr(errorText(e)); }
    setBusy('');
  };

  const remove = async () => {
    setBusy('delete'); setErr('');
    try {
      await api.deleteGateway(f.id);
      await loadGateways();
      toast(`Removed ${f.name || f.id}.`);
      onClose();
    } catch (e) { setErr(errorText(e)); setConfirmDelete(false); }
    setBusy('');
  };

  const needle = q.trim().toLowerCase();
  const shown = models.filter((m) => !needle || m.id.toLowerCase().includes(needle) || (m.name || '').toLowerCase().includes(needle));
  const [kl, kc] = KEY_STATE[gateway?.keyState] || KEY_STATE.unset;

  return html`<${Dialog} open=${open} onClose=${() => !busy && onClose()} title=${isNew ? 'Add provider' : `Edit ${gateway?.name || gateway?.id || ''}`}
    footer=${html`
      ${!isNew && html`<${Button} kind="danger" class="mr-auto" disabled=${!!busy} onClick=${() => setConfirmDelete(true)}>Delete</${Button}>`}
      <${Button} kind="ghost" disabled=${!!busy} onClick=${onClose}>Cancel</${Button}>
      <${Button} kind="primary" busy=${busy === 'save'} disabled=${!valid || !!busy} onClick=${save}>${isNew ? 'Add provider' : 'Save'}</${Button}>`}>
    <form class="flex flex-col gap-4" onSubmit=${(e) => { e.preventDefault(); if (valid) save(); }}>
      ${err && html`<${ErrorBlock} title="Could not save" message=${err} />`}
      <div class="grid sm:grid-cols-2 gap-3">
        <label class="flex flex-col gap-1.5 text-[13px]"><span class="text-muted">ID</span>
          <input class=${inputCls} value=${f.id} disabled=${!isNew} onInput=${(e) => set('id')(e.target.value)} placeholder="openrouter" autofocus=${isNew} />
          ${isNew && f.id && !idOk && html`<span class="text-xs text-bad">Letters, digits, dot, dash, underscore.</span>`}
        </label>
        <label class="flex flex-col gap-1.5 text-[13px]"><span class="text-muted">Display name</span>
          <input class=${inputCls} value=${f.name} onInput=${(e) => set('name')(e.target.value)} placeholder="Optional" />
        </label>
      </div>
      <label class="flex flex-col gap-1.5 text-[13px]"><span class="text-muted">API style</span>
        <${Select} label="API style" value=${f.style} options=${STYLES} onChange=${set('style')} />
      </label>
      <label class="flex flex-col gap-1.5 text-[13px]"><span class="text-muted">Endpoint</span>
        <input class=${inputCls} value=${f.endpoint} onInput=${(e) => set('endpoint')(e.target.value)} placeholder="https://api.example.com/v1" inputmode="url" />
      </label>
      <div class="flex flex-col gap-1.5 text-[13px]">
        <div class="flex items-center justify-between"><span class="text-muted">API key</span>${!isNew && html`<span class=${cx('text-xs', kc)}>${kl}</span>`}</div>
        <input class=${inputCls} type="password" autocomplete="off" value=${f.apiKey} disabled=${f.clearKey}
          onInput=${(e) => set('apiKey')(e.target.value)} placeholder=${isNew ? 'Paste a key, or env:VAR_NAME' : 'Leave blank to keep the stored key'} />
        ${!isNew && gateway?.keySet && html`<label class="flex items-center gap-2 text-xs text-muted cursor-pointer">
          <input type="checkbox" checked=${f.clearKey} onChange=${(e) => set('clearKey')(e.target.checked)} class="accent-[var(--accent)]" /> Remove the stored key on save
        </label>`}
      </div>
      <div class="flex items-center justify-between text-[13px]">
        <div><div>Stream responses</div><div class="text-xs text-muted">Turn off only for providers that reject streaming.</div></div>
        <${Toggle} label="Stream responses" checked=${f.stream} onChange=${set('stream')} />
      </div>

      <div class="flex flex-col gap-2 text-[13px]">
        <div class="flex items-center justify-between gap-2">
          <span class="text-muted">Models <span class="text-faint">(${models.length})</span></span>
          ${!isNew && html`<${Button} kind="ghost" busy=${busy === 'import'} disabled=${!!busy} onClick=${refresh}><${Icon} name="refresh" size=${14} /> Refresh from provider</${Button}>`}
        </div>
        ${note && html`<div class="text-xs text-muted">${note}</div>`}
        ${isNew && html`<p class="text-xs text-muted">Models are imported from the provider's catalog when you save.</p>`}
        <div class="flex gap-2">
          ${models.length > 0 && html`<input class=${inputCls} value=${q} onInput=${(e) => setQ(e.target.value)} placeholder="Filter models" />`}
          <${AddModel} models=${models} onAdd=${(id) => { setModels([...models, { id }]); setQ(id); }} />
        </div>
        ${models.length > 0 && html`<div class="rounded-lg border border-line max-h-[26rem] overflow-y-auto divide-y divide-line">
          ${shown.map((m) => html`<${ModelRow} key=${m.id} model=${m} gatewayStyle=${f.style}
            onChange=${(next) => setModels(models.map((x) => (x === m ? next : x)))}
            onRemove=${() => setModels(models.filter((x) => x !== m))} />`)}
          ${!shown.length && html`<div class="px-3 py-2 text-faint">No model matches.</div>`}
        </div>`}
        ${models.length > 0 && html`<p class="text-xs text-faint">Model changes apply when you save the provider.</p>`}
      </div>
    </form>
    <${Dialog} open=${confirmDelete} onClose=${() => setConfirmDelete(false)} title="Delete provider?"
      footer=${html`<${Button} kind="ghost" onClick=${() => setConfirmDelete(false)}>Keep</${Button}>
        <${Button} kind="danger" busy=${busy === 'delete'} onClick=${remove}>Delete</${Button}>`}>
      <p class="text-muted">Removes ${f.name || f.id} and its stored key. Threads bound to it need another model before their next turn.</p>
    </${Dialog}>
  </${Dialog}>`;
}

function AddModel({ models, onAdd }) {
  const [open, setOpen] = useState(false);
  const [id, setId] = useState('');
  const dup = models.some((m) => m.id === id.trim());
  const add = () => { const v = id.trim(); if (!v || dup) return; onAdd(v); setId(''); setOpen(false); };
  if (!open) return html`<${Button} kind="ghost" onClick=${() => setOpen(true)}><${Icon} name="plus" size=${14} /> Add model</${Button}>`;
  return html`<div class="flex gap-2 shrink-0">
    <input class=${cx(inputCls, 'sm:w-48', dup && 'border-bad')} autofocus value=${id} placeholder="model id" aria-label="New model id"
      onInput=${(e) => setId(e.target.value)} onKeyDown=${(e) => { if (e.key === 'Enter') { e.preventDefault(); add(); } if (e.key === 'Escape') { e.stopPropagation(); setOpen(false); } }} />
    <${Button} onClick=${add} disabled=${!id.trim() || dup}>Add</${Button}>
  </div>`;
}

const EFFORTS = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];
const STYLE_SHORT = { completion: 'Chat Completions', responses: 'Responses', messages: 'Messages', generatecontent: 'generateContent' };

// ModelRow: one catalog entry with an inline editor for the per-model
// overrides the gateway document carries (API style pin, defaults, limits).
function ModelRow({ model: m, gatewayStyle, onChange, onRemove }) {
  const [open, setOpen] = useState(false);
  const put = (k, v) => {
    const next = { ...m };
    if (v === '' || v === undefined || v === null || Number.isNaN(v)) delete next[k]; else next[k] = v;
    onChange(next);
  };
  const num = (k, int) => (e) => {
    const t = e.target.value.trim();
    put(k, t === '' ? '' : int ? parseInt(t, 10) : parseFloat(t));
  };
  const field = (label, k, opts = {}) => html`<label class="flex flex-col gap-1">
    <span class="text-xs text-muted">${label}</span>
    <input class=${inputCls} inputmode=${opts.int ? 'numeric' : 'decimal'} value=${m[k] ?? ''} placeholder=${opts.ph || 'Provider default'}
      onChange=${num(k, opts.int)} />
  </label>`;
  return html`<div class=${cx(m.listed === false && !open && 'opacity-50')}>
    <div class="flex items-center gap-2 px-3 h-9">
      <button type="button" onClick=${() => setOpen(!open)} aria-expanded=${open} class="flex-1 min-w-0 flex items-center gap-2 text-left">
        <${Icon} name=${open ? 'chevronDown' : 'chevronRight'} size=${13} class="text-faint" />
        <span class="truncate">${m.name || m.id}${m.name && m.name !== m.id && html`<span class="ml-2 text-faint font-mono text-xs">${m.id}</span>`}</span>
      </button>
      ${m.style && m.style !== gatewayStyle && html`<span class="text-[11px] text-accent border border-accent/40 rounded px-1.5 shrink-0" title="Uses its own API style">${STYLE_SHORT[m.style]}</span>`}
      ${m.contextWindow > 0 && html`<span class="text-xs text-faint shrink-0">${fmtTokens(m.contextWindow)}</span>`}
      <button type="button" aria-label=${'Remove ' + m.id} title="Remove from this provider"
        onClick=${onRemove} class="size-6 inline-flex items-center justify-center rounded text-faint hover:text-fg hover:bg-hover shrink-0">
        <${Icon} name="x" size=${13} /></button>
    </div>
    ${open && html`<div class="px-3 pb-3 pt-1 grid sm:grid-cols-2 gap-3 bg-bg/40">
      <label class="flex flex-col gap-1 sm:col-span-2">
        <span class="text-xs text-muted">API style for this model</span>
        <${Select} label="API style for this model" value=${m.style || ''}
          options=${[{ value: '', text: `Same as provider (${STYLE_SHORT[gatewayStyle]})` }, ...STYLES]}
          onChange=${(v) => put('style', v)} />
        <span class="text-xs text-faint">Pin when this model only works on another protocol — e.g. GPT models on Responses, Claude on Messages.</span>
      </label>
      <label class="flex flex-col gap-1">
        <span class="text-xs text-muted">Display name</span>
        <input class=${inputCls} value=${m.name ?? ''} placeholder=${m.id} onChange=${(e) => put('name', e.target.value.trim())} />
      </label>
      <label class="flex flex-col gap-1">
        <span class="text-xs text-muted">Default reasoning effort</span>
        <${Select} label="Default reasoning effort" value=${m.effort || 'default'}
          options=${[{ value: 'default', text: 'Provider default' }, ...EFFORTS.map((e) => ({ value: e, text: e[0].toUpperCase() + e.slice(1) }))]}
          onChange=${(v) => put('effort', v === 'default' ? '' : v)} />
      </label>
      ${field('Context window (tokens)', 'contextWindow', { int: true, ph: 'Unknown' })}
      ${field('Max output tokens', 'maxOutputTokens', { int: true })}
      ${field('Temperature', 'temperature')}
      ${field('Top P', 'topP')}
    </div>`}
  </div>`;
}

// ---------------- General ----------------

const FIELDS = [
  { group: 'Conversation', note: 'Applies to the next turn.', items: [
    { key: 'conversation.max_turn_steps', label: 'Max steps per turn', desc: 'Model calls one turn may make before it stops.', type: 'int' },
    { key: 'conversation.compact_budget_pct', label: 'Auto-compact threshold', desc: 'Percent of the context window that triggers compaction.', type: 'int', suffix: '%' },
    { key: 'conversation.compact_keep_tail', label: 'Messages kept on compaction', desc: 'Most recent messages left verbatim.', type: 'int' },
    { key: 'conversation.mailbox_cap', label: 'Room queue size', desc: 'Pending jobs a thread may hold.', type: 'int' },
  ] },
  { group: 'Provider network', note: 'Applies after restart.', items: [
    { key: 'service.header_timeout', label: 'First-byte timeout', desc: 'How long to wait for a provider to start answering, e.g. 30s.', type: 'duration' },
    { key: 'service.idle_timeout', label: 'Stream idle timeout', desc: 'Longest silence allowed mid-stream, e.g. 90s.', type: 'duration' },
  ] },
  { group: 'Server', note: 'Applies after restart.', items: [
    { key: 'server.port', label: 'Port', desc: 'Loopback port for the web UI and API.', type: 'int' },
    { key: 'server.hosts', label: 'Allowed host names', desc: 'Comma-separated, without port.', type: 'list' },
  ] },
  { group: 'Logs', note: 'Applies after restart.', items: [
    { key: 'log.max_lines', label: 'Lines per log file', type: 'int' },
    { key: 'log.max_files', label: 'Log files kept', type: 'int' },
  ] },
];

const getPath = (o, k) => k.split('.').reduce((a, p) => a?.[p], o);

function SettingField({ field, value, onSaved }) {
  const show = (v) => (field.type === 'list' ? (v || []).join(', ') : v ?? '');
  const [text, setText] = useState(show(value));
  const [st, setSt] = useState({ s: 'idle' });
  useEffect(() => setText(show(value)), [JSON.stringify(value)]);
  const save = async () => {
    if (text === show(value)) return;
    let v = text.trim();
    if (field.type === 'int') {
      if (!/^\d+$/.test(v)) { setSt({ s: 'error', m: 'Enter a whole number.' }); return; }
      v = Number(v);
    } else if (field.type === 'list') v = v.split(',').map((x) => x.trim()).filter(Boolean);
    setSt({ s: 'saving' });
    try {
      onSaved(await api.setSetting(field.key, v));
      setSt({ s: 'saved' });
      setTimeout(() => setSt((x) => (x.s === 'saved' ? { s: 'idle' } : x)), 1500);
    } catch (e) { setSt({ s: 'error', m: errorText(e) }); }
  };
  return html`<${Row} title=${field.label} desc=${field.desc}>
    <div class="flex flex-col items-stretch sm:items-end gap-1 w-full sm:w-auto">
      <div class="flex items-center gap-2">
        <input class=${cx(inputCls, field.type === 'list' ? 'sm:w-64' : 'sm:w-32', st.s === 'error' && 'border-bad')} value=${text}
          inputmode=${field.type === 'int' ? 'numeric' : undefined} aria-label=${field.label}
          onInput=${(e) => setText(e.target.value)} onBlur=${save} onKeyDown=${(e) => e.key === 'Enter' && e.target.blur()} />
        <span class="w-4 flex justify-center">
          ${st.s === 'saving' && html`<${Spinner} size=${13} />`}
          ${st.s === 'saved' && html`<${Icon} name="check" size=${14} class="text-ok" />`}
        </span>
      </div>
      ${st.s === 'error' && html`<span class="text-xs text-bad max-w-72">${st.m}</span>`}
    </div>
  </${Row}>`;
}

function General() {
  const s = useStore();
  const [doc, setDoc] = useState({ status: 'loading' });
  const load = () => {
    setDoc({ status: 'loading' });
    api.settings().then((d) => setDoc({ status: 'ready', d }), (e) => setDoc({ status: 'error', error: errorText(e) }));
  };
  useEffect(load, []);
  return html`<div class="flex flex-col gap-8">
    <section>
      <${SectionTitle} title="Appearance" desc="Stored in this browser only." />
      <${Card}>
        <${Row} title="Theme">
          <${Select} label="Theme" class="w-40" value=${s.prefs.theme}
            options=${[{ value: 'system', text: 'System' }, { value: 'dark', text: 'Dark' }, { value: 'light', text: 'Light' }]}
            onChange=${(v) => setPref('theme', v)} />
        </${Row}>
      </${Card}>
    </section>
    ${doc.status === 'loading' && html`<div class="flex flex-col gap-2"><div class="skeleton h-5 w-40"></div><div class="skeleton h-40 rounded-xl"></div></div>`}
    ${doc.status === 'error' && html`<${ErrorBlock} title="Could not load settings" message=${doc.error} onRetry=${load} />`}
    ${doc.status === 'ready' && FIELDS.map((g) => html`<section key=${g.group}>
      <${SectionTitle} title=${g.group} desc=${g.note} />
      <${Card}>${g.items.map((f) => html`<${SettingField} key=${f.key} field=${f} value=${getPath(doc.d, f.key)} onSaved=${(d) => setDoc({ status: 'ready', d })} />`)}</${Card}>
    </section>`)}
  </div>`;
}

// ---------------- Tools ----------------

function Tools() {
  const [t, setT] = useState({ status: 'loading' });
  const [open, setOpen] = useState(null);
  const load = () => {
    setT({ status: 'loading' });
    api.tools().then((r) => setT({ status: 'ready', items: r.data }), (e) => setT({ status: 'error', error: errorText(e), notFound: e.status === 404 }));
  };
  useEffect(load, []);
  const PERM = { agent: 'Agent', system: 'System', general: 'General' };
  return html`<div>
    <${SectionTitle} title="Tools" desc="What the model can call in a new thread. Tool input and output show up in the transcript." />
    ${t.status === 'loading' && html`<div class="skeleton h-64 rounded-xl"></div>`}
    ${t.status === 'error' && html`<${ErrorBlock} title=${t.notFound ? 'No tool catalog' : 'Could not load tools'} message=${t.error} onRetry=${t.notFound ? null : load} />`}
    ${t.status === 'ready' && !t.items.length && html`<p class="text-muted">No tools are registered.</p>`}
    ${t.status === 'ready' && t.items.length > 0 && html`<${Card}>
      ${t.items.map((x) => html`<div key=${x.name}>
        <button type="button" onClick=${() => setOpen(open === x.name ? null : x.name)} aria-expanded=${open === x.name}
          class="w-full flex items-center gap-3 px-4 py-3 text-left hover:bg-hover">
          <span class=${cx('size-2 rounded-full shrink-0', x.enabled ? 'bg-ok' : 'bg-faint')} title=${x.enabled ? 'Enabled' : 'Disabled'}></span>
          <span class="font-mono text-[13px]">${x.name}</span>
          <span class="flex-1 truncate text-xs text-muted">${x.description}</span>
          <span class="text-[11px] text-faint border border-line rounded px-1.5">${PERM[x.permission] || x.permission}</span>
        </button>
        ${open === x.name && html`<div class="px-4 pb-3 flex flex-col gap-2">
          <p class="text-[13px] text-muted whitespace-pre-wrap">${x.description}</p>
          ${x.parameters && html`<pre class="rawtext">${JSON.stringify(x.parameters, null, 2)}</pre>`}
        </div>`}
      </div>`)}
    </${Card}>`}
  </div>`;
}

// ---------------- About ----------------

function About() {
  const s = useStore();
  const u = s.update;
  const [busy, setBusy] = useState('');
  const [prog, setProg] = useState(null);

  const check = async () => {
    setBusy('check');
    try { state.update = await api.checkUpdate(); emit(); } catch (e) { toast(errorText(e), 'error'); }
    setBusy('');
  };
  const poll = async () => {
    for (;;) {
      let p;
      try { p = await api.installProgress(); } catch { p = null; }
      if (p) setProg(p);
      if (p && p.state !== 'running') return p;
      await new Promise((r) => setTimeout(r, 1000));
    }
  };
  const install = async () => {
    setBusy('install');
    try {
      setProg(await api.installUpdate());
      const p = await poll();
      if (p.state === 'succeeded') {
        // The server restarts; wait for it to answer again, then reload.
        for (let i = 0; i < 120; i++) {
          await new Promise((r) => setTimeout(r, 1000));
          try { const v = await api.update(); if (v.current !== u.current) { location.reload(); return; } } catch { /* restarting */ }
        }
        toast('Update installed. Reload the page once the server is back.');
      }
    } catch (e) { toast(errorText(e), 'error'); }
    setBusy('');
  };

  const pct = prog?.bytesTotal ? Math.round((prog.bytesDone / prog.bytesTotal) * 100) : null;
  return html`<div class="flex flex-col gap-8">
    <section>
      <${SectionTitle} title="About NusaLLM" desc="A local agent harness. Everything runs on this machine." />
      <${Card}>
        <${Row} title="Version" desc=${u?.enabled === false ? 'Development build — update checks are off.' : null}>
          <span class="font-mono text-[13px]">${u?.current || '—'}</span>
        </${Row}>
        ${u?.enabled !== false && html`<${Row} title="Latest release"
          desc=${u?.error ? u.error : u?.checkedAt ? 'Checked ' + new Date(u.checkedAt).toLocaleString() : 'Not checked yet'}>
          ${u?.latest && html`<span class="font-mono text-[13px]">${u.latest}</span>`}
          <${Button} busy=${busy === 'check'} disabled=${!!busy} onClick=${check}>Check now</${Button}>
        </${Row}>`}
        ${u?.available && html`<div class="px-4 py-3.5 flex flex-col gap-3">
          <div class="flex items-center justify-between gap-4">
            <div><div class="text-[13px] font-medium">Update to ${u.latest}</div>
              ${u.releaseUrl && html`<a class="text-xs text-accent underline" href=${u.releaseUrl} target="_blank" rel="noopener noreferrer">Release notes</a>`}</div>
            ${u.installable
              ? html`<${Button} kind="primary" busy=${busy === 'install'} disabled=${!!busy} onClick=${install}><${Icon} name="download" size=${14} /> Install & restart</${Button}>`
              : html`<code class="text-xs text-muted">${u.installCommand}</code>`}
          </div>
          ${prog && html`<div class="flex flex-col gap-1.5" role="status">
            <div class="h-1.5 rounded-full bg-line overflow-hidden"><div class="h-full bg-accent transition-all" style=${{ width: (pct ?? (prog.state === 'succeeded' ? 100 : 5)) + '%' }}></div></div>
            <div class="text-xs text-muted">${prog.state === 'failed' ? html`<span class="text-bad">${prog.error || 'Install failed.'}</span>` : prog.state === 'succeeded' ? 'Installed — restarting the server…' : `${prog.phase || 'Working'}${pct != null ? ` · ${pct}%` : ''}`}</div>
          </div>`}
          ${u.notes && html`<details class="text-[13px]"><summary class="cursor-pointer text-muted">What's new</summary><pre class="rawtext mt-2">${u.notes}</pre></details>`}
        </div>`}
      </${Card}>
    </section>
  </div>`;
}
