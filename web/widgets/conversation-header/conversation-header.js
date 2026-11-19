import { Widget, el, emit, clear, deepActiveElement, focusKeyOf, findByFocusKey, nodeWithin } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

// Thread chrome: title with inline rename, the model and workspace
// pickers, compaction, and delete confirmation. On desktop these sit in
// one row. On phones the row keeps the drawer toggle, the title, and a
// thread-actions button, and the secondary actions move into a bottom
// sheet. The layout switch is CSS-only, so both layouts share one DOM.
// It emits intents only — the shell performs the API calls.
class ConversationHeader extends Widget {
  static stylesheet = new URL('./conversation-header.css', import.meta.url);

  constructor() {
    super();
    this._conversation = null;
    this._gateways = [];
    this._selection = null;
    this._connection = 'closed';
    this._compactState = null;
    this._compactAvailable = false;
    this._renaming = false;
    this._renameDraft = null;
    this._confirmingDelete = false;
    this._deletePending = false;
    // The control a render could not refocus yet — it matched but sat
    // disabled (a compact in flight). Held across renders until the
    // control re-enables or vanishes.
    this._pendingFocus = null;
    // Beside .header rather than inside it, so a header re-render never
    // detaches an open modal.
    this.sheet = el('dialog', { class: 'sheet', 'aria-label': 'Thread actions' });
    this.sheet.addEventListener('click', (e) => { if (e.target === this.sheet) this._closeSheet(); });
    // close() fires this for X, the backdrop, and Escape. sheet.open is
    // already false here, so the render below must not rebuild or close
    // the dialog again. Clearing the flag without rendering would leave
    // a desktop confirm row that phones hide until the viewport widens.
    this.sheet.addEventListener('close', () => {
      this._confirmingDelete = false;
      this._render();
    });
    this.shadowRoot.append(this.sheet);
    // A modal sheet must not survive the switch to the desktop layout:
    // its trigger is gone there, and the page would stay inert behind it.
    this._desktopQuery = matchMedia('(min-width: 761px)');
    this._desktopQuery.addEventListener('change', (e) => { if (e.matches) this._closeSheet(); });
    this._render();
  }

  // Focus targets for the shell's overlay transitions: the settings page
  // and the phone drawer take focus on open and hand it back here on
  // close. The refs are rebuilt by every _render, so read them fresh.
  focusSettings() { this._settingsBtn?.focus?.(); }
  focusSidebarToggle() { this._toggleBtn?.focus?.(); }

  // A same-room metadata refresh must not steal in-progress UI — the
  // rename draft and the delete confirm survive; only a different room
  // resets them.
  set conversation(c) {
    const sameRoom = !!c && !!this._conversation && c.id === this._conversation.id;
    this._conversation = c;
    if (!sameRoom) {
      this._renaming = false;
      this._renameDraft = null;
      this._confirmingDelete = false;
      this._pendingFocus = null;
      this._deletePending = false;
    }
    this._render();
  }
  // The shell clears this when the delete it started settles. A failed
  // delete keeps the same room, so the flag — not a room change — is
  // what re-enables the trigger. Setting the same value must not
  // rebuild the header. Overlapping deletes share this one flag; the
  // shell, not a room change, decides which completion may clear it.
  set deletePending(v) {
    const next = !!v;
    if (next === this._deletePending) return;
    this._deletePending = next;
    this._render();
  }
  get deletePending() { return this._deletePending; }
  // gateways/selection land mid-pick — forwarding to the live picker
  // keeps its open menu alive; a full re-render would destroy it.
  set gateways(list) {
    this._gateways = list || [];
    if (this.picker) this.picker.gateways = this._gateways;
    else this._render();
  }
  set selection(sel) {
    this._selection = sel;
    if (this.picker) this.picker.selection = sel;
    if (this.wsPicker) this.wsPicker.selection = sel;
    if (!this.picker && !this.wsPicker) this._render();
  }
  set connection(state) { this._connection = state; this._render(); }
  set compactState(state) { this._compactState = state; this._render(); }
  set compactAvailable(v) { this._compactAvailable = !!v; this._render(); }
  // Visible update (see lib/update-state.js visibleUpdate) or null. The pill
  // only exists while there is something to read.
  set update(info) { this._update = info || null; this._render(); }

