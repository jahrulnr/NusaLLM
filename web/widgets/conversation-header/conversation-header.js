import { Widget, el, emit, clear } from '../../lib/dom.js';
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
    this._confirmingDelete = false;
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

  set conversation(c) { this._conversation = c; this._renaming = false; this._confirmingDelete = false; this._render(); }
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
    clear(this.root);
    this.root.classList.add('header');
    const conv = this._conversation;

    this.root.append(el('button', {
      class: 'icon-btn sidebar-toggle', type: 'button', 'aria-label': 'Toggle sidebar',
      onclick: () => emit(this, 'sidebar-toggle'),
    }, [icon('menu', 15)]));

    // Title — click to rename inline; no prompt().
    if (this._renaming && conv) {
      const input = el('input', {
        class: 'rename-input', type: 'text', value: conv.title || '',
        'aria-label': 'Rename conversation',
      });
      // One-shot: Enter's re-render can still fire blur; Escape must
      // never commit. `settled` gates the first terminal action only.
      let settled = false;
      const commit = () => {
        if (settled) return;
        settled = true;
        const title = input.value.trim();
        this._renaming = false;
        if (title && title !== conv.title) emit(this, 'conversation-rename', { title });
        this._render();
      };
      const cancel = () => {
        if (settled) return;
        settled = true;
        this._renaming = false;
        this._render();
      };
      input.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') commit();
        if (e.key === 'Escape') cancel();
      });
      input.addEventListener('blur', commit);
      this.root.append(input);
      setTimeout(() => input.focus(), 0);
    } else {
      this.root.append(el('button', {
        class: 'title', type: 'button',
        'aria-label': conv ? `Rename ${conv.title || 'conversation'}` : 'No conversation',
        onclick: () => { if (conv) { this._renaming = true; this._render(); } },
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
          onclick: () => emit(this, 'conversation-delete'),
        }, [el('span', { text: 'Delete' })]),
        el('button', {
          class: 'confirm-no', type: 'button',
          onclick: () => { this._confirmingDelete = false; this._render(); },
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
          onclick: () => { this._confirmingDelete = true; this._render(); },
        }, [icon('trash', 13)]),
      );
    }

    this.root.append(el('button', {
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
            onclick: () => { this._closeSheet(); emit(this, 'conversation-delete'); },
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
  }

  _sheetActions() {
    const conv = this._conversation;
    const action = (label, iconName, onclick, { danger = false, disabled = false } = {}) => el('button', {
      class: `sheet-action${danger ? ' danger' : ''}`, type: 'button', disabled: disabled || null, onclick,
    }, [icon(iconName, 18), el('span', { text: label })]);
    const items = [];
    if (conv) {
      items.push(action('Rename thread', 'pencil', () => {
        // Rename runs after the sheet closes, so the input gets focus.
        this._closeSheet();
        this._renaming = true;
        this._render();
      }));
      items.push(action(this._compactLabel('Compact thread'), 'compact', () => {
        this._closeSheet();
        emit(this, 'conversation-compact');
      }, { disabled: !!(this._compactState || !this._compactAvailable) }));
    }
    items.push(action('Settings', 'gear', () => {
      this._closeSheet();
      emit(this, 'settings-open');
    }));
    if (conv) {
      items.push(action('Delete thread', 'trash', () => {
        this._confirmingDelete = true;
        this._renderSheet();
        // The confirm opens on Keep, the safe choice.
        this.sheet.querySelector('.sheet-no').focus();
      }, { danger: true }));
    }
    return items;
  }
}

customElements.define('nusa-conversation-header', ConversationHeader);
