import { Widget, el, emit, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

// Full workspace page for gateway, model, app, and tool configuration.
class SettingsPage extends Widget {
  static stylesheet = new URL('./settings-page.css', import.meta.url);

  constructor() {
    super();
    this._render();
  }

  set gateways(value) { this.providers.gateways = value || []; }
  set discoveredModels(value) { this.providers.discoveredModels = value; }
  set gatewaySaveResult(value) { this.providers.gatewaySaveResult = value; }
  set settings(value) { this.editor.settings = value || {}; }
  set tools(value) { this.catalog.tools = value || []; }

  _render() {
    clear(this.root);
    this.root.classList.add('page');
    this.providers = el('nusa-provider-manager');
    this.editor = el('nusa-settings-editor');
    this.catalog = el('nusa-tool-catalog');
    this.root.append(
      el('header', { class: 'top' }, [
        el('button', {
          class: 'back', type: 'button', 'aria-label': 'Back to conversation',
          onclick: () => emit(this, 'settings-close'),
        }, [icon('chevron', 15), el('span', { text: 'Back' })]),
        el('h1', { text: 'Settings' }),
      ]),
      el('div', { class: 'content' }, [
        el('section', { class: 'section', 'aria-label': 'Providers and models' }, [
          el('h2', { text: 'Providers & models' }), this.providers,
        ]),
        el('section', { class: 'section', 'aria-label': 'Application settings' }, [
          el('h2', { text: 'Application' }), this.editor,
        ]),
        el('section', { class: 'section', 'aria-label': 'Available tools' }, [
          el('h2', { text: 'Tools' }), this.catalog,
        ]),
      ]),
    );
  }
}

customElements.define('nusa-settings-page', SettingsPage);
