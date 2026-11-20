// Tiny app store: one mutable state tree, change notification batched per
// animation frame, and a Preact hook that re-renders subscribers. Streaming
// frames mutate in place and call `emit()`, so a burst of deltas costs one
// render.
import { useLayoutEffect, useReducer, useRef } from '/vendor/preact/htm-preact.mjs';

export const state = {
  conn: 'connecting',          // connecting | open | closed
  route: { view: 'home' },     // { view: 'home' | 'chat' | 'settings', id?, section? }
  sidebarOpen: false,          // mobile drawer
  convs: { items: [], total: 0, hasMore: false, status: 'loading', error: null, more: false },
  gateways: { items: [], status: 'loading', error: null },
  rooms: {},                   // conversationId (or fresh key) -> Room
  update: null,
  toasts: [],
  prefs: loadPrefs(),
};

const subs = new Set();
let queued = false;
let version = 0;
export function emit() {
  if (queued) return;
  queued = true;
  // rAF batches deltas per frame; the timeout keeps hidden tabs (where
  // rAF pauses) current.
  const flush = () => {
    if (!queued) return;
    queued = false;
    version++;
    for (const fn of [...subs]) fn();
  };
  requestAnimationFrame(flush);
  setTimeout(flush, 100);
}

export function useStore() {
  const [, force] = useReducer((n) => n + 1, 0);
  const seen = useRef(version);
  seen.current = version;
  useLayoutEffect(() => {
    subs.add(force);
    // A flush between render and subscribe would otherwise be missed.
    if (seen.current !== version) force();
    return () => subs.delete(force);
  }, []);
  return state;
}

let toastSeq = 0;
export function toast(message, kind = 'info', ms = kind === 'error' ? 8000 : 3500) {
  const t = { id: ++toastSeq, message, kind };
  state.toasts = [...state.toasts, t];
  emit();
  if (ms) setTimeout(() => dismissToast(t.id), ms);
}
export function dismissToast(id) {
  state.toasts = state.toasts.filter((t) => t.id !== id);
  emit();
}

// UI preferences only (theme, last model choice) — never conversation
// content or credentials.
function loadPrefs() {
  try { return { theme: 'system', ...JSON.parse(localStorage.getItem('nusallm.prefs') || '{}') }; }
  catch { return { theme: 'system' }; }
}
export function setPref(key, value) {
  state.prefs = { ...state.prefs, [key]: value };
  try { localStorage.setItem('nusallm.prefs', JSON.stringify(state.prefs)); } catch { /* private mode */ }
  emit();
}
