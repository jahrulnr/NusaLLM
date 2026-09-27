import { Widget, el, emit, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

// Route/model selector: groups usable models by gateway, disables
// broken or empty gateways, emits model-change {route, modelId}.
// The document-level dismiss listener is tracked by exact identity —
// every render/open/close detaches it before any new install, so no
// capture listeners accumulate.
class ModelPicker extends Widget {
  static stylesheet = new URL('./model-picker.css', import.meta.url);

  constructor() {
    super();
    this._gateways = [];
    this._selection = null;
    this._open = false;
    this._dismiss = null;
    this._render();
  }

  set gateways(list) { this._gateways = list || []; this._render(); }
  set selection(sel) { this._selection = sel; this._render(); }

  _label() {
    const s = this._selection;
    if (!s) return 'Select model';
    return s.route ? `${s.route} · ${s.modelId}` : s.modelId;
  }

  _detachDismiss() {
    if (this._dismiss) {
      document.removeEventListener('pointerdown', this._dismiss, true);
      this._dismiss = null;
    }
  }

  _openMenu(focus = true) {
    if (this._open) return;
    this._open = true;
    this._render();
    if (!focus) return;
    const target = this.root.querySelector('.menu-item.selected') || this.root.querySelector('.menu-item');
    if (target) target.focus();
  }

  _closeMenu(refocus = false) {
    if (!this._open) return;
    this._open = false;
    this._render();
    if (refocus && this.trigger) this.trigger.focus();
  }

  _onMenuKeydown(e) {
    const items = [...this.root.querySelectorAll('.menu-item')];
    const idx = items.indexOf(this.shadowRoot.activeElement);
    if (e.key === 'Escape') {
      e.preventDefault();
      this._closeMenu(true);
    } else if (e.key === 'Tab') {
      // Tab must move focus normally — never trap it. Close after the
      // focus has moved out.
      setTimeout(() => this._closeMenu(false), 0);
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      const next = items[(idx + 1 + items.length) % items.length];
      if (next) next.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      const prev = items[(idx - 1 + items.length) % items.length];
      if (prev) prev.focus();
    }
  }

  _render() {
    this._detachDismiss(); // any pending outside-tap listener dies here
    clear(this.root);
    this.root.classList.add('picker');
    const usable = (this._gateways || []).filter((g) => !g.broken && (g.models || []).length);

    this.trigger = el('button', {
      class: 'trigger', type: 'button', 'aria-haspopup': 'listbox',
      'aria-expanded': this._open ? 'true' : 'false',
      onclick: () => (this._open ? this._closeMenu() : this._openMenu(false)),
    }, [
      el('span', { class: 'trigger-label', text: this._label() }),
      icon('chevron', 11),
    ]);
    this.trigger.addEventListener('keydown', (e) => {
      if ((e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') && !this._open) {
        e.preventDefault();
        this._openMenu();
      } else if (e.key === 'ArrowDown' && this._open) {
        e.preventDefault();
        const first = this.root.querySelector('.menu-item');
        if (first) first.focus();
      } else if (e.key === 'Escape' && this._open) {
        this._closeMenu(true);
      }
    });
    this.root.append(this.trigger);

    if (!this._open) return;
    const menu = el('div', { class: 'menu', role: 'listbox', 'aria-label': 'Models' });
    if (!usable.length) {
      menu.append(el('div', { class: 'menu-empty', text: 'No gateways configured' }));
    }
    for (const g of usable) {
      menu.append(el('div', { class: 'menu-group', text: g.name || g.id }));
      for (const m of g.models) {
        const sel = this._selection && this._selection.modelId === m.id && this._selection.route === g.id;
        menu.append(el('button', {
          class: `menu-item${sel ? ' selected' : ''}`,
          type: 'button',
          role: 'option',
          'aria-selected': sel ? 'true' : 'false',
          onclick: () => {
            this._closeMenu();
            emit(this, 'model-change', { route: g.id, modelId: m.id });
          },
        }, [
          el('span', { class: 'menu-id', text: m.id }),
          m.contextWindow ? el('span', { class: 'menu-meta', text: `${Math.round(m.contextWindow / 1024)}k` }) : null,
        ]));
      }
    }
    menu.addEventListener('keydown', (e) => this._onMenuKeydown(e));
    this.root.append(menu);

    const dismiss = (e) => {
      // Shadow-internal taps retarget to the host — contains() covers it.
      if (!this.contains(e.target) && e.target !== this) this._closeMenu();
    };
    this._dismiss = dismiss;
    document.addEventListener('pointerdown', dismiss, true);
  }

  disconnectedCallback() {
    this._detachDismiss();
  }
}

customElements.define('nusa-model-picker', ModelPicker);
