import { Widget, el, emit, deepActiveElement, findByFocusKey } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

// Composer: autosizing textarea, Enter sends / Shift+Enter newline.
// While a turn runs anywhere — or the view's queue holds a backlog,
// or the room sits behind a refresh barrier — Enter captures the line
// locally. The send control stays an icon; its accessible name and the
// shortcut say queue. The pending panel lists
// room-bound queue entries and steering receipts; foreign rooms show
// an Open action instead of mutating this draft. Steer is a separate
// control, visible only while this view owns the running send turn;
// Stop likewise only inside that room. The buffer is only cleared by
// clear() — the shell calls it once the queue or an opened turn owns
// the text, so a pre-open error never eats the draft.
// Decimal-K token abbreviation — mirrors Go's FormatScaledTokens so the
// footer matches the TUI's rendering.
function fmtTok(n) {
  if (!n || n <= 0) return '0';
  if (n >= 1e6) {
    const whole = Math.floor(n / 1e6);
    const frac = Math.floor(((n % 1e6) * 10) / 1e6);
    return frac === 0 ? `${whole}M` : `${whole}.${frac}M`;
  }
  if (n >= 1e3) return `${n % 1e3 === 0 ? n / 1e3 : Math.round(n / 1e3)}K`;
  return String(n);
}

const STATE_LABELS = {
  queued: 'Queued',
  submitting: 'Sending steer…',
  steering: 'Waiting for next step',
  recalling: 'Recalling…',
  unknown: 'Delivery unknown',
};

// The fields a pending row is built from — when none changed the row
// node is reused, so focus and scroll survive unrelated updates.
function rowSig(item) {
  return [item.state, item.text, item.roomLabel, item.foreign, item.online].join('');
}

// An action button by its data-action identity — walked, not queried,
// so the headless stand-ins resolve it exactly like the real DOM.
// Without an action, the row's first action button answers.
function actionIn(node, action) {
  if (!node) return null;
  if (action ? node.getAttribute?.('data-action') === action
    : node.className === 'pending-action') return node;
  for (const child of node.children || []) {
    const hit = actionIn(child, action);
    if (hit) return hit;
  }
  return null;
}

class ChatComposer extends Widget {
  static stylesheet = new URL('./chat-composer.css', import.meta.url);

  constructor() {
    super();
    this._busy = false;
    this._connection = 'closed';
    this._selection = null;
    this._canSend = false;
    this._context = null; // {estimate, total} token stats
    this._canSteer = false;
    this._canStop = false;
    this._pendingItems = [];
    this._queuePaused = false;
    this._canResume = false;
    this._queueMode = false;
    this._queueNotice = '';
    this._rowCache = new Map(); // item.id -> {node, sig}
    this._render();
  }

  set busy(v) { this._busy = !!v; this._syncButton(); }
  set connection(state) { this._connection = state; this._syncButton(); }
  set selection(sel) { this._selection = sel; this._syncButton(); }
  set canSend(v) { this._canSend = !!v; this._syncButton(); }
  set context(stats) { this._context = stats || null; this._syncButton(); }
  get context() { return this._context; }
  // Steer is visible only while the current view owns an opened send
  // turn — a foreign room shows nothing rather than a dead button.
  set canSteer(v) { this._canSteer = !!v; this._syncButton(); }
  // Stop is visible only inside the room owning the running turn.
  set canStop(v) { this._canStop = !!v; this._syncButton(); }
  // Queue mode makes Enter/primary capture locally — busy, backlogged,
  // or behind a refresh barrier.
  set queueMode(v) { this._queueMode = !!v; this._syncButton(); }
  set pendingItems(list) { this._pendingItems = list || []; this._syncPending(); }
  set queuePaused(v) { this._queuePaused = !!v; this._syncPending(); }
  set canResume(v) { this._canResume = !!v; this._syncPending(); }
  set queueNotice(v) { this._queueNotice = v || ''; this._syncPending(); }
  // Resume's footer shows only while the queue holds lines this view
  // owns — separate from canResume's click eligibility.
  set resumeVisible(v) { this._resumeVisible = !!v; this._syncPending(); }