  _render() {
    // A rebuild mid-rename would commit whatever the input holds —
    // carry the live draft forward so a same-room refresh loses nothing.
    if (this._renaming) {
      const cur = this.root.querySelector('.rename-input');
      if (cur) this._renameDraft = cur.value;
    }
    // A rebuild under the user's focus must not drop it to BODY: the
    // fresh control matching the old one's identity inherits it. A new
    // in-root focus supersedes any queued restore — the user moved on.
    const prev = deepActiveElement();
    let focusKey = prev && nodeWithin(this.root, prev) ? focusKeyOf(prev) : null;
    if (focusKey) this._pendingFocus = null;
    else if (this._pendingFocus) focusKey = this._pendingFocus;
    clear(this.root);
    this.root.classList.add('header');
    const conv = this._conversation;

    this.root.append(this._toggleBtn = el('button', {
      class: 'icon-btn sidebar-toggle', type: 'button', 'aria-label': 'Toggle sidebar',
      onclick: () => emit(this, 'sidebar-toggle'),
    }, [icon('menu', 15)]));

    // Title — click to rename inline; no prompt().
    if (this._renaming && conv) {
      const input = el('input', {
        class: 'rename-input', type: 'text', value: this._renameDraft ?? (conv.title || ''),
        'aria-label': 'Rename conversation',
      });
      // One-shot: Enter's re-render can still fire blur; Escape must
      // never commit. `settled` gates the first terminal action only.
      let settled = false;
      // The input's successor on settle is the title control — but only
      // when the keyboard settled the edit. A blur means focus already
      // moved on; grabbing it back would steal the click's target.
      const commit = (refocus) => {
        if (settled) return;
        settled = true;
        const title = input.value.trim();
        this._renaming = false;
        this._renameDraft = null;
        if (title && title !== conv.title) emit(this, 'conversation-rename', { title });
        this._render();
        if (refocus) this._titleBtn?.focus?.();
      };
      const cancel = () => {
        if (settled) return;
        settled = true;
        this._renaming = false;
        this._renameDraft = null;
        this._render();
        this._titleBtn?.focus?.();
      };
      input.addEventListener('keydown', (e) => {
        // An open IME composition owns Enter/Escape until it commits —
        // acting on either mid-composition submits a half-written title
        // or kills the editor the candidate list needs.
        if (e.isComposing) return;
        // Enter must be cancelled, not just handled: the commit moves
        // focus to the title button, and an uncancelled Enter goes on to
        // activate whatever has focus — reopening the editor it just
        // closed (the edit textarea does the same).
        if (e.key === 'Enter') { e.preventDefault(); commit(true); }
        if (e.key === 'Escape') cancel();
      });
      input.addEventListener('blur', () => commit(false));
      this.root.append(input);
      setTimeout(() => input.focus(), 0);
    } else {
      this.root.append(this._titleBtn = el('button', {
        class: 'title', type: 'button',
        'aria-label': conv ? `Rename ${conv.title || 'conversation'}` : 'No conversation',
        onclick: () => { if (conv) { this._renaming = true; this._renameDraft = null; this._render(); } },
      }, [
        el('span', { text: conv ? (conv.title || 'Untitled') : 'NusaLLM' }),
        conv ? icon('pencil', 11) : null,
      ]));
    }

    this.root.append(el('div', { class: 'spacer' }));

    // Reuse the picker element — re-append moves the node, preserving its
    // shadow state (open menu, draft step) across header re-renders; the
    // picker reinstalls its document dismiss listener on reconnect.
    if (!this.picker) this.picker = el('nusa-model-picker');
    this.picker.gateways = this._gateways;
    this.picker.selection = this._selection;

    // Workspace is a separate room-level boundary — its own picker, not a
    // model-picker layer. Same reuse: re-append preserves popup state.
    if (!this.wsPicker) this.wsPicker = el('nusa-workspace-picker');
    this.wsPicker.selection = this._selection;

    // One row for both pickers: a flex row on phones, transparent on desktop.
    this.root.append(el('div', { class: 'picks' }, [this.picker, this.wsPicker]));

    // Phones give the pill its own grid column; the class keeps the layout
    // unchanged while no update is showing.
    this.root.classList.toggle('has-update', !!this._update);
    if (this._update) this.root.append(this._updatePill());

    if (conv && this._confirmingDelete) {
      this.root.append(el('div', { class: 'confirm', role: 'alertdialog', 'aria-label': 'Confirm delete' }, [
        el('span', { class: 'confirm-text', text: 'Delete this thread?' }),
        el('button', {
          class: 'confirm-yes', type: 'button',
          onclick: () => this._requestDelete(false),
        }, [el('span', { text: 'Delete' })]),
        this._confirmNo = el('button', {
          class: 'confirm-no', type: 'button',
          onclick: () => {
            this._confirmingDelete = false;
            this._render();
            // Keep resolves the confirm under its own focus — the
            // Delete trigger it replaced inherits it, same hand-off
            // the phone sheet makes.
            findByFocusKey(this.root, 'icon-btn danger')?.focus?.();
          },
        }, [el('span', { text: 'Keep' })]),
      ]));
    } else if (conv) {
      const compactDisabled = this._compactState || !this._compactAvailable;
      this.root.append(
        el('button', {
          class: 'compact-btn', type: 'button', 'aria-label': 'Compact conversation',
          disabled: compactDisabled,
          onclick: () => emit(this, 'conversation-compact'),
        }, [el('span', { text: this._compactLabel('Compact') })]),
        el('button', {
          class: 'icon-btn danger', type: 'button', 'aria-label': 'Delete conversation',
          disabled: this._deletePending || null,
          onclick: () => {
            if (this._deletePending) return;
            this._confirmingDelete = true;
            this._render();
            // The destructive choice is never the default: Keep owns
            // focus, the same rule the phone sheet follows.
            this._confirmNo?.focus?.();
          },
        }, [icon('trash', 13)]),
      );
    }

    this.root.append(this._settingsBtn = el('button', {
      class: 'icon-btn settings-btn', type: 'button', 'aria-label': 'Open settings',
      onclick: () => emit(this, 'settings-open'),
    }, [icon('gear', 15)]));
    // Phones only (CSS). Always present, so Settings stays reachable
    // before a thread exists.
    this.root.append(el('button', {
      class: 'icon-btn more-btn', type: 'button', 'aria-label': 'Thread actions', 'aria-haspopup': 'dialog',
      onclick: () => this._openSheet(),
    }, [icon('more', 18)]));
    if (this.sheet.open) this._renderSheet();
    if (focusKey) {
      const hit = findByFocusKey(this.root, focusKey);
      // A match that sits disabled (a compact in flight) cannot hold
      // focus — keep the key queued so the re-enable render lands it.
      if (hit && !hit.disabled) {
        hit.focus?.();
        this._pendingFocus = null;
      } else if (hit) {
        this._pendingFocus = focusKey;
      } else {
        // The focused control died with the room — Delete, Compact,
        // and the confirm row all leave with it. The keyboard must
        // not stay on the detached node: Settings renders in every
        // state, so it is the surviving successor.
        this._pendingFocus = null;
        this._settingsBtn?.focus?.();
      }
    }
  }

