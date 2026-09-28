import { Widget, el, emit, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

const STYLES = ['completion', 'responses', 'messages', 'generatecontent'];

// Provider CRUD. The editor keeps the wire body honest: apiKey is a
// tri-state (Keep omits it, Replace sends the string, Clear sends ""),
// and editing preserves every advanced field of surviving models —
// only the id set is edited here. The stored key or ref is never
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
    clear(this.root);
    this.root.classList.add('providers');

    if (this._editing || this._creating) {
      this._renderForm();
      return;
    }

    this.root.append(el('div', { class: 'row-head' }, [
      el('span', { class: 'count', text: `${this._gateways.length} gateway${this._gateways.length === 1 ? '' : 's'}` }),
      el('button', {
        class: 'btn accent', type: 'button',
        onclick: () => { this._creating = true; this._render(); },
      }, [icon('plus', 12), el('span', { text: 'Add gateway' })]),
    ]));

    if (!this._gateways.length) {
      this.root.append(el('div', { class: 'hint', text: 'No gateways yet — add one to enable sending.' }));
      return;
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
  }

  _renderForm() {
    const g = this._editing; // null when creating
    const field = (label, node) => el('label', { class: 'field' }, [
      el('span', { class: 'field-label', text: label }), node,
    ]);

    const id = el('input', { class: 'in', value: g ? g.id : '', disabled: !!g, 'aria-label': 'Gateway id' });
    const name = el('input', { class: 'in', value: g ? g.name : '', 'aria-label': 'Name' });
    const style = el('select', { class: 'in', 'aria-label': 'API style' },
      STYLES.map((s) => el('option', { value: s, text: s, selected: g && g.style === s })));
    const endpoint = el('input', { class: 'in', value: g ? g.endpoint : '', 'aria-label': 'Endpoint URL' });
    const stream = el('input', { type: 'checkbox', checked: g ? !!g.stream : true, 'aria-label': 'Streaming' });
    const models = el('textarea', {
      class: 'in models', rows: '4',
      placeholder: 'one model id per line',
      'aria-label': 'Model ids',
    });
    models.value = g ? (g.models || []).map((m) => m.id).join('\n') : '';

    // API key tri-state — the current value never renders, only a mode.
    const keyMode = el('select', { class: 'in', 'aria-label': 'API key mode' }, [
      el('option', { value: 'keep', text: g && g.keySet ? 'Keep stored key' : 'No key', selected: true }),
      el('option', { value: 'replace', text: 'Replace key…' }),
      g && g.keySet ? el('option', { value: 'clear', text: 'Clear stored key' }) : null,
    ]);
    const keyValue = el('input', {
      class: 'in', type: 'password', autocomplete: 'off',
      placeholder: 'new api key', 'aria-label': 'New API key', hidden: true,
    });
    keyMode.addEventListener('change', () => {
      keyValue.hidden = keyMode.value !== 'replace';
    });

    const error = el('div', { class: 'form-error' });

    const save = () => {
      if (this._saving) return;
      const gid = g ? g.id : id.value.trim();
      const modelIds = models.value.split('\n').map((s) => s.trim()).filter(Boolean);
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
        models: modelIds.map((mid) => byId.get(mid) || {
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

    this.root.append(
      el('div', { class: 'form-head', text: g ? `Edit ${g.id}` : 'New gateway' }),
      field('id', id),
      field('name', name),
      field('api style', style),
      field('endpoint', endpoint),
      field('stream', el('span', { class: 'check' }, [stream])),
      field('models', models),
      el('div', { class: 'import-actions' }, [
        el('button', {
          class: 'btn', type: 'button', disabled: !g,
          onclick: () => this._fetchModels(g.id),
        }, [el('span', { text: 'Fetch models from provider' })]),
        !g ? el('span', { class: 'hint', text: 'Save the gateway first to use its endpoint and key.' }) : null,
      ]),
      this.discoveryHost = el('div', { class: 'discovery' }),
      field('api key', keyMode),
      field('', keyValue),
      error,
      el('div', { class: 'form-actions' }, [
        el('button', { class: 'btn accent', type: 'button', onclick: save }, [el('span', { text: 'Save' })]),
        el('button', {
          class: 'btn', type: 'button',
          onclick: () => {
            if (this._saving) return;
            this._editing = null;
            this._creating = false;
            this._discovery = null;
            this._render();
          },
        }, [el('span', { text: 'Cancel' })]),
      ]),
    );
    this._modelsInput = models;
    this._formError = error;
    this._renderDiscovery();
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
    const existing = new Set(this._modelsInput.value.split('\n').map((s) => s.trim()).filter(Boolean));
    const available = (result.models || []).filter((id) => !existing.has(id));
    if (!available.length) {
      this.discoveryHost.append(el('p', { class: 'hint', text: 'No new models found.' }));
      return;
    }
    const search = el('input', { class: 'in', type: 'search', placeholder: 'Filter models', 'aria-label': 'Filter discovered models' });
    const select = el('select', { class: 'in discovered-list', multiple: true, size: '10', 'aria-label': 'Discovered models' },
      available.map((id) => el('option', { value: id, text: id })));
    search.addEventListener('input', () => {
      const q = search.value.toLowerCase();
      for (const option of select.options) option.hidden = !option.value.toLowerCase().includes(q);
    });
    this.discoveryHost.append(
      el('div', { class: 'discovery-head', text: `${available.length} new models available` }),
      search,
      select,
      el('button', {
        class: 'btn accent', type: 'button',
        onclick: () => {
          const chosen = [...select.selectedOptions].map((option) => option.value);
          const ids = new Set(this._modelsInput.value.split('\n').map((s) => s.trim()).filter(Boolean));
          for (const id of chosen) ids.add(id);
          this._modelsInput.value = [...ids].join('\n');
          this._renderDiscovery();
        },
      }, [el('span', { text: 'Add selected to gateway' })]),
    );
  }
}

customElements.define('nusa-provider-manager', ProviderManager);