  get value() { return this.input ? this.input.value : ''; }
  set value(v) { if (this.input) { this.input.value = v || ''; this._autosize(); this._syncButton(); } }
  clear() { this.value = ''; if (this.input) this.input.focus(); }

  _render() {
    this.root.classList.add('composer');
    this.input = el('textarea', {
      class: 'input', rows: '1',
      placeholder: 'Ask anything, or describe a task…',
      'aria-label': 'Message input',
    });
    this.input.addEventListener('input', () => { this._autosize(); this._syncButton(); });
    this.input.addEventListener('keydown', (e) => {
      // IME composition must finish on its own terms — Enter inside a
      // composing session commits the candidate, never the message.
      if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
        e.preventDefault();
        this._send();
      }
    });
    this.button = el('button', { class: 'send', type: 'button' });
    this.steerButton = el('button', { class: 'steer', type: 'button', hidden: true });
    this.steerButton.addEventListener('click', () => this._steer());
    this.stopButton = el('button', {
      class: 'send cancel', type: 'button', hidden: true,
      'aria-label': 'Stop current turn', title: 'Stop this turn',
    }, [icon('stop', 13)]);
    this.stopButton.addEventListener('click', () => emit(this, 'turn-cancel'));
    this.pendingPanel = el('div', { class: 'pending', hidden: true, role: 'list', 'aria-label': 'Pending messages' });
    this.pendingHead = el('div', { class: 'pending-title' });
    this.pendingBody = el('div', { class: 'pending-rows' });
    this.pendingNotice = el('div', { class: 'pending-notice', role: 'status', hidden: true });
    this.resumeButton = el('button', {
      class: 'pending-resume', type: 'button',
      onclick: () => emit(this, 'queue-resume'),
    }, [el('span', { text: 'Resume queue' })]);
    this.pendingFoot = el('div', { class: 'pending-foot', hidden: true }, [this.resumeButton]);
    this.pendingPanel.append(this.pendingHead, this.pendingNotice, this.pendingBody, this.pendingFoot);
    this.statusEl = el('span', { class: 'status' });
    this.contextPct = el('span', { class: 'context-pct' });
    this.contextUsage = el('span', { class: 'context-usage' });
    this.contextFill = el('span', { class: 'context-fill' });
    this.contextTrigger = el('summary', { class: 'context-trigger', 'aria-label': 'Context usage' }, [
      el('span', { class: 'context-dot', 'aria-hidden': 'true' }),
      el('span', { text: 'Context' }),
      this.contextPct,
    ]);
    this.contextDetails = el('details', { class: 'context', hidden: true }, [
      this.contextTrigger,
      el('div', { class: 'context-popover' }, [
        el('div', { class: 'context-head' }, [
          el('span', { text: 'Context usage' }),
          el('button', {
            class: 'context-close', type: 'button', 'aria-label': 'Close context usage',
            onclick: () => this._closeContext(),
          }, [icon('x', 13)]),
        ]),
        this.contextUsage,
        el('div', { class: 'context-track' }, [this.contextFill]),
        el('div', { class: 'context-caption', text: 'Estimated tokens in this conversation' }),
      ]),
    ]);
    this.root.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') this._closeContext();
    });
    this.shortcutEl = el('span', { class: 'shortcut', text: 'Enter to send · Shift+Enter for new line' });
    this.root.append(
      this.pendingPanel,
      this.input,
      el('div', { class: 'footer' }, [
        el('div', { class: 'footer-left' }, [
          this.contextDetails,
          this.statusEl,
        ]),
        el('div', { class: 'footer-right' }, [
          this.shortcutEl,
          this.steerButton,
          this.stopButton,
          this.button,
        ]),
      ]),
    );
    this._syncButton();
    this._syncPending();
  }

  _autosize() {
    this.input.style.height = 'auto';
    this.input.style.height = Math.min(this.input.scrollHeight, 220) + 'px';
  }

  // Closing the popover must not strand focus: the close control hides
  // with it, so the summary — the only surviving focusable — inherits
  // it. A close while focus lives elsewhere (or the popover was never
  // open) leaves focus alone.
  _closeContext() {
    if (!this.contextDetails.open) return;
    const inside = this.contextDetails.contains(deepActiveElement());
    this.contextDetails.open = false;
    if (inside) this.contextTrigger.focus();
  }

  // Enter submits: the shell routes it — a dispatchable view sends,
  // anything else captures the line into the local queue. Queueing is
  // legitimate while busy or briefly offline, so queue mode never
  // blocks submission here; only a free send needs the socket open.
  _send() {
    const content = this.input.value.trim();
    if (!content || !this._canSend) return;
    if (!this._queueMode && this._connection !== 'open') return;
    emit(this, 'turn-send', { content });
  }

  _steer() {
    const content = this.input.value.trim();
    if (!content || !this._canSend || !this._canSteer || this._connection !== 'open') return;
    emit(this, 'turn-steer', { content });
  }

  // One row per pending entry. Actions reflect what the entry's state
  // honestly allows — foreign rooms navigate instead of mutating this
  // draft, unknown delivery is an explicit Copy/Dismiss decision, and
  // recall stays disabled while the socket can't carry it.
  _pendingRow(item) {
    const row = el('div', { class: 'pending-item', role: 'listitem', 'data-id': item.id });
    row.append(
      el('div', { class: 'pending-head' }, [
        el('span', { class: `pending-state pending-${item.state}`, text: STATE_LABELS[item.state] || item.state }),
        el('span', { class: 'pending-room', text: item.roomLabel || '' }),
      ]),
      el('div', { class: 'pending-text', text: item.text }),
    );
    const actions = el('div', { class: 'pending-actions' });
    const action = (label, event, { aria = label, disabled = false, title = '', focusInput = true } = {}) =>
      el('button', {
        class: 'pending-action', type: 'button',
        'aria-label': aria, disabled, title,
        onclick: () => {
          // The emit below synchronously rebuilds this row — capture
          // its position first so the post-action focus lands on the
          // successor, not a detached index.
          const idx = [...this.pendingBody.querySelectorAll('.pending-item')].indexOf(row);
          emit(this, event, { id: item.id });
          // Deliberate focus: restored text lands in the input, a
          // removed row hands focus to the next action or the input.
          if (focusInput) this.input.focus();
          else this._focusNextAction(idx);
        },
        'data-action': event,
      }, [el('span', { text: label })]);
    if (item.foreign) {
      actions.append(action('Open', 'queue-open', {
        aria: `Open ${item.roomLabel || 'owning room'}`,
      }));
      // In-flight receipts stay undismissable; everything else can be
      // let go — the room may not even exist to Open into.
      if (item.state === 'unknown' || item.state === 'queued') {
        actions.append(action('Dismiss', 'queue-dismiss', { aria: 'Dismiss entry' }));
      }
    } else if (item.state === 'queued') {
      actions.append(
        action('Restore', 'queue-restore', { aria: 'Restore queued text to draft' }),
        action('Remove', 'queue-remove', { aria: 'Remove queued text', focusInput: false }),
      );
    } else if (item.state === 'steering' || item.state === 'submitting') {
      actions.append(action('Restore', 'queue-restore', {
        aria: 'Recall steering input',
        title: item.online
          ? 'Withdraw while still pending'
          : 'Recall needs the socket — reconnect to withdraw',
        disabled: !item.online,
      }));
    } else if (item.state === 'unknown') {
      actions.append(
        action('Check history', 'queue-check', { aria: 'Check room history' }),
        action('Copy to draft', 'queue-copy', { aria: 'Copy text to draft' }),
        action('Dismiss', 'queue-dismiss', { aria: 'Dismiss unknown entry' }),
      );
    }
    if (actions.childElementCount) row.append(actions);
    return row;
  }

  // Focus the action of whichever row now sits where the removed one
  // was — the successor when there is one, else the new last row.
  _focusNextAction(idx) {
    const rows = [...this.pendingBody.querySelectorAll('.pending-item')];
    const next = rows[Math.min(idx, rows.length - 1)];
    const btn = next && next.querySelector('.pending-action');
    (btn || this.input).focus();
  }

  // Cheap controls only — the pending panel is maintained separately
  // in _syncPending so every keystroke doesn't rebuild its rows.
  _syncButton() {
    if (!this.button) return;
    const empty = !this.input.value.trim();
    this.button.replaceChildren();
    // The control is icon-only in every state. Queue mode changes the
    // accessible name and when Enter captures locally; it does not add
    // a caption.
    this.button.className = 'send';
    this.button.append(icon('arrow-up', 16));
    if (this._queueMode) {
      this.button.setAttribute('aria-label', 'Queue message');
      this.button.disabled = !this._canSend || empty;
    } else {
      this.button.setAttribute('aria-label', 'Send message');
      this.button.disabled = !this._canSend || this._connection !== 'open' || empty;
    }
    this.button.onclick = () => this._send();
    // Steer exists only as an armed exact-turn action — busy in a
    // foreign room hides it entirely, never a disabled global.
    const steerVisible = this._busy && this._canSteer;
    this.steerButton.hidden = !steerVisible;
    this.steerButton.disabled = this._connection !== 'open' || empty;
    this.steerButton.replaceChildren(
      icon('chevron', 12),
      el('span', { text: 'Steer' }),
    );
    this.stopButton.hidden = !this._canStop;
    const reason = !this._canSend ? 'Select a model to send' : '';
    this.statusEl.textContent = reason;
    this.statusEl.hidden = !reason;
    this.shortcutEl.textContent = this._queueMode
      ? 'Enter to queue · Shift+Enter for new line'
      : 'Enter to send · Shift+Enter for new line';
    const c = this._context;
    // Stats can vanish while the popover is open. Hiding the details
    // hides the summary with them — focusing that trigger leaves the
    // keyboard on a control that is no longer in the tab order. The
    // composer input is the control that remains.
    const contextHides = !c?.total && this.contextDetails.contains(this.shadowRoot.activeElement);
    this.contextDetails.hidden = !c?.total;
    if (c?.total) {
      const pct = Math.max(0, Math.min(100, Math.round((c.estimate || 0) / c.total * 100)));
      this.contextPct.textContent = `${pct}%`;
      this.contextUsage.textContent = `~${fmtTok(c.estimate)} / ${fmtTok(c.total)} tokens · ${pct}% used`;
      this.contextFill.style.width = `${pct}%`;
    } else {
      this.contextDetails.open = false;
      if (contextHides) this.input.focus();
    }
  }

  // The pending panel syncs on queue state only. Rows are keyed by
  // entry id and reused while their content is unchanged, so focus and
  // scroll position survive context updates, typing, and unrelated
  // property writes — no DOM churn on long content.
  _syncPending() {
    if (!this.pendingPanel) return;
    const items = this._pendingItems || [];
    if (!items.length) {
      // Hiding under a focused control drops focus to BODY — the check
      // covers the whole panel (rows AND the Resume footer), not just
      // the body rows, and must run before the hide while the control
      // still reports as the active element.
      if (this.pendingPanel.contains(this.shadowRoot.activeElement)) this.input.focus();
      this.pendingPanel.hidden = true;
      this._rowCache.clear();
      this.pendingBody.replaceChildren();
      return;
    }
    this.pendingPanel.hidden = false;
    this.pendingHead.textContent = this._queuePaused
      ? `Pending (${items.length}) · paused`
      : `Pending (${items.length})`;
    this.pendingNotice.hidden = !this._queueNotice;
    this.pendingNotice.textContent = this._queueNotice;
    // Focus preservation: a row REPLACED by a state change hands focus
    // to the same action in its successor. Identity is the entry id —
    // a preceding row disappearing shifts every index, so the old
    // index would land focus on a different entry's action. A removed
    // entry still falls back to the row now in its place, then the
    // input. Captured before reconcile and applied only when focus
    // actually lived inside the panel.
    const focused = this.shadowRoot.activeElement;
    let keepFocus = null;
    if (focused && this.pendingBody.contains(focused)) {
      const rows = [...this.pendingBody.children];
      const row = rows.find((r) => r.contains(focused));
      if (row) {
        keepFocus = {
          id: row.getAttribute('data-id'),
          action: focused.getAttribute('data-action'),
          idx: rows.indexOf(row),
        };
      }
    }
    const scroll = this.pendingBody.scrollTop;
    const seen = new Set();
    let i = 0;
    for (const item of items) {
      seen.add(item.id);
      const sig = rowSig(item);
      let cached = this._rowCache.get(item.id);
      if (!cached || cached.sig !== sig) {
        const node = this._pendingRow(item);
        // Rows are this body's direct children by construction —
        // insert-then-remove is the in-place swap on every DOM the
        // widget runs under.
        if (cached) {
          this.pendingBody.insertBefore(node, cached.node);
          this.pendingBody.removeChild(cached.node);
        } else this.pendingBody.append(node);
        cached = { node, sig };
        this._rowCache.set(item.id, cached);
      }
      // Ordered reconcile — a reordered queue keeps nodes but fixes
      // their position without rebuilding them.
      if (this.pendingBody.children[i] !== cached.node) {
        this.pendingBody.insertBefore(cached.node, this.pendingBody.children[i] || null);
      }
      i++;
    }
    for (const [id, cached] of this._rowCache) {
      if (!seen.has(id)) {
        // Rows are this body's direct children by construction —
        // removeChild is exact here and answers on every DOM the
        // widget runs under.
        this.pendingBody.removeChild(cached.node);
        this._rowCache.delete(id);
      }
    }
    this.pendingBody.scrollTop = scroll;
    if (keepFocus) {
      // The entry's own row — reused or rebuilt — wins; only a removed
      // entry degrades to the successor in its old place.
      const row = (keepFocus.id && findByFocusKey(this.pendingBody, keepFocus.id))
        || this.pendingBody.children[keepFocus.idx]
        || this.pendingBody.lastElementChild;
      const btn = row && (keepFocus.action
        ? actionIn(row, keepFocus.action)
        : null) || (row && actionIn(row, null));
      // A successor that cannot take focus (the rebuilt action disabled
      // while offline) is no successor — the input receives the
      // keyboard instead of the detached button. The disabled state is
      // read from the property or the attribute: the headless
      // stand-ins reflect the attribute as a string, which is falsy as
      // a property.
      const off = !!btn && (btn.disabled === true || btn.getAttribute?.('disabled') != null);
      if (btn && !off) btn.focus();
      else this.input.focus();
    }
    // Resume shows only while paused WITH this view's lines to send;
    // eligibility to click stays on canResume — required/failed
    // barriers are exactly what Resume retries. The footer can hide
    // under its own focus while rows remain — the input inherits it,
    // same rule as the whole panel closing.
    const footHides = !this._resumeVisible && this.pendingFoot.contains(this.shadowRoot.activeElement);
    // Disabling a focused control drops the browser's focus to BODY —
    // capture the focus before the barrier flip, and hand it to the
    // input below when the control stays visible but ineligible.
    const resumeFocused = this.pendingFoot.contains(this.shadowRoot.activeElement);
    this.pendingFoot.hidden = !this._resumeVisible;
    if (footHides) this.input.focus();
    this.resumeButton.disabled = !this._canResume;
    if (this.resumeButton.disabled && resumeFocused) this.input.focus();
    this.resumeButton.title = this._canResume
      ? 'Send the next queued message'
      : 'Nothing ready to send — busy, offline, or checking history';
  }
}

customElements.define('nusa-chat-composer', ChatComposer);
