import { Widget, el, clear } from '../../lib/dom.js';
import { statusOrb, statusOrbStylesheet } from '../../lib/status-orb.js';

// Thinking lifecycle affordance — native <details>. Provider reasoning
// arrives as ordered turn.reasoning chunks and renders as live Markdown.
// The card stays collapsed until the user opens it. The orb carries the
// live/done signal so a stream does not open and shut the panel.
class ThinkingBlock extends Widget {
  static stylesheets = [new URL('./thinking-block.css', import.meta.url), statusOrbStylesheet];

  constructor() {
    super();
    this._state = { status: 'done', startedAt: null, elapsedMs: null };
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
    this.orb = statusOrb();
    this.label = el('span', { class: 'label' });
    this.summaryEl.append(this.orb.node, this.label, el('span', {
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
    this.details.addEventListener('toggle', () => {
      if (this.details.open && this._bodyPinned) {
        this.bodyEl.scrollTop = this.bodyEl.scrollHeight;
        this._lastBodyTop = this.bodyEl.scrollTop;
      }
    });
    this.root.append(this.details);
  }

  _sync() {
    const s = this._state;
    const active = s.status === 'active';

    this.orb.set(active ? 'live' : 'done');
    this.details.classList.toggle('live', active);
    this.details.setAttribute('aria-busy', active ? 'true' : 'false');
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
  }

  _elapsed() {
    const ms = this._state.elapsedMs;
    if (ms == null) return '—';
    return ms < 1000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(1)}s`;
  }
}

customElements.define('nusa-thinking-block', ThinkingBlock);