  _updatePill() {
    const latest = this._update.latest;
    return el('button', {
      class: 'update-pill', type: 'button',
      'aria-label': `Update to NusaLLM ${latest} available. Review update.`,
      onclick: () => emit(this, 'update-open'),
    }, [
      el('span', { class: 'update-dot', 'aria-hidden': 'true' }),
      el('span', { text: `Update ${latest}` }),
    ]);
  }

  _compactLabel(idle) {
    if (this._compactState === 'running') return 'Compacting…';
    if (this._compactState === 'done') return 'Compacted';
    return idle;
  }

  _openSheet() {
    if (this.sheet.open) return;
    this._confirmingDelete = false;
    // Sheet is still closed, so this rebuilds the header only — a confirm
    // row painted before the sheet opened has to leave with the flag.
    this._render();
    this._renderSheet();
    this.sheet.showModal();
    this.sheet.querySelector('.sheet-action, .sheet-no')?.focus();
  }

  _closeSheet() {
    if (this.sheet.open) this.sheet.close();
  }

  _renderSheet() {
    // The sheet lives outside .header, so the header's focus restore
    // never sees an action focused here. A same-room refresh rebuilds
    // the sheet and would otherwise leave focus on the detached button.
    const prev = deepActiveElement();
    const focusKey = nodeWithin(this.sheet, prev) ? focusKeyOf(prev) : null;
    const conv = this._conversation;
    const head = el('div', { class: 'sheet-head' }, [
      el('span', { class: 'sheet-title', text: conv ? (conv.title || 'Untitled') : 'NusaLLM' }),
      el('button', {
        class: 'sheet-close', type: 'button', 'aria-label': 'Close',
        onclick: () => this._closeSheet(),
      }, [icon('x', 18)]),
    ]);
    // Keep is first: the destructive action is never the default.
    const body = this._confirmingDelete && conv
      ? el('div', { class: 'sheet-confirm' }, [
        el('span', { class: 'sheet-confirm-text', text: 'Delete this thread?' }),
        el('div', { class: 'sheet-confirm-row' }, [
          el('button', {
            class: 'sheet-no', type: 'button',
            onclick: () => {
              this._confirmingDelete = false;
              // The header may already show .confirm from a render that
              // ran while this flag was true. Rebuilding the sheet alone
              // would leave that row in the shared DOM.
              this._render();
              // Back to the action that opened the confirm, so focus never
              // drops to the body inside the modal.
              this.sheet.querySelector('.sheet-action.danger').focus();
            },
          }, [el('span', { text: 'Keep' })]),
          el('button', {
            class: 'sheet-yes', type: 'button',
            onclick: () => this._requestDelete(true),
          }, [el('span', { text: 'Delete' })]),
        ]),
      ])
      : el('div', { class: 'sheet-list' }, this._sheetActions());
    clear(this.sheet);
    this.sheet.append(el('div', { class: 'sheet-panel' }, [
      el('span', { class: 'sheet-grip', 'aria-hidden': 'true' }),
      head,
      body,
    ]));
    if (!focusKey) return;
    const hit = findByFocusKey(this.sheet, focusKey);
    if (hit && !hit.disabled) hit.focus?.();
    else {
      // The focused action died with the room (or rebuilt disabled) —
      // leaving the keyboard on the detached node strands focus
      // behind the modal. The first surviving action carries it;
      // Settings renders in every state, and the confirm row's Keep
      // answers before it.
      this.sheet.querySelector('.sheet-action, .sheet-no')?.focus?.();
    }
  }

