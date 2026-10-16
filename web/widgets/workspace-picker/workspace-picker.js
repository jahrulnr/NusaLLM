import { Widget, el, emit, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';
import { APIClient } from '../../lib/api-client.js';

// On phones the menu is a bottom sheet instead of a popover.
const SHEET = '(max-width: 760px)';

// Workspace picker: a popup file-manager fed entirely by /api/fs/browse —
// directories only, home by default, ascendable to the filesystem root.
// Workspace is a room-level boundary (tools and exec run there), separate
// from the model picker's per-turn route/effort selection, so it lives in
// its own control and emits workspace-change. The document-level dismiss
// listener is tracked by exact identity — every render/open/close
// detaches it before any new install, so no capture listeners
// accumulate.
class WorkspacePicker extends Widget {
  static stylesheet = new URL('./workspace-picker.css', import.meta.url);

  constructor() {
    super();
    this.api = new APIClient();
    this._selection = null;
    this._open = false;
    this._dismiss = null;
    this._browse = null;      // {path, parent, roots, entries} | null
    this._browseError = '';
    this._gen = 0;            // stale-response token for browse fetches
    this._render();
  }

  set selection(sel) { this._selection = sel; this._render(); }

  _label() {
    const ws = (this._selection && this._selection.workspace) || '';
    if (!ws) return 'Workspace';
    const tail = ws.replace(/[/\\]+$/, '').split(/[/\\]/).pop();
    return tail || ws;
  }

  _detachDismiss() {
    if (this._dismiss) {
      document.removeEventListener('pointerdown', this._dismiss, true);
      this._dismiss = null;
    }
    if (this._reclamp) {
      window.removeEventListener('resize', this._reclamp);
      this._reclamp = null;
      // Leave _menuEl. A header re-render disconnects this host, and
      // connectedCallback installs a new listener against the open menu.
    }
  }

  _installDismiss() {
    this._detachDismiss();
    const dismiss = (e) => {
      // Nested shadow roots retarget e.target at the outermost host —
      // the composed path lists the real route through every boundary.
      if (!e.composedPath().includes(this)) this._closeMenu();
    };
    this._dismiss = dismiss;
    document.addEventListener('pointerdown', dismiss, true);
    const reclamp = () => { if (this._menuEl) this._clampMenu(this._menuEl); };
    this._reclamp = reclamp;
    window.addEventListener('resize', reclamp);
  }

  // Keep the popup inside the viewport even when the header picker
  // sits at either edge — recomputed on each render and on resize. On
  // phones the menu is a full-width bottom sheet and needs no clamp.
  _clampMenu(menu) {
    if (matchMedia(SHEET).matches) { menu.style.left = ''; return; }
    const bounds = this.getBoundingClientRect();
    const width = menu.getBoundingClientRect().width;
    menu.style.left = `${Math.max(8 - bounds.left, Math.min(0, innerWidth - 8 - bounds.left - width))}px`;
  }

  _openMenu() {
    if (this._open) return;
    this._open = true;
    this._render();
    this._navigate((this._selection && this._selection.workspace) || '');
  }

  _closeMenu(refocus = false) {
    if (!this._open) return;
    this._open = false;
    this._render();
    if (refocus && this.trigger) this.trigger.focus();
  }

  // Header re-renders move this node (disconnect+reconnect) — the menu's
  // shadow DOM survives, but the document listener must be reinstalled.
  connectedCallback() {
    if (this._open) this._installDismiss();
  }

  disconnectedCallback() {
    this._detachDismiss();
  }

  _navigate(path) {
    this._browse = null;
    this._browseError = '';
    this._render();
    const gen = ++this._gen;
    this.api.browseFs(path)
      .then((res) => { if (gen === this._gen) { this._browse = res; this._render(); } })
      .catch((err) => { if (gen === this._gen) { this._browseError = err && err.detail || 'browse failed'; this._render(); } });
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

  _choose(workspace) {
    emit(this, 'workspace-change', { workspace });
    this._closeMenu();
  }

  _renderMenu(menu) {
    if (this._browseError) {
      menu.append(el('div', { class: 'menu-empty', text: this._browseError }));
      return;
    }
    if (!this._browse) {
      menu.append(el('div', { class: 'menu-empty', text: 'Loading…' }));
      return;
    }
    const b = this._browse;
    const current = (this._selection && this._selection.workspace) || '';
    menu.append(el('div', { class: 'ws-path', text: b.path, title: b.path }));
    const roots = el('div', { class: 'ws-roots' });
    for (const r of b.roots || []) {
      roots.append(el('button', {
        class: 'ws-root', type: 'button', text: r.name,
        onclick: () => this._navigate(r.path),
      }));
    }
    menu.append(roots);
    const entries = el('div', { class: 'ws-entries', role: 'listbox', 'aria-label': 'Directories' });
    if (b.parent) {
      entries.append(this._item('..', 'up', () => this._navigate(b.parent)));
    }
    for (const entry of b.entries || []) {
      entries.append(this._item(entry.name, '', () => this._navigate(entry.path), entry.path === current));
    }
    if (b.entries && !b.entries.length && !b.parent) {
      entries.append(el('div', { class: 'menu-empty', text: 'No subdirectories' }));
    }
    menu.append(entries);
    menu.append(el('div', { class: 'ws-actions' }, [
      el('button', {
        class: 'ws-choose', type: 'button',
        text: `Use ${b.path}`,
        onclick: () => this._choose(b.path),
      }),
      current ? el('button', {
        class: 'ws-keep', type: 'button',
        text: 'Reset to default',
        title: 'Clear the workspace binding — tools run on the configured root',
        onclick: () => this._choose(''),
      }) : null,
    ]));
  }

  _render() {
    this._detachDismiss(); // any pending outside-tap listener dies here
    clear(this.root);
    this.root.classList.add('picker');

    const ws = (this._selection && this._selection.workspace) || '';
    this.trigger = el('button', {
      class: 'trigger', type: 'button', 'aria-haspopup': 'listbox',
      'aria-expanded': this._open ? 'true' : 'false',
      title: ws || 'Workspace (default tool root)',
      onclick: () => (this._open ? this._closeMenu() : this._openMenu()),
    }, [
      icon('folder', 11),
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

    if (!this._open) {
      this._menuEl = null;
      return;
    }
    // Phones: dims the page and takes the tap that dismisses the sheet.
    this.root.append(el('div', { class: 'menu-backdrop', 'aria-hidden': 'true', onclick: () => this._closeMenu() }));
    const menu = el('div', { class: 'menu', 'aria-label': 'Workspace' });
    this._renderMenu(menu);
    menu.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        this._closeMenu(true);
      }
    });
    this.root.append(menu);
    this._clampMenu(menu);
    this._menuEl = menu;
    this._installDismiss();
  }
}

customElements.define('nusa-workspace-picker', WorkspacePicker);
