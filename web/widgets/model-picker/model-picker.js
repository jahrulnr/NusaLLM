import { Widget, el, emit, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';
import { APIClient } from '../../lib/api-client.js';

const EFFORTS = ['default', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];
// On phones the menu is a bottom sheet instead of a popover.
const SHEET = '(max-width: 760px)';

// Layered model picker: search → model (grouped by gateway) → effort →
// upstream route (aggregator providers, or Auto). Each completed layer
// emits model-change immediately — a model pick must apply even if the
// menu closes before the optional refinement layers finish. The search
// step's manage row is an exit, not a layer — it emits manage-models
// for the gateway in context and never touches the draft. Workspace is
// a separate room-level boundary — nusa-workspace-picker owns it. The
// document-level dismiss listener is tracked by exact identity — every
// render/open/close detaches it before any new install, so no capture
// listeners accumulate.
class ModelPicker extends Widget {
  static stylesheet = new URL('./model-picker.css', import.meta.url);

  constructor() {
    super();
    this.api = new APIClient();
    this._gateways = [];
    this._selection = null;
    this._open = false;
    this._dismiss = null;
    this._step = 'search';
    this._query = '';
    this._draft = null;       // {route, modelId, effort, provider} in progress
    this._providers = null;   // upstream choices for the draft route/model
    this._providersFailed = false;
    this._gen = 0;            // stale-response token for the provider fetch
    this._render();
  }

  set gateways(list) { this._gateways = list || []; this._render(); }
  set selection(sel) { this._selection = sel; this._render(); }

  _label() {
    const s = this._selection;
    if (!s) return 'Select model';
    let text = s.route ? `${s.route} · ${s.modelId}` : s.modelId;
    if (s.effort && s.effort !== 'default') text += ` · ${s.effort}`;
    if (s.provider) text += ` › ${s.provider}`;
    return text;
  }

  _detachDismiss() {
    if (this._dismiss) {
      document.removeEventListener('pointerdown', this._dismiss, true);
      this._dismiss = null;
    }
    if (this._reclamp) {
      window.removeEventListener('resize', this._reclamp);
      this._reclamp = null;
      this._menuEl = null;
    }
  }

  _openMenu() {
    if (this._open) return;
    this._open = true;
    this._step = 'search';
    this._query = '';
    this._draft = null;
    this._render();
    const input = this.root.querySelector('.menu-search');
    if (input) input.focus();
  }

  _closeMenu(refocus = false) {
    if (!this._open) return;
    this._open = false;
    this._draft = null;
    this._render();
    if (refocus && this.trigger) this.trigger.focus();
  }

  // Emit the accumulated draft. Every completed layer calls this so a
  // selection applies the moment it's coherent — abandoning the menu
  // mid-flow never loses a pick.
  _emitDraft() {
    const d = this._draft;
    if (!d || !d.modelId) return;
    emit(this, 'model-change', {
      route: d.route,
      modelId: d.modelId,
      effort: d.effort || '',
      provider: d.provider || '',
    });
  }

  _onMenuKeydown(e) {
    if (e.key === 'Escape') {
      e.preventDefault();
      this._closeMenu(true);
    } else if (e.key === 'Tab') {
      // Tab must move focus normally — never trap it. Close after the
      // focus has moved out.
      setTimeout(() => this._closeMenu(false), 0);
    }
  }

  _stepTitle(text) {
    return el('div', { class: 'menu-group' }, [
      el('button', {
        class: 'menu-back', type: 'button', 'aria-label': 'Back',
        onclick: () => { this._step = this._step === 'route' ? 'effort' : 'search'; this._render(); },
      }, [icon('chevron', 10)]),
      el('span', { text }),
    ]);
  }

  _item(label, meta, onclick, sel = false) {
    return el('button', {
      class: `menu-item${sel ? ' selected' : ''}`, type: 'button', role: 'option',
      'aria-selected': sel ? 'true' : 'false', onclick,
    }, [
      el('span', { class: 'menu-id', text: label }),
      meta ? el('span', { class: 'menu-meta', text: meta }) : null,
    ]);
  }

  _renderSearch(menu) {
    const list = el('div', { class: 'menu-results' });
    const input = el('input', {
      class: 'menu-search', type: 'text', placeholder: 'Search models…',
      'aria-label': 'Search models', value: this._query,
    });
    // Only the result list re-renders per keystroke: the field keeps its
    // focus and caret, and the phone keyboard stays up.
    input.addEventListener('input', () => {
      this._query = input.value;
      clear(list);
      this._renderResults(list);
    });
    input.addEventListener('keydown', (e) => { if (e.key === 'Escape') { e.preventDefault(); this._closeMenu(true); } });
    menu.append(input, list);
    this._renderResults(list);
    // Manage-models navigates to the gateway's settings view — the
    // draft's route while a pick is in progress, else the current
    // selection's; with no gateway in context there is nothing to
    // manage and the row stays out.
    const gatewayId = this._draft?.route || this._selection?.route;
    if (gatewayId) {
      menu.append(el('button', {
        class: 'menu-manage', type: 'button',
        onclick: () => { emit(this, 'manage-models', { gatewayId }); this._closeMenu(); },
      }, [icon('gear', 11), el('span', { text: 'Manage models' })]));
    }
  }

  _renderResults(list) {
    const q = this._query.trim().toLowerCase();
    const usable = (this._gateways || []).filter((g) => !g.broken && (g.models || []).length);
    const groups = usable
      .map((g) => ({
        g,
        models: q ? g.models.filter((m) => m.id.toLowerCase().includes(q) || (m.name || '').toLowerCase().includes(q) || g.id.toLowerCase().includes(q) || (g.name || '').toLowerCase().includes(q)) : g.models,
      }))
      .filter(({ models }) => models.length);
    if (!groups.length) {
      list.append(el('div', { class: 'menu-empty', text: usable.length ? 'No matching models' : 'No gateways configured' }));
      return;
    }
    for (const { g, models } of groups) {
      list.append(el('div', { class: 'menu-group', text: g.name || g.id }));
      for (const m of models) {
        const sel = this._selection && this._selection.modelId === m.id && this._selection.route === g.id;
        list.append(this._item(m.id, m.contextWindow ? `${Math.round(m.contextWindow / 1024)}k` : '', () => {
          // Re-picking the current model keeps its tuning; a different
          // model starts clean so a stale pin can't leak across.
          const same = this._selection && this._selection.modelId === m.id && this._selection.route === g.id;
          this._draft = {
            route: g.id, modelId: m.id,
            effort: same ? this._selection.effort : '',
            provider: same ? this._selection.provider : '',
          };
          this._emitDraft();
          this._step = 'effort';
          this._render();
        }, sel));
      }
    }
  }

  _renderEffort(menu) {
    menu.append(this._stepTitle(this._draft.modelId));
    const current = this._selection && this._selection.route === this._draft.route
      && this._selection.modelId === this._draft.modelId ? this._selection.effort : '';
    // A catalog-declared effort list narrows the choices; an undeclared
    // one keeps the full set — the provider remains the final validator.
    // Declared entries are re-sorted to the canonical order — providers
    // emit them in arbitrary order.
    const model = (this._gateways.find((g) => g.id === this._draft.route)?.models || [])
      .find((m) => m.id === this._draft.modelId);
    const efforts = model?.supportedEfforts?.length
      ? EFFORTS.filter((e) => e === 'default' || model.supportedEfforts.includes(e))
      : EFFORTS;
    for (const e of efforts) {
      menu.append(this._item(e, e === 'default' ? 'provider decides' : '', () => {
        this._draft.effort = e;
        this._emitDraft();
        this._step = 'route';
        this._providers = null;
        this._providersFailed = false;
        this._render();
        const gen = ++this._gen;
        this.api.discoverGatewayProviders(this._draft.route, this._draft.modelId)
          .then((res) => { if (gen === this._gen) { this._providers = (res && res.data) || []; this._render(); } })
          .catch(() => { if (gen === this._gen) { this._providers = []; this._providersFailed = true; this._render(); } });
      }, current === e || (e === 'default' && (!current || !efforts.includes(current)))));
    }
  }

  _renderRoute(menu) {
    menu.append(this._stepTitle('Route'));
    const draft = this._draft;
    const pick = (provider) => {
      draft.provider = provider;
      this._emitDraft();
      this._closeMenu();
    };
    menu.append(this._item('Auto', 'gateway picks the upstream', () => pick(''),
      !!(this._selection && this._selection.route === draft.route
        && this._selection.modelId === draft.modelId && !this._selection.provider)));
    if (this._providers === null) {
      menu.append(el('div', { class: 'menu-empty', text: 'Loading providers…' }));
      return;
    }
    for (const p of this._providers) {
      menu.append(this._item(p.name || p.slug, '', () => pick(p.slug),
        !!(this._selection && this._selection.provider === p.slug)));
    }
    if (this._providersFailed) {
      menu.append(el('div', { class: 'menu-empty', text: 'Upstream list unavailable — Auto still works' }));
    }
  }

  _render() {
    this._detachDismiss(); // any pending outside-tap listener dies here
    clear(this.root);
    this.root.classList.add('picker');

    this.trigger = el('button', {
      class: 'trigger', type: 'button', 'aria-haspopup': 'listbox',
      'aria-expanded': this._open ? 'true' : 'false',
      title: this._label(), // pill truncates on narrow headers — tooltip shows the full pick
      onclick: () => (this._open ? this._closeMenu() : this._openMenu()),
    }, [
      el('span', { class: 'trigger-label', text: this._label() }),
      icon('chevron', 11),
    ]);
    this.trigger.addEventListener('keydown', (e) => {
      if ((e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') && !this._open) {
        e.preventDefault();
        this._openMenu();
      } else if (e.key === 'Escape' && this._open) {
        this._closeMenu(true);
      }
    });
    this.root.append(this.trigger);

    if (!this._open) return;
    // Phones: dims the page and takes the tap that dismisses the sheet.
    // It sits inside this host, so the outside-tap listener ignores it and
    // the tap never reaches the page underneath.
    this.root.append(el('div', { class: 'menu-backdrop', 'aria-hidden': 'true', onclick: () => this._closeMenu() }));
    const menu = el('div', { class: 'menu', role: 'listbox', 'aria-label': 'Models' });
    switch (this._step) {
      case 'effort': this._renderEffort(menu); break;
      case 'route': this._renderRoute(menu); break;
      default: this._renderSearch(menu);
    }
    menu.addEventListener('keydown', (e) => this._onMenuKeydown(e));
    this.root.append(menu);
    this._clampMenu(menu);
    this._menuEl = menu;

    this._installDismiss();
  }

  // Keep the popup inside the viewport even when the header picker
  // sits at either edge — recomputed on each render and on resize. On
  // phones the menu is a full-width bottom sheet and needs no clamp.
  _clampMenu(menu) {
    if (matchMedia(SHEET).matches) { menu.style.left = ''; return; }
    const bounds = this.root.getBoundingClientRect();
    const width = menu.getBoundingClientRect().width;
    menu.style.left = `${Math.max(8 - bounds.left, Math.min(0, innerWidth - 8 - bounds.left - width))}px`;
  }

  _installDismiss() {
    this._detachDismiss();
    const dismiss = (e) => {
      // Nested shadow roots retarget e.target at the outermost host
      // (nusa-app here) — contains() can never pass. The composed path
      // lists the real route through every shadow boundary instead.
      if (!e.composedPath().includes(this)) this._closeMenu();
    };
    this._dismiss = dismiss;
    document.addEventListener('pointerdown', dismiss, true);
    const reclamp = () => { if (this._menuEl) this._clampMenu(this._menuEl); };
    this._reclamp = reclamp;
    window.addEventListener('resize', reclamp);
  }

  // Header re-renders move this node (disconnect+reconnect) — the menu's
  // shadow DOM survives, but the document listener must be reinstalled.
  connectedCallback() {
    if (this._open) this._installDismiss();
  }

  disconnectedCallback() {
    this._detachDismiss();
  }
}

customElements.define('nusa-model-picker', ModelPicker);