  // One confirmation, one delete. The confirm row dies immediately so a
  // second click cannot emit again, and the trigger stays disabled until
  // the shell reports the request settled. Settings is the control that
  // remains once the confirm button is gone.
  _requestDelete(fromSheet) {
    if (this._deletePending || !this._conversation) return;
    this._deletePending = true;
    this._confirmingDelete = false;
    if (fromSheet) this._closeSheet();
    else this._render();
    this._settingsBtn?.focus?.();
    emit(this, 'conversation-delete');
  }

  _sheetActions() {
    const conv = this._conversation;
    const action = (id, label, iconName, onclick, { danger = false, disabled = false } = {}) => el('button', {
      class: `sheet-action${danger ? ' danger' : ''}`, type: 'button', 'data-id': id,
      disabled: disabled || null, onclick,
    }, [icon(iconName, 18), el('span', { text: label })]);
    const items = [];
    if (conv) {
      items.push(action('rename', 'Rename thread', 'pencil', () => {
        // Rename runs after the sheet closes, so the input gets focus.
        this._closeSheet();
        this._renaming = true;
        this._renameDraft = null;
        this._render();
      }));
      items.push(action('compact', this._compactLabel('Compact thread'), 'compact', () => {
        this._closeSheet();
        emit(this, 'conversation-compact');
      }, { disabled: !!(this._compactState || !this._compactAvailable) }));
    }
    items.push(action('settings', 'Settings', 'gear', () => {
      this._closeSheet();
      emit(this, 'settings-open');
    }));
    if (conv) {
      items.push(action('delete', 'Delete thread', 'trash', () => {
        if (this._deletePending) return;
        this._confirmingDelete = true;
        this._renderSheet();
        // The confirm opens on Keep, the safe choice.
        this.sheet.querySelector('.sheet-no').focus();
      }, { danger: true, disabled: this._deletePending }));
    }
    return items;
  }
}

customElements.define('nusa-conversation-header', ConversationHeader);
