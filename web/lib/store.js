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

// onEmit lets a test observe announcements (a write that changes nothing must
// announce nothing) without mounting the component tree.
export function onEmit(fn) { subs.add(fn); return () => subs.delete(fn); }

// ---------- record application (decisions.md D-08) ----------
//
// One funnel for every conversation record a read or a write brings in. The
// store remembers the last applied `updatedAt` per id: a newer answer lands, an
// older one is discarded (a slow read can no longer undo a rename), and an
// equal one only lands when it would change what a surface draws. Nothing here
// touches room-local state — draft, queue, steers, live turn, history window,
// reading position and the unsent picker choice stay the room's.

// The fields each surface draws, in a fixed order. Equal key → no write, so a
// refresh that changed only counters costs no render. Adding a consumer of a
// new field means adding it here.
const KEY_FIELDS = {
  // Title and relative time per row, plus the live dot.
  sidebar: ['title', 'updatedAt', 'activeTurnId', 'activeTurnKind'],
  // The thread header, the compact action, the picker's binding, the live turn.
  header: ['title', 'route', 'modelId', 'workspace', 'canCompact', 'activeTurnId', 'activeTurnKind'],
};

export function recordKey(surface, record) {
  const fields = KEY_FIELDS[surface] || KEY_FIELDS.header;
  return fields.map((f) => {
    const v = record?.[f];
    return v === undefined || v === null ? '' : String(v);
  }).join('\u0001');
}

const versions = new Map();   // id -> last applied version, in ms
const records = new Map();    // id -> the newest record the store accepted
const writes = new Map();     // id -> local write serial

const versionOf = (rec) => {
  const t = Date.parse(rec?.updatedAt ?? '');
  return Number.isFinite(t) ? t : 0;
};

// noteRecordWrite marks a local write for `id`; a read that started before it
// cannot carry its result, so the reader re-reads once (chat.js).
export function noteRecordWrite(id) { if (id) writes.set(id, (writes.get(id) || 0) + 1); }
export function recordWriteSerial(id) { return writes.get(id) || 0; }
export function forgetRecord(id) { versions.delete(id); records.delete(id); writes.delete(id); }

// newestRecord answers the newest record the store was given for `id`, even
// when no row or room holds it (the list may page it back in later).
export function newestRecord(id) { return records.get(id); }

// applyRecord funnels one answer for one conversation into the list row and,
// when the room is open, its info. Returns true when the answer was accepted
// (it is now the newest this store knows) and false when it was discarded as
// stale — a caller that rebuilds a list must not resurrect a rejected record.
export function applyRecord(id, info) {
  if (!id || !info) return false;
  const known = versions.get(id);
  const version = versionOf(info);
  if (known !== undefined && version < known) return false;
  versions.set(id, Math.max(version, known ?? 0));
  records.set(id, info);
  let changed = false;
  const items = state.convs.items;
  const at = items.findIndex((x) => x.id === id);
  if (at >= 0 && recordKey('sidebar', items[at]) !== recordKey('sidebar', info)) {
    items[at] = info;
    changed = true;
  }
  const room = state.rooms[id];
  if (room && recordKey('header', room.info) !== recordKey('header', info)) {
    room.info = info;
    changed = true;
  }
  if (changed) emit();
  return true;
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
