import { Widget, el, emit, clear, deepActiveElement } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

// Left rail: brand, New thread, search, the recent conversation list,
// the Load more control, and a connection/status footer. Pure rendering
// — the shell owns the list data and reacts to the emitted events.
class NavSidebar extends Widget {
  static stylesheet = new URL('./nav-sidebar.css', import.meta.url);

  constructor() {
    super();
    this._conversations = [];
    this._activeId = null;
    this._loading = false;
    this._open = true;
    this._connection = 'closed';
    this._query = '';
    this._hasMore = false;
    this._total = 0;
    this._moreBusy = false;
    this._render();
  }

  // Host attribute mutations belong here — a custom element's
  // constructor may not touch its own attributes.
  connectedCallback() { this.open = this._open; }

  set conversations(list) { this._conversations = list || []; this._renderList(); }
  set activeId(id) { this._activeId = id; this._renderList(); }
  set loading(v) { this._loading = !!v; this._renderList(); }
  // Paging state for the list. `hasMore`/`total` come from the server's
  // page meta; `moreBusy` is the shell's in-flight load-more request —
  // deliberately separate from `loading` so the list stays usable
  // (selection, search) while a page is on the wire.
  set hasMore(v) { this._hasMore = !!v; this._renderList(); }
  set total(v) { this._total = Number(v) || 0; this._renderList(); }
  set moreBusy(v) { this._moreBusy = !!v; this._renderList(); }
  set open(v) {
    this._open = !!v;
    this.classList.toggle('closed', !this._open);
    // Collapsed means unreachable: keep the closed rail out of the tab
    // order at every viewport, not just the mobile overlay.
    this.inert = !this._open;
    this.setAttribute('aria-hidden', this._open ? 'false' : 'true');
  }
  set connection(state) { this._connection = state; this._renderFooter(); }
  // Focus target for the phone overlay — the Close control is both the
  // first interactive element and the way out.
  focusInitial() { this._closeBtn?.focus?.(); }

  // Keyboard containment for the phone overlay. The drawer covers the
  // page while open, so Tab at either end wraps to the other end rather
  // than walking into the page hidden behind the scrim. Returns true when
  // the key was handled.
  trapTab(event) {
    const els = this._focusables();
    if (!els.length) return false;
    const idx = els.indexOf(deepActiveElement());
    if (idx === -1) {
      event.preventDefault();
      els[0].focus();
      return true;
    }
    if (!event.shiftKey && idx === els.length - 1) {
      event.preventDefault();
      els[0].focus();
      return true;
    }
    if (event.shiftKey && idx === 0) {
      event.preventDefault();
      els[els.length - 1].focus();
      return true;
    }
    return false;
  }

  _focusables() {
    return [...this.shadowRoot.querySelectorAll('button, input, select, textarea, a[href], [tabindex]')]
      .filter((n) => !n.disabled && n.tabIndex >= 0 && n.getClientRects().length > 0);
  }

  // Programmatic reset for the shell's new-browsing-context navigations —
  // clears the box and the narrowed list without emitting a user search.
  set query(v) {
    this._query = v || '';
    if (this.searchEl) this.searchEl.value = this._query;
    this._renderList();
  }

  _render() {
    clear(this.root);
    this.root.classList.add('nav');

    const search = el('input', {
      class: 'search', type: 'search', placeholder: 'Search threads',
      'aria-label': 'Search conversations',
      oninput: (e) => {
        this._query = e.target.value;
        emit(this, 'conversation-search', { query: this._query });
      },
    });

    this.root.append(
      el('div', { class: 'brand' }, [
        el('span', { class: 'brand-mark', text: 'N' }),
        el('span', { class: 'brand-name', text: 'NusaLLM' }),
        this._closeBtn = el('button', {
          class: 'icon-btn close-btn', type: 'button', 'aria-label': 'Close sidebar',
          onclick: () => emit(this, 'sidebar-close'),
        }, [icon('x', 14)]),
      ]),
      el('button', {
        class: 'new-btn', type: 'button',
        onclick: () => emit(this, 'conversation-new'),
      }, [icon('plus', 13), el('span', { text: 'New thread' })]),
      this.searchEl = search,
      this.listEl = el('nav', { class: 'list', 'aria-label': 'Conversations' }),
      this.footerEl = el('div', { class: 'footer' }),
    );
    this._renderList();
    this._renderFooter();
  }

