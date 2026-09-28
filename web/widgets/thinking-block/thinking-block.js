import { Widget, el, clear } from '../../lib/dom.js';

// Thinking lifecycle affordance — native <details>. Provider reasoning
// arrives as ordered turn.reasoning chunks and remains inert plain text.
class ThinkingBlock extends Widget {
  static stylesheet = new URL('./thinking-block.css', import.meta.url);

  constructor() {
    super();
    this._state = { status: 'done', startedAt: null, elapsedMs: null };
    this._userToggled = false;
    this._renderedText = '';
    this._build();
    this._sync();
  }

  // state = {status:'active'|'done', startedAt:number|null, elapsedMs:number|null, text:string}
  set state(s) {
    this._state = s || { status: 'done', startedAt: null, elapsedMs: null, text: '' };
    this._sync();
  }

  _build() {
    clear(this.root);
    this.root.classList.add('thinking');
    this.details = el('details', { class: 'details' });
    this.summaryEl = el('summary', { class: 'summary' });
    this.orb = el('span', { class: 'orb', 'aria-hidden': 'true' });
    this.label = el('span', { class: 'label' });
    this.summaryEl.append(this.orb, this.label, el('span', {
      class: 'chev', 'aria-hidden': 'true', text: '›',
    }));
    this.bodyEl = el('div', { class: 'body' });
    this.reasoningText = document.createTextNode('');
    this.summaryText = el('div', { class: 'summary-text' }, [this.reasoningText]);
    this.bodyEl.append(this.summaryText);
    this.details.append(this.summaryEl, this.bodyEl);
    // Gesture listeners (not the async 'toggle' event) — once the user
    // chooses, their state wins over the status-derived default.
    const markToggled = () => { this._userToggled = true; };
    this.summaryEl.addEventListener('click', markToggled);
    this.summaryEl.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') markToggled();
    });
    this.root.append(this.details);
  }

  _sync() {
    const s = this._state;
    const active = s.status === 'active';

    this.orb.className = `orb${active ? ' active' : ''}`;
    this.label.textContent = active ? 'Thinking' : s.elapsedMs == null ? 'Thought' : `Thought for ${this._elapsed()}`;

    // Provider chunks are plain text. Append only the new suffix.
    const text = s.text || '';
    if (text.startsWith(this._renderedText)) {
      this.reasoningText.appendData(text.slice(this._renderedText.length));
    } else {
      this.reasoningText.data = text;
    }
    this._renderedText = text;
    this.summaryText.hidden = !text;

    if (!this._userToggled) this.details.open = active;
  }

  _elapsed() {
    const ms = this._state.elapsedMs;
    if (ms == null) return '—';
    return ms < 1000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(1)}s`;
  }
}

customElements.define('nusa-thinking-block', ThinkingBlock);
