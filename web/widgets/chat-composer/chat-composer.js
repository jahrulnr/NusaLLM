import { Widget, el, emit } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

// Composer: autosizing textarea, Enter sends / Shift+Enter newline,
// busy swaps the send button for a cancel. The buffer is only cleared
// by clear() — the shell calls it once turn.opened lands, so a
// pre-open command error never eats the draft.
// Decimal-K token abbreviation — mirrors Go's FormatScaledTokens so the
// footer matches the TUI's rendering.
function fmtTok(n) {
  if (!n || n <= 0) return '0';
  if (n >= 1e6) {
    const whole = Math.floor(n / 1e6);
    const frac = Math.floor(((n % 1e6) * 10) / 1e6);
    return frac === 0 ? `${whole}M` : `${whole}.${frac}M`;
  }
  if (n >= 1e3) return `${n % 1e3 === 0 ? n / 1e3 : Math.round(n / 1e3)}K`;
  return String(n);
}

class ChatComposer extends Widget {
  static stylesheet = new URL('./chat-composer.css', import.meta.url);

  constructor() {
    super();
    this._busy = false;
    this._connection = 'closed';
    this._selection = null;
    this._canSend = false;
    this._context = null; // {estimate, total} token stats
    this._render();
  }

  set busy(v) { this._busy = !!v; this._syncButton(); }
  set connection(state) { this._connection = state; this._syncButton(); }
  set selection(sel) { this._selection = sel; this._syncButton(); }
  set canSend(v) { this._canSend = !!v; this._syncButton(); }
  set context(stats) { this._context = stats || null; this._syncButton(); }

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
    this.tokensEl = el('div', { class: 'tokens', 'aria-label': 'Context usage' });
    this.root.append(
      el('div', { class: 'row' }, [this.input, this.button]),
      el('div', { class: 'footer' }, [this.statusEl, this.tokensEl]),
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
    const c = this._context;
    this.tokensEl.textContent = c && c.total
      ? `~${fmtTok(c.estimate)}/${fmtTok(c.total)} Tokens`
      : '';
  }
}

customElements.define('nusa-chat-composer', ChatComposer);
