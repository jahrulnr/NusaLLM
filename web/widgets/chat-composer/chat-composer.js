import { Widget, el, emit } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

// Composer: autosizing textarea, Enter sends / Shift+Enter newline,
// busy swaps the send button for a cancel. The buffer is only cleared
// by clear() — the shell calls it once turn.opened lands, so a
// pre-open command error never eats the draft.
class ChatComposer extends Widget {
  static stylesheet = new URL('./chat-composer.css', import.meta.url);

  constructor() {
    super();
    this._busy = false;
    this._connection = 'closed';
    this._selection = null;
    this._canSend = false;
    this._render();
  }

  set busy(v) { this._busy = !!v; this._syncButton(); }
  set connection(state) { this._connection = state; this._syncButton(); }
  set selection(sel) { this._selection = sel; this._syncButton(); }
  set canSend(v) { this._canSend = !!v; this._syncButton(); }

  get value() { return this.input ? this.input.value : ''; }
  set value(v) { if (this.input) { this.input.value = v || ''; this._autosize(); } }
  clear() { this.value = ''; if (this.input) this.input.focus(); }

  _render() {
    this.root.classList.add('composer');
    this.input = el('textarea', {
      class: 'input', rows: '1',
      placeholder: 'Message the model — Enter sends, Shift+Enter breaks a line',
      'aria-label': 'Message input',
    });
    this.input.addEventListener('input', () => this._autosize());
    this.input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        this._send();
      }
    });
    this.button = el('button', { class: 'send', type: 'button' });
    this.statusEl = el('div', { class: 'status' });
    this.root.append(
      el('div', { class: 'row' }, [this.input, this.button]),
      this.statusEl,
    );
    this._syncButton();
  }

  _autosize() {
    this.input.style.height = 'auto';
    this.input.style.height = Math.min(this.input.scrollHeight, 200) + 'px';
  }

  _send() {
    const content = this.input.value.trim();
    if (!content || this._busy || !this._canSend || this._connection !== 'open') return;
    emit(this, 'turn-send', { content });
  }

  _syncButton() {
    if (!this.button) return;
    this.button.replaceChildren();
    if (this._busy) {
      this.button.className = 'send cancel';
      this.button.setAttribute('aria-label', 'Cancel turn');
      this.button.disabled = false;
      this.button.onclick = () => emit(this, 'turn-cancel');
      this.button.append(icon('stop', 13));
    } else {
      this.button.className = 'send';
      this.button.setAttribute('aria-label', 'Send message');
      this.button.disabled = !this._canSend || this._connection !== 'open';
      this.button.onclick = () => this._send();
      this.button.append(icon('send', 13));
    }
    const reason = this._busy ? 'responding…'
      : this._connection !== 'open' ? 'socket ' + this._connection
      : !this._canSend ? 'select a model'
      : '';
    this.statusEl.textContent = reason;
  }
}

customElements.define('nusa-chat-composer', ChatComposer);
