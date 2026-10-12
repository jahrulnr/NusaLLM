import { Widget, el, emit, clear } from '../../lib/dom.js';
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
        el('button', {
          class: 'icon-btn close-btn', type: 'button', 'aria-label': 'Close sidebar',
          onclick: () => emit(this, 'sidebar-close'),
        }, [icon('x', 14)]),
      ]),
      el('button', {
        class: 'new-btn', type: 'button',
        onclick: () => emit(this, 'conversation-new'),
      }, [icon('plus', 13), el('span', { text: 'New thread' })]),
      search,
      this.listEl = el('nav', { class: 'list', 'aria-label': 'Conversations' }),
      this.footerEl = el('div', { class: 'footer' }),
    );
    this._renderList();
    this._renderFooter();
  }

  _renderList() {
    if (!this.listEl) return;
    clear(this.listEl);
    const q = this._query.trim().toLowerCase();
    const rows = (this._conversations || []).filter((c) =>
      !q || (c.title || c.id).toLowerCase().includes(q));
    if (this._loading) {
      this.listEl.append(el('div', { class: 'hint', text: 'Loading…' }));
      return;
    }
    if (!rows.length) {
      this.listEl.append(el('div', { class: 'hint', text: q ? 'No matches' : 'No threads yet' }));
    }
    for (const c of rows) {
      const active = c.id === this._activeId;
      this.listEl.append(el('button', {
        class: `item${active ? ' active' : ''}`,
        type: 'button',
        'aria-current': active ? 'true' : null,
        onclick: () => emit(this, 'conversation-select', { id: c.id }),
      }, [
        el('span', { class: 'item-title', text: c.title || 'Untitled' }),
        el('span', { class: 'item-meta', text: this._when(c.updatedAt) }),
      ]));
    }
    this._renderMore();
  }

  // Load-more control, rendered after the rows. The server still holds
  // pages behind the loaded set, and the client-side search only sees
  // what is loaded — so the reach is stated outright instead of hidden.
  _renderMore() {
    if (!this._hasMore) return;
    this.listEl.append(el('div', { class: 'more' }, [
      el('button', {
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
