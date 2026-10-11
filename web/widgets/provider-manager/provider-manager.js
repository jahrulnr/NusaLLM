import { Widget, el, emit, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

const STYLES = ['completion', 'responses', 'messages', 'generatecontent'];

// Provider CRUD. The editor keeps the wire body honest: apiKey is a
// tri-state (Keep omits it, Replace sends the string, Clear sends ""),
// and editing preserves every advanced field of surviving models.
// The server merges the provider's advertised model ids into the
// record on save and on Refresh — the stored key or ref is never
// rendered.
class ProviderManager extends Widget {
  static stylesheet = new URL('./provider-manager.css', import.meta.url);

  constructor() {
    super();
    this._gateways = [];
    this._editing = null;   // existing gateway object being edited
    this._creating = false;
    this._confirmId = null; // gateway id awaiting delete confirmation
    this._importing = false;
    this._saving = false;
    this._pendingSaveId = null;
    this._render();
  }

  set gateways(list) {
    this._gateways = list || [];
    if (!this._editing && !this._creating) this._render();
  }

  set gatewaySaveResult(result) {
    if (!result || result.id !== this._pendingSaveId) return;
    this._saving = false;
    this._pendingSaveId = null;
    if (result.error) {
      if (this._formError) this._formError.textContent = result.error;
      return;
    }
    // The record saved even when the provider catalog would not merge —
    // adopt the stored catalog, keep the dialog open, and show the
    // failure where save errors render.
    if (result.models) {
      this._modelIds = new Set(result.models.map((m) => m.id));
      if (this._editing) this._editing = { ...this._editing, models: result.models };
      this._renderModels();
    }
    if (result.catalogError) {
      if (this._formError) this._formError.textContent = result.catalogError;
      return;
    }
    this._editing = null;
    this._creating = false;
    this._render();
  }

  // Refresh answer: the import runs server-side against the stored
  // endpoint and key. The response's post-merge catalog replaces the
  // form's id list in place — the dialog stays open.
  set gatewayImportResult(result) {
    if (!result || !this._editing || result.id !== this._editing.id) return;
    this._importing = false;
    this._syncImportState();
    if (result.error) {
      this._noteImport(result.error, true);
      return;
    }
    if (result.models) {
      this._editing = { ...this._editing, models: result.models };
      this._modelIds = new Set(result.models.map((m) => m.id));
      this._renderModels();
    }
    if (result.catalogError) {
      this._noteImport(result.catalogError, true);
      return;
    }
    const n = (result.added || []).length;
    this._noteImport(n
      ? `Imported ${n} new model${n === 1 ? '' : 's'} from the provider catalog.`
      : 'No new models from the provider.', false);
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
            onclick: () => { this._editing = g; this._importing = false; this._render(); },
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
      this._importing = false;
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
        this._importBtn = el('button', {
          class: 'btn', type: 'button', disabled: !g || this._importing,
          onclick: () => this._importModels(g.id),
        }, [el('span', { text: this._importing ? 'Importing…' : 'Refresh models from provider' })]),
        g
          ? el('span', { class: 'hint', text: 'Re-reads the provider catalog with the saved endpoint and key.' })
          : el('span', { class: 'hint', text: 'Save the gateway to import its models.' }),
      ]),
      this._importNote = el('div'),
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

  // The import runs server-side against the stored record — a field the
  // user edited but never saved (endpoint, key) does NOT apply here.
  _importModels(id) {
    this._importing = true;
    this._syncImportState();
    this._noteImport('Importing the provider catalog…', false);
    emit(this, 'gateway-import', { id });
  }

  _syncImportState() {
    if (!this._importBtn) return;
    this._importBtn.disabled = !this._editing || this._importing;
    this._importBtn.textContent = this._importing
      ? 'Importing…' : 'Refresh models from provider';
  }

  _noteImport(text, isError) {
    if (!this._importNote) return;
    this._importNote.textContent = text;
    this._importNote.className = isError ? 'form-error' : 'hint';
  }
}

customElements.define('nusa-provider-manager', ProviderManager);
