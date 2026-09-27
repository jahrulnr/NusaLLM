import { Widget, el, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

// Read-only tool catalog: name, description, permission, enabled.
// Runtime tool I/O never reaches this widget — the API doesn't ship it.
class ToolCatalog extends Widget {
  static stylesheet = new URL('./tool-catalog.css', import.meta.url);

  constructor() {
    super();
    this._tools = [];
    this._render();
  }

  set tools(list) { this._tools = list || []; this._render(); }

  _render() {
    clear(this.root);
    this.root.classList.add('tools');
    if (!this._tools.length) {
      this.root.append(el('div', { class: 'hint', text: 'No tools registered.' }));
      return;
    }
    for (const t of this._tools) {
      this.root.append(el('div', { class: `tool${t.enabled ? '' : ' off'}` }, [
        icon('tool', 12),
        el('div', { class: 'tool-main' }, [
          el('span', { class: 'tool-name', text: t.name }),
          t.description ? el('span', { class: 'tool-desc', text: t.description }) : null,
        ]),
        el('span', { class: `perm ${t.permission || ''}`, text: t.enabled ? t.permission : 'disabled' }),
      ]));
    }
  }
}

customElements.define('nusa-tool-catalog', ToolCatalog);
