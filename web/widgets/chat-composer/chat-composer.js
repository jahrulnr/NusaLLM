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
  set value(v) { if (this.input) { this.input.value = v || ''; this._autosize(); this._syncButton(); } }
  clear() { this.value = ''; if (this.input) this.input.focus(); }

  _render() {
    this.root.classList.add('composer');
    this.input = el('textarea', {
      class: 'input', rows: '1',
      placeholder: 'Ask anything, or describe a task…',
      'aria-label': 'Message input',
    });
    this.input.addEventListener('input', () => { this._autosize(); this._syncButton(); });
    this.input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        this._send();
      }
    });
    this.button = el('button', { class: 'send', type: 'button' });
    this.statusEl = el('span', { class: 'status' });
    this.contextPct = el('span', { class: 'context-pct' });
    this.contextUsage = el('span', { class: 'context-usage' });
    this.contextFill = el('span', { class: 'context-fill' });
    this.contextDetails = el('details', { class: 'context', hidden: true }, [
      el('summary', { class: 'context-trigger', 'aria-label': 'Context usage' }, [
        el('span', { class: 'context-dot', 'aria-hidden': 'true' }),
        el('span', { text: 'Context' }),
        this.contextPct,
      ]),
      el('div', { class: 'context-popover' }, [
        el('div', { class: 'context-head' }, [
          el('span', { text: 'Context usage' }),
          el('button', {
            class: 'context-close', type: 'button', 'aria-label': 'Close context usage',
            onclick: () => { this.contextDetails.open = false; },
          }, [icon('x', 13)]),
        ]),
        this.contextUsage,
        el('div', { class: 'context-track' }, [this.contextFill]),
        el('div', { class: 'context-caption', text: 'Estimated tokens in this conversation' }),
      ]),
    ]);
    this.root.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') this.contextDetails.open = false;
    });
    this.root.append(
      this.input,
      el('div', { class: 'footer' }, [
        el('div', { class: 'footer-left' }, [
          this.contextDetails,
          this.statusEl,
        ]),
        el('div', { class: 'footer-right' }, [
          el('span', { class: 'shortcut', text: 'Enter to send · Shift+Enter for new line' }),
          this.button,
        ]),
      ]),
    );
    this._syncButton();
  }

  _autosize() {
    this.input.style.height = 'auto';
    this.input.style.height = Math.min(this.input.scrollHeight, 220) + 'px';
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
      this.button.disabled = !this._canSend || this._connection !== 'open' || !this.input.value.trim();
      this.button.onclick = () => this._send();
      this.button.append(icon('arrow-up', 16));
    }
    const reason = this._busy ? 'Responding…'
      : !this._canSend ? 'Select a model to send'
      : '';
    this.statusEl.textContent = reason;
    this.statusEl.hidden = !reason;
    const c = this._context;
    this.contextDetails.hidden = !c?.total;
    if (c?.total) {
      const pct = Math.max(0, Math.min(100, Math.round((c.estimate || 0) / c.total * 100)));
      this.contextPct.textContent = `${pct}%`;
      this.contextUsage.textContent = `~${fmtTok(c.estimate)} / ${fmtTok(c.total)} tokens · ${pct}% used`;
      this.contextFill.style.width = `${pct}%`;
    } else {
      this.contextDetails.open = false;
    }
  }
}

customElements.define('nusa-chat-composer', ChatComposer);
