import { Widget, el, emit, clear } from '../../lib/dom.js';

// Settings editor: groups projected values by section and emits the
// existing dotted key when a field changes.
class SettingsEditor extends Widget {
  static stylesheet = new URL('./settings-editor.css', import.meta.url);

  constructor() {
    super();
    this._settings = {};
    this._render();
  }

  set settings(doc) { this._settings = doc || {}; this._render(); }

  _render() {
    const expanded = new Set([...this.root.querySelectorAll('details[open]')].map((group) => group.dataset.section));
    clear(this.root);
    this.root.classList.add('settings');
    const sections = Object.entries(this._settings || {}).filter(([, value]) => value && typeof value === 'object');
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
        fields.append(el('div', { class: 'setting' }, [
          el('label', { class: 'setting-key', text: leaf.replaceAll('_', ' '), 'for': `s-${key}`, title: key }),
          this._input(key, value),
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
