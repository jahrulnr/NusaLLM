import { Widget, el, emit, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

// Full workspace page for gateway, model, and application configuration.
class SettingsPage extends Widget {
  static stylesheet = new URL('./settings-page.css', import.meta.url);

  constructor() {
    super();
    this._render();
  }

  set gateways(value) { this.providers.gateways = value || []; }
  set gatewayImportResult(value) { this.providers.gatewayImportResult = value; }
  set gatewaySaveResult(value) { this.providers.gatewaySaveResult = value; }
  set settings(value) { this.editor.settings = value || {}; }

  _render() {
    clear(this.root);
    this.root.classList.add('page');
    this.providers = el('nusa-provider-manager');
    this.editor = el('nusa-settings-editor');
    this.root.append(
      el('header', { class: 'top' }, [
        el('div', { class: 'top-inner' }, [
          el('button', {
            class: 'back', type: 'button', 'aria-label': 'Back to conversation',
            onclick: () => emit(this, 'settings-close'),
          }, [icon('chevron', 16)]),
          el('h1', { text: 'Settings' }),
        ]),
      ]),
      el('div', { class: 'content' }, [
        el('details', { class: 'section' }, [
          el('summary', { text: 'Providers & models' }),
          el('div', { class: 'section-body' }, [this.providers]),
        ]),
        el('details', { class: 'section' }, [
          el('summary', { text: 'Application' }),
          el('div', { class: 'section-body' }, [this.editor]),
        ]),
      ]),
    );
  }
}

customElements.define('nusa-settings-page', SettingsPage);
