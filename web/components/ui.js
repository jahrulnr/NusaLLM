// Small shared primitives: popover, dialog, select, spinner, state blocks.
import { html, useEffect, useRef } from '/vendor/preact/htm-preact.mjs';
import SlimSelect from '/vendor/slim-select/slimselect.es.js';
import { state, useStore, dismissToast } from '../lib/store.js';
import { Icon } from './icons.js';

export const cx = (...a) => a.filter(Boolean).join(' ');

export function Spinner({ size = 14, class: cls = '' }) {
  return html`<svg class=${cx('animate-spin shrink-0', cls)} width=${size} height=${size} viewBox="0 0 24 24" fill="none" aria-hidden="true">
    <circle cx="12" cy="12" r="9" stroke="currentColor" stroke-opacity=".25" stroke-width="3" />
    <path d="M21 12a9 9 0 0 0-9-9" stroke="currentColor" stroke-width="3" stroke-linecap="round" />
  </svg>`;
}

export function IconButton({ icon, label, onClick, class: cls = '', disabled, size = 16, active, ...rest }) {
  return html`<button type="button" title=${label} aria-label=${label} onClick=${onClick} disabled=${disabled}
    class=${cx('inline-flex items-center justify-center rounded-md size-8 text-muted hover:bg-hover hover:text-fg disabled:opacity-40 disabled:hover:bg-transparent transition-colors', active && 'bg-hover text-fg', cls)} ...${rest}>
    <${Icon} name=${icon} size=${size} />
  </button>`;
}

export function Button({ children, onClick, kind = 'default', class: cls = '', disabled, busy, type = 'button', ...rest }) {
  const kinds = {
    default: 'bg-raised border border-line hover:bg-hover text-fg',
    primary: 'bg-accent text-accent-fg hover:brightness-110',
    danger: 'bg-bad/15 text-bad hover:bg-bad/25 border border-bad/30',
    ghost: 'text-muted hover:text-fg hover:bg-hover',
  };
  return html`<button type=${type} onClick=${onClick} disabled=${disabled || busy}
    class=${cx('inline-flex items-center justify-center gap-1.5 whitespace-nowrap shrink-0 rounded-lg px-3 h-8 text-[13px] font-medium disabled:opacity-50 transition', kinds[kind], cls)} ...${rest}>
    ${busy && html`<${Spinner} size=${13} />`}${children}
  </button>`;
}

// Popover anchored to its wrapper; a bottom sheet on narrow screens.
// Closes on outside pointerdown and Escape.
export function Popover({ open, onClose, children, class: cls = '', placement = 'top-start', label }) {
  const ref = useRef();
  useEffect(() => {
    if (!open) return;
    const down = (e) => { if (e.target.closest?.('.ss-content')) return; if (ref.current && !ref.current.contains(e.target) && !ref.current.parentElement.contains(e.target)) onClose(); };
    const key = (e) => { if (e.key === 'Escape') { e.stopPropagation(); onClose(); } };
    document.addEventListener('pointerdown', down, true);
    document.addEventListener('keydown', key, true);
    return () => { document.removeEventListener('pointerdown', down, true); document.removeEventListener('keydown', key, true); };
  }, [open]);
  if (!open) return null;
  const pos = {
    'top-start': 'sm:bottom-full sm:left-0 sm:mb-2',
    'top-end': 'sm:bottom-full sm:right-0 sm:mb-2',
    'bottom-start': 'sm:top-full sm:left-0 sm:mt-1',
    'bottom-end': 'sm:top-full sm:right-0 sm:mt-1',
  }[placement];
  return html`
    <div class="fixed inset-0 z-40 bg-black/40 sm:hidden" aria-hidden="true"></div>
    <div ref=${ref} role="dialog" aria-label=${label}
      class=${cx('z-50 bg-raised border border-line rounded-xl shadow-pop text-[13px] overflow-hidden',
        'fixed inset-x-2 bottom-2 max-h-[75dvh] sm:absolute sm:inset-x-auto sm:bottom-auto', pos, cls)}>
      ${children}
    </div>`;
}

export function Dialog({ open, onClose, title, children, footer, class: cls = '' }) {
  const ref = useRef();
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);
  return html`<dialog ref=${ref} onClose=${onClose} onCancel=${(e) => { e.preventDefault(); onClose(); }}
    class=${cx('bg-panel text-fg border border-line rounded-2xl shadow-pop p-0 backdrop:bg-black/50 w-[min(640px,calc(100vw-24px))] max-h-[min(85dvh,900px)] m-auto', cls)}>
    ${open && html`<div class="flex flex-col max-h-[min(85dvh,900px)]">
      <header class="flex items-center justify-between gap-3 px-5 py-3.5 border-b border-line">
        <h2 class="text-[15px] font-semibold">${title}</h2>
        <${IconButton} icon="x" label="Close" onClick=${onClose} />
      </header>
      <div class="flex-1 min-h-0 overflow-y-auto px-5 py-4">${children}</div>
      ${footer && html`<footer class="flex items-center justify-end gap-2 px-5 py-3 border-t border-line">${footer}</footer>`}
    </div>`}
  </dialog>`;
}

