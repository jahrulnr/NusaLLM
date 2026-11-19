import { Widget, el, emit, clear, deepActiveElement, focusKeyOf, findByFocusKey, listOptions } from '../../lib/dom.js';
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
    this._innerFocus = null;   // focus key of the node inside this shadow
    this._clearTok = null;     // pending focusout clear token
    this._droppedOpen = false; // the drop-close below ran on our own detach
    // A header rebuild detaches and re-appends this host — the shadow
    // menu survives but the browser drops whatever focus it held,
    // firing focusout mid-detach. The clear must wait a microtask: a
    // synchronous detach+reattach is a move, not a leave, and the
    // recorded key hands focus back on reconnect.
    this.root.addEventListener('focusin', (e) => {
      this._clearTok = null;
      this._innerFocus = focusKeyOf(e.target);
    });
    this.root.addEventListener('focusout', (e) => {
      if (e.relatedTarget && this.root.contains(e.relatedTarget)) return;
      const tok = this._clearTok = {};
      // Our own render also swaps the focused node — that drop is a
      // move, not a leave, and the render's restore hands focus to the
      // replacement.
      const wasOpen = this._open && !this._rendering;
      if (wasOpen) {
        // Focus genuinely left, so the popover dismisses — but this same
        // event fires mid-detach while the host is inside a removeChild:
        // mutating the DOM here (render) throws inside the browser's
        // removal. Mark closed now, paint it on the microtask — a
        // same-task reconnect cancels that and reopens instead.
        this._droppedOpen = true;
        this._open = false;
      }
      queueMicrotask(() => {
        if (this._clearTok !== tok) return;
        this._innerFocus = null;
        if (wasOpen && !this._open) this._render();
      });
    });
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
    // Opening from the keyboard rebuilds the trigger under the key —
    // focus must land inside the popover or the user is stranded on
    // BODY. The entries arrive async, so the menu container holds it.
    this._menuEl?.focus?.();
    this._navigate((this._selection && this._selection.workspace) || '');
  }

  _closeMenu(refocus = false) {
    if (!this._open) return;
    this._open = false;
    this._render();
    if (refocus && this.trigger) this.trigger.focus();
  }

  // Header re-renders move this node (disconnect+reconnect) — the menu's
  // shadow DOM survives, but the document listener must be reinstalled
  // and the detached focus handed back.
  connectedCallback() {
    this._clearTok = null; // a pending detach-clear must not erase this
    if (this._droppedOpen) {
      this._droppedOpen = false;
      // The leave that closed us was our own detach — reopen what the
      // removal interrupted. A real leave clears the key first, so it
      // stays closed.
      if (this._innerFocus) { this._open = true; this._render(); }
    }
    if (this._open) {
      this._installDismiss();
      if (this._innerFocus) {
        (findByFocusKey(this.root, this._innerFocus) || this._menuEl)?.focus?.();
      }
    }
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

  _item(label, meta, onclick, sel = false, key = label) {
    return el('button', {
      class: `menu-item${sel ? ' selected' : ''}`, type: 'button', role: 'option',
      'data-id': key,
      'aria-selected': sel ? 'true' : 'false', onclick,
    }, [
      el('span', { class: 'menu-id', text: label }),
      meta ? el('span', { class: 'menu-meta', text: meta }) : null,
    ]);
  }

  _choose(workspace) {
    emit(this, 'workspace-change', { workspace });
    // A deliberate pick hands focus back to the trigger — the menu's
    // DOM is about to die, and focus on a dead node drops to body.
    this._closeMenu(true);
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
        class: 'ws-root', type: 'button', text: r.name, 'data-id': `root:${r.path}`,
        onclick: () => this._navigate(r.path),
      }));
    }
    menu.append(roots);
    const entries = el('div', { class: 'ws-entries', role: 'listbox', 'aria-label': 'Directories' });
    if (b.parent) {
      entries.append(this._item('..', 'up', () => this._navigate(b.parent), false, 'up'));
    }
    for (const entry of b.entries || []) {
      entries.append(this._item(entry.name, '', () => this._navigate(entry.path), entry.path === current, entry.path));
    }
    if (b.entries && !b.entries.length && !b.parent) {
      entries.append(el('div', { class: 'menu-empty', text: 'No subdirectories' }));
    }
    menu.append(entries);
    menu.append(el('div', { class: 'ws-actions' }, [
      el('button', {
        class: 'ws-choose', type: 'button', 'data-id': 'choose',
        text: `Use ${b.path}`,
        onclick: () => this._choose(b.path),
      }),
      current ? el('button', {
        class: 'ws-keep', type: 'button', 'data-id': 'keep',
        text: 'Reset to default',
        title: 'Clear the workspace binding — tools run on the configured root',
        onclick: () => this._choose(''),
      }) : null,
    ]));
  }

  _render() {
    this._detachDismiss(); // any pending outside-tap listener dies here
    // Browse and open re-renders replace the focused node — the
    // replacement carrying the same identity inherits focus; an item
    // whose directory left the list hands it to the menu container.
    const prev = deepActiveElement();
    const focusKey = prev && this.root.contains?.(prev) ? focusKeyOf(prev) : null;
    // Removing the focused node fires focusout before the unlink —
    // flag the removal window so the leave-close knows this drop is a
    // move the restore below is about to undo.
    this._rendering = true;
    try { clear(this.root); } finally { this._rendering = false; }
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

    if (this._open) {
      // Phones: dims the page and takes the tap that dismisses the sheet.
      this.root.append(el('div', { class: 'menu-backdrop', 'aria-hidden': 'true', onclick: () => this._closeMenu() }));
      // tabindex=-1: the container itself is the first focus target —
      // entries stream in async and there is nothing else to hold the
      // keyboard flow during Loading.
      const menu = el('div', { class: 'menu', tabindex: '-1', 'aria-label': 'Workspace' });
      this._renderMenu(menu);
      menu.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') {
          e.preventDefault();
          this._closeMenu(true);
        } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
          // The menu advertises listbox semantics — arrows must walk
          // the options, the same contract the model picker keeps.
          e.preventDefault();
          const options = listOptions(menu);
          if (!options.length) return;
          // Focus inside the shadow resolves to the host at document
          // level; the shadow root's own activeElement names the option.
          const active = this.shadowRoot?.activeElement || document.activeElement;
          const i = options.indexOf(active);
          const dir = e.key === 'ArrowDown' ? 1 : -1;
          const next = i < 0 ? (dir > 0 ? 0 : options.length - 1)
            : Math.max(0, Math.min(options.length - 1, i + dir));
          options[next].focus();
        }
      });
      this.root.append(menu);
      this._clampMenu(menu);
      this._menuEl = menu;
      this._installDismiss();
    } else {
      this._menuEl = null;
    }
    if (focusKey) {
      const hit = findByFocusKey(this.root, focusKey);
      (hit || this._menuEl)?.focus?.();
    }
  }
}

customElements.define('nusa-workspace-picker', WorkspacePicker);
