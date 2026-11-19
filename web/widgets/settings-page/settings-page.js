import { Widget, el, emit, clear, deepActiveElement, focusKeyOf, findByFocusKey } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';
import { ago } from '../../lib/update-state.js';

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
  set gatewayDeleteResult(value) { this.providers.gatewayDeleteResult = value; }
  set settings(value) { this.editor.settings = value || {}; }
  // manage-models entry point: the sections start collapsed, so the
  // provider section opens before the gateway focus delegates down.
  set manageModelsFor(id) {
    this.expandProviders();
    this.providers.modelsFor = id;
  }
  // Opens the provider section on its own, so first-run entry points land on
  // the add-gateway controls without a gateway in context.
  expandProviders() {
    this.providers.closest('details').open = true;
  }
  // Focus lands inside the newly opened page — the Back control is both
  // the first interactive element and the way out, so a keyboard or
  // screen-reader user starts in the view that just replaced the chat.
  focusInitial() { this._back?.focus?.(); }

  // The update status (null until the first answer; `skipped` marks a version
  // the person hid), whether a manual check is on the wire, and the install in
  // progress, if any. The shell owns all three; About only shows them.
  set update(value) { this._update = value || null; this._renderAbout(); }
  set updateBusy(v) { this._updateBusy = !!v; this._renderAbout(); }
  set updateInstall(view) { this._install = view || null; this._renderAbout(); }

  _render() {
    clear(this.root);
    this.root.classList.add('page');
    this.providers = el('nusa-provider-manager');
    this.editor = el('nusa-settings-editor');
    // tabindex=-1: the section is the parking spot while the focused
    // action rebuilds itself disabled mid-flight (a check on the wire).
    this.about = el('div', { class: 'about', tabindex: '-1' });
    this.root.append(
      el('header', { class: 'top' }, [
        el('div', { class: 'top-inner' }, [
          this._back = el('button', {
            class: 'back', type: 'button', 'aria-label': 'Back to conversation',
            onclick: () => emit(this, 'settings-close'),
          }, [icon('chevron', 16)]),
          el('h1', { text: 'Settings' }),
        ]),
      ]),
      el('div', { class: 'content' }, [
        el('details', { class: 'section', open: true }, [
          el('summary', { text: 'Providers & models' }),
          el('div', { class: 'section-body' }, [this.providers]),
        ]),
        el('details', { class: 'section', open: true }, [
          el('summary', { text: 'Application' }),
          el('div', { class: 'section-body' }, [this.editor]),
        ]),
        el('details', { class: 'section', open: true }, [
          el('summary', { text: 'About' }),
          el('div', { class: 'section-body' }, [this.about]),
        ]),
      ]),
    );
    this._renderAbout();
  }

  // Version and update status. Every state reads in plain words, and each
  // state offers the next step: install, follow an install, see the notes, or
  // check again.
  _renderAbout() {
    if (!this.about) return;
    // Status renders rebuild the whole section — a focused action dies
    // with it. Its live successor inherits focus by key; while that
    // successor is disabled mid-flight the key queues and the section
    // itself holds focus so the keyboard stays inside the page.
    const prev = deepActiveElement();
    // The section itself is the parking spot while a disabled successor
    // waits — it is not user focus; only a real descendant supersedes a
    // queued restore.
    let focusKey = prev && prev !== this.about && this.about.contains?.(prev) ? focusKeyOf(prev) : null;
    if (focusKey) this._aboutFocus = null;
    else if (this._aboutFocus) focusKey = this._aboutFocus;
    clear(this.about);
    const info = this._update;
    this.about.append(
      this._aboutRow('Version', [el('span', { text: info ? (info.current || '—') : 'Loading…' })]),
      this._aboutRow('Updates', this._updatesValue(info)),
      this._aboutRow('Last checked', this._checkedValue(info)),
    );
    if (focusKey) {
      const hit = findByFocusKey(this.about, focusKey);
      if (hit && !hit.disabled) {
        hit.focus?.();
        this._aboutFocus = null;
      } else if (hit) {
        this._aboutFocus = focusKey;
        this.about.focus?.();
      } else {
        // The focused action was removed — Update now leaves when the
        // release is no longer available. Check is the control that stays.
        const check = findByFocusKey(this.about, 'update-check');
        if (check && !check.disabled) {
          check.focus?.();
          this._aboutFocus = null;
        } else if (check) {
          this._aboutFocus = 'update-check';
          this.about.focus?.();
        } else {
          this._aboutFocus = null;
        }
      }
    }
  }

  _aboutRow(label, value) {
    return el('div', { class: 'about-row' }, [
      el('div', { class: 'about-label', text: label }),
      el('div', { class: 'about-value' }, value),
    ]);
  }

  _checkedValue(info) {
    const checked = info ? (ago(info.checkedAt) || 'never') : '';
    const nodes = [el('span', { text: checked })];
    // Any release build can re-check, including one whose first load failed
    // (that status has no `enabled` field). Only dev builds have no check.
    if (info && info.enabled !== false) {
      const busy = !!this._updateBusy;
      nodes.push(el('button', {
        class: 'about-btn', type: 'button', disabled: busy || null,
        'data-id': 'update-check',
        onclick: () => emit(this, 'update-check'),
      }, [icon('refresh', 14), el('span', { text: busy ? 'Checking…' : 'Check for updates' })]));
    }
    return nodes;
  }

  // One state wins, in this order: an install in flight, then a newer release,
  // then a failed check, then a dev build, then up to date.
  _updatesValue(info) {
    if (!info) return [el('span', { text: 'Checking…' })];
    const install = this._install;
    if (install && install.state === 'running') {
      return [
        el('span', { class: 'about-strong', text: `Updating to ${install.version}…` }),
        el('div', { class: 'about-actions' }, [
          this._aboutButton('View progress', 'accent', () => emit(this, 'update-progress-open'), null, 'progress'),
        ]),
      ];
    }
    if (info.available) return this._availableValue(info);
    if (info.error) {
      return [
        el('span', { class: 'about-warn', text: "Couldn't check for updates." }),
        el('div', { class: 'about-note', text: info.error }),
      ];
    }
    if (info.enabled === false) {
      return [el('span', { class: 'about-note', text: 'Update checks run in release builds only.' })];
    }
    return [el('span', { text: info.latest ? `Up to date (${info.latest}).` : 'Not checked yet.' })];
  }

  _availableValue(info) {
    const actions = [];
    if (info.installable) {
      actions.push(this._aboutButton('Update now', 'accent', () => emit(this, 'update-install', { version: info.latest }), 'arrow-up', 'install'));
    }
    actions.push(this._aboutButton("What's new", '', () => emit(this, 'update-open'), null, 'notes'));
    if (info.skipped) {
      actions.push(this._aboutButton('Show again', '', () => emit(this, 'update-unskip'), null, 'unskip'));
    }
    return [
      el('span', { class: 'about-strong', text: `NusaLLM ${info.latest} is available.` }),
      info.skipped ? el('span', { class: 'about-note', text: 'You skipped this version.' }) : null,
      info.installable ? null : el('span', { class: 'about-note', text: "This build cannot replace itself. What's new has the install command." }),
      info.error ? el('span', { class: 'about-note', text: `Last check failed: ${info.error}` }) : null,
      el('div', { class: 'about-actions' }, actions),
    ];
  }

  _aboutButton(label, tone, onclick, iconName = null, key = null) {
    return el('button', {
      class: `about-btn${tone ? ` ${tone}` : ''}`, type: 'button', 'data-id': key, onclick,
    }, [iconName ? icon(iconName, 14) : null, el('span', { text: label })]);
  }
}

customElements.define('nusa-settings-page', SettingsPage);
