import { Widget, el, clear } from '../../lib/dom.js';

// Thinking lifecycle affordance — native <details>. Provider reasoning
// arrives as ordered turn.reasoning chunks and renders as live Markdown.
class ThinkingBlock extends Widget {
  static stylesheet = new URL('./thinking-block.css', import.meta.url);

  constructor() {
    super();
    this._state = { status: 'done', startedAt: null, elapsedMs: null };
    this._userToggled = false;
    this._renderedText = '';
    this._bodyPinned = true;
    this._lastBodyTop = 0;
    this._build();
    this._resize = new ResizeObserver(() => {
      if (this._bodyPinned) {
        this.bodyEl.scrollTop = this.bodyEl.scrollHeight;
        this._lastBodyTop = this.bodyEl.scrollTop;
      }
    });
    this._resize.observe(this.summaryText);
    this._sync();
  }

  connectedCallback() { this._resize.observe(this.summaryText); }
  disconnectedCallback() { this._resize.disconnect(); }

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
    this.reasoning = document.createElement('nusa-markdown-view');
    this.summaryText = el('div', { class: 'summary-text' }, [this.reasoning]);
    this.bodyEl.append(this.summaryText);
    this.bodyEl.addEventListener('scroll', () => {
      const top = this.bodyEl.scrollTop;
      const atEnd = top + this.bodyEl.clientHeight >= this.bodyEl.scrollHeight - 2;
      if (atEnd) this._bodyPinned = true;
      else if (top < this._lastBodyTop) this._bodyPinned = false;
      this._lastBodyTop = top;
    });
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

    // Append only the new suffix so completed Markdown stays mounted.
    const text = s.text || '';
    if (text.startsWith(this._renderedText)) {
      this.reasoning.appendStreamText(text.slice(this._renderedText.length));
    } else {
      this.reasoning.content = text;
    }
    if (!active) this.reasoning.finishStream();
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
