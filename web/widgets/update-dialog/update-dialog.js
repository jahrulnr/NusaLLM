import { Widget, el, emit, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';
import { releaseBody, releasedOn } from '../../lib/update-state.js';

const COPIED_MS = 2500;

// "Update available": what changed, the action that installs it, and the ways
// out. Update now starts the in-app install and the progress popup takes over.
// Where this build cannot replace itself, the same slot copies the install
// command instead. A native <dialog>: focus stays inside and Escape closes it.
class UpdateDialog extends Widget {
  static stylesheet = new URL('./update-dialog.css', import.meta.url);

  constructor() {
    super();
    this._info = null;
    this._copiedTimer = null;

    this.dialog = el('dialog', {
      class: 'dialog',
      'aria-labelledby': 'update-title',
      onclick: (e) => { if (e.target === this.dialog) this.close(); },
    });
    this.body = el('div', { class: 'panel' });
    this.dialog.append(this.body);
    this.root.append(this.dialog);
    this._render();
  }

  set info(value) {
    this._info = value || null;
    this._render();
  }

  open() {
    if (!this._info || !this._info.latest) return; // no release to show
    this._render();
    if (!this.dialog.open) this.dialog.showModal();
    (this.primaryBtn || this.closeX).focus();
  }

  close() {
    if (this.dialog.open) this.dialog.close();
  }

  async _copy() {
    const command = this._info && this._info.installCommand;
    if (!command) return;
    try {
      await navigator.clipboard.writeText(command);
      this._setCopied(true);
    } catch {
      this._setCopied(false);
    }
  }

  // ok: true after a copy (show "Copied"), false after a blocked copy (say how
  // to copy by hand), null when the flash times out (back to idle).
  _setCopied(ok) {
    clearTimeout(this._copiedTimer);
    if (this.statusEl) {
      this.statusEl.textContent = ok === false ? 'Copy was blocked. Select the command above and copy it.' : '';
    }
    if (this.copyControl) {
      this.copyControl.classList.toggle('done', ok === true);
      this.copyText.textContent = ok === true ? 'Copied' : this.copyIdle;
    }
    if (ok === true) {
      this._copiedTimer = setTimeout(() => this._setCopied(null), COPIED_MS);
    }
  }

  _skip() {
    emit(this, 'update-skip', { version: this._info.latest });
    this.close();
  }

  _install() {
    emit(this, 'update-install', { version: this._info.latest });
    this.close();
  }

  _render() {
    clear(this.body);
    this.primaryBtn = null;
    this.copyControl = null;
    this.statusEl = null;
    const info = this._info;
    if (!info || !info.latest) return;

    const installable = !!info.installable;
    const released = releasedOn(info.publishedAt);
    const notes = releaseBody(info.notes);

    this.closeX = el('button', {
      class: 'close', type: 'button', 'aria-label': 'Close', onclick: () => this.close(),
    }, [icon('x', 16)]);

    // One copy control. Installable builds keep it small beside the command and
    // put Update now in the footer. Builds that cannot install make it the primary.
    this.copyIdle = installable ? 'Copy command' : 'Copy install command';
    this.copyText = el('span', { text: this.copyIdle });
    const copyButton = el('button', {
      class: installable ? 'btn ghost small' : 'btn primary',
      type: 'button', onclick: () => this._copy(),
    }, [icon('copy', 13), this.copyText]);
    this.copyControl = copyButton;
    this.primaryBtn = installable
      ? el('button', {
        class: 'btn primary', type: 'button', onclick: () => this._install(),
      }, [icon('arrow-up', 14), el('span', { text: 'Update now' })])
      : copyButton;

    const releaseLink = info.releaseUrl
      ? el('a', {
        class: 'release-link', href: info.releaseUrl, target: '_blank', rel: 'noopener noreferrer',
      }, [el('span', { text: 'View release' }), icon('external', 12)])
      : null;

    this.statusEl = el('div', { class: 'status', role: 'status', 'aria-live': 'polite' });

    const notesEl = el('nusa-markdown-view', { class: 'notes-view' });
    notesEl.content = notes || 'No release notes were published for this version.';

    this.body.append(
      el('span', { class: 'grip', 'aria-hidden': 'true' }),
      el('header', { class: 'head' }, [
        el('span', { class: 'mark', 'aria-hidden': 'true' }, [icon('arrow-up', 16)]),
        el('div', { class: 'heading' }, [
          el('h2', { id: 'update-title', class: 'title', text: 'Update available' }),
          el('p', { class: 'sub', text: `NusaLLM ${info.latest} is ready to install.` }),
        ]),
        this.closeX,
      ]),
      el('div', { class: 'versions' }, [
        el('span', { class: 'from', text: info.current }),
        icon('chevron', 12),
        el('span', { class: 'to', text: info.latest }),
        el('span', { class: 'badge', text: 'Latest' }),
        released ? el('span', { class: 'released', text: `Released ${released}` }) : null,
      ]),
      el('section', { class: 'block' }, [
        el('div', { class: 'block-head' }, [
          el('h3', { class: 'label', text: "What's new" }),
          releaseLink,
        ]),
        el('div', { class: 'notes' }, [notesEl]),
      ]),
      el('section', { class: 'block' }, [
        el('div', { class: 'block-head' }, [
          el('h3', { class: 'label', text: 'Install in a terminal' }),
          installable ? copyButton : null,
        ]),
        el('pre', { class: 'command' }, [el('code', { text: info.installCommand || '' })]),
        el('p', {
          class: 'hint',
          text: installable
            ? 'Update now installs this release in place and restarts NusaLLM. The command does the same from a terminal.'
            : 'This build cannot replace itself. Run the command in a terminal to install the release.',
        }),
      ]),
      this.statusEl,
      el('footer', { class: 'actions' }, [
        el('button', {
          class: 'btn ghost', type: 'button', onclick: () => this._skip(),
        }, [el('span', { text: 'Skip this version' })]),
        el('span', { class: 'spacer' }),
        el('button', {
          class: 'btn ghost', type: 'button', onclick: () => this.close(),
        }, [el('span', { text: 'Later' })]),
        this.primaryBtn,
      ]),
    );
  }
}

customElements.define('nusa-update-dialog', UpdateDialog);
