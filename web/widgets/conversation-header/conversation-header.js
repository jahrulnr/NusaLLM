import { Widget, el, emit, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

// Thread chrome: title with inline rename, model picker, compaction,
// in-row delete confirmation, and the sidebar/settings actions. It emits
// intents only — the shell performs the API calls.
class ConversationHeader extends Widget {
  static stylesheet = new URL('./conversation-header.css', import.meta.url);

  constructor() {
    super();
    this._conversation = null;
    this._gateways = [];
    this._selection = null;
    this._connection = 'closed';
    this._renaming = false;
    this._confirmingDelete = false;
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
    this.root.append(this.picker);

    // Workspace is a separate room-level boundary — its own picker, not a
    // model-picker layer. Same reuse: re-append preserves popup state.
    if (!this.wsPicker) this.wsPicker = el('nusa-workspace-picker');
    this.wsPicker.selection = this._selection;
    this.root.append(this.wsPicker);

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
      this.root.append(
        el('button', {
          class: 'icon-btn', type: 'button', 'aria-label': 'Compact conversation',
          onclick: () => emit(this, 'conversation-compact'),
        }, [icon('refresh', 13)]),
        el('button', {
          class: 'icon-btn danger', type: 'button', 'aria-label': 'Delete conversation',
          onclick: () => { this._confirmingDelete = true; this._render(); },
        }, [icon('trash', 13)]),
      );
    }

    this.root.append(el('button', {
      class: 'icon-btn', type: 'button', 'aria-label': 'Open settings',
      onclick: () => emit(this, 'settings-open'),
    }, [icon('gear', 15)]));
  }
}

customElements.define('nusa-conversation-header', ConversationHeader);