  _renderList() {
    if (!this.listEl) return;
    // Every mutation rebuilds the list — a rebuild under the user's
    // focus must hand it to the logical successor (same row id, else the
    // row that took its slot, else the refreshed Load more) instead of
    // dropping to BODY and forcing a full Tab cycle back into the rail.
    const prev = deepActiveElement();
    let focusKey = null;
    // A busy Load more parks focus on the old last row (focus() on the
    // disabled control no-ops). When the loaded page then lands below
    // it, that parked row must not keep the keyboard: the first row the
    // old render did not have — the new page — is the successor the
    // user asked for, else the refreshed control.
    let parked = null;
    if (prev) {
      if (prev === this._moreBtn) focusKey = { more: true };
      else {
        const at = (this._items || []).indexOf(prev);
        if (at >= 0) {
          focusKey = { id: prev.getAttribute?.('data-id'), at };
          if (this._moreBusy && at === this._items.length - 1) {
            parked = new Set((this._items || []).map((b) => b.getAttribute?.('data-id')));
          }
        }
      }
    }
    clear(this.listEl);
    this._items = [];
    this._moreBtn = null;
    const q = this._query.trim().toLowerCase();
    const rows = (this._conversations || []).filter((c) =>
      !q || (c.title || c.id).toLowerCase().includes(q));
    if (this._loading) {
      this.listEl.append(el('div', { class: 'hint', text: 'Loading…' }));
      if (focusKey) this.searchEl?.focus?.();
      return;
    }
    if (!rows.length) {
      this.listEl.append(el('div', { class: 'hint', text: q ? 'No matches' : 'No threads yet' }));
    }
    for (const c of rows) {
      const active = c.id === this._activeId;
      const row = el('button', {
        class: `item${active ? ' active' : ''}`,
        type: 'button',
        'data-id': c.id,
        'aria-current': active ? 'true' : null,
        onclick: () => emit(this, 'conversation-select', { id: c.id }),
      }, [
        el('span', { class: 'item-title', text: c.title || 'Untitled' }),
        el('span', { class: 'item-meta', text: this._when(c.updatedAt) }),
      ]);
      this._items.push(row);
      this.listEl.append(row);
    }
    this._renderMore();
    if (focusKey) {
      const items = this._items;
      // The busy render disables the Load-more control and focus() on a
      // disabled node no-ops — a successor that can't take focus is no
      // successor, so the last loaded row stands in until it can.
      const moreOk = this._moreBtn && !this._moreBtn.disabled ? this._moreBtn : null;
      const target = parked
        ? items.find((b) => !parked.has(b.getAttribute?.('data-id')))
          || moreOk || items[items.length - 1] || this.searchEl
        : focusKey.more
          ? moreOk || items[items.length - 1] || this.searchEl
          : items.find((b) => b.getAttribute?.('data-id') === focusKey.id)
            || items[Math.min(focusKey.at, items.length - 1)]
            || moreOk || this.searchEl;
      target?.focus?.();
    }
  }

  // Load-more control, rendered after the rows. The server still holds
  // pages behind the loaded set, and the client-side search only sees
  // what is loaded — so the reach is stated outright instead of hidden.
  _renderMore() {
    if (!this._hasMore) return;
    this.listEl.append(el('div', { class: 'more' }, [
      this._moreBtn = el('button', {
        class: 'more-btn', type: 'button',
        disabled: this._moreBusy || null,
        onclick: () => emit(this, 'conversation-more'),
      }, [el('span', { text: this._moreBusy ? 'Loading…' : 'Load more' })]),
      el('div', {
        class: 'coverage',
        text: `Loaded ${(this._conversations || []).length} of ${this._total}`,
      }),
    ]));
  }

  _when(iso) {
    if (!iso) return '';
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? '' : d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  }

  _renderFooter() {
    if (!this.footerEl) return;
    clear(this.footerEl);
    const label = { open: 'live', connecting: 'connecting', closed: 'offline' }[this._connection] || this._connection;
    this.footerEl.append(
      el('span', { class: `conn-dot ${this._connection}` }),
      el('span', { class: 'conn-label', text: label }),
    );
  }
}

customElements.define('nusa-nav-sidebar', NavSidebar);
