import { Widget, el } from '../../lib/dom.js';
import { APIClient } from '../../lib/api-client.js';
import { TurnSocket } from '../../lib/socket-client.js';
import {
  createTurnProjection, applyTurnFrame, settleTurnProjection,
} from '../../lib/turn-projection.js';

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
    this.main = el('main', { class: 'main' }, [this.header, this.thread, this.composer]);
    this.workspace = el('div', { class: 'workspace' }, [this.main, this.settingsPage]);
    this.settingsPage.hidden = true;
    this.root.append(
      el('div', { class: 'grid' }, [this.sidebar, this.workspace]),
      this.toasts,
    );
    this.sidebar.open = this.sidebarOpen;
  }

  _listen() {
    const on = (target, type, fn) => target.addEventListener(type, (e) => fn(e.detail || {}));

    on(this.sidebar, 'conversation-select', ({ id }) => this.select(id));
    on(this.sidebar, 'conversation-new', () => this.newConversation());
    on(this.sidebar, 'conversation-search', ({ query }) => this._filter(query));

    on(this.header, 'sidebar-toggle', () => { this.sidebarOpen = !this.sidebarOpen; this.sidebar.open = this.sidebarOpen; });
    on(this.header, 'settings-open', () => this.openSettings());
    on(this.header, 'model-change', (d) => this._setSelection(d));
    on(this.header, 'workspace-change', (d) => this._setWorkspace(d.workspace));
    on(this.header, 'conversation-rename', ({ title }) => this._rename(title));
    on(this.header, 'conversation-compact', () => this._compact());
    on(this.header, 'conversation-delete', () => this._delete());

    on(this.thread, 'message-edit', ({ messageId, content }) => this._edit(messageId, content));

    on(this.composer, 'turn-send', ({ content }) => this._send(content));
    on(this.composer, 'turn-cancel', () => {
      // The command's own turn is the target — never whatever room the
      // user happens to be viewing. Pre-open the server has no turnId
      // to cancel yet — arm the flag and fire the exact cancel the
      // moment turn.opened fills the ids in.
      if (!this.command) return;
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
    on(this.settingsPage, 'gateway-model-discover', ({ id }) => this._discoverGatewayModels(id));
    on(this.settingsPage, 'setting-change', ({ key, value }) => this._patchSetting(key, value));

    this.socket.addEventListener('connection', (e) => this._onConnection(e.detail.state));
    this.socket.addEventListener('frame', (e) => this._onFrame(e.detail.frame));
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
    this.sidebar.activeId = id;
    if (matchMedia('(max-width: 760px)').matches) {
      this.sidebarOpen = false;
      this.sidebar.open = false;
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
      this.conversation = conv;
      this.header.conversation = conv;
      this._syncCompact();
      this.messages = page.messages || [];
      this.thread.messages = this.messages;
      this.thread.historyNotice = page.effectiveStart > 0;
      if (conv.modelId) this._setSelection({
        route: conv.route, modelId: conv.modelId,
        effort: '', provider: '', workspace: conv.workspace || '',
      });
      this.sidebar.activeId = id;
    } catch (err) {
      if (gen !== this.generation) return;
      // The room is gone or unreadable — land on onboarding rather
      // than a half-loaded view with an armed composer.
      this._toast('error', 'Conversation', this._errText(err));
      this.newConversation();
      return;
    } finally {
      if (gen === this.generation) this.viewLoading = false;
    }
    this.thread.loading = false;
    this._syncComposer();
    this._syncStream();
    this._syncContext();
  }

  newConversation() {
    this._showChat();
    this.generation++;
    this.activeId = null;
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
  }

  _filter(query) {
    const q = (query || '').trim().toLowerCase();
    this.sidebar.conversations = !q ? this.allConversations
      : this.allConversations.filter((c) => (c.title || c.id).toLowerCase().includes(q));
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
    const visible = cmd && cmd.conversationId === this.activeId
      ? cmd.stream : this.settledStream && this.settledStream.conversationId === this.activeId
        ? this.settledStream.stream : null;
    this.thread.stream = visible;
  }

  // --- commands ---------------------------------------------------------

  _send(content) {
    const sel = this.selection || {};
    const requestId = this.socket.sendTurn({
      conversationId: this.activeId,
      route: sel.route || '',
      modelId: sel.modelId || '',
      content,
      effort: sel.effort || '',
      provider: sel.provider || '',
      workspace: sel.workspace || '',
    });
    this.command = {
      kind: 'send', requestId, turnId: null,
      conversationId: this.activeId, content, ephemeral: false,
      cancelRequested: false,
      // Only a send issued on the empty onboarding view may claim the
      // minted room when it opens — never force a user who navigated
      // meanwhile back to it.
      claimFreshView: this.activeId === null,
      stream: createTurnProjection(requestId),
    };
    this.composer.busy = true; // draft is NOT cleared until opened
    this._syncStream();
  }

  _edit(messageId, content) {
    const requestId = this.socket.editTurn({
      conversationId: this.activeId, messageId, content,
    });
    this.command = {
      kind: 'edit', requestId, turnId: null,
      conversationId: this.activeId, content, ephemeral: false,
      cancelRequested: false, claimFreshView: false,
      stream: createTurnProjection(requestId),
    };
    this.composer.busy = true;
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
        // Claim the minted room's view ONLY when the send started on
        // the empty onboarding view and the user still hasn't picked
        // anything else. Never hijack a mid-wait navigation.
        if (cmd.claimFreshView && f.conversationId && this.activeId === null) {
          this.activeId = f.conversationId;
          this.sidebar.activeId = f.conversationId;
          this._setHash(f.conversationId);
          this.header.conversation = this.conversation = { id: f.conversationId };
          this.thread.hasConversation = true;
        }
        if (cmd.kind === 'send') {
          // The draft drops only if the composer still holds it — a
          // message typed while waiting belongs to the next turn.
          if (this.composer.value.trim() === cmd.content) this.composer.clear();
          if (!cmd.ephemeral) {
            cmd.ephemeral = true;
            if (cmd.conversationId === this.activeId) {
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
        if (cmd && cmd.turnId === f.turnId) {
          applyTurnFrame(cmd.stream, f);
          this._syncStream();
        }
        break;
      case 'turn.completed':
      case 'turn.failed':
        this._onTerminal(f);
        break;
      case 'turn.resync_required':
        if (f.conversationId === this.activeId) this._refresh(f.conversationId);
        this._refreshList();
        break;
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
        conversationId: cmd.conversationId,
        stream: cmd.stream,
      };
      this.command = null;
      if (f.type === 'turn.failed' && !interruptedTurn(f)) {
        this._toast('error', 'Turn failed', (f.payload && f.payload.message) || f.code || 'error');
      }
      // Authoritative refresh of the command's own room — even if the
      // user navigated away mid-turn, its state must settle. _refresh
      // already settles the list; no second list GET.
      if (cmd.conversationId) this._refresh(cmd.conversationId);
      else this._refreshList();
      this._syncComposer();
      this._syncStream();
      this._syncContext(); // usage landed — the estimate moved
      return;
    }
    // A turn this tab didn't drive (watched, or another tab's): refresh
    // its room only if it's the one on screen; the list always settles.
    if (f.conversationId === this.activeId) this._refresh(f.conversationId);
    this._refreshList();
  }

  _onError(f) {
    // A command rejected before turn.opened: the send draft is still in
    // the composer (nothing cleared it); an edit leaves the persisted
    // original untouched. Only matching requestIds retire the command.
    if (this.command && f.requestId === this.command.requestId) {
      this.command = null;
      this._syncComposer();
      this._syncStream();
    }
    this._toast('warn', 'Command', f.message || f.code || 'rejected');
  }

  _onConnection(state) {
    this.sidebar.connection = state;
    this.header.connection = state;
    this.composer.connection = state;
    if (state === 'closed' && this.command && !this.command.turnId) {
      // The command never reached turn.opened — delivery is unknowable.
      // Never auto-resend: tell the user to check history.
      this.command = null;
      this._syncComposer();
      this._syncStream();
      this._toast('warn', 'Connection',
        'Connection dropped before acknowledgement; check thread history before retrying.');
    } else if (state === 'open') {
      // A reconnect (not the first open) must settle whatever the
      // server persisted while this tab was detached.
      if (this._hasOpened) this._refreshList();
      this._hasOpened = true;
    }
  }

  async _refresh(convId) {
    const gen = this.generation;
    let loaded = false;
    try {
      const [conv, page] = await Promise.all([
        this.api.getConversation(convId),
        this.api.getMessages(convId, { start: -100, limit: 100 }),
      ]);
      if (convId === this.activeId && gen === this.generation) {
        this.conversation = conv;
        this.header.conversation = conv;
        this._syncCompact();
        // History adopts matching live widgets before the stream is
        // retired, keeping their DOM, styles, and details state intact.
        this.messages = page.messages || [];
        this.thread.messages = this.messages;
        this.thread.historyNotice = page.effectiveStart > 0;
        loaded = true;
      }
    } catch { /* list refresh still runs */ }
    // Drop the live projection only after history has the persisted
    // turn. A failed refresh keeps the interrupted text on screen.
    if (loaded && this.settledStream?.conversationId === convId) {
      this.settledStream = null;
      this._syncStream();
    }
    this._refreshList();
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
      await this.api.saveGateway(id, body);
      await this._refreshGateways();
      this.settingsPage.gatewaySaveResult = { id };
      this._toast('info', 'Gateway', `${id} saved.`);
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

  async _discoverGatewayModels(id) {
    try {
      const result = await this.api.discoverGatewayModels(id);
      this.settingsPage.discoveredModels = { id, models: result.data || [] };
    } catch (err) {
      this.settingsPage.discoveredModels = { id, error: this._errText(err) };
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