// Select wraps slim-select; options: [{ value, text }].
export function Select({ value, options, onChange, search = false, class: cls = '', label }) {
  const el = useRef();
  const ss = useRef();
  const cb = useRef(onChange);
  cb.current = onChange;
  useEffect(() => {
    ss.current = new SlimSelect({
      select: el.current,
      // A modal <dialog> sits in the top layer, so the list must live
      // inside it; fixed positioning keeps viewport coordinates either way.
      settings: { showSearch: search, contentLocation: el.current.closest('dialog') || document.body, contentPosition: 'fixed', openPosition: 'auto', modal: 'off' },
      events: { afterChange: (sel) => { const v = sel[0]?.value; if (v !== undefined && v !== el.current._value) { el.current._value = v; cb.current(v); } } },
    });
    return () => ss.current?.destroy();
  }, []);
  useEffect(() => {
    if (!ss.current) return;
    el.current._value = value;
    ss.current.setData(options.map((o) => ({ value: o.value, text: o.text, selected: o.value === value })));
  }, [value, JSON.stringify(options)]);
  return html`<div class=${cx('min-w-40', cls)}><select ref=${el} aria-label=${label}></select></div>`;
}

export function Toggle({ checked, onChange, label, disabled }) {
  return html`<button type="button" role="switch" aria-checked=${checked} aria-label=${label} disabled=${disabled}
    onClick=${() => onChange(!checked)}
    class=${cx('relative inline-flex h-5 w-9 shrink-0 rounded-full transition-colors disabled:opacity-50', checked ? 'bg-accent' : 'bg-line')}>
    <span class=${cx('absolute top-0.5 size-4 rounded-full bg-white shadow transition-transform', checked ? 'translate-x-4.5' : 'translate-x-0.5')}></span>
  </button>`;
}

export function ErrorBlock({ title = 'Something went wrong', message, onRetry, class: cls = '' }) {
  return html`<div role="alert" class=${cx('rounded-xl border border-bad/30 bg-bad/10 px-4 py-3 text-[13px]', cls)}>
    <div class="flex items-start gap-2.5">
      <${Icon} name="alert" class="text-bad mt-0.5" />
      <div class="flex-1 min-w-0">
        <div class="font-medium">${title}</div>
        ${message && html`<div class="text-muted mt-0.5 break-words">${message}</div>`}
      </div>
      ${onRetry && html`<${Button} onClick=${onRetry}>Retry</${Button}>`}
    </div>
  </div>`;
}

export function Toasts() {
  const s = useStore();
  return html`<div class="fixed z-[60] bottom-4 right-4 left-4 sm:left-auto flex flex-col gap-2 items-end pointer-events-none" aria-live="polite">
    ${s.toasts.map((t) => html`<div key=${t.id} role=${t.kind === 'error' ? 'alert' : 'status'}
      class=${cx('pointer-events-auto max-w-sm w-full sm:w-auto flex items-start gap-2 rounded-xl border px-3.5 py-2.5 text-[13px] shadow-pop bg-raised',
        t.kind === 'error' ? 'border-bad/40' : 'border-line')}>
      ${t.kind === 'error' && html`<${Icon} name="alert" class="text-bad mt-0.5" />`}
      <span class="flex-1 break-words">${t.message}</span>
      <button class="text-faint hover:text-fg" aria-label="Dismiss" onClick=${() => dismissToast(t.id)}><${Icon} name="x" size=${14} /></button>
    </div>`)}
  </div>`;
}

export function Kbd({ children }) {
  return html`<kbd class="inline-flex items-center rounded border border-line bg-panel px-1 text-[11px] font-sans text-faint">${children}</kbd>`;
}

export const fmtTokens = (n) => (n >= 1e6 ? (n / 1e6).toFixed(n >= 1e7 ? 0 : 1) + 'M' : n >= 1e3 ? Math.round(n / 1e3) + 'K' : String(n ?? 0));

export function relTime(iso) {
  if (!iso) return '';
  const d = (Date.now() - new Date(iso).getTime()) / 1000;
  if (d < 60) return 'now';
  if (d < 3600) return Math.floor(d / 60) + 'm';
  if (d < 86400) return Math.floor(d / 3600) + 'h';
  if (d < 7 * 86400) return Math.floor(d / 86400) + 'd';
  return new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

export { state };
