import { Widget, el, emit, clear } from '../../lib/dom.js';

// The control kind _input renders for a leaf — part of the document's
// shape, so a kind change must fall back to a full render.
function kindOf(value) {
  if (Array.isArray(value)) return 'list';
  if (typeof value === 'boolean') return 'bool';
  if (typeof value === 'number') return 'num';
  return 'text';
}

// The object-valued top-level entries the editor renders — the only
// part of the document with visible controls.
function sectionEntries(settings) {
  return Object.entries(settings || {})
    .filter(([, value]) => value && typeof value === 'object');
}

// The element actually holding focus, pierced through open shadow
// roots — document.activeElement stops at the outermost host.
function deepActiveElement() {
  let active = document.activeElement;
  while (active && active.shadowRoot && active.shadowRoot.activeElement) {
    active = active.shadowRoot.activeElement;
  }
  return active;
}

// Settings editor: groups projected values by section and emits the
// existing dotted key when a field changes.
class SettingsEditor extends Widget {
  static stylesheet = new URL('./settings-editor.css', import.meta.url);

  constructor() {
    super();
    this._settings = {};
    this._inputs = new Map();
    this._render();
  }

  set settings(doc) {
    this._settings = doc || {};
    // A PATCH round-trip returns the same document shape: refresh the
    // values in place. Rebuilding would detach the focused field
    // mid-draft — blur to BODY fires a change the user did not ask for.
    if (this._signature() === this._renderedSig) {
      this._syncInputs();
      return;
    }
    this._render();
  }

  // The rendered shape: ordered sections, each leaf, and the control
  // kind rendering it. A matching document reuses the standing DOM.
  // Order counts — render order follows the document's, and the
  // backend projection is sorted, so order is stable across responses.
  _signature() {
    return JSON.stringify(sectionEntries(this._settings)
      .map(([section, values]) => [section, Object.entries(values)
        .map(([leaf, value]) => [leaf, kindOf(value)])]));
  }

  // Same-shape refresh: write projected values into the standing
  // inputs. A focused draft-capable field is skipped — its draft may
  // be newer than this response, and its own change re-projects it.
  _syncInputs() {
    const focused = deepActiveElement();
    for (const [section, values] of sectionEntries(this._settings)) {
      for (const [leaf, value] of Object.entries(values)) {
        const input = this._inputs.get(`${section}.${leaf}`);
        if (!input) continue;
        const kind = kindOf(value);
        // A checkbox holds no draft — checked is committed state, so
        // it syncs even while focused (e.g. reverting a failed save).
        if (kind === 'bool') {
          if (input.checked !== value) input.checked = value;
          continue;
        }
        if (input === focused) continue;
        const text = kind === 'list' ? value.join('\n') : String(value ?? '');
        if (input.value !== text) input.value = text;
      }
    }
  }

  _render() {
    const expanded = new Set([...this.root.querySelectorAll('details[open]')].map((group) => group.dataset.section));
    clear(this.root);
    this._inputs = new Map();
    this._renderedSig = this._signature();
    this.root.classList.add('settings');
    const sections = sectionEntries(this._settings);
    if (!sections.length) {
      this.root.append(el('div', { class: 'hint', text: 'No settings exposed.' }));
      return;
    }
    this.root.append(el('p', { class: 'save-hint', text: 'Auto-save: text and number fields save when you leave them; checkboxes save immediately.' }));
    for (const [section, values] of sections) {
      const group = el('details', { class: 'group', 'data-section': section, open: expanded.has(section) }, [
        el('summary', { text: section[0].toUpperCase() + section.slice(1) }),
      ]);
      const body = el('div', { class: 'group-body' });
      if (section === 'server') {
        body.append(el('p', { class: 'hint', text: 'Port and host changes take effect after restart.' }));
      }
      const fields = el('div', { class: 'fields' });
      for (const [leaf, value] of Object.entries(values)) {
        const key = `${section}.${leaf}`;
        const input = this._input(key, value);
        this._inputs.set(key, input);
        fields.append(el('div', { class: 'setting' }, [
          el('label', { class: 'setting-key', text: leaf.replaceAll('_', ' '), 'for': `s-${key}`, title: key }),
          input,
        ]));
      }
      body.append(fields);
      group.append(body);
      this.root.append(group);
    }
  }

  _input(key, value) {
    if (Array.isArray(value)) {
      const input = el('textarea', {
        class: 'in list-input', id: `s-${key}`, rows: 2,
        'aria-label': key, spellcheck: 'false',
      });
      input.value = value.join('\n');
      input.addEventListener('change', () => {
        emit(this, 'setting-change', { key, value: input.value.split('\n').map((item) => item.trim()).filter(Boolean) });
      });
      return input;
    }
    if (typeof value === 'boolean') {
      const input = el('input', { type: 'checkbox', id: `s-${key}`, checked: value });
      input.addEventListener('change', () =>
        emit(this, 'setting-change', { key, value: input.checked }));
      return input;
    }
    if (typeof value === 'number') {
      const input = el('input', { class: 'in', type: 'number', id: `s-${key}`, value });
      input.addEventListener('change', () =>
        emit(this, 'setting-change', { key, value: Number(input.value) }));
      return input;
    }
    const input = el('input', { class: 'in', type: 'text', id: `s-${key}`, value: String(value ?? '') });
    input.addEventListener('change', () =>
      emit(this, 'setting-change', { key, value: input.value }));
    return input;
  }
}

customElements.define('nusa-settings-editor', SettingsEditor);
