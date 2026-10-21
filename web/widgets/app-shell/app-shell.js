import { Widget, el, emit, sheetsReady } from '../../lib/dom.js';
import { APIClient } from '../../lib/api-client.js';
import { TurnSocket } from '../../lib/socket-client.js';
import {
  createTurnProjection, applyTurnFrame, settleTurnProjection,
} from '../../lib/turn-projection.js';
import {
  createQueue, enqueue, steer, requeue, bindRoom, takeQueued,
  hasQueuedForRoom, hasHeldForRoom, findEntry, removeEntry,
  markInFlightUnknown, pauseQueue, unpauseQueue,
  steerAcked, steerProjected, steerRecalled, reconcileSnapshot,
  markTurnUnknown,
  QUEUED, SUBMITTING, STEERING, RECALLING, UNKNOWN,
} from '../../lib/pending-queue.js';

// Orchestration only. The shell owns the API/socket instances, the
// selection + command state, the URL hash, and the wiring between
// widgets' events and their properties — it renders no domain markup
// of its own.
//
// Turn lifecycle lives in ONE object, `this.command`:
//   {kind:'send'|'edit', requestId, turnId:null|string,
//    conversationId:null|string, content, ephemeral:false,
//    stream: createTurnProjection(requestId) — ordered segments
// Navigation changes only the *view* — the command survives switching
// rooms and its stream re-projects only into its own room.
class AppShell extends Widget {
  static stylesheet = new URL('./app-shell.css', import.meta.url);

  constructor() {
    super();
    this.api = new APIClient();
    this.socket = new TurnSocket();

    this.allConversations = [];
    this.conversationsPage = { offset: 0, limit: 100, total: 0, hasMore: false };
    // _listSeq counts list COMMITS — a response captured before one is
    // stale by construction and drops on landing. _refreshSeq orders
    // overlapping refreshes among themselves (newest start wins), so a
    // refresh merely being on the wire never cancels a list mutation
    // that commits honestly mid-flight.
    this._listSeq = 0;
    this._refreshSeq = 0;
    this._moreBusy = false;     // a load-more list page is on the wire
    this.gateways = [];
    this.settings = {};

    this.activeId = null;       // selected conversation id (the VIEW)
    this.conversation = null;   // its detail projection
    this.messages = [];         // its latest message window
    this.history = { start: 0, total: 0 }; // the window's server-side span
    this._historySeq = 0;       // bumped by every write through _assignHistory
    this._earlierLoading = false; // a load-earlier page is in flight
    this.selection = null;      // {route, modelId, effort, provider, workspace} next-send binding
    this.command = null;        // the one in-flight command (see above)
    this.settledStream = null;  // keep the final live view through history refresh
    this.queue = createQueue(); // room-bound pending submissions + steer receipts
    // Definitive turn failures, keyed by room — rendered as a card on
    // the thread where the failed input sits, never as a queue row:
    // the queue models submissions, not turn outcomes. Two sources:
    // terminal-driven (turn.failed) and history-derived (a persisted
    // dead tail — see _markInterrupted). A dismissed derived card would
    // re-derive on the next refresh, so dismissal tombstones the tail
    // it judged; a new tail record invalidates the tombstone.
    this.failedTurns = new Map();
    this._dismissedTails = new Map();
    this._settledSeq = 0;       // identities settled streams across refreshes
    // The fresh (un-minted) view's stable identity — queue entries and
    // commands bind to it, not to a bare null every new view shares.
    this.freshToken = 'fresh:0';
    this._freshSeq = 0;
    // Per-room dispatch gate: a turn terminal requires an authoritative
    // refresh before the next send leaves for that room; epoch rejects
    // stale refresh responses.
    this._gates = new Map();
    this.compactPending = new Set(); // room ids with a compact request in flight
    this.generation = 0;        // stale-response token
    this.sidebarOpen = matchMedia('(min-width: 761px)').matches;
    this.settingsOpen = false;

    this._build();
    this._listen();
    this.socket.connect();
    this._boot();
    // The index.html boot overlay covers the unstyled gap before modules
    // and sheets arrive; once the shell's initial styles apply, tell it
    // the app is dressed so it can lift the cover.
    sheetsReady().then(() => {
      const go = () => emit(this, 'app:ready');
      if (typeof requestAnimationFrame === 'function') requestAnimationFrame(go);
      else go();
    });
    addEventListener('hashchange', () => {
      if (location.hash === '#settings') {
        this._showSettings();
        return;
      }
      this._showChat();
      // Only our own hash shape is meaningful — unrelated fragments
      // (e.g. a stray '#x') must not bounce the view to onboarding.
      const h = location.hash;
      if (h && !h.startsWith('#conversation=')) return;
      if (!this._selectFromHash() && this.activeId) this.newConversation();
    });
  }

  _build() {
    this.sidebar = el('nusa-nav-sidebar');
    this.header = el('nusa-conversation-header');
    this.thread = el('nusa-message-thread');
    this.composer = el('nusa-chat-composer');
    this.settingsPage = el('nusa-settings-page');
    this.toasts = el('nusa-toast-stack');
    this.scrim = el('div', { class: 'scrim', hidden: true });
    this.main = el('main', { class: 'main' }, [this.header, this.thread, this.composer]);
    this.workspace = el('div', { class: 'workspace' }, [this.main, this.settingsPage]);
    this.settingsPage.hidden = true;
    // Sync before the drawer joins the DOM, so a phone never paints it
    // open and then slides it shut on load.
    this._syncSidebar();
    // The scrim sits beside .grid, never inside it — the grid
    // children's entrance transform would become its fixed
    // containing block.
    this.root.append(
      el('div', { class: 'grid' }, [this.sidebar, this.workspace]),
      this.scrim,
      this.toasts,
    );
  }

