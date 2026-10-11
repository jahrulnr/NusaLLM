import { Widget, el } from '../../lib/dom.js';
import { APIClient } from '../../lib/api-client.js';
import { TurnSocket } from '../../lib/socket-client.js';
import {
  createTurnProjection, applyTurnFrame, settleTurnProjection,
} from '../../lib/turn-projection.js';
import {
  createQueue, enqueue, steer, requeue, bindRoom, takeQueued,
  hasQueuedForRoom, findEntry, removeEntry, markInFlightUnknown,
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
    this.gateways = [];
    this.settings = {};

    this.activeId = null;       // selected conversation id (the VIEW)
    this.conversation = null;   // its detail projection
    this.messages = [];         // its latest message window
    this.selection = null;      // {route, modelId, effort, provider, workspace} next-send binding
    this.command = null;        // the one in-flight command (see above)
    this.settledStream = null;  // keep the final live view through history refresh
    this.queue = createQueue(); // room-bound pending submissions + steer receipts
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

    this.root.classList.add('shell');
    this._build();
    this._listen();
    this.socket.connect();
    this._boot();
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
    // The scrim sits beside .grid, never inside it — the grid
    // children's entrance transform would become its fixed
    // containing block.
    this.root.append(
      el('div', { class: 'grid' }, [this.sidebar, this.workspace]),
      this.scrim,
      this.toasts,
    );
    this._syncSidebar();
  }

  _listen() {
    const on = (target, type, fn) => target.addEventListener(type, (e) => fn(e.detail || {}, e));

    on(this.sidebar, 'conversation-select', ({ id }) => this.select(id));
    on(this.sidebar, 'conversation-new', () => this.newConversation());
    on(this.sidebar, 'conversation-search', ({ query }) => this._filter(query));
    on(this.sidebar, 'sidebar-close', () => { this.sidebarOpen = false; this._syncSidebar(); });
    on(this.scrim, 'click', () => { this.sidebarOpen = false; this._syncSidebar(); });

    on(this.header, 'sidebar-toggle', () => { this.sidebarOpen = !this.sidebarOpen; this._syncSidebar(); });
    on(this.header, 'settings-open', () => this.openSettings());
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
      this.sidebar.conversations = this.allConversations;
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
    this.thread.historyNotice = false;
    this._defaultSelection();
    this._syncComposer();
    this._syncStream();
    this._syncContext();
    this.composer.clear();
    this._drainQueue(); // a pre-open queue drains once the view is free
  }

  _filter(query) {
    const q = (query || '').trim().toLowerCase();
    this.sidebar.conversations = !q ? this.allConversations
      : this.allConversations.filter((c) => (c.title || c.id).toLowerCase().includes(q));
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
    this.composer.busy = !!this.command; // stays busy in every room
    // Stop/Steer scope to the turn's own room — no hidden global
    // targets while a command runs elsewhere.
    const cmd = this.command;
    this.composer.canStop = this._commandOwnsView(cmd);
    this.composer.canSteer = !!(cmd && cmd.kind === 'send' && cmd.turnId &&
      this._commandOwnsView(cmd) && this.socket.state === 'open');
    this.thread.commandsBusy = !!cmd;
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
    this.composer.queuePaused = this.queue.paused;
    const g = this._gate(viewKey);
    this.composer.queueNotice = this.queue.notice || '';
    // Resume is offered whenever this view holds queued lines — its
    // very job is retrying a required/failed refresh, so the barrier
    // itself must not disable it. Only busy, offline, loading, or a
    // refresh already in flight make it inert.
    const resumable = this.queue.paused && hasQueuedForRoom(this.queue, viewKey);
    this.composer.resumeVisible = resumable;
    this.composer.canResume = resumable && !this.command &&
      !this.viewLoading && this.socket.state === 'open' && !g.pending;
    // Enter captures locally whenever dispatch can't happen now:
    // busy, paused, backed-up, or a refresh barrier for this view.
    this.composer.queueMode = !!this.command || this.queue.paused ||
      hasQueuedForRoom(this.queue, viewKey) ||
      g.required || g.pending > 0 || g.failed;
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
    return !!cmd && (cmd.conversationId || cmd.queueRoom) === this._viewRoomKey();
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
  }

  // One dispatch rule for every path that could start a turn: the
  // socket must be open, no command in flight, the view settled, and
  // the room's authoritative refresh not outstanding or failed.
  _dispatchable(roomKey) {
    if (this.command || this.viewLoading) return false;
    if (this.socket.state !== 'open') return false;
    const g = this._gate(roomKey);
    return !(g.required || g.pending > 0 || g.failed);
  }

  // The auto-drain additionally waits on an unpaused queue.
  _drainable(roomKey) {
    return this._dispatchable(roomKey) && !this.queue.paused;
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
    // Paused counts as undrainable here too — the composer's Queue label
    // must mean what it says.
    if (!this._drainable(roomKey) || hasQueuedForRoom(this.queue, roomKey)) {
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
  // first — entries for other rooms wait for their own view, a paused
  // queue waits for an explicit Resume, and a refresh barrier holds
  // everything back until history is authoritative.
  _drainQueue() {
    if (!this._drainable(this._viewRoomKey())) return;
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
    this.queue.paused = true;
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
  // the draft — the user checked history and chose to let it go.
  _queueDismiss(id) {
    const e = findEntry(this.queue, id);
    if (!e || e.state !== UNKNOWN) return;
    removeEntry(this.queue, e);
    this._syncQueue();
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
  async _queueResume() {
    this.queue.paused = false;
    this.queue.notice = '';
    const key = this._viewRoomKey();
    const g = this._gate(key);
    if (!String(key).startsWith('fresh:') && (g.required || g.failed)) {
      const ok = await this._refresh(key);
      if (ok !== 'ok') {
        this.queue.paused = true;
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
    // does — and never overwrites an in-flight command's sequencing.
    // Returning false keeps the editor open with the typed content.
    if (this.command) {
      this._toast('warn', 'Edit', 'Finish or stop the running turn before editing.');
      return false;
    }
    if (!this._dispatchable(this._viewRoomKey())) {
      this._toast('warn', 'Edit', 'Wait for the room to finish refreshing before editing.');
      return false;
    }
    const prior = this.messages.slice();
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
      removedIds,
      stream: createTurnProjection(requestId),
    };
    this._syncComposer();
    this._syncStream();
    return true;
  }

  // A pre-open edit failure puts the cut history back. Once the turn
  // has opened, the terminal HTTP read is authoritative instead.
  _restoreUnopenedEdit(cmd) {
    if (!cmd || cmd.kind !== 'edit' || cmd.turnId || !cmd.priorMessages) return;
    if (this.activeId !== cmd.conversationId) return;
    this.messages = cmd.priorMessages;
    this.thread.messages = this.messages;
  }

  // A room loaded while a transport turn is still running attaches to
  // it. This tab's own in-flight command is left alone — it is already
  // subscribed, and replacing it would drop that turn's client state.
  _attachLiveTurn(conv) {
    const turnId = conv && conv.activeTurnId;
    if (!turnId || !conv.id || this.command) return;
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

  _onFrame(f) {
    const cmd = this.command;
    switch (f.type) {
      case 'turn.opened': {
        if (!cmd || f.requestId !== cmd.requestId) return; // foreign turn
        if (cmd.turnId) return;                            // replayed opened — idempotent
        cmd.turnId = f.turnId;
        cmd.conversationId = f.conversationId;
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
              room: cmd.conversationId || cmd.queueRoom,
              opts: cmd.opts,
            });
            lost.state = UNKNOWN;
            this.command = null;
            this._syncComposer();
            this._syncStream();
          }
          this.queue.paused = true;
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
            if (ok !== 'ok') { this.queue.paused = true; this._syncQueue(); }
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
        if (f.conversationId !== this.activeId || this.command) break;
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
          this._refresh(f.conversationId);
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
      const roomKey = cmd.conversationId || cmd.queueRoom;
      const g = this._gate(roomKey);
      g.required = true;
      this.command = null;
      // The terminal steering snapshot is authoritative — a consumed
      // input whose event raced past a reconnect still reconciles;
      // recalled inputs return their text to the owner.
      const settled = reconcileSnapshot(this.queue, f.turnId, f.payload?.steering || [], { terminal: true });
      for (const e of settled.restored) this._restoreOwned(e);
      const failed = f.type === 'turn.failed';
      if (failed) {
        // Never auto-drain after a failed or canceled turn — Resume is
        // an explicit user call.
        this.queue.paused = true;
        if (!interruptedTurn(f)) {
          this._toast('error', 'Turn failed', (f.payload && f.payload.message) || f.code || 'error');
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
        const ownerKey = cmd.conversationId || cmd.queueRoom;
        if (cmd.fromQueue) {
          requeue(this.queue, cmd.fromQueue);
          this.queue.paused = true;
        } else if (ownerKey === this._viewRoomKey() &&
                   this.composer.value.trim() !== cmd.content.trim()) {
          this._restoreDraft(cmd.content);
        } else {
          enqueue(this.queue, {
            text: cmd.content, room: ownerKey, opts: cmd.opts,
          });
          this.queue.paused = true;
        }
      }
      // The barrier: the room's refresh must land before anything
      // drains — a stale response waits for the in-flight newer one.
      this._refreshBarrier(roomKey).then((ok) => {
        if (!ok) {
          this.queue.paused = true;
          this.queue.notice = 'History refresh failed — Resume retries it';
          this._syncQueue();
          return;
        }
        this._drainQueue();
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
    this._syncQueue();
    if (f.conversationId === this.activeId) this._refresh(f.conversationId);
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
        // queue — paused, so nothing retries the rejected item
        // unintentionally.
        requeue(this.queue, cmd.fromQueue);
        this.queue.paused = true;
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
            room: cmd.conversationId || cmd.queueRoom,
            opts: cmd.opts,
          });
          this.queue.paused = true;
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
      this.queue.paused = true;
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
            room: cmd.conversationId || cmd.queueRoom,
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
            if (ok === 'ok' && !this.command) this._drainQueue();
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

  async _refreshList() {
    try {
      const page = await this.api.listConversations({ limit: 100 });
      this.allConversations = page.data || [];
      this.sidebar.conversations = this.allConversations;
      this.sidebar.activeId = this.activeId;
    } catch (err) {
      this._toast('warn', 'Conversations', this._errText(err));
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
      this.allConversations = this.allConversations.filter((c) => c.id !== id);
      this.sidebar.conversations = this.allConversations;
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
