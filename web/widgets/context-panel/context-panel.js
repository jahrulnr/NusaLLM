import { Widget, el, emit, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

const TABS = ['overview', 'providers', 'settings', 'tools'];

// Right inspector drawer. Tabs compose the provider/settings/tool
// widgets; their semantic events bubble (composed) straight through to
// the shell. Overview is a compact read of the active conversation.
class ContextPanel extends Widget {
  static stylesheet = new URL('./context-panel.css', import.meta.url);

  constructor() {
    super();
    this._open = false;
    this._tab = 'overview';
    this._conversation = null;
    this._gateways = [];
    this._settings = {};
    this._tools = [];
    this._render();
  }

  // Host attribute mutations belong here — a custom element's
  // constructor may not touch its own attributes. Born closed:
  // inert + aria-hidden apply on attach.
  connectedCallback() { this.open = this._open; }

  set open(v) {
    this._open = !!v;
    this.classList.toggle('open', this._open);
    // A closed drawer must leave the tab order entirely, not just slide
    // offscreen.
    this.inert = !this._open;
    this.setAttribute('aria-hidden', this._open ? 'false' : 'true');
  }
  get open() { return this._open; }
  set tab(t) { if (TABS.includes(t)) { this._tab = t; this._render(); } }
  set conversation(c) { this._conversation = c; this._renderTab(); }
  set gateways(list) { this._gateways = list || []; this._renderTab(); }
  set settings(doc) { this._settings = doc || {}; this._renderTab(); }
  set tools(list) { this._tools = list || []; this._renderTab(); }

  _render() {
    clear(this.root);
    this.root.classList.add('panel');

    this.root.append(el('div', { class: 'head' }, [
      el('div', { class: 'tabs', role: 'tablist' }, TABS.map((t) =>
        el('button', {
          class: `tab${t === this._tab ? ' active' : ''}`,
          type: 'button', role: 'tab',
          'aria-selected': t === this._tab ? 'true' : 'false',
          onclick: () => { this._tab = t; this._render(); },
        }, [el('span', { text: t })]))),
      el('button', {
        class: 'icon-btn', type: 'button', 'aria-label': 'Close context panel',
        onclick: () => emit(this, 'context-close'),
      }, [icon('x', 13)]),
    ]));

    this.body = el('div', { class: 'body' });
    this.root.append(this.body);
    this._renderTab();
  }

  _renderTab() {
    if (!this.body) return;
    clear(this.body);
    switch (this._tab) {
      case 'providers': {
        const w = el('nusa-provider-manager');
        w.gateways = this._gateways;
        this.body.append(w);
        break;
      }
      case 'settings': {
        const w = el('nusa-settings-editor');
        w.settings = this._settings;
        this.body.append(w);
        break;
      }
      case 'tools': {
        const w = el('nusa-tool-catalog');
        w.tools = this._tools;
        this.body.append(w);
        break;
      }
      default:
        this._overview();
    }
  }

  _overview() {
    const c = this._conversation;
    if (!c) {
      this.body.append(el('div', { class: 'hint', text: 'No conversation selected.' }));
      return;
    }
    const rows = [
      ['id', c.id],
      ['model', c.modelId ? `${c.route ? c.route + ' · ' : ''}${c.modelId}` : '—'],
      ['messages', String(c.messageCount ?? 0)],
      ['live-tail messages', String(c.activeCount ?? 0)],
      ['sealed segments', String(c.sealedSegments ?? 0)],
    ];
    this.body.append(
      el('dl', { class: 'facts' }, rows.flatMap(([k, v]) => [
        el('dt', { text: k }), el('dd', { text: v }),
      ])),
    );
  }
}

customElements.define('nusa-context-panel', ContextPanel);
