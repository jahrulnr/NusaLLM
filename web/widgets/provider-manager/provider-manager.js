import { Widget, el, emit, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

const STYLES = ['completion', 'responses', 'messages', 'generatecontent'];

// Provider CRUD. The editor keeps the wire body honest: apiKey is a
// tri-state (Keep omits it, Replace sends the string, Clear sends ""),
// and editing preserves every advanced field of surviving models.
// Models are viewed through a searchable list; only explicit imports
// can add ids. The stored key or ref is never
// rendered.
class ProviderManager extends Widget {
  static stylesheet = new URL('./provider-manager.css', import.meta.url);

  constructor() {
    super();
    this._gateways = [];
    this._editing = null;   // existing gateway object being edited
    this._creating = false;
    this._confirmId = null; // gateway id awaiting delete confirmation
    this._discovery = null;
    this._saving = false;
    this._pendingSaveId = null;
    this._render();
  }

  set gateways(list) {
    this._gateways = list || [];
    if (!this._editing && !this._creating) this._render();
  }

  set discoveredModels(result) {
    if (!result || result.id !== this._editing?.id) return;
    this._discovery = result;
    this._renderDiscovery();
  }

  set gatewaySaveResult(result) {
    if (!result || result.id !== this._pendingSaveId) return;
    this._saving = false;
    this._pendingSaveId = null;
    if (result.error) {
      if (this._formError) this._formError.textContent = result.error;
      return;
    }
    this._editing = null;
    this._creating = false;
    this._discovery = null;
    this._render();
  }

  _render() {
    if (this._dialog?.open) this._dialog.close();
    this._dialog = null;
    clear(this.root);
    this.root.classList.add('providers');

    this.root.append(el('div', { class: 'row-head' }, [
      el('span', { class: 'count', text: `${this._gateways.length} gateway${this._gateways.length === 1 ? '' : 's'}` }),
      el('button', {
        class: 'btn accent', type: 'button',
        onclick: () => { this._creating = true; this._render(); },
      }, [icon('plus', 12), el('span', { text: 'Add gateway' })]),
    ]));

    if (!this._gateways.length) {
      this.root.append(el('div', { class: 'hint', text: 'No gateways yet — add one to enable sending.' }));
    }
    for (const g of this._gateways) {
      if (this._confirmId === g.id) {
        // Deleting a gateway also drops its credential record — make it
        // a real confirmation, never a single click.
        this.root.append(el('div', { class: 'row confirm', role: 'alertdialog', 'aria-label': `Delete ${g.id}?` }, [
          el('span', { class: 'confirm-text', text: `Delete ${g.name || g.id} and its key?` }),
          el('button', {
            class: 'btn danger-btn', type: 'button',
            onclick: () => {
              this._confirmId = null;
              emit(this, 'gateway-delete', { id: g.id });
            },
          }, [el('span', { text: 'Delete' })]),
          el('button', {
            class: 'btn', type: 'button',
            onclick: () => { this._confirmId = null; this._render(); },
          }, [el('span', { text: 'Keep' })]),
        ]));
        continue;
      }
      this.root.append(el('div', { class: 'row' }, [
        el('div', { class: 'row-main' }, [
          el('span', { class: 'row-id', text: g.name || g.id }),
          el('span', { class: 'row-meta', text: `${g.style} · ${g.endpoint || 'no endpoint'}` }),
          el('span', { class: 'row-meta', text: `${(g.models || []).length} model(s) · key ${g.keyState || 'unset'}${g.broken ? ' · broken' : ''}` }),
        ]),
        el('div', { class: 'row-actions' }, [
          el('button', {
            class: 'icon-btn', type: 'button', 'aria-label': `Edit ${g.id}`,
            onclick: () => { this._editing = g; this._discovery = null; this._render(); },
          }, [icon('pencil', 12)]),
          el('button', {
            class: 'icon-btn danger', type: 'button', 'aria-label': `Delete ${g.id}`,
            onclick: () => { this._confirmId = g.id; this._render(); },
          }, [icon('trash', 12)]),
        ]),
      ]));
    }
    if (this._editing || this._creating) this._renderForm();
  }

  _renderForm() {
    const g = this._editing; // null when creating
    const dialog = el('dialog', { class: 'editor-dialog', 'aria-label': g ? `Edit ${g.id}` : 'New gateway' });
    const form = el('div', { class: 'editor-form' });
    const close = () => {
      if (this._saving) return;
      this._editing = null;
      this._creating = false;
      this._discovery = null;
      this._render();
    };
    dialog.addEventListener('cancel', (event) => {
      event.preventDefault();
      close();
    });
    const field = (label, node) => el('label', { class: 'field' }, [
      el('span', { class: 'field-label', text: label }), node,
    ]);

    const id = el('input', { class: 'in', value: g ? g.id : '', disabled: !!g, 'aria-label': 'Gateway id' });
    const name = el('input', { class: 'in', value: g ? g.name : '', 'aria-label': 'Name' });
    const style = el('select', { class: 'in', 'aria-label': 'API style' },
      STYLES.map((s) => el('option', { value: s, text: s, selected: g && g.style === s })));
    const endpoint = el('input', { class: 'in', value: g ? g.endpoint : '', 'aria-label': 'Endpoint URL' });
    const stream = el('input', { type: 'checkbox', checked: g ? !!g.stream : true, 'aria-label': 'Streaming' });
    this._modelIds = new Set((g?.models || []).map((m) => m.id));
    this._modelSearch = el('input', {
      class: 'in', type: 'search', placeholder: 'Search configured models',
      'aria-label': 'Search configured models',
    });
    this._modelList = el('div', { class: 'model-list', role: 'list', 'aria-label': 'Configured models', tabindex: '0' });
    this._modelCount = el('span', { class: 'count' });
    this._modelSearch.addEventListener('input', () => this._renderModels());

    // API key tri-state — the current value never renders, only a mode.
    const keyMode = el('select', { class: 'in', 'aria-label': 'API key mode' }, [
      el('option', { value: 'keep', text: g && g.keySet ? 'Keep stored key' : 'No key', selected: true }),
      el('option', { value: 'replace', text: 'Replace key…' }),
      g && g.keySet ? el('option', { value: 'clear', text: 'Clear stored key' }) : null,
    ]);
    const keyValue = el('input', {
      class: 'in', type: 'password', autocomplete: 'off',
      placeholder: 'new api key', 'aria-label': 'New API key',
    });
    const keyField = field('new api key', keyValue);
    keyField.hidden = true;
    keyMode.addEventListener('change', () => {
      keyField.hidden = keyMode.value !== 'replace';
    });

    const error = el('div', { class: 'form-error' });

    const save = () => {
      if (this._saving) return;
      const gid = g ? g.id : id.value.trim();
      if (!gid) {
        error.textContent = 'gateway id is required';
        return;
      }
      // Preserve every advanced field for surviving ids; defaults for new.
      const byId = new Map((g ? g.models || [] : []).map((m) => [m.id, m]));
      const body = {
        name: name.value.trim(),
        style: style.value,
        endpoint: endpoint.value.trim(),
        stream: stream.checked,
        models: [...this._modelIds].map((mid) => byId.get(mid) || {
          id: mid, route: '', contextWindow: 0,
          pricing: { inputPerMillionUsd: 0, outputPerMillionUsd: 0, cacheReadPerMillionUsd: 0, cacheWritePerMillionUsd: 0 },
        }),
      };
      // Replace must carry a real key — an empty string only crosses
      // via the explicit Clear mode.
      if (keyMode.value === 'replace') {
        if (!keyValue.value) {
          error.textContent = 'new API key is empty — use Clear to remove the stored key';
          return;
        }
        body.apiKey = keyValue.value;
      }
      if (keyMode.value === 'clear') body.apiKey = '';
      this._saving = true;
      this._pendingSaveId = gid;
      error.textContent = 'Saving…';
      emit(this, 'gateway-save', { id: gid, body });
    };

    form.append(
      field('id', id),
      field('name', name),
      field('api style', style),
      field('endpoint', endpoint),
      field('stream', el('span', { class: 'check' }, [stream])),
      el('div', { class: 'field' }, [
        el('div', { class: 'model-head' }, [
          el('span', { class: 'field-label', text: 'models' }), this._modelCount,
        ]),
        this._modelSearch,
        this._modelList,
      ]),
      el('div', { class: 'import-actions' }, [
        el('button', {
          class: 'btn', type: 'button', disabled: !g,
          onclick: () => this._fetchModels(g.id),
        }, [el('span', { text: 'Fetch models from provider' })]),
        !g ? el('span', { class: 'hint', text: 'Save the gateway first to use its endpoint and key.' }) : null,
      ]),
      this.discoveryHost = el('div', { class: 'discovery' }),
      field('api key', keyMode),
      keyField,
      error,
    );
    dialog.append(
      el('div', { class: 'dialog-head' }, [
        el('div', { class: 'dialog-title', text: g ? `Edit ${g.id}` : 'New gateway' }),
        el('button', { class: 'icon-btn dialog-close', type: 'button', 'aria-label': 'Close gateway editor', onclick: close }, [icon('x', 16)]),
      ]),
      form,
      el('div', { class: 'form-actions' }, [
        el('button', { class: 'btn accent', type: 'button', onclick: save }, [el('span', { text: 'Save' })]),
        el('button', { class: 'btn', type: 'button', onclick: close }, [el('span', { text: 'Cancel' })]),
      ]),
    );
    this.root.append(dialog);
    this._dialog = dialog;
    dialog.showModal();
    this._renderModels();
    this._formError = error;
    this._renderDiscovery();
  }

  _renderModels() {
    const ids = [...this._modelIds];
    const q = this._modelSearch.value.trim().toLowerCase();
    const visible = ids.filter((id) => id.toLowerCase().includes(q));
    this._modelCount.textContent = q ? `${visible.length} of ${ids.length}` : `${ids.length} configured`;
    clear(this._modelList);
    if (!visible.length) {
      this._modelList.append(el('div', { class: 'model-empty', text: q ? 'No matching models.' : 'No models configured yet.' }));
      return;
    }
    for (const id of visible) this._modelList.append(el('div', { class: 'model-item', role: 'listitem', text: id }));
  }

  _fetchModels(id) {
    this._discovery = { id, loading: true };
    this._renderDiscovery();
    emit(this, 'gateway-model-discover', { id });
  }

  _renderDiscovery() {
    if (!this.discoveryHost) return;
    clear(this.discoveryHost);
    const result = this._discovery;
    if (!result || result.id !== this._editing?.id) return;
    if (result.loading) {
      this.discoveryHost.append(el('p', { class: 'hint', text: 'Fetching provider models…' }));
      return;
    }
    if (result.error) {
      this.discoveryHost.append(el('p', { class: 'form-error', text: result.error }));
      return;
    }
    const catalog = result.models || [];
    if (!catalog.length) {
      this.discoveryHost.append(el('p', { class: 'hint', text: 'Provider returned no models.' }));
      return;
    }
    const newCount = catalog.filter((id) => !this._modelIds.has(id)).length;
    const search = el('input', { class: 'in', type: 'search', placeholder: 'Search provider models', 'aria-label': 'Search provider models' });
    const select = el('select', { class: 'in discovered-list', multiple: true, size: '10', 'aria-label': 'Discovered models' },
      catalog.map((id) => el('option', {
        value: id,
        text: this._modelIds.has(id) ? `${id} · configured` : id,
        disabled: this._modelIds.has(id),
      })));
    search.addEventListener('input', () => {
      const q = search.value.toLowerCase();
      for (const option of select.options) option.hidden = !option.value.toLowerCase().includes(q);
    });
    this.discoveryHost.append(
      el('div', { class: 'discovery-head', text: `${catalog.length} available from provider · ${newCount} new` }),
      search,
      select,
      el('button', {
        class: 'btn accent', type: 'button', disabled: !newCount,
        onclick: () => {
          const chosen = [...select.selectedOptions].filter((option) => !option.hidden).map((option) => option.value);
          for (const id of chosen) this._modelIds.add(id);
          this._renderModels();
          this._renderDiscovery();
        },
      }, [el('span', { text: 'Add selected to gateway' })]),
    );
  }
}

customElements.define('nusa-provider-manager', ProviderManager);
