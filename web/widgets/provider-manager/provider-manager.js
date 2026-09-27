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
    this._render();
  }

  set gateways(list) {
    this._gateways = list || [];
    if (!this._editing && !this._creating) this._render();
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
            onclick: () => { this._editing = g; this._render(); },
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
      const gid = g ? g.id : id.value.trim();
      const modelIds = models.value.split('\n').map((s) => s.trim()).filter(Boolean);
      if (!gid || !modelIds.length) {
        error.textContent = 'id and at least one model id are required';
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
      emit(this, 'gateway-save', { id: gid, body });
      this._editing = null;
      this._creating = false;
      this._render();
    };

    this.root.append(
      el('div', { class: 'form-head', text: g ? `Edit ${g.id}` : 'New gateway' }),
      field('id', id),
      field('name', name),
      field('api style', style),
      field('endpoint', endpoint),
      field('stream', el('span', { class: 'check' }, [stream])),
      field('models', models),
      field('api key', keyMode),
      field('', keyValue),
      error,
      el('div', { class: 'form-actions' }, [
        el('button', { class: 'btn accent', type: 'button', onclick: save }, [el('span', { text: 'Save' })]),
        el('button', {
          class: 'btn', type: 'button',
          onclick: () => { this._editing = null; this._creating = false; this._render(); },
        }, [el('span', { text: 'Cancel' })]),
      ]),
    );
  }
}

customElements.define('nusa-provider-manager', ProviderManager);
