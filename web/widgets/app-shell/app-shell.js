import { Widget, el } from '../../lib/dom.js';
import { APIClient } from '../../lib/api-client.js';
import { TurnSocket } from '../../lib/socket-client.js';

// Orchestration only. The shell owns the API/socket instances, the
// selection + command state, the URL hash, and the wiring between
// widgets' events and their properties — it renders no domain markup
// of its own.
//
// Turn lifecycle lives in ONE object, `this.command`:
//   {kind:'send'|'edit', requestId, turnId:null|string,
//    conversationId:null|string, content, ephemeral:false,
//    stream:{active,text,phase,tools}}
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
    this.tools = [];

    this.activeId = null;       // selected conversation id (the VIEW)
    this.conversation = null;   // its detail projection
    this.messages = [];         // its latest message window
    this.selection = null;      // {route, modelId} next-send binding
    this.command = null;        // the one in-flight command (see above)
    this.generation = 0;        // stale-response token
    this.sidebarOpen = matchMedia('(min-width: 761px)').matches;

    this.root.classList.add('shell');
    this._build();
    this._listen();
    this.socket.connect();
    this._boot();
    addEventListener('hashchange', () => {
      // Hash removal while a room is active falls back to onboarding.
      if (!this._selectFromHash() && this.activeId) this.newConversation();
    });
  }

  _build() {
    this.sidebar = el('nusa-nav-sidebar');
    this.header = el('nusa-conversation-header');
    this.thread = el('nusa-message-thread');
    this.composer = el('nusa-chat-composer');
    this.panel = el('nusa-context-panel');
    this.toasts = el('nusa-toast-stack');
    this.main = el('main', { class: 'main' }, [this.header, this.thread, this.composer]);
    this.root.append(
      el('div', { class: 'grid' }, [this.sidebar, this.main, this.panel]),
      this.toasts,
    );
    this.sidebar.open = this.sidebarOpen;
  }

  _listen() {
    const on = (target, type, fn) => target.addEventListener(type, (e) => fn(e.detail || {}));

    on(this.sidebar, 'conversation-select', ({ id }) => this.select(id));
    on(this.sidebar, 'conversation-new', () => this.newConversation());
    on(this.sidebar, 'conversation-search', ({ query }) => this._filter(query));
    on(this.sidebar, 'context-open', () => { this.panel.open = true; });

    on(this.header, 'sidebar-toggle', () => { this.sidebarOpen = !this.sidebarOpen; this.sidebar.open = this.sidebarOpen; });
    on(this.header, 'context-toggle', () => { this.panel.open = !this.panel.open; });
    on(this.header, 'model-change', (d) => this._setSelection(d));
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

    on(this.panel, 'context-close', () => { this.panel.open = false; });
    on(this.panel, 'gateway-save', ({ id, body }) => this._saveGateway(id, body));
    on(this.panel, 'gateway-delete', ({ id }) => this._deleteGateway(id));
    on(this.panel, 'setting-change', ({ key, value }) => this._patchSetting(key, value));

    this.socket.addEventListener('connection', (e) => this._onConnection(e.detail.state));
    this.socket.addEventListener('frame', (e) => this._onFrame(e.detail.frame));
  }

  async _boot() {
    const [convs, gws, cfg, tools] = await Promise.allSettled([
      this.api.listConversations({ limit: 100 }),
      this.api.listGateways({ limit: 100 }),
      this.api.getSettings(),
      this.api.listTools(),
    ]);
    if (convs.status === 'fulfilled') {
      this.allConversations = convs.value.data || [];
      this.sidebar.conversations = this.allConversations;
    } else this._toast('error', 'Conversations', this._errText(convs.reason));
    if (gws.status === 'fulfilled') {
      this.gateways = gws.value.data || [];
      this.header.gateways = this.gateways;
      this.panel.gateways = this.gateways;
      this._defaultSelection();
    } else this._toast('error', 'Gateways', this._errText(gws.reason));
    if (cfg.status === 'fulfilled') {
      this.settings = cfg.value;
      this.panel.settings = this.settings;
    } else this._toast('error', 'Settings', this._errText(cfg.reason));
    if (tools.status === 'fulfilled') {
      this.tools = tools.value.data || [];
      this.panel.tools = this.tools;
    } else this._toast('error', 'Tools', this._errText(tools.reason));

    if (!this._selectFromHash() && this.allConversations.length) {
      this.select(this.allConversations[0].id);
    }
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
    this.selection = sel;
    this.header.selection = sel;
    this._syncComposer();
  }

  async select(id) {
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
    this.panel.conversation = null;
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
      this.panel.conversation = conv;
      this.messages = page.messages || [];
      this.thread.messages = this.messages;
      this.thread.historyNotice = page.effectiveStart > 0;
      if (conv.modelId) this._setSelection({ route: conv.route, modelId: conv.modelId });
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
  }

  newConversation() {
    this.generation++;
    this.activeId = null;
    this.viewLoading = false;
    this.conversation = null;
    this.messages = [];
    this._setHash('');
    this.sidebar.activeId = null;
    this.header.conversation = null;
    this.panel.conversation = null;
    this.thread.hasConversation = false;
    this.thread.messages = this.messages;
    this.thread.historyNotice = false;
    this._defaultSelection();
    this._syncComposer();
    this._syncStream();
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

  // The live stream renders only in the room that owns the command.
  _syncStream() {
    const cmd = this.command;
    this.thread.stream = (cmd && cmd.conversationId === this.activeId)
      ? cmd.stream : null;
  }

  // --- commands ---------------------------------------------------------

  _send(content) {
    const requestId = this.socket.sendTurn({
      conversationId: this.activeId,
      route: this.selection ? this.selection.route : '',
      modelId: this.selection ? this.selection.modelId : '',
      content,
    });
    this.command = {
      kind: 'send', requestId, turnId: null,
      conversationId: this.activeId, content, ephemeral: false,
      cancelRequested: false,
      // Only a send issued on the empty onboarding view may claim the
      // minted room when it opens — never force a user who navigated
      // meanwhile back to it.
      claimFreshView: this.activeId === null,
      stream: { active: true, text: '', phase: null, tools: [] },
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
      stream: { active: true, text: '', phase: null, tools: [] },
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
      case 'turn.delta':
        if (cmd && cmd.turnId === f.turnId) {
          cmd.stream.text += (f.payload && f.payload.text) || '';
          this._syncStream();
        }
        break;
      case 'turn.phase':
        if (cmd && cmd.turnId === f.turnId) {
          cmd.stream.phase = f.payload && f.payload.phase;
          this._syncStream();
        }
        break;
      case 'turn.tool_call':
        if (cmd && cmd.turnId === f.turnId) {
          cmd.stream.tools.push({
            callId: f.payload.callId, name: f.payload.name, output: false,
          });
          this._syncStream();
        }
        break;
      case 'turn.tool_output':
        if (cmd && cmd.turnId === f.turnId) {
          const t = cmd.stream.tools.find((x) => x.callId === f.payload.callId);
          if (t) t.output = true;
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
      this.command = null;
      if (f.type === 'turn.failed') {
        this._toast('error', 'Turn failed', (f.payload && f.payload.message) || f.code || 'error');
      }
      // Authoritative refresh of the command's own room — even if the
      // user navigated away mid-turn, its state must settle. _refresh
      // already settles the list; no second list GET.
      if (cmd.conversationId) this._refresh(cmd.conversationId);
      else this._refreshList();
      this._syncComposer();
      this._syncStream();
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
    try {
      const [conv, page] = await Promise.all([
        this.api.getConversation(convId),
        this.api.getMessages(convId, { start: -100, limit: 100 }),
      ]);
      if (convId === this.activeId && gen === this.generation) {
        this.conversation = conv;
        this.header.conversation = conv;
        this.panel.conversation = conv;
        this.messages = page.messages || [];
        this.thread.messages = this.messages;
        this.thread.historyNotice = page.effectiveStart > 0;
      }
    } catch { /* list refresh still runs */ }
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
      }
      this._refreshList();
    } catch (err) { this._toast('error', 'Rename', this._errText(err)); }
  }

  async _compact() {
    const id = this.activeId;
    try {
      await this.api.compactConversation(id);
      this._toast('info', 'Compact', 'Compaction accepted.');
      this._refresh(id); // the captured target, not whatever is selected now
    } catch (err) { this._toast('error', 'Compact', this._errText(err)); }
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
      this._toast('info', 'Gateway', `${id} saved.`);
    } catch (err) { this._toast('error', 'Gateway', this._errText(err)); }
  }

  async _deleteGateway(id) {
    try {
      await this.api.deleteGateway(id);
      await this._refreshGateways();
      this._toast('info', 'Gateway', `${id} deleted.`);
    } catch (err) { this._toast('error', 'Gateway', this._errText(err)); }
  }

  async _refreshGateways() {
    const page = await this.api.listGateways({ limit: 100 });
    this.gateways = page.data || [];
    this.header.gateways = this.gateways;
    this.panel.gateways = this.gateways;
    this._reconcileSelection();
  }

  async _patchSetting(key, value) {
    try {
      this.settings = await this.api.patchSetting(key, value);
      this.panel.settings = this.settings;
      this._toast('info', 'Settings', `${key} updated.`);
    } catch (err) {
      // Re-project the last good document so a failed input doesn't
      // masquerade as a saved value.
      this.panel.settings = this.settings;
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

customElements.define('nusa-app', AppShell);