  _listen() {
    const on = (target, type, fn) => target.addEventListener(type, (e) => fn(e.detail || {}, e));

    on(this.sidebar, 'conversation-select', ({ id }) => this.select(id));
    on(this.sidebar, 'conversation-new', () => this.newConversation());
    // The widget narrows rows by its own query — the shell re-projects
    // the FULL loaded set so the coverage hint keeps counting loaded
    // threads rather than the match count.
    on(this.sidebar, 'conversation-search', () => this._pushConversations());
    on(this.sidebar, 'conversation-more', () => this._loadMoreConversations());
    on(this.sidebar, 'sidebar-close', () => { this.sidebarOpen = false; this._syncSidebar(); });
    on(this.scrim, 'click', () => { this.sidebarOpen = false; this._syncSidebar(); });

    on(this.header, 'sidebar-toggle', () => { this.sidebarOpen = !this.sidebarOpen; this._syncSidebar(); });
    on(this.header, 'settings-open', () => this.openSettings());
    // The picker's manage-models exit opens the same settings page,
    // pre-pointed at the gateway it was browsing.
    on(this.header, 'manage-models', ({ gatewayId }) => {
      if (gatewayId) this.settingsPage.manageModelsFor = gatewayId;
      this.openSettings();
    });
    on(this.header, 'model-change', (d) => this._setSelection(d));
    on(this.header, 'workspace-change', (d) => this._setWorkspace(d.workspace));
    on(this.header, 'conversation-rename', ({ title }) => this._rename(title));
    on(this.header, 'conversation-compact', () => this._compact());
    on(this.header, 'conversation-delete', () => this._delete());

    // preventDefault tells the thread the dispatch was refused — it
    // keeps the editor open with the typed content for retry.
    on(this.thread, 'message-edit', ({ messageId, content }, e) => {
      if (this._edit(messageId, content) === false) e.preventDefault();
    });
    on(this.thread, 'history-earlier', () => this._loadEarlier());
    on(this.thread, 'turn-retry', () => this._turnRetry());
    on(this.thread, 'turn-failed-dismiss', () => this._turnFailedDismiss());

    on(this.composer, 'turn-send', ({ content }) => this._send(content));
    on(this.composer, 'turn-steer', ({ content }) => this._steer(content));
    on(this.composer, 'queue-restore', ({ id }) => this._queueRestore(id));
    on(this.composer, 'queue-copy', ({ id }) => this._queueCopy(id));
    on(this.composer, 'queue-dismiss', ({ id }) => this._queueDismiss(id));
    on(this.composer, 'queue-remove', ({ id }) => this._queueRemove(id));
    on(this.composer, 'queue-open', ({ id }) => this._queueOpen(id));
    on(this.composer, 'queue-check', ({ id }) => this._queueCheck(id));
    on(this.composer, 'queue-resume', () => { this._queueResume(); });
    on(this.composer, 'turn-cancel', () => {
      // The command's own turn is the target — never whatever room the
      // user happens to be viewing, and never from a foreign view:
      // Stop is armed only inside the owning room (canStop), and this
      // guard is the second half of that contract.
      if (!this._commandOwnsView(this.command)) return;
      if (this.command.turnId) {
        this.socket.cancelTurn({
          conversationId: this.command.conversationId,
          turnId: this.command.turnId,
        });
      } else {
        this.command.cancelRequested = true;
      }
    });

    on(this.settingsPage, 'settings-close', () => this.closeSettings());
    on(this.settingsPage, 'gateway-save', ({ id, body }) => this._saveGateway(id, body));
    on(this.settingsPage, 'gateway-delete', ({ id }) => this._deleteGateway(id));
    on(this.settingsPage, 'gateway-import', ({ id }) => this._importGatewayModels(id));
    on(this.settingsPage, 'setting-change', ({ key, value }) => this._patchSetting(key, value));

    this.socket.addEventListener('connection', (e) => this._onConnection(e.detail.state));
    this.socket.addEventListener('frame', (e) => this._onFrame(e.detail.frame));

    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && this.sidebarOpen &&
          matchMedia('(max-width: 760px)').matches) {
        this.sidebarOpen = false;
        this._syncSidebar();
      }
    });
  }

  async _boot() {
    const [convs, gws, cfg] = await Promise.allSettled([
      this.api.listConversations({ limit: 100 }),
      this.api.listGateways({ limit: 100 }),
      this.api.getSettings(),
    ]);
    if (convs.status === 'fulfilled') {
      this.allConversations = convs.value.data || [];
      this.conversationsPage = this._pageMeta(convs.value.page);
      this._pushConversations();
    } else this._toast('error', 'Conversations', this._errText(convs.reason));
    if (gws.status === 'fulfilled') {
      this.gateways = gws.value.data || [];
      this.header.gateways = this.gateways;
      this.settingsPage.gateways = this.gateways;
      this._defaultSelection();
    } else this._toast('error', 'Gateways', this._errText(gws.reason));
    if (cfg.status === 'fulfilled') {
      this.settings = cfg.value;
      this.settingsPage.settings = this.settings;
      this._syncCompact();
    } else this._toast('error', 'Settings', this._errText(cfg.reason));
    const settingsRoute = location.hash === '#settings';
    if (!this._selectFromHash() && this.allConversations.length) {
      this.select(this.allConversations[0].id);
    }
    if (settingsRoute) this.openSettings();
  }

  // --- view selection (never touches this.command) ---------------------

  // Returns true when a (sane) hash requested a conversation. A
  // malformed encoding clears the hash rather than throwing.
  _selectFromHash() {
    const m = location.hash.match(/^#conversation=(.+)$/);
    if (!m) return false;
    let id;
    try {
      id = decodeURIComponent(m[1]);
    } catch {
      this._setHash('');
      return false;
    }
    if (id !== this.activeId) this.select(id);
    return true;
  }

  _setHash(id) {
    const hash = id ? `#conversation=${encodeURIComponent(id)}` : '';
    history.replaceState(null, '', location.pathname + hash);
  }

  _showSettings() {
    this.settingsOpen = true;
    this.main.hidden = true;
    this.settingsPage.hidden = false;
  }

  _showChat() {
    this.settingsOpen = false;
    this.main.hidden = false;
    this.settingsPage.hidden = true;
  }

  openSettings() {
    this._showSettings();
    if (location.hash !== '#settings') location.hash = 'settings';
  }

  closeSettings() {
    this._showChat();
    this._setHash(this.activeId);
  }

  _defaultSelection() {
    if (this.selection && this._usable(this.selection)) return;
    this.selection = null;
    for (const g of this.gateways) {
      if (!g.broken && (g.models || []).length) {
        this.selection = { route: g.id, modelId: g.models[0].id };
        break;
      }
    }
    this.header.selection = this.selection;
    this._syncComposer();
  }

  _usable(sel) {
    return !!(sel && this.gateways.some((g) =>
      g.id === sel.route && !g.broken &&
      (g.models || []).some((m) => m.id === sel.modelId)));
  }

  // After provider CRUD the next-send selection must point at a real
  // usable model — a deleted gateway must not leave Send armed on a new
  // room. A loaded conversation's own binding stays displayable.
  _reconcileSelection() {
    if (!this.activeId || !(this.conversation && this.conversation.modelId)) {
      if (!this._usable(this.selection)) {
        this.selection = null;
        this._defaultSelection();
      } else {
        this.header.selection = this.selection;
        this._syncComposer();
      }
    } else {
      this.header.selection = this.selection;
      this._syncComposer();
    }
  }

  _setSelection(sel) {
    // model-change doesn't carry workspace — the separate workspace
    // picker's binding survives a model swap; a room load passes
    // conv.workspace explicitly.
    const workspace = sel.workspace !== undefined ? sel.workspace
      : (this.selection && this.selection.workspace) || '';
    this.selection = { ...sel, workspace };
    this.header.selection = this.selection;
    this._syncComposer();
    this._syncContext();
  }

  _setWorkspace(workspace) {
    this.selection = { ...(this.selection || {}), workspace: workspace || '' };
    this.header.selection = this.selection;
    this._syncComposer();
  }

  // The composer footer reports "~used/total Tokens" from the backend —
  // a fresh room still resolves the capacity half via route+modelId.
  async _syncContext() {
    const gen = this.generation;
    try {
      const stats = await this.api.contextStats({
        conversationId: this.activeId || '',
        route: this.selection ? this.selection.route : '',
        modelId: this.selection ? this.selection.modelId : '',
      });
      if (gen === this.generation) this.composer.context = stats;
    } catch {
      if (gen === this.generation) this.composer.context = null;
    }
  }

  async select(id) {
    this._showChat();
    const gen = ++this.generation;
    this.activeId = id;
    this.viewLoading = true; // blocks send until the room resolves
    // A selection load writes the room's history — it shares the
    // per-room epoch with terminal refreshes, so a late selection
    // response can never clobber a newer refresh's history.
    const g = this._gate(id);
    const myEpoch = ++g.epoch;
    g.pending++;
    this.sidebar.activeId = id;
    if (matchMedia('(max-width: 760px)').matches) {
      this.sidebarOpen = false;
      this._syncSidebar();
    }
    this.thread.loading = true;
    this.thread.hasConversation = true;
    // An earlier-page request from the room being left still lands —
    // generation drops its data, but its busy flag must not linger on
    // this room's notice control until then.
    this._earlierLoading = false;
    this.thread.earlierLoading = false;
    this._setHash(id);
    // Placeholder chrome — the previous room's title/binding must not
    // linger as if it described this id.
    this.conversation = { id };
    this.header.conversation = this.conversation;
    this._syncCompact();
    this.messages = [];
    this.thread.messages = this.messages;
    this._syncComposer();
    try {
      const [conv, page] = await Promise.all([
        this.api.getConversation(id),
        this.api.getMessages(id, { start: -100, limit: 100 }),
      ]);
      if (gen !== this.generation) return; // a newer selection won
      if (myEpoch !== g.epoch) return;     // a newer history load owns the room
      this.conversation = conv;
      this.header.conversation = conv;
      this._syncCompact();
      this._assignHistory(page);
      this.thread.messages = this.messages;
      this.thread.historyNotice = page.effectiveStart > 0;
      if (conv.modelId) this._setSelection({
        route: conv.route, modelId: conv.modelId,
        effort: '', provider: '', workspace: conv.workspace || '',
      });
      this.sidebar.activeId = id;
      // A successful load is authoritative for the room — it clears
      // the dispatch barrier the same way a refresh does.
      g.required = false;
      g.failed = false;
      this._attachLiveTurn(conv);
      this._markInterrupted(conv);
    } catch (err) {
      if (gen !== this.generation) return;
      // A superseded writer's failure belongs to the newer load that
      // owns the room — never toast or navigate on old news.
      if (myEpoch !== g.epoch) return;
      g.failed = true;
      // The room is gone or unreadable — land on onboarding rather
      // than a half-loaded view with an armed composer.
      this._toast('error', 'Conversation', this._errText(err));
      this.newConversation();
      return;
    } finally {
      // pending drops before the composer reads the gate. Syncing while
      // this load was still counted left an idle room in queue mode.
      const settled = --g.pending === 0;
      if (gen === this.generation) {
        this.viewLoading = false;
        this.thread.loading = false;
        // The view-finalization syncs must also run for an epoch-stale
        // response — the loading state froze the composer and only this
        // finally re-arms it once the room's newest writer has landed.
        this._syncStream();
        this._syncContext();
      }
      if (gen === this.generation || settled) this._syncComposer();
      // The last outstanding history writer re-evaluates the drain —
      // a newer refresh may have cleared the barrier while this one
      // was still in flight.
      if (settled) this._drainQueue();
    }
  }

  // Every explicit "New" mints a fresh-view token so its queue entries
  // and in-flight sends can never be confused with another un-minted
  // view's. Selecting a fresh entry's Open action returns to that same
  // token via _showFresh — the lines stay attached.
  newConversation() {
    this.freshToken = `fresh:${++this._freshSeq}`;
    this._showFresh(this.freshToken);
  }

  _showFresh(token) {
    this._showChat();
    this.generation++;
    this.activeId = null;
    this.freshToken = token;
    this.viewLoading = false;
    this.conversation = null;
    this.messages = [];
    this._setHash('');
    this.sidebar.activeId = null;
    this.header.conversation = null;
    this._syncCompact();
    this.thread.hasConversation = false;
    this.thread.messages = this.messages;
    this.history = { start: 0, total: 0 };
    this.thread.historyNotice = false;
    this._earlierLoading = false;
    this.thread.earlierLoading = false;
    this._defaultSelection();
    this._syncComposer();
    this._syncStream();
    this._syncContext();
    this.composer.clear();
    this._drainQueue(); // a pre-open queue drains once the view is free
  }

  // Every sidebar list write goes through here — the widget's coverage
  // hint counts whatever array it is handed, so rows and paging meta
  // are always pushed together, and the array is always the full
  // loaded set for the widget's own _query to narrow.
  _pushConversations() {
    this.sidebar.conversations = this.allConversations;
    // `total` before `hasMore`: the widget renders its coverage hint the
    // moment `hasMore` flips true and reads `total` as it does, so the
    // total must already be current. Keeping that order holds even if an
    // await is ever introduced between the setters — no intermediate
    // state can then show a stale total.
    this.sidebar.total = this.conversationsPage.total;
    this.sidebar.hasMore = this.conversationsPage.hasMore;
    this.sidebar.moreBusy = this._moreBusy;
  }

  // Wire meta may be absent on a degenerate response — fall back to
  // the loaded count so the coverage the sidebar states stays true.
  _pageMeta(page) {
    const p = page || {};
    return {
      offset: p.offset || 0,
      limit: p.limit || 100,
      total: typeof p.total === 'number' ? p.total : this.allConversations.length,
      hasMore: !!p.hasMore,
    };
  }

  // The scrim belongs to the mobile overlay only — on desktop the
  // rail is a grid track and there is nothing to dismiss through.
  _syncSidebar() {
    this.sidebar.open = this.sidebarOpen;
    this.scrim.hidden = !this.sidebarOpen ||
      matchMedia('(min-width: 761px)').matches;
  }

  _syncComposer() {
    // A fresh room needs an explicit model pick; an existing room can
    // ride its own binding — but never while its load is in flight,
    // or the previous room's binding would ride a send into the new one.
    const canSend = !this.viewLoading && (this.activeId
      ? true
      : this._usable(this.selection));
    this.composer.canSend = canSend;
    this.composer.selection = this.selection;
    const cmd = this.command;
    // A local command stays busy in every room; a watched turn only
    // busies the room it runs in.
    const gated = this._commandGates(this._viewRoomKey());
    this.composer.busy = gated;
    // Stop/Steer scope to the turn's own room — no hidden global
    // targets while a command runs elsewhere.
    this.composer.canStop = this._commandOwnsView(cmd);
    this.composer.canSteer = !!(cmd && cmd.kind === 'send' && cmd.turnId &&
      this._commandOwnsView(cmd) && this.socket.state === 'open');
    this.thread.commandsBusy = gated;
    this._syncQueue();
  }

  // The pending panel lists every queue entry with its owning room —
  // local text the shell holds, submitted steering awaiting its
  // receipt, and transmitted commands whose outcome is unknown. Foreign
  // rows navigate to their room rather than mutating this draft.
  _syncQueue() {
    const viewKey = this._viewRoomKey();
    this.composer.pendingItems = this.queue.items.map((e) => ({
      id: e.id, text: e.text, state: e.state, roomLabel: this._roomLabel(e.room),
      foreign: e.room !== viewKey,
      online: this.socket.state === 'open',
    }));
    // "Paused" means this view's own lane holds entries — holds in
    // other rooms are their rooms' business.
    this.composer.queuePaused = hasHeldForRoom(this.queue, viewKey);
    const g = this._gate(viewKey);
    this.composer.queueNotice = this.queue.notice || '';
    // Resume is offered whenever this view holds held-back lines — its
    // very job is retrying a required/failed refresh, so the barrier
    // itself must not disable it. Only busy, offline, loading, or a
    // refresh already in flight make it inert.
    const resumable = hasHeldForRoom(this.queue, viewKey);
    this.composer.resumeVisible = resumable;
    this.composer.canResume = resumable && !this._commandGates(viewKey) &&
      !this.viewLoading && this.socket.state === 'open' && !g.pending;
    // Enter captures locally whenever dispatch can't happen now:
    // a command gating this view's room, a backlog, an offline socket,
    // or a refresh barrier for this view. A bare pause is no reason —
    // fresh lines are not held.
    this.composer.queueMode = this._commandGates(viewKey) ||
      this.socket.state !== 'open' ||
      hasQueuedForRoom(this.queue, viewKey) ||
      g.required || g.pending > 0 || g.failed;
    this._syncFailed();
  }

  // The failed-turn card lives on the thread under the input it failed
  // on. Retry is armed only while that input is still the last real
  // user line — later workspace pins and the failed turn's own debris
  // are regenerated by the retried turn — and while the room could
  // actually dispatch the edit it becomes.
  _syncFailed() {
    const viewKey = this._viewRoomKey();
    const f = this.failedTurns.get(viewKey);
    this.thread.failedTurn = f ? {
      note: f.note,
      title: f.title,
      retryable: this._dispatchable(viewKey) && this._failedTargetId(f) != null,
    } : null;
  }

  _roomLabel(roomId) {
    if (!roomId || String(roomId).startsWith('fresh:')) return 'New conversation';
    const conv = this.allConversations.find((c) => c.id === roomId);
    const title = (conv && conv.title) || (this.conversation?.id === roomId && this.conversation?.title);
    return title || (roomId.length > 14 ? roomId.slice(0, 14) + '…' : roomId);
  }

  // The current view's stable queue key — the conversation id, or this
  // fresh view's own token so two sequential "new conversation" views
  // never share pending lines.
  _viewRoomKey() { return this.activeId || this.freshToken; }

  _gate(roomKey) {
    let g = this._gates.get(roomKey);
    if (!g) {
      g = { required: false, pending: 0, failed: false, epoch: 0, inflight: null };
      this._gates.set(roomKey, g);
    }
    return g;
  }

  // One command-vs-view ownership check: the minted conversation id
  // once the turn opened, else the room identity captured at submit.
  // Two distinct fresh views compare different tokens — never null===null.
  _commandOwnsView(cmd) {
    return !!cmd && this._commandRoomKey(cmd) === this._viewRoomKey();
  }

  // The room a command occupies: the minted conversation id once the
  // turn opened, else the room identity captured at submit/attach —
  // a fresh un-minted send binds to its own fresh token.
  _commandRoomKey(cmd) {
    return cmd ? cmd.conversationId || cmd.queueRoom : null;
  }

  // Whether the in-flight command gates roomKey for dispatch and busy
  // affordances. A command this tab drove is the single global
  // in-flight slot — it gates every room. A watched (foreign-tab) turn
  // gates only the room it runs in: another room still dispatches,
  // resumes, or edits, and doing so retires the watch's slot.
  _commandGates(roomKey) {
    const cmd = this.command;
    return !!cmd && (!cmd.watched || this._commandRoomKey(cmd) === roomKey);
  }

  // Every history writer assigns through here so an in-flight send's
  // optimistic user line survives a response landing before the server
  // committed it — it is re-appended only while the owning command is
  // alive and the new page doesn't already hold the committed line.
  _assignHistory(page) {
    let pageMessages = page.messages || [];
    const edit = this.command;
    // An in-flight edit's optimistic cut survives a refresh that still
    // shows the pre-commit tail. A page that already dropped that tail
    // — or grew messages the cut did not remove — is the newer history
    // and is left alone.
    if (edit && edit.kind === 'edit' && edit.editMessageId && this._commandOwnsView(edit)) {
      const idx = pageMessages.findIndex((m) => m.id === edit.editMessageId);
      if (idx >= 0) {
        const removed = new Set(edit.removedIds || []);
        const after = pageMessages.slice(idx + 1);
        const hasNew = after.some((m) => !removed.has(m.id));
        const hasOldTail = after.some((m) => removed.has(m.id));
        const contentStale = pageMessages[idx].content !== edit.content;
        if (!hasNew && (hasOldTail || contentStale)) {
          pageMessages = pageMessages.slice(0, idx).concat([
            { ...pageMessages[idx], content: edit.content },
          ]);
        }
      }
    }
    const optimistic = this.messages.filter((m) =>
      typeof m.id === 'string' && m.id.startsWith('pending-'));
    this.messages = pageMessages;
    for (const m of optimistic) {
      const owned = this.command &&
        this.command.turnId === m.id.slice('pending-'.length);
      // Committed means a NEW user id carrying the same content — an
      // identical line that predates this dispatch doesn't count.
      const committed = owned && this.messages.some((x) =>
        x.role === 'user' && x.content === m.content &&
        !(this.command.historyIds && this.command.historyIds.has(x.id)));
      if (owned && !committed) this.messages.push(m);
    }
    // The window the view now owns — every writer through here lands
    // the newest page. _historySeq lets an in-flight earlier-page
    // request tell it is answering a window that no longer exists.
    this.history = {
      start: page.effectiveStart ?? 0,
      total: typeof page.total === 'number' ? page.total : (this.history?.total || 0),
    };
    this._historySeq++;
    // A fresh window supersedes any in-flight "Load earlier" page — that
    // request no longer owns the view, so its guarded release will skip.
    // Release the busy flags here instead, or the control would stick.
    this._earlierLoading = false;
    this.thread.earlierLoading = false;
  }

  // "Load earlier" grows the window one page upward from its current
  // start, prepended without duplicating ids — existing records win an
  // id collision since they are the fresher copy. The response applies
  // only while the window it was computed against is still the view's:
  // a room reload or an authoritative refresh landing mid-flight bumps
  // _historySeq and resets to the newest page, so the stale page drops
  // instead of resurrecting records a rewrite may have cut. A page that
  // no longer reaches the window start is dropped for the same reason —
  // a gap above the list is worse than a lost click.
  async _loadEarlier() {
    const id = this.activeId;
    const history = this.history;
    if (!id || this._earlierLoading || this.viewLoading ||
        !history || history.start <= 0) return;
    const start = Math.max(0, history.start - 100);
    const limit = history.start - start; // 1..100 — inside the contract cap
    this._earlierLoading = true;
    this.thread.earlierLoading = true;
    const gen = this.generation;
    const seq = this._historySeq;
    // The request owns the busy flag only while it still describes the
    // view it was issued for. A room switch (new generation/id) or an
    // authoritative window write (new _historySeq) hands ownership to
    // another request, whose flag this one must not release.
    const owns = () => gen === this.generation && id === this.activeId &&
      seq === this._historySeq;
    try {
      const page = await this.api.getMessages(id, { start, limit });
      if (!owns()) return;
      const end = page.nextStart ??
        (page.effectiveStart ?? start) + (page.messages || []).length;
      if (end < this.history.start) return;
      const seen = new Set(this.messages.map((m) => m.id));
      this.messages = [
        ...(page.messages || []).filter((m) => !seen.has(m.id)),
        ...this.messages,
      ];
      this.history = {
        start: page.effectiveStart ?? start,
        total: typeof page.total === 'number' ? page.total : history.total,
      };
      this.thread.messages = this.messages;
      this.thread.historyNotice = this.history.start > 0;
    } catch (err) {
      this._toast('error', 'History', this._errText(err));
    } finally {
      if (owns()) {
        this._earlierLoading = false;
        this.thread.earlierLoading = false;
      }
    }
  }

  // One dispatch rule for every path that could start a turn: the
  // socket must be open, no command may gate this room, the view
  // settled, and the room's authoritative refresh not outstanding or
  // failed. A paused queue is NOT a dispatch brake — the hold lives on
  // the entries it captured, so fresh input still flows.
  _dispatchable(roomKey) {
    if (this._commandGates(roomKey) || this.viewLoading) return false;
    if (this.socket.state !== 'open') return false;
    const g = this._gate(roomKey);
    return !(g.required || g.pending > 0 || g.failed);
  }

  _captureOpts() {
    const sel = this.selection || {};
    return {
      route: sel.route || '', modelId: sel.modelId || '',
      effort: sel.effort || '', provider: sel.provider || '',
      workspace: sel.workspace || '',
    };
  }

  // Restore text the queue owned back into the draft — appended after a
  // newline when a draft already exists, never overwriting it.
  _restoreDraft(text) {
    const cur = this.composer.value;
    this.composer.value = cur.trim() ? `${cur}\n${text}` : text;
  }

  _syncCompact() {
    const conv = this.conversation;
    const running = !!(conv?.id && this.compactPending.has(conv.id));
    const done = !!(conv?.sealedSegments && conv.canCompact === false);
    const state = running ? 'running' : done ? 'done' : null;
    this.header.compactState = state;
    this.compactAvailable = !!(conv?.canCompact && !state);
    this.header.compactAvailable = this.compactAvailable;
    this.thread.compactState = state;
  }

  // The live stream renders only in the room that owns the command.
  _syncStream() {
    const cmd = this.command;
    const visible = this._commandOwnsView(cmd)
      ? cmd.stream : this.settledStream && this.settledStream.conversationId === this.activeId
        ? this.settledStream.stream : null;
    this.thread.stream = visible;
    this._syncFailed();
  }

  // --- commands ---------------------------------------------------------

  // Enter routes one submission through the single dispatch gate: a
  // dispatchable view sends it now; anything else — busy, paused,
  // backed-up, refresh barrier, offline — captures it locally for the
  // current view. The queue owns the text, so the draft clears at once.
  _send(content) {
    const text = (content || '').trim();
    if (!text) return;
    const roomKey = this._viewRoomKey();
    // A paused queue doesn't hold fresh input — only undispatchable
    // rooms or a waiting backlog capture the line locally.
    if (!this._dispatchable(roomKey) || hasQueuedForRoom(this.queue, roomKey)) {
      enqueue(this.queue, { text, room: roomKey, opts: this._captureOpts() });
      this.composer.clear();
      this._syncQueue();
      this._drainQueue();
      return;
    }
    this._sendCommand({ content: text, room: roomKey, opts: this._captureOpts() });
  }

  _sendCommand({ content, room, opts, fromQueue = null }) {
    const fresh = String(room).startsWith('fresh:');
    const requestId = this.socket.sendTurn({
      conversationId: fresh ? null : room,
      route: opts.route || '',
      modelId: opts.modelId || '',
      content,
      effort: opts.effort || '',
      provider: opts.provider || '',
      workspace: opts.workspace || '',
    });
    // Dispatchable means the slot is free or holds only a watched turn
    // in ANOTHER room — displacing that watch is safe: the socket keeps
    // cursor-tracking the foreign turn and its terminal reconciles
    // through the non-owning path.
    this.command = {
      kind: 'send', requestId, turnId: null,
      conversationId: fresh ? null : room, queueRoom: room,
      content, ephemeral: false,
      cancelRequested: false,
      // Only a send issued on the fresh view that is STILL the view may
      // claim the minted room — never force a user who navigated or
      // opened a newer fresh view back to it.
      claimFreshView: fresh,
      opts: { ...opts }, // captured at submit — never mutable current opts
      fromQueue, // a dispatched queue entry rejoins on an unpersisted failure
      // History ids at dispatch — a refresh landing mid-send tells a
      // NEW committed user line apart from an older identical one
      // (the same text sent twice must not hide this optimistic line).
      historyIds: new Set(this.messages.map((m) => m.id)),
      stream: createTurnProjection(requestId),
    };
    this._syncComposer();
    this._syncStream();
  }

  // _steer submits the draft into the exact opened send turn the view
  // owns. Ineligible views fall back to the queue/send path — the text
  // is never dropped by an ineligible button press.
  _steer(content) {
    const text = (content || '').trim();
    if (!text) return;
    const cmd = this.command;
    const eligible = cmd && cmd.kind === 'send' && cmd.turnId &&
      this._commandOwnsView(cmd) && this.socket.state === 'open';
    if (!eligible) {
      this._send(text);
      return;
    }
    const inputId = this.socket.steerTurn({
      conversationId: cmd.conversationId,
      turnId: cmd.turnId,
      content: text,
    });
    steer(this.queue, {
      text, room: this._viewRoomKey(), opts: this._captureOpts(),
      turnId: cmd.turnId, inputId,
    });
    this.composer.clear();
    this._syncQueue();
  }

  // The auto-drain: only the current view's queue dispatches, oldest
  // first — entries for other rooms wait for their own view, a held
  // head waits for an explicit Resume, and a refresh barrier holds
  // everything back until history is authoritative.
  _drainQueue() {
    if (!this._dispatchable(this._viewRoomKey())) return;
    const entry = takeQueued(this.queue, this._viewRoomKey());
    if (!entry) return;
    this._sendCommand({
      content: entry.text, room: entry.room, opts: entry.opts, fromQueue: entry,
    });
    this._syncQueue();
  }

  // Text the queue owns comes back through one owner-aware path: into
  // the draft when its room is the current view, otherwise requeued
  // paused into its own room — never silently into an unrelated draft.
  _restoreOwned(entry) {
    if (entry.room === this._viewRoomKey()) {
      this._restoreDraft(entry.text);
      return;
    }
    enqueue(this.queue, { text: entry.text, room: entry.room, opts: entry.opts });
    // The returned copy waits for the user's Resume — its own room's
    // lane holds, other rooms keep flowing.
    pauseQueue(this.queue, entry.room);
    this.queue.notice = `"${this._roomLabel(entry.room)}" — text returned to that room's queue`;
  }

  _queueRestore(id) {
    const e = findEntry(this.queue, id);
    if (!e || e.room !== this._viewRoomKey()) return;
    if (e.state === QUEUED) {
      removeEntry(this.queue, e);
      this._restoreDraft(e.text);
      this._syncQueue();
      return;
    }
    // Recall is the only honest withdrawal for a submitted input, and
    // only while the socket can carry it — an offline Restore must NOT
    // drop a receipt the server may still hold.
    if (e.state === STEERING || e.state === SUBMITTING) {
      if (e.turnId && e.inputId && this.socket.state === 'open') {
        e.preRecall = e.state;
        e.recallId = this.socket.steerRecall({
          conversationId: String(e.room).startsWith('fresh:') ? null : e.room,
          turnId: e.turnId, inputId: e.inputId,
        });
        e.state = RECALLING;
        this._syncQueue();
      } else {
        this.queue.notice = 'Recall needs the socket — reconnect to withdraw this input';
        this._syncQueue();
      }
    }
  }

  // Copy is the explicit user decision on an unknown-delivery entry:
  // the text is appended to this view's draft and the receipt let go —
  // never an automatic retry.
  _queueCopy(id) {
    const e = findEntry(this.queue, id);
    if (!e || e.state !== UNKNOWN || e.room !== this._viewRoomKey()) return;
    removeEntry(this.queue, e);
    this._restoreDraft(e.text);
    this._syncQueue();
  }

  // Dismiss acknowledges an unknown-delivery entry without touching
  // the draft — the user checked history and chose to let it go. A
  // foreign queued line can also be dropped: its room may no longer
  // exist to Open into.
  _queueDismiss(id) {
    const e = findEntry(this.queue, id);
    if (!e) return;
    const droppable = e.state === UNKNOWN ||
      (e.state === QUEUED && e.room !== this._viewRoomKey());
    if (!droppable) return;
    removeEntry(this.queue, e);
    this._syncQueue();
  }

  // Retry re-issues turn.edit against the failed turn's own user
  // line — the turn regenerates under it, so a retry never appends a
  // duplicate message. The gate is strict: turn.edit drops everything
  // after its target, so Retry exists only while no NEWER user input
  // sits after that line — a retry must never silently rewind another
  // turn's messages. The workspace pins and assistant/tool debris the
  // failed turn left behind are fair game: the retried turn
  // regenerates them.
  _turnRetry() {
    const roomKey = this._viewRoomKey();
    const f = this.failedTurns.get(roomKey);
    if (!f) return;
    const messageId = this._failedTargetId(f);
    if (!messageId) {
      this._toast('warn', 'Retry',
        'Only a failed turn still at the last input can be retried.');
      return;
    }
    this._edit(messageId, f.text);
    this._syncQueue();
  }

  _turnFailedDismiss() {
    const key = this._viewRoomKey();
    const f = this.failedTurns.get(key);
    // A history-derived card re-derives on every authoritative landing
    // — tombstone the tail it judged so dismissal sticks until the
    // tail advances (a new turn supersedes it anyway).
    if (f && f.tailId) this._dismissedTails.set(key, f.tailId);
    this.failedTurns.delete(key);
    this._syncQueue();
  }

  // The message id a failed turn's retry would edit — resolvable only
  // while the failed line is the last real user input rendered. Any
  // later origin line is a turn the retry must not rewind; system
  // records (workspace pins, compaction checkpoints) and the failed
  // turn's own output don't count — the retried turn replaces them.
  // Resolution is lazy here and pinned by the post-terminal refresh
  // where it runs.
  _failedTargetId(f) {
    const id = f.retryMessageId || this._retryMessageId(f);
    if (!id) return null;
    const idx = this.messages.findIndex((m) => m.id === id);
    if (idx < 0) return null;
    for (const m of this.messages.slice(idx + 1)) {
      if (m.role === 'user' && m.userType !== 'system') return null;
    }
    return id;
  }

  // The persisted user line a failed send ran from — the earliest user
  // record carrying the submitted text that the dispatch-time history
  // did not already contain. A later identical line never qualifies,
  // and optimistic pending lines never qualify either.
  _retryMessageId(f) {
    for (const m of this.messages) {
      if (m.role !== 'user' || m.userType === 'system' ||
          m.content !== f.text) continue;
      if (typeof m.id === 'string' && m.id.startsWith('pending-')) continue;
      if (f.historyIds && f.historyIds.has(m.id)) continue;
      return m.id;
    }
    return null;
  }

  // Remove drops a locally queued line — never a server deletion.
  _queueRemove(id) {
    const e = findEntry(this.queue, id);
    if (!e || e.state !== QUEUED) return;
    removeEntry(this.queue, e);
    this._syncQueue();
  }

  // Open navigates to the room that owns a pending entry — for a fresh
  // token it selects that exact fresh view rather than minting a new
  // key, so its draft and queue stay attached.
  _queueOpen(id) {
    const e = findEntry(this.queue, id);
    if (!e) return;
    if (e.room === this._viewRoomKey()) return;
    if (String(e.room).startsWith('fresh:')) this._showFresh(e.room);
    else this.select(e.room);
  }

  _queueCheck(id) {
    const e = findEntry(this.queue, id);
    if (!e) return;
    this._queueOpen(id);
    if (!String(e.room).startsWith('fresh:')) this._refresh(e.room);
    else this._refreshList();
  }

  // Resume is explicit: a room whose refresh is required re-reads
  // history first — the drain waits for the barrier, never bypasses.
  // Offline it changes nothing: releasing the holds early would let a
  // reconnect drain fire the backlog on its own.
  async _queueResume() {
    if (this.socket.state !== 'open') {
      this.queue.notice = 'Reconnect before resuming the queue';
      this._syncQueue();
      return;
    }
    const key = this._viewRoomKey();
    // Resume releases this room's held lane — another room's holds
    // wait for their own Resume.
    unpauseQueue(this.queue, key);
    this.queue.notice = '';
    const g = this._gate(key);
    if (!String(key).startsWith('fresh:') && (g.required || g.failed)) {
      const ok = await this._refresh(key);
      if (ok !== 'ok') {
        pauseQueue(this.queue, key);
        this.queue.notice = 'History refresh failed — try Resume again';
        this._syncQueue();
        return;
      }
    }
    this._drainQueue();
    this._syncQueue();
  }

  _edit(messageId, content) {
    // An edit crosses the same history/offline barrier every dispatch
    // does — and never overwrites a command gating this room's
    // sequencing. Returning false keeps the editor open with the typed
    // content. A watched turn in another room does not veto: the
    // dispatch displaces it like a send does.
    if (this._commandGates(this._viewRoomKey())) {
      this._toast('warn', 'Edit', 'Finish or stop the running turn before editing.');
      return false;
    }
    if (!this._dispatchable(this._viewRoomKey())) {
      this._toast('warn', 'Edit', 'Wait for the room to finish refreshing before editing.');
      return false;
    }
    const prior = this.messages.slice();
    // The window those messages came from travels with them — a
    // "Load earlier" can commit while this edit is still pre-open, and
    // the snapshot must put the start back with the cut messages.
    const priorHistory = { ...this.history };
    const idx = prior.findIndex((m) => m.id === messageId);
    const removedIds = idx >= 0 ? prior.slice(idx + 1).map((m) => m.id) : [];
    if (idx >= 0) {
      this.messages = prior.slice(0, idx).concat([{ ...prior[idx], content }]);
      this.thread.messages = this.messages;
    }
    const requestId = this.socket.editTurn({
      conversationId: this.activeId, messageId, content,
    });
    this.command = {
      kind: 'edit', requestId, turnId: null,
      conversationId: this.activeId, queueRoom: this._viewRoomKey(),
      content, ephemeral: false,
      cancelRequested: false, claimFreshView: false,
      editMessageId: messageId,
      priorMessages: prior,
      priorHistory,
      removedIds,
      stream: createTurnProjection(requestId),
    };
    this._syncComposer();
    this._syncStream();
    return true;
  }

  // A pre-open edit failure puts the cut history back. Once the turn
  // has opened, the terminal HTTP read is authoritative instead. The
  // window comes back with the messages it describes: a "Load earlier"
  // that committed while this edit was pre-open moved history.start
  // under the snapshot, and leaving it there would make the next
  // "Load earlier" fetch only up to that newer start — the range
  // between the restored window and the stale start would vanish from
  // the list with no notice.
  _restoreUnopenedEdit(cmd) {
    if (!cmd || cmd.kind !== 'edit' || cmd.turnId || !cmd.priorMessages) return;
    if (this.activeId !== cmd.conversationId) return;
    this.messages = cmd.priorMessages;
    this.thread.messages = this.messages;
    if (cmd.priorHistory) this.history = { ...cmd.priorHistory };
    this.thread.historyNotice = this.history.start > 0;
    // _historySeq is NOT bumped here. An earlier page already in flight
    // for this same start is still the correct page for the restored
    // window, and the `end < history.start` guard drops any page that
    // no longer reaches it — bumping would throw that legitimate page
    // away instead of letting it land.
  }

  // A room loaded while a transport turn is still running attaches to
  // it. This tab's own in-flight command is left alone — it is already
  // subscribed, and replacing it would drop that turn's client state.
  // A watched command only yields to a DIFFERENT live turn: the slot
  // follows the room on screen, and re-attaching the same turn is a
  // no-op.
  _attachLiveTurn(conv) {
    const turnId = conv && conv.activeTurnId;
    if (!turnId || !conv.id) return;
    const cmd = this.command;
    if (cmd && (!cmd.watched || cmd.turnId === turnId)) return;
    const kind = conv.activeTurnKind === 'edit' ? 'edit' : 'send';
    const requestId = this.socket.watchTurn({
      conversationId: conv.id, turnId, afterSeq: -1,
    });
    this.command = {
      kind, requestId, turnId,
      conversationId: conv.id, queueRoom: conv.id,
      content: '', ephemeral: true,
      cancelRequested: false, claimFreshView: false,
      watched: true,
      stream: createTurnProjection(requestId),
    };
    this._syncComposer();
    this._syncStream();
  }

  // History-derived interrupted card. A persisted tail can prove a
  // turn died without any terminal verdict — the hub's turn journal is
  // process-memory, so a service restart (or an evicted journal) makes
  // the turn unknowable while history keeps its partial output. The
  // verdict table, read against the last real record in the window:
  //
  //   last real record              verdict
  //   ──────────────────────────────────────────
  //   real user input               dead — its send produced nothing
  //   assistant finish 'tool_calls' dead — runTurn only ends a turn on
  //                                 a non-tool_calls step; a live turn
  //                                 would always continue past this
  //   assistant any other finish    settled — a normal tail
  //
  // "Real" skips optimistic pending-* lines and harness records:
  // compaction checkpoints persist as role:'user' + userType:'system',
  // workspace pins persist as role:'system' carrying an injection call —
  // neither is a turn input nor a verdict-bearing tail, so the scan only
  // ever lands on real user input or a real assistant reply. Steer folds
  // persist as ordinary user inputs. Nothing here distinguishes a restart
  // from a user cancel or an upstream failure — the card stays
  // cause-neutral.
  // Skips that keep the card honest:
  //   - conv.activeTurnId / an own command on the room: a live turn's
  //     tail is legitimately open.
  //   - an existing failedTurns card: the terminal's own note wins.
  //   - a tombstoned tail: the user dismissed this exact dead tail.
  // Retry pins the LAST real user input by id — turn.edit regenerates
  // from it, dropping the dead turn's debris; a tail whose origin
  // scrolled above the window yields a dismiss-only card.
  _markInterrupted(conv) {
    if (!conv || !conv.id || conv.id !== this.activeId) return;
    if (conv.activeTurnId) return;
    const viewKey = this._viewRoomKey();
    const cmd = this.command;
    if (cmd && this._commandRoomKey(cmd) === viewKey) return;
    if (this.failedTurns.has(viewKey)) return;
    let lastReal = null, lastUser = null;
    for (let i = this.messages.length - 1; i >= 0 && (!lastReal || !lastUser); i--) {
      const m = this.messages[i];
      if (typeof m.id === 'string' && m.id.startsWith('pending-')) continue;
      const realInput = m.role === 'user' && m.userType !== 'system';
      if (realInput && !lastUser) lastUser = m;
      if (!lastReal && (realInput || m.role === 'assistant')) lastReal = m;
    }
    const dead = !!lastReal &&
      (lastReal.role === 'user' || lastReal.finishReason === 'tool_calls');
    if (!dead) {
      this._dismissedTails.delete(viewKey);
      return;
    }
    if (this._dismissedTails.get(viewKey) === lastReal.id) return;
    this.failedTurns.set(viewKey, {
      title: 'Turn interrupted',
      note: 'This turn did not finish — its reply may be incomplete.',
      text: lastUser ? lastUser.content : '',
      retryMessageId: lastUser ? lastUser.id : null,
      historyIds: null,
      tailId: lastReal.id,
    });
    // The card answers the parked UNKNOWN row's question — the send
    // DID persist and its turn died. Keeping both surfaces armed would
    // invite a duplicate of the same text.
    if (lastUser) {
      for (const e of [...this.queue.items]) {
        if (e.state === UNKNOWN && e.room === viewKey && e.text === lastUser.content) {
          removeEntry(this.queue, e);
        }
      }
    }
    // The settled projection of the dead turn is superseded — the
    // persisted tail plus the card is the honest view.
    if (this.settledStream && this.settledStream.conversationId === conv.id) {
      this.settledStream = null;
      this._syncStream();
    }
    this._syncFailed();
  }

  _onFrame(f) {
    const cmd = this.command;
    switch (f.type) {
      case 'turn.opened': {
        if (!cmd || f.requestId !== cmd.requestId) {
          // A foreign turn opened — in the room on screen its sender's
          // user line just persisted, so the estimate already moved.
          if (f.conversationId === this.activeId) this._syncContext();
          return;
        }
        if (cmd.turnId) return;                            // replayed opened — idempotent
        cmd.turnId = f.turnId;
        cmd.conversationId = f.conversationId;
        // The room's last failure is superseded once a new own turn
        // actually opens — not at dispatch, so a pre-open rejection
        // leaves the retry card standing.
        if (!cmd.watched) this.failedTurns.delete(this._commandRoomKey(cmd));
        // A send that minted its room pins THE ORIGIN fresh token's
        // queued lines to it — entries on a newer fresh token or a real
        // room are never rebound into this room.
        if (cmd.claimFreshView && f.conversationId) {
          bindRoom(this.queue, cmd.queueRoom, f.conversationId);
          // The in-flight queue entry was detached before dispatch —
          // re-home it too or a later requeue would land in a dead
          // fresh token.
          if (cmd.fromQueue) cmd.fromQueue.room = f.conversationId;
        }
        // Claim the minted room's view ONLY when the send's own fresh
        // token is still the current view — a user who clicked New or
        // navigated meanwhile is never dragged back.
        if (cmd.claimFreshView && f.conversationId &&
            this.activeId === null && this.freshToken === cmd.queueRoom) {
          this.activeId = f.conversationId;
          this.sidebar.activeId = f.conversationId;
          this._setHash(f.conversationId);
          this.header.conversation = this.conversation = { id: f.conversationId };
          this.thread.hasConversation = true;
        }
        if (cmd.kind === 'send' && !cmd.fromQueue) {
          // The draft drops only if THIS view's composer still holds
          // it — an identical draft on another view is untouched, and
          // a message typed while waiting belongs to the next turn.
          // Queue-dispatched sends never clear: their text left the
          // draft at enqueue, so matching text now is the user's own.
          if (this._commandOwnsView(cmd) &&
              this.composer.value.trim() === cmd.content.trim()) {
            this.composer.clear();
          }
        }
        if (cmd.kind === 'send') {
          if (!cmd.ephemeral) {
            cmd.ephemeral = true;
            if (this._commandOwnsView(cmd)) {
              this.messages = [...this.messages,
                { id: `pending-${f.turnId}`, role: 'user', userType: 'origin', content: cmd.content }];
              this.thread.messages = this.messages;
            }
          }
        }
        // A cancel requested before the server knew the turnId fires
        // now, against the exact turn.
        if (cmd.cancelRequested) {
          this.socket.cancelTurn({
            conversationId: cmd.conversationId, turnId: cmd.turnId,
          });
        }
        // The send's user line just persisted — the badge moves at
        // turn start, not only at settle. Gated on the viewed room:
        // an unclaimed mint or a queue dispatch into another room
        // must not fetch stats for a room that isn't on screen.
        if (f.conversationId === this.activeId) this._syncContext();
        this._syncComposer();
        this._syncStream();
        break;
      }
      // All stream interpretation lives in the projection — the shell
      // only correlates frames to the owning command.
      case 'turn.phase':
      case 'turn.reasoning':
      case 'turn.delta':
      case 'turn.tool_call':
      case 'turn.tool_input':
      case 'turn.tool_output':
      case 'turn.step_finished':
      case 'turn.steer':
        // Live context: the finished step's provider-reported input is
        // the room's real fill — fresher than the heuristic the REST
        // estimate replays (which still settles the badge at terminal).
        // Keyed on the viewed room, not the command: frames keep
        // arriving for a subscribed turn that outlived its command
        // slot. Zero/absent usage means the provider didn't report —
        // the heuristic stands; a null context has no total to fill
        // against, so merging into {} is a safe no-op.
        if (f.type === 'turn.step_finished' &&
            f.conversationId === this.activeId &&
            f.payload?.usage?.totalInputTokens > 0 &&
            this.composer.context?.total) {
          this.composer.context = {
            ...this.composer.context,
            estimate: f.payload.usage.totalInputTokens,
          };
        }
        if (cmd && cmd.turnId === f.turnId) {
          if (f.type === 'turn.steer' && f.payload?.inputId) {
            // A projected steering input — the terminal snapshot knows
            // it landed even if this frame was missed on a reconnect.
            if (steerProjected(this.queue, f.turnId, f.payload.inputId)) {
              this._syncQueue();
            }
          }
          applyTurnFrame(cmd.stream, f);
          this._syncStream();
        }
        break;
      case 'turn.steer_ack':
        // Acknowledged — never consumed. The receipt waits for its
        // steer frame or the terminal snapshot.
        if (steerAcked(this.queue, f.turnId, f.payload?.inputId)) this._syncQueue();
        break;
      case 'turn.steer_recalled': {
        const e = steerRecalled(this.queue, f.turnId, f.payload?.inputId);
        if (e) {
          // Owner-aware restore: the draft only if its room is the
          // current view, else a paused copy back into that room.
          this._restoreOwned(e);
          this.queue.notice = 'Steer input recalled';
          this._syncQueue();
        }
        break;
      }
      case 'turn.completed':
      case 'turn.failed':
        this._onTerminal(f);
        break;
      case 'turn.resync_required': {
        // The retained journal can't satisfy the watch. The payload's
        // `turn` verdict decides: "active" means the server still owns
        // its mailbox — reconcile but never requeue (the inputs may
        // still fold); a retained terminal verdict replays through the
        // normal terminal path; "unknown" means nothing is recoverable.
        const p = f.payload || {};
        if (p.turn === 'completed' || p.turn === 'failed') {
          const terminal = p.terminal || { steering: p.steering || [] };
          this._onTerminal({
            type: `turn.${p.turn}`, conversationId: f.conversationId,
            turnId: f.turnId, requestId: f.requestId, payload: terminal,
          });
          break;
        }
        if (p.turn === 'unknown') {
          // Expired or never-seen: the turn's outcome is unknowable.
          // Retire the command honestly — the stream settles as-is —
          // and surface the send's text as unknown rather than guessing
          // persistence from content. A turn this tab only watched has
          // no text to park; HTTP history is the record.
          markTurnUnknown(this.queue, f.turnId);
          const cmd = this.command;
          if (cmd && cmd.turnId === f.turnId && cmd.watched) {
            this.command = null;
            this._syncComposer();
            this._syncStream();
            // A foreign turn's fate never pauses this tab's queue —
            // but the room still gets the authoritative re-read.
            if (cmd.conversationId) this._refresh(cmd.conversationId);
            break;
          }
          if (cmd && cmd.turnId === f.turnId) {
            settleTurnProjection(cmd.stream);
            this.settledStream = {
              conversationId: cmd.conversationId, turnId: f.turnId,
              stream: cmd.stream, seq: ++this._settledSeq,
            };
            const lost = enqueue(this.queue, {
              text: cmd.content,
              room: this._commandRoomKey(cmd),
              opts: cmd.opts,
            });
            lost.state = UNKNOWN;
            this.command = null;
            this._syncComposer();
            this._syncStream();
          }
          // The resync contract requires the authoritative re-read —
          // it also resolves the room's gates and, when the persisted
          // tail proves the turn died, parks the interrupted card.
          const lostRoom = f.conversationId ||
            (cmd && this._commandRoomKey(cmd));
          if (lostRoom && !String(lostRoom).startsWith('fresh:')) {
            this._refresh(lostRoom);
          }
          pauseQueue(this.queue, lostRoom);
          this.queue.notice = 'The turn could not be recovered — check history before retrying';
          this._syncQueue();
          break;
        }
        // "active": reconcile the receipt snapshot without requeueing —
        // inputs the server still owns stay bound to the turn.
        const resynced = reconcileSnapshot(
          this.queue, f.turnId, p.steering || [], { terminal: false });
        for (const e of resynced.restored) this._restoreOwned(e);
        this._syncQueue();
        if (f.conversationId) {
          this._refresh(f.conversationId).then((ok) => {
            if (ok !== 'ok') { pauseQueue(this.queue, f.conversationId); this._syncQueue(); }
          });
        }
        this._refreshList();
        break;
      }
      case 'conversation.activity': {
        // A room-level notice every socket hears — unsequenced,
        // unjournaled. payload.requestId is the opening command's own
        // correlation id: the issuing tab's send/edit flow already
        // refreshes, so only foreign notices act — and a foreign
        // notice never touches this tab's in-flight command.
        const own = cmd && f.payload?.requestId === cmd.requestId;
        if (own) break;
        this._refreshList();
        if (f.conversationId !== this.activeId ||
            this._commandGates(this._viewRoomKey())) break;
        if (f.payload?.state === 'opened') {
          // The room on screen gained a live turn — attach like a
          // fresh room load that found it.
          this._attachLiveTurn({
            id: f.conversationId,
            activeTurnId: f.turnId,
            activeTurnKind: f.payload?.kind,
          });
        } else {
          // A foreign turn ended in the open room — the same
          // authoritative history re-read a resync'd terminal drives.
          // The badge settles with it: an unattached foreign turn's
          // terminal never crosses this socket's turn frames.
          this._refresh(f.conversationId);
          this._syncContext();
        }
        break;
      }
      case 'error':
        this._onError(f);
        break;
    }
  }

  _onTerminal(f) {
    const cmd = this.command;
    if (cmd && f.turnId === cmd.turnId) {
      // Settle the projection honestly before the authoritative refresh
      // replaces it.
      settleTurnProjection(cmd.stream);
      this.settledStream = {
        conversationId: cmd.conversationId, turnId: f.turnId,
        stream: cmd.stream, seq: ++this._settledSeq,
      };
      // The owning room's gate goes up BEFORE the command retires —
      // no path may dispatch into it until history is authoritative.
      const roomKey = this._commandRoomKey(cmd);
      const g = this._gate(roomKey);
      g.required = true;
      this.command = null;
      // The terminal steering snapshot is authoritative — a consumed
      // input whose event raced past a reconnect still reconciles;
      // recalled inputs return their text to the owner.
      const settled = reconcileSnapshot(this.queue, f.turnId, f.payload?.steering || [], { terminal: true });
      for (const e of settled.restored) this._restoreOwned(e);
      const failed = f.type === 'turn.failed';
      // A completed turn — whoever drove it — supersedes the room's
      // failure card: its output now trails the failed line, and an
      // armed Retry would silently rewind the visible reply.
      if (!failed) this.failedTurns.delete(roomKey);
      if (failed) {
        // Never auto-drain the backlog after a failed or canceled turn
        // — Resume is an explicit user call. Lines submitted later are
        // fresh intent, not the failed turn's fallout.
        pauseQueue(this.queue, roomKey);
        if (!interruptedTurn(f)) {
          const msg = (f.payload && f.payload.message) || f.code || 'error';
          this._toast('error', 'Turn failed', msg);
          // A definitive failure keeps an explicit retry affordance:
          // the card on the thread re-issues turn.edit against the
          // failed turn's user line while no newer input follows it —
          // regenerating, never rewinding later turns. A send that
          // never committed its user line has nothing to retry — its
          // text returns through the draft/queue paths instead.
          if (!cmd.watched &&
              !(cmd.kind === 'send' && f.payload?.userMessagePersisted === false)) {
            this.failedTurns.set(roomKey, {
              text: cmd.content, note: msg,
              retryMessageId: cmd.kind === 'edit' ? cmd.editMessageId : null,
              // The send's dispatch-time history ids — the scan for
              // its persisted line must skip every user record that
              // predates this dispatch, including an identical text.
              historyIds: cmd.historyIds || null,
            });
          }
        }
      }
      // Persistence is authoritative: only userMessagePersisted ===
      // false means the send never committed — never infer from
      // ephemeral or stream state.
      if (cmd.kind === 'send' && !cmd.watched && f.payload?.userMessagePersisted === false) {
        // The owning room is the minted conversation when the send
        // opened one — not the stale fresh token. A dispatched queue
        // entry rejoins paused at its ordinal; a direct send returns to
        // the draft ONLY when its room is the current view and the
        // draft isn't still holding the same un-sent text — otherwise
        // a paused owned copy in the originating room.
        const ownerKey = this._commandRoomKey(cmd);
        if (cmd.fromQueue) {
          requeue(this.queue, cmd.fromQueue);
          pauseQueue(this.queue, ownerKey);
        } else if (ownerKey === this._viewRoomKey() &&
                   this.composer.value.trim() !== cmd.content.trim()) {
          this._restoreDraft(cmd.content);
        } else {
          enqueue(this.queue, {
            text: cmd.content, room: ownerKey, opts: cmd.opts,
          });
          pauseQueue(this.queue, ownerKey);
        }
      }
      // The barrier: the room's refresh must land before anything
      // drains — a stale response waits for the in-flight newer one.
      this._refreshBarrier(roomKey).then((ok) => {
        if (!ok) {
          pauseQueue(this.queue, roomKey);
          this.queue.notice = 'History refresh failed — Resume retries it';
          this._syncQueue();
          return;
        }
        // Pin the failed turn's retry target while the refreshed
        // history is authoritative — a later identical send must never
        // become the line this card edits. The sync must run even when
        // nothing drains: the card's Retry arms on the target this
        // refresh just resolved.
        if (roomKey === this._viewRoomKey()) {
          const card = this.failedTurns.get(roomKey);
          if (card && !card.retryMessageId) card.retryMessageId = this._retryMessageId(card);
        }
        this._drainQueue();
        this._syncQueue();
      });
      this._syncComposer();
      this._syncStream();
      this._syncContext(); // usage landed — the estimate moved
      return;
    }
    // A turn this tab didn't drive (watched, or another tab's): refresh
    // its room only if it's the one on screen; the list always settles.
    // Its steering snapshot still reconciles this tab's receipts.
    const other = reconcileSnapshot(this.queue, f.turnId, f.payload?.steering || [], { terminal: true });
    for (const e of other.restored) this._restoreOwned(e);
    // A foreign turn completing in a room supersedes that room's
    // failure card the same way an own completion does.
    if (f.type === 'turn.completed') this.failedTurns.delete(f.conversationId);
    this._syncQueue();
    if (f.conversationId === this.activeId) {
      this._refresh(f.conversationId);
      this._syncContext();
    }
    this._refreshList();
  }

  // Waits for the room's refresh to land: starts one now, and if that
  // response turns out stale (a newer refresh superseded it), follows
  // the in-flight newer one instead of pausing on old news.
  async _refreshBarrier(roomKey) {
    const g = this._gate(roomKey);
    let ok = String(roomKey).startsWith('fresh:')
      ? await this._refreshList().then(() => 'ok')
      : await this._refresh(roomKey);
    if (ok === 'stale' && g.inflight) ok = await g.inflight;
    // Stale is fine when a newer refresh already cleared the gate —
    // the room IS authoritative; only an uncleared barrier blocks.
    return ok === 'ok' || (!g.required && !g.failed);
  }

  _onError(f) {
    // A command rejected before turn.opened: the send draft is still in
    // the composer (nothing cleared it); an edit puts back the history
    // the optimistic cut replaced. Only matching requestIds retire the command.
    if (this.command && f.requestId === this.command.requestId) {
      const cmd = this.command;
      this._restoreUnopenedEdit(cmd);
      if (cmd.fromQueue) {
        // A queue dispatch rejected pre-open returns to the local
        // queue — held, so nothing retries the rejected item
        // unintentionally.
        requeue(this.queue, cmd.fromQueue);
        pauseQueue(this.queue, this._commandRoomKey(cmd));
      } else if (cmd.kind === 'send' && cmd.content) {
        // A direct send rejected before opened must not drop its
        // text: back into the owning view's draft — unless that draft
        // still holds exactly the same un-sent line — else a paused
        // owned copy parked in the command's own room.
        if (this._commandOwnsView(cmd)) {
          if (this.composer.value.trim() !== cmd.content.trim()) {
            this._restoreDraft(cmd.content);
          }
        } else {
          enqueue(this.queue, {
            text: cmd.content,
            room: this._commandRoomKey(cmd),
            opts: cmd.opts,
          });
          pauseQueue(this.queue, this._commandRoomKey(cmd));
        }
      }
      this.command = null;
      this._syncComposer();
      this._syncStream();
    } else if (f.requestId) {
      // An error for a queue-owned command is never a silent drop: a
      // rejected steer submission never landed — restore its text. A
      // failed recall just reverts to steering — the input may still
      // be pending.
      const e = this.queue.items.find(
        (it) => it.inputId === f.requestId || it.recallId === f.requestId);
      if (e && e.state !== QUEUED) {
        if (e.recallId === f.requestId) {
          e.recallId = null;
          e.state = e.preRecall || STEERING;
          e.preRecall = null;
        } else {
          removeEntry(this.queue, e);
          this._restoreOwned(e);
        }
        this._syncQueue();
      }
    }
    this._toast('warn', 'Command', f.message || f.code || 'rejected');
  }

  _onConnection(state) {
    this.sidebar.connection = state;
    this.header.connection = state;
    this.composer.connection = state;
    if (state === 'closed') {
      // A socket drop freezes the drain until the user inspects. Local
      // queued lines were never transmitted — they stay queued. A
      // steering submission or recall whose ack never arrived is
      // unknowable (it may not have been transmitted at all); an acked
      // input keeps steering — replay reconciles its receipt on the
      // next watch.
      pauseQueue(this.queue);
      if (markInFlightUnknown(this.queue)) {
        // Routine delivery-uncertainty goes in the panel's inline
        // notice — a toast would cover the composer controls it
        // describes.
        this.queue.notice = 'A submission may not have reached the server — check history';
      }
      this._syncQueue();
      if (this.command && !this.command.turnId) {
        // The command never reached turn.opened — delivery is unknowable.
        // Never auto-resend: surface it in the panel as unknown, owned
        // by the room/token captured at submit — not the mutable view.
        // An edit's optimistic cut is undone; the parked text is the
        // draft that may not have reached the server.
        const cmd = this.command;
        this._restoreUnopenedEdit(cmd);
        if (cmd.fromQueue) {
          requeue(this.queue, cmd.fromQueue);
          cmd.fromQueue.state = UNKNOWN;
          cmd.fromQueue.turnId = null;
        } else {
          const e = enqueue(this.queue, {
            text: cmd.content,
            room: this._commandRoomKey(cmd),
            opts: cmd.opts,
          });
          e.state = UNKNOWN;
        }
        this.command = null;
        this._syncStream();
        this._syncComposer();
      }
    } else if (state === 'open') {
      // A reconnect (not the first open) must settle whatever the
      // server persisted while this tab was detached.
      if (this._hasOpened) {
        this._refreshList();
        if (this.activeId) {
          this._refresh(this.activeId).then((ok) => {
            if (ok === 'ok' && !this._commandGates(this._viewRoomKey())) this._drainQueue();
          });
        } else if (hasQueuedForRoom(this.queue, this.freshToken)) {
          // A fresh view needs no history — its queue drains directly.
          this._drainQueue();
        }
      }
      this._hasOpened = true;
      this.queue.notice = '';
      this._syncQueue();
    }
  }

  // Returns 'ok' when this call's response is the one that landed as
  // authoritative (fetch succeeded AND still the latest for the room),
  // 'stale' when a newer refresh superseded it mid-flight, 'failed'
  // when the fetch errored. Callers gate queue drains on 'ok' — a
  // stale response must never overwrite newer history.
  async _refresh(convId) {
    const g = this._gate(convId);
    const myEpoch = ++g.epoch;
    g.pending++;
    const inflight = (g.inflight = (async () => {
      const gen = this.generation;
      // A settled stream created after this refresh started belongs to
      // a newer turn — this response must never retire it.
      const settledAtStart = this._settledSeq;
      let outcome = 'failed';
      try {
        const [conv, page] = await Promise.all([
          this.api.getConversation(convId),
          this.api.getMessages(convId, { start: -100, limit: 100 }),
        ]);
        if (myEpoch === g.epoch) {
          outcome = 'ok';
          g.required = false;
          g.failed = false;
          if (convId === this.activeId && gen === this.generation) {
            this.conversation = conv;
            this.header.conversation = conv;
            this._syncCompact();
            // When this refresh is landing the view a select() is still
            // loading, it owns the room's binding too — the same fields
            // select() would adopt. An ordinary same-room refresh never
            // overrides the user's picks.
            if (this.viewLoading && conv.modelId) this._setSelection({
              route: conv.route, modelId: conv.modelId,
              effort: '', provider: '', workspace: conv.workspace || '',
            });
            // History adopts matching live widgets before the stream is
            // retired, keeping their DOM, styles, and details state intact.
            this._assignHistory(page);
            this.thread.messages = this.messages;
            this.thread.historyNotice = page.effectiveStart > 0;
            this._attachLiveTurn(conv);
            this._markInterrupted(conv);
          }
          // Drop the live projection only after history has the
          // persisted turn — and only when this refresh observed that
          // settle, so a stale response can never retire a newer
          // turn's stream.
          if (this.settledStream &&
              this.settledStream.conversationId === convId &&
              this.settledStream.seq <= settledAtStart) {
            this.settledStream = null;
            this._syncStream();
          }
        } else {
          outcome = 'stale'; // a newer refresh owns the room now
        }
      } catch {
        if (myEpoch === g.epoch) g.failed = true;
      } finally {
        if (--g.pending === 0) this._drainQueue();
        if (g.inflight === inflight) g.inflight = null;
      }
      // Refresh outcomes change dispatch eligibility (pending--,
      // required/failed) — re-project the composer so Resume/Queue
      // never linger on a pre-refresh answer.
      this._refreshList();
      this._syncComposer();
      return outcome;
    })());
    return inflight;
  }

  // Re-reads every page the user already loaded — titles, order, and
  // the active-turn markers stay fresh without shrinking the list back
  // to one page. _refreshSeq keeps the newest refresh authoritative
  // among overlapping ones; _listSeq makes the response drop when a
  // list mutation (load-more, delete) committed while it was in
  // flight — its whole-span answer no longer describes the set.
  async _refreshList() {
    const seq = ++this._refreshSeq;
    const listSeq = this._listSeq;
    const pages = Math.max(1, Math.ceil(this.allConversations.length / 100));
    try {
      const results = await Promise.all(Array.from({ length: pages },
        (_, i) => this.api.listConversations({ offset: i * 100, limit: 100 })));
      if (seq !== this._refreshSeq || listSeq !== this._listSeq) return;
      ++this._listSeq;
      const seen = new Set();
      this.allConversations = results.flatMap((r) => r.data || [])
        .filter((c) => !seen.has(c.id) && seen.add(c.id));
      this.conversationsPage = this._pageMeta(results[results.length - 1].page);
      this._pushConversations();
      this.sidebar.activeId = this.activeId;
    } catch (err) {
      if (seq !== this._refreshSeq) return;
      this._toast('warn', 'Conversations', this._errText(err));
    }
  }

  // The sidebar's explicit next page: appends what follows the loaded
  // set and reports the coverage back. A second click while a request
  // is on the wire is ignored here — moreBusy also drives the widget's
  // disabled state. Landing order decides races: an append that
  // commits while a refresh is still on the wire bumps _listSeq so the
  // refresh's pre-append span answer drops; an append whose page was
  // fetched before another commit (refresh applied, delete) is itself
  // offset-stale and drops through the same check.
  async _loadMoreConversations() {
    if (this._moreBusy || !this.conversationsPage.hasMore) return;
    const seq = this._listSeq;
    this._moreBusy = true;
    this.sidebar.moreBusy = true;
    try {
      const page = await this.api.listConversations({
        offset: this.allConversations.length, limit: 100,
      });
      if (seq !== this._listSeq) return;
      ++this._listSeq;
      const seen = new Set(this.allConversations.map((c) => c.id));
      this.allConversations = [
        ...this.allConversations,
        ...(page.data || []).filter((c) => !seen.has(c.id)),
      ];
      this.conversationsPage = this._pageMeta(page.page);
      this._pushConversations();
    } catch (err) {
      if (seq === this._listSeq) {
        this._toast('warn', 'Conversations', this._errText(err));
      }
    } finally {
      this._moreBusy = false;
      this.sidebar.moreBusy = false;
    }
  }

  // --- mutations: capture the target — a response must never overwrite a
  // newer selection ----------------------------------------------------

  async _rename(title) {
    const id = this.activeId;
    const gen = this.generation;
    try {
      const conv = await this.api.renameConversation(id, title);
      if (this.activeId === id && gen === this.generation) {
        this.conversation = conv;
        this.header.conversation = conv;
        this._syncCompact();
      }
      this._refreshList();
    } catch (err) { this._toast('error', 'Rename', this._errText(err)); }
  }

  async _compact() {
    const id = this.activeId;
    if (!id || this.compactPending.has(id) || !this.compactAvailable) return;
    this.compactPending.add(id);
    this._syncCompact();
    try {
      const conv = await this.api.compactConversation(id);
      if (this.activeId === id) {
        this.conversation = conv;
        this.header.conversation = conv;
      }
      await this._refresh(id); // the captured target, not whatever is selected now
      this._syncContext();
    } catch (err) { this._toast('error', 'Compact', this._errText(err)); }
    finally {
      this.compactPending.delete(id);
      this._syncCompact();
    }
  }

  async _delete() {
    const id = this.activeId;
    try {
      await this.api.deleteConversation(id);
      // Committed — every list response already on the wire answers a
      // roster that still contained this row and must not land it back.
      ++this._listSeq;
      this.allConversations = this.allConversations.filter((c) => c.id !== id);
      // The removed row was loaded, so the local span and the coverage
      // it reports shrink by exactly one — no refresh needed.
      const p = this.conversationsPage;
      this.conversationsPage = {
        ...p,
        total: Math.max(0, p.total - 1),
        hasMore: this.allConversations.length < Math.max(0, p.total - 1),
      };
      this._pushConversations();
      if (this.activeId === id) this.newConversation();
      this._toast('info', 'Deleted', 'Thread removed.');
    } catch (err) { this._toast('error', 'Delete', this._errText(err)); }
  }

  async _saveGateway(id, body) {
    try {
      const doc = await this.api.saveGateway(id, body);
      // The record saved before the catalog merge ran — refresh the
      // list behind the dialog either way, then hand the outcome back:
      // a catalogError keeps the editor open with the failure text.
      await this._refreshGateways();
      const catalogError = (doc && doc.catalogError) || '';
      this.settingsPage.gatewaySaveResult = {
        id, models: doc.models || [], added: doc.added || [], catalogError,
      };
      if (!catalogError) this._toast('info', 'Gateway', `${id} saved.`);
    } catch (err) {
      const message = this._errText(err);
      this.settingsPage.gatewaySaveResult = { id, error: message };
      this._toast('error', 'Gateway', message);
    }
  }

  async _deleteGateway(id) {
    try {
      await this.api.deleteGateway(id);
      await this._refreshGateways();
      this._toast('info', 'Gateway', `${id} deleted.`);
    } catch (err) { this._toast('error', 'Gateway', this._errText(err)); }
  }

  async _importGatewayModels(id) {
    try {
      const doc = await this.api.importGatewayModels(id);
      await this._refreshGateways();
      this.settingsPage.gatewayImportResult = {
        id, models: doc.models || [], added: doc.added || [],
        catalogError: doc.catalogError || '',
      };
    } catch (err) {
      this.settingsPage.gatewayImportResult = { id, error: this._errText(err) };
    }
  }

  async _refreshGateways() {
    const page = await this.api.listGateways({ limit: 100 });
    this.gateways = page.data || [];
    this.header.gateways = this.gateways;
    this.settingsPage.gateways = this.gateways;
    this._reconcileSelection();
  }

  async _patchSetting(key, value) {
    try {
      this.settings = await this.api.patchSetting(key, value);
      this.settingsPage.settings = this.settings;
      this._syncCompact();
      this._toast('info', 'Settings', `${key} updated.`);
    } catch (err) {
      // Re-project the last good document so a failed input doesn't
      // masquerade as a saved value.
      this.settingsPage.settings = this.settings;
      this._toast('error', 'Settings', this._errText(err));
    }
  }

  // --- misc ---------------------------------------------------------------

  _toast(tone, title, message) {
    this.toasts.push({ tone, title, message });
  }

  _errText(err) {
    return (err && (err.detail || err.message)) || 'request failed';
  }
}

// A user stop is a 408 interrupted terminal. The partial reply stays;
// it is not a failed turn.
function interruptedTurn(frame) {
  const payload = (frame && frame.payload) || {};
  return payload.status === 408 || payload.code === 'interrupted';
}

customElements.define('nusa-app', AppShell);
