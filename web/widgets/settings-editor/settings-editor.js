import { Widget, el, emit, clear } from '../../lib/dom.js';

// Settings leaf editor: renders every projected leaf
// ("section.leaf" -> value), commits on change, and emits
// setting-change {key,value}. Durations render as text; booleans as
// checkboxes; numbers as number inputs.
class SettingsEditor extends Widget {
  static stylesheet = new URL('./settings-editor.css', import.meta.url);

  constructor() {
    super();
    this._settings = {};
    this._render();
  }

  set settings(doc) { this._settings = doc || {}; this._render(); }

  _leaves() {
    const out = [];
    for (const [section, obj] of Object.entries(this._settings || {})) {
      if (!obj || typeof obj !== 'object') continue;
      for (const [leaf, value] of Object.entries(obj)) {
        out.push({ key: `${section}.${leaf}`, value });
      }
    }
    return out;
  }

  _render() {
    clear(this.root);
    this.root.classList.add('settings');
    const leaves = this._leaves();
    if (!leaves.length) {
      this.root.append(el('div', { class: 'hint', text: 'No settings exposed.' }));
      return;
    }
    for (const { key, value } of leaves) {
      this.root.append(el('div', { class: 'setting' }, [
        el('label', { class: 'setting-key', text: key, 'for': `s-${key}` }),
        this._input(key, value),
      ]));
    }
  }

  _input(key, value) {
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
