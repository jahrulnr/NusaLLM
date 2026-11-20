// Chat controller: owns the socket, the per-room state, and every command.
// Components read `state` and call these functions; nothing else talks to
// the socket.
import { state, emit, toast } from './store.js';
import { api, errorText } from './api.js';
import { TurnSocket, newRequestId } from './socket.js';
import { newTurn, applyFrame } from './turn.js';

const HISTORY_PAGE = 60;
const FRESH = 'new';

// requestId -> { key, kind, text, messageId? } for commands awaiting a reply.
const requests = new Map();
let socket;

// ---------- rooms ----------

function blankRoom(key, id) {
  return {
    key, id,
    info: null, infoStatus: id ? 'loading' : 'ready', infoError: null,
    messages: [], hist: { status: id ? 'loading' : 'ready', error: null, start: 0, total: 0, more: false },
    live: null, pendingOpen: null, settling: false,
    failed: null, queue: [], steers: [],
    draft: '', sel: {}, ctx: null, cutHistory: false,
  };
}

export function room(key) {
  if (!state.rooms[key]) state.rooms[key] = blankRoom(key, key === FRESH ? null : key);
  return state.rooms[key];
}
export const freshRoom = () => room(FRESH);
export const currentRoom = () => (state.route.view === 'chat' ? room(state.route.id) : state.route.view === 'home' ? freshRoom() : null);
export const isBusy = (r) => !!(r.live || r.pendingOpen || r.settling);

// ---------- routing ----------

export function navigate(hash) {
  if (location.hash !== hash) location.hash = hash;
  else applyRoute();
}

