import { Widget, el, emit, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';
import {
  headline, restartNote, percent, sizeLabel, stepStates,
} from '../../lib/update-install.js';

// The in-app update's progress popup: the install step by step, the byte bar
// while downloading, and the restart in plain words. It never starts or stops
// the install (the server owns that), so closing it leaves the update running
// and About can reopen it. Backdrop clicks do not close it; Escape and the
// close control do.
const RETRYABLE = ['download', 'verify', 'install'];

class UpdateProgress extends Widget {
  static stylesheet = new URL('./update-progress.css', import.meta.url);

  constructor() {
    super();
    this._view = { state: 'idle' };
    this.primaryBtn = null;
    this.dialog = el('dialog', { class: 'dialog', 'aria-labelledby': 'progress-title' });
    this.panel = el('div', { class: 'panel' });
    this.dialog.append(this.panel);
    this.root.append(this.dialog);
    this._render();
  }

  set view(value) {
    this._view = value || { state: 'idle' };
    this._render();
  }

  get view() {
    return this._view;
  }

  open() {
    if (this._view.state === 'idle') return; // no install to show
    this._render();
    if (!this.dialog.open) this.dialog.showModal();
    (this.primaryBtn || this.closeX).focus();
  }

  close() {
    if (this.dialog.open) this.dialog.close();
  }

  _render() {
    clear(this.panel);
    const v = this._view;
    this.primaryBtn = null;
    if (v.state === 'idle') return;

    const pct = percent(v);
    const steps = stepStates(v);
    const restarting = steps.some((s) => s.id === 'restart' && s.state === 'active');
    this.closeX = el('button', {
      class: 'close', type: 'button', 'aria-label': 'Close',
      onclick: () => this.close(),
    }, [icon('x', 16)]);

    // append() prints a null slot as the text "null", so the empty ones are dropped first.
    this.panel.append(...[
      el('header', { class: 'head' }, [
        el('span', { class: 'mark', 'aria-hidden': 'true' }, [icon(v.state === 'succeeded' ? 'check' : 'arrow-up', 16)]),
        el('div', { class: 'heading' }, [
          el('h2', { id: 'progress-title', class: 'title', text: headline(v) }),
          el('p', { class: 'sub', text: v.from ? `${v.from} → ${v.version}` : (v.version || '') }),
        ]),
        this.closeX,
      ]),
      pct === null ? null : this._bar(v, pct),
      el('ol', { class: 'steps' }, steps.map((s) => el('li', { class: `step ${s.state}` }, [
        el('span', { class: 'step-mark', 'aria-hidden': 'true' }, [markOf(s.state)]),
        el('span', { class: 'step-label', text: s.label }),
      ]))),
      restarting ? el('p', { class: 'note', role: 'status', text: restartNote(v) }) : null,
      v.state === 'failed' && v.error ? el('p', { class: 'error', role: 'alert', text: v.error }) : null,
      this._footer(v),
    ].filter(Boolean));
  }

  _bar(v, pct) {
    return el('div', { class: 'bar-wrap' }, [
      el('div', {
        class: 'bar', role: 'progressbar', 'aria-label': 'Download progress',
        'aria-valuemin': '0', 'aria-valuemax': '100', 'aria-valuenow': String(pct),
      }, [el('span', { class: 'fill', style: `width: ${pct}%` })]),
      el('div', {
        class: 'bar-meta',
        text: `${sizeLabel(v.bytesDone)} of ${sizeLabel(v.bytesTotal)} · ${pct}%`,
      }),
    ]);
  }

  _footer(v) {
    const close = el('button', {
      class: 'btn ghost', type: 'button', onclick: () => this.close(),
    }, [el('span', { text: 'Close' })]);
    if (v.state === 'succeeded') {
      this.primaryBtn = el('button', {
        class: 'btn primary', type: 'button', onclick: () => emit(this, 'update-reload'),
      }, [el('span', { text: 'Reload NusaLLM' })]);
      return el('footer', { class: 'actions' }, [el('span', { class: 'spacer' }), close, this.primaryBtn]);
    }
    if (v.state === 'failed') {
      if (RETRYABLE.includes(v.phase)) {
        this.primaryBtn = el('button', {
          class: 'btn primary', type: 'button', onclick: () => emit(this, 'update-retry'),
        }, [el('span', { text: 'Try again' })]);
        return el('footer', { class: 'actions' }, [
          el('p', { class: 'hint', text: 'Nothing was installed. The current version keeps running.' }),
          el('span', { class: 'spacer' }), close, this.primaryBtn,
        ]);
      }
      return el('footer', { class: 'actions' }, [
        el('p', { class: 'hint', text: 'The new version is installed. Restart NusaLLM to run it.' }),
        el('span', { class: 'spacer' }), close,
      ]);
    }
    return el('footer', { class: 'actions' }, [
      el('p', { class: 'hint', text: 'Closing this window does not stop the update.' }),
      el('span', { class: 'spacer' }), close,
    ]);
  }
}

function markOf(state) {
  if (state === 'done') return icon('check', 12);
  if (state === 'failed') return icon('x', 12);
  if (state === 'active') return el('span', { class: 'spin' });
  return el('span', { class: 'dot' });
}

customElements.define('nusa-update-progress', UpdateProgress);