function applyRoute() {
  const h = location.hash.replace(/^#\/?/, '');
  const [a, b] = h.split('/');
  if (a === 'c' && b) state.route = { view: 'chat', id: decodeURIComponent(b) };
  else if (a === 'settings') state.route = { view: 'settings', section: b || 'providers', arg: h.split('/')[2] };
  else state.route = { view: 'home' };
  state.sidebarOpen = false;
  if (state.route.view === 'chat') openRoom(state.route.id);
  if (state.route.view === 'home') fetchContext(freshRoom());
  emit();
}

// ---------- boot ----------

export function start() {
  window.addEventListener('hashchange', applyRoute);
  socket = new TurnSocket({
    onFrame,
    onState: (s) => { state.conn = s; emit(); },
    onOpen: reattach,
  });
  loadConversations();
  loadGateways();
  checkUpdate();
  setInterval(checkUpdate, 60 * 60_000);
  applyRoute();
}

export async function loadConversations(more = false) {
  const c = state.convs;
  if (more) c.more = true; else if (!c.items.length) c.status = 'loading';
  emit();
  try {
    const page = await api.conversations(more ? c.items.length : 0, 30);
    const items = more ? [...c.items, ...page.data.filter((x) => !c.items.some((y) => y.id === x.id))] : page.data;
    // A non-"more" refresh keeps already-paged tail entries so the list
    // never shrinks under the reader.
    if (!more && c.items.length > items.length) {
      for (const it of c.items) if (!items.some((x) => x.id === it.id)) items.push(it);
    }
    Object.assign(c, { items, total: page.page.total, hasMore: items.length < page.page.total, status: 'ready', error: null });
  } catch (err) {
    if (more) toast(errorText(err), 'error');
    else if (!c.items.length) Object.assign(c, { status: 'error', error: errorText(err) });
    else toast('Could not refresh threads: ' + errorText(err), 'error');
  } finally {
    c.more = false;
    emit();
  }
}

let convTimer;
function refreshConversationsSoon() {
  clearTimeout(convTimer);
  convTimer = setTimeout(() => loadConversations(), 250);
}

export async function loadGateways() {
  const g = state.gateways;
  g.status = g.items.length ? 'ready' : 'loading';
  emit();
  try {
    const page = await api.gateways();
    Object.assign(g, { items: page.data, status: 'ready', error: null });
  } catch (err) {
    Object.assign(g, { status: 'error', error: errorText(err) });
  }
  emit();
}

async function checkUpdate() {
  try { state.update = await api.update(); emit(); } catch { /* the About page surfaces errors */ }
}

// ---------- room loading ----------

export async function openRoom(id) {
  const r = room(id);
  if (r.live) { loadInfo(r); return; }
  await Promise.all([loadInfo(r), loadHistory(r)]);
  if (r.info?.activeTurnId && !r.live) watch(r, r.info.activeTurnId, r.info.activeTurnKind, -1);
  fetchContext(r);
}

async function loadInfo(r) {
  try {
    r.info = await api.conversation(r.id);
    r.infoStatus = 'ready';
    r.infoError = null;
  } catch (err) {
    r.infoStatus = err.status === 404 ? 'notfound' : 'error';
    r.infoError = errorText(err);
  }
  emit();
}

export async function loadHistory(r) {
  r.hist.status = r.messages.length ? 'refreshing' : 'loading';
  emit();
  try {
    const page = await api.messages(r.id, -HISTORY_PAGE, HISTORY_PAGE);
    r.messages = page.messages;
    Object.assign(r.hist, { status: 'ready', error: null, start: page.effectiveStart, total: page.total, more: page.effectiveStart > 0 });
  } catch (err) {
    if (err.status === 404) { r.infoStatus = 'notfound'; r.hist.status = 'ready'; }
    else Object.assign(r.hist, { status: r.messages.length ? 'ready' : 'error', error: errorText(err) });
    if (r.messages.length) toast('Could not refresh history: ' + errorText(err), 'error');
  }
  emit();
}

export async function loadEarlier(r) {
  if (r.hist.loadingEarlier || r.hist.start <= 0) return;
  r.hist.loadingEarlier = true;
  r.hist.earlierError = null;
  emit();
  try {
    const start = Math.max(0, r.hist.start - HISTORY_PAGE);
    const page = await api.messages(r.id, start, r.hist.start - start);
    const known = new Set(r.messages.map((m) => m.id));
    r.messages = [...page.messages.filter((m) => !known.has(m.id)), ...r.messages];
    r.hist.start = page.effectiveStart;
    r.hist.more = page.effectiveStart > 0;
  } catch (err) {
    r.hist.earlierError = errorText(err);
  } finally {
    r.hist.loadingEarlier = false;
    emit();
  }
}

export async function fetchContext(r) {
  const sel = selection(r);
  if (!r.id && !(sel.route && sel.modelId)) { r.ctx = null; emit(); return; }
  try {
    r.ctx = await api.context({ conversationId: r.id, route: sel.route, modelId: sel.modelId });
    r.ctx.live = null;
  } catch { r.ctx = null; }
  emit();
}

// ---------- selection ----------

// selection resolves what the next command runs with: explicit composer
// choice, else the room's binding, else the last model the user picked.
export function selection(r) {
  const p = state.prefs.lastModel || {};
  const bound = r.info?.route ? { route: r.info.route, modelId: r.info.modelId } : null;
  const base = r.sel.route ? r.sel : bound || p;
  return {
    route: base.route || '',
    modelId: base.modelId || '',
    effort: r.sel.effort ?? (bound ? 'default' : p.effort) ?? 'default',
    provider: r.sel.provider ?? (bound ? '' : p.provider) ?? '',
    workspace: r.sel.workspace ?? r.info?.workspace ?? '',
  };
}

export function setSelection(r, patch) {
  r.sel = { ...r.sel, ...patch };
  if (patch.route !== undefined || patch.modelId !== undefined || patch.effort !== undefined || patch.provider !== undefined) {
    const s = selection(r);
    state.prefs = { ...state.prefs, lastModel: { route: s.route, modelId: s.modelId, effort: s.effort, provider: s.provider } };
    try { localStorage.setItem('nusallm.prefs', JSON.stringify(state.prefs)); } catch { /* ignore */ }
    fetchContext(r);
  }
  emit();
}

function turnPayload(r, extra) {
  const s = selection(r);
  const p = { ...extra };
  if (s.route && s.modelId) { p.route = s.route; p.modelId = s.modelId; }
  if (s.effort && s.effort !== 'default') p.effort = s.effort;
  if (s.provider) p.provider = s.provider;
  if (r.sel.workspace !== undefined && r.sel.workspace !== (r.info?.workspace || '')) p.workspace = r.sel.workspace;
  return p;
}

// ---------- commands ----------

function command(r, kind, cmd, text) {
  const requestId = newRequestId();
  if (!socket.send({ ...cmd, requestId })) {
    toast('Not connected — your message was kept. Try again when the connection is back.', 'error');
    return null;
  }
  requests.set(requestId, { key: r.key, kind, text, turnId: cmd.turnId });
  return requestId;
}

// submit is the composer's Enter: send now, or queue behind the running turn.
export function submit(r, text) {
  text = text.trim();
  if (!text) return false;
  if (isBusy(r) || r.queue.length) {
    r.queue.push({ id: newRequestId(), text });
    // An explicit send in an idle room releases a held queue, oldest first.
    if (!isBusy(r)) resumeQueue(r);
    emit();
    return true;
  }
  return sendNow(r, text);
}

function sendNow(r, text) {
  const s = selection(r);
  if (!r.id && !(s.route && s.modelId)) {
    toast('Choose a model before sending.', 'error');
    return false;
  }
  const requestId = command(r, 'send', {
    type: 'turn.send',
    conversationId: r.id,
    payload: turnPayload(r, { content: text }),
  }, text);
  if (!requestId) return false;
  r.pendingOpen = { requestId, text, kind: 'send' };
  r.failed = null;
  emit();
  return true;
}

export function steer(r, text) {
  text = text.trim();
  if (!text || !r.live || r.live.kind !== 'send') return false;
  const inputId = command(r, 'steer', { type: 'turn.steer', conversationId: r.id, turnId: r.live.turnId, payload: { content: text } }, text);
  if (!inputId) return false;
  r.steers.push({ inputId, text, state: 'sending' });
  emit();
  return true;
}

export function recallSteer(r, inputId) {
  if (!r.live) return;
  command(r, 'recall', { type: 'turn.steer_recall', conversationId: r.id, turnId: r.live.turnId, payload: { inputId } });
}

export function cancel(r) {
  if (!r.live) return;
  command(r, 'cancel', { type: 'turn.cancel', conversationId: r.id, turnId: r.live.turnId });
  r.live.canceling = true;
  emit();
}

export function retry(r) {
  r.failed = null;
  r.held = false;
  const requestId = command(r, 'resume', { type: 'turn.resume', conversationId: r.id, payload: turnPayload(r, {}) });
  if (requestId) r.pendingOpen = { requestId, kind: 'resume' };
  emit();
}

export function editMessage(r, messageId, text) {
  text = text.trim();
  if (!text) return false;
  const requestId = command(r, 'edit', { type: 'turn.edit', conversationId: r.id, payload: turnPayload(r, { messageId, content: text }) }, text);
  if (!requestId) return false;
  r.pendingOpen = { requestId, text, kind: 'edit', messageId };
  r.failed = null;
  emit();
  return true;
}

export function removeQueued(r, id) {
  r.queue = r.queue.filter((q) => q.id !== id);
  emit();
}
export function restoreQueued(r, id) {
  const q = r.queue.find((x) => x.id === id);
  if (!q) return;
  r.draft = r.draft ? r.draft + '\n' + q.text : q.text;
  removeQueued(r, id);
}

function watch(r, turnId, kind, afterSeq) {
  if (!r.live || r.live.turnId !== turnId) {
    r.live = newTurn({ turnId, conversationId: r.id, kind: kind || 'send' });
    r.cutHistory = afterSeq < 0;
  }
  command(r, 'watch', { type: 'turn.watch', conversationId: r.id, turnId, afterSeq });
  emit();
}

// After a (re)connect: re-attach every live turn from its last seq. A send
// that never got its turn.opened has unknown delivery — the text goes back
// to the draft and the user decides after checking history.
let connectedOnce = false;
function reattach() {
  if (!connectedOnce) { connectedOnce = true; return; }
  for (const r of Object.values(state.rooms)) {
    if (r.live && !r.live.terminal) {
      command(r, 'watch', { type: 'turn.watch', conversationId: r.id, turnId: r.live.turnId, afterSeq: r.live.lastSeq });
    }
    if (r.pendingOpen) {
      requests.delete(r.pendingOpen.requestId);
      if (r.pendingOpen.text) r.draft = r.draft ? r.pendingOpen.text + '\n' + r.draft : r.pendingOpen.text;
      r.pendingOpen = null;
      toast('Connection dropped before the server confirmed your message. Check the thread before resending.', 'error', 0);
    }
    for (const s of r.steers) if (s.state === 'sending') s.state = 'unknown';
  }
  if (state.route.view === 'chat') openRoom(state.route.id);
  loadConversations();
  emit();
}

// ---------- frames ----------

function roomForFrame(f) {
  const req = f.requestId && requests.get(f.requestId);
  if (req) return room(req.key);
  if (f.conversationId && state.rooms[f.conversationId]) return state.rooms[f.conversationId];
  return null;
}

function onFrame(f) {
  switch (f.type) {
    case 'turn.opened': return onOpened(f);
    case 'error': return onError(f);
    case 'conversation.activity': return onActivity(f);
    case 'turn.cancel_ack': requests.delete(f.requestId); return;
    case 'turn.resync_required': return onResync(f);
  }
  const r = roomForFrame(f);
  if (!r) return;
  const inputId = f.payload?.inputId;
  if (f.type === 'turn.steer_ack') {
    const s = r.steers.find((x) => x.inputId === inputId);
    if (s) s.state = 'pending';
  } else if (f.type === 'turn.steer_recalled') {
    const s = r.steers.find((x) => x.inputId === inputId);
    if (s) {
      r.steers = r.steers.filter((x) => x !== s);
      r.draft = r.draft ? s.text + '\n' + r.draft : s.text;
    }
  }
  if (!r.live || r.live.turnId !== f.turnId) { emit(); return; }
  if (!applyFrame(r.live, f)) return;
  if (f.type === 'turn.steer' && f.payload?.final) r.steers = r.steers.filter((x) => x.inputId !== inputId);
  if (f.type === 'turn.step_finished' && f.payload?.usage && r.ctx) r.ctx.live = f.payload.usage.totalInputTokens;
  if (f.type === 'turn.completed' || f.type === 'turn.failed') onTerminal(r, f);
  emit();
}

function onOpened(f) {
  const req = requests.get(f.requestId);
  if (!req) return;
  let r = room(req.key);
  if (!r.id) {
    // A fresh view just got its server-minted id: move it under that key.
    const moved = { ...r, key: f.conversationId, id: f.conversationId, info: null, infoStatus: 'loading' };
    state.rooms[f.conversationId] = moved;
    state.rooms[FRESH] = blankRoom(FRESH, null);
    req.key = f.conversationId;
    r = moved;
    if (state.route.view === 'home') navigate('#/c/' + encodeURIComponent(f.conversationId));
    loadInfo(r);
  }
  r.live = newTurn({ turnId: f.turnId, requestId: f.requestId, conversationId: f.conversationId, kind: req.kind === 'send' ? 'send' : 'edit' });
  r.live.lastSeq = 0;
  r.live.userText = req.kind === 'send' || req.kind === 'edit' ? req.text : null;
  r.live.editOf = req.kind === 'edit' ? r.pendingOpen?.messageId : null;
  r.cutHistory = false;
  r.pendingOpen = null;
  refreshConversationsSoon();
  emit();
}

function onError(f) {
  const req = f.requestId && requests.get(f.requestId);
  requests.delete(f.requestId);
  if (!req) { toast(f.message || 'The server rejected a command.', 'error'); return; }
  const r = room(req.key);
  if (req.kind === 'send' || req.kind === 'edit' || req.kind === 'resume') {
    if (r.pendingOpen?.requestId === f.requestId) r.pendingOpen = null;
    if (req.kind === 'send' && req.text) r.draft = r.draft ? req.text + '\n' + r.draft : req.text;
  } else if (req.kind === 'steer') {
    const s = r.steers.find((x) => x.inputId === f.requestId);
    if (s) { r.steers = r.steers.filter((x) => x !== s); r.draft = r.draft ? s.text + '\n' + r.draft : s.text; }
  } else if (req.kind === 'watch') {
    if (r.live && !r.live.terminal) { r.live = null; loadHistory(r); }
  }
  toast(f.message || 'The server rejected the command.', 'error');
  emit();
}

function onResync(f) {
  const req = requests.get(f.requestId);
  requests.delete(f.requestId);
  const r = req ? room(req.key) : state.rooms[f.conversationId];
  if (!r) return;
  const p = f.payload || {};
  if (p.turn === 'active') { if (r.live) r.cutHistory = false; emit(); return; }
  if (p.terminal && r.live?.turnId === f.turnId) {
    applyFrame(r.live, { ...p.terminal, seq: undefined });
    onTerminal(r, p.terminal);
  } else {
    r.live = null;
    r.cutHistory = false;
    for (const s of r.steers) s.state = 'unknown';
    loadInfo(r);
    loadHistory(r);
  }
  emit();
}

function onActivity(f) {
  refreshConversationsSoon();
  const r = state.rooms[f.conversationId];
  if (!r) return;
  const p = f.payload || {};
  const ours = p.requestId && requests.has(p.requestId);
  if (ours || r.live?.turnId === f.turnId) return;
  if (p.state === 'opened' && !r.live && !r.pendingOpen && state.route.id === r.id) watch(r, f.turnId, p.kind, -1);
  if (p.state !== 'opened' && !r.live) { loadInfo(r); loadHistory(r); }
}

async function onTerminal(r, f) {
  requests.delete(f.requestId);
  const p = f.payload || {};
  const live = r.live;
  if (f.type === 'turn.failed') {
    if (live?.kind === 'send' && live.userText && p.userMessagePersisted === false) {
      r.draft = r.draft ? live.userText + '\n' + r.draft : live.userText;
    }
    if (p.status !== 408) r.failed = { status: p.status, code: p.code, message: p.message || 'The turn failed.' };
    else if (!live?.canceling) toast('Turn was interrupted.', 'info');
  }
  // Steering inputs the turn never committed come back to the user.
  const back = [];
  for (const s of p.steering || []) {
    const mine = r.steers.find((x) => x.inputId === s.inputId);
    if (!mine) continue;
    if (s.state === 'pending' || s.state === 'claimed') back.push({ id: s.inputId, text: mine.text });
    else if (s.state === 'recalled') r.draft = r.draft ? mine.text + '\n' + r.draft : mine.text;
    r.steers = r.steers.filter((x) => x !== mine);
  }
  r.steers = r.steers.filter((x) => x.state === 'unknown');
  if (back.length) r.queue.unshift(...back);

  r.settling = true;
  emit();
  await Promise.all([loadInfo(r), loadHistory(r)]);
  r.live = null;
  r.cutHistory = false;
  r.settling = false;
  fetchContext(r);
  refreshConversationsSoon();
  emit();
  // Drain the queue only after a clean finish; a failure or stop holds it.
  r.held = f.type === 'turn.failed' && r.queue.length > 0;
  if (!r.held && r.queue.length && r.hist.status === 'ready') {
    const next = r.queue.shift();
    if (!sendNow(r, next.text)) r.queue.unshift(next);
  }
}

export function resumeQueue(r) {
  if (isBusy(r) || !r.queue.length) return;
  r.failed = null;
  r.held = false;
  const next = r.queue.shift();
  if (!sendNow(r, next.text)) r.queue.unshift(next);
}

// ---------- room management ----------

export async function rename(r, title) {
  try {
    r.info = await api.rename(r.id, title);
    refreshConversationsSoon();
  } catch (err) { toast('Rename failed: ' + errorText(err), 'error'); }
  emit();
}

export async function removeRoom(r) {
  try {
    await api.remove(r.id);
    delete state.rooms[r.id];
    state.convs.items = state.convs.items.filter((c) => c.id !== r.id);
    state.convs.total = Math.max(0, state.convs.total - 1);
    if (state.route.id === r.id) navigate('#/');
    toast('Thread deleted.');
  } catch (err) { toast('Delete failed: ' + errorText(err), 'error'); }
  emit();
}

export async function compact(r) {
  r.compacting = true;
  emit();
  try {
    r.info = await api.compact(r.id);
    await loadHistory(r);
    fetchContext(r);
    toast('History compacted.');
  } catch (err) { toast('Compaction failed: ' + errorText(err), 'error'); }
  r.compacting = false;
  emit();
}
