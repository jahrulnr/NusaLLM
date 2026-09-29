import { Widget, el, clear } from '../../lib/dom.js';
import { statusOrb, statusOrbStylesheet } from '../../lib/status-orb.js';
import { toolOrbPhase } from '../../lib/tool-phase.js';

// One tool call rendered with owner-visible I/O. Input and output stay
// exact and are inserted as text via code-block, never interpreted as
// HTML/Markdown. Statuses stay honest protocol
// states: `requested` is a request, `streaming` observed live chunks
// (not finished), `settled` is proven-ended, `pending` is a persisted
// call with no recorded outcome.
class ToolActivity extends Widget {
  static stylesheet = new URL('./tool-activity.css', import.meta.url);

  constructor() {
    super();
    this.shadowRoot.insertBefore(el('link', { rel: 'stylesheet', href: statusOrbStylesheet }), this.root);
    this._tool = null;
    this._build();
    this._sync();
  }

  // tool = {callId, name, status:'requested'|'streaming'|'settled'|'pending',
  //         input?, output?, resultStored?}
  // `input`/`output` are exact strings from the wire record — live
  // chunks accumulate into `output`; a persisted `completed` record
  // lands as resultStored+plain output from the HTTP projection.
  set tool(t) {
    this._tool = t || null;
    this._sync();
  }

  get expanded() { return this.details.open; }
  set expanded(v) { this.details.open = !!v; }

  _build() {
    clear(this.root);
    this.root.classList.add('tool');
    this.details = el('details', { class: 'details' });
    this.summaryEl = el('summary', { class: 'summary' });
    this.orb = statusOrb();
    this.nameEl = el('span', { class: 'name' });
    this.callIdEl = el('span', { class: 'callid' });
    this.statusEl = el('span', { class: 'status' });
    this.summaryEl.append(
      this.orb.node,
      this.nameEl,
      this.callIdEl,
      this.statusEl,
      el('span', { class: 'chev', 'aria-hidden': 'true', text: '›' }),
    );
    this.bodyEl = el('div', { class: 'body' });
    this.details.append(this.summaryEl, this.bodyEl);
    this.details.addEventListener('toggle', () => {
      if (this.details.open && this._tool?.status === 'streaming') this.outputCode.scrollToEnd();
    });
    this.root.append(this.details);

    // Both sections are visible as soon as the tool card opens. Code
    // widgets stay mounted across stream updates and keep scroll state.
    this.inputCode = document.createElement('nusa-code-block');
    this.inputCode.classList.add('tool-code');
    this.inputNote = el('p', { class: 'note', text: 'No input was recorded.' });
    this.inputSec = this._section('Input', [this.inputNote, this.inputCode]);

    this.outputCode = document.createElement('nusa-code-block');
    this.outputCode.classList.add('tool-code');
    this.outputCode.language = 'text';
    this.outputNote = el('p', { class: 'note' });
    this.outputSec = this._section('Output', [this.outputNote, this.outputCode]);

    this.bodyEl.append(this.inputSec, this.outputSec);
  }

  _section(title, content) {
    return el('section', { class: 'sec', 'aria-label': title }, [
      el('div', { class: 'sec-heading', text: title }),
      el('div', { class: 'sec-body' }, content),
    ]);
  }

  _sync() {
    const t = this._tool;
    const status = t?.status || 'pending';

    // Input: preserve the exact argument string produced by the model.
    const input = t?.input ?? '';
    if (input) {
      let lang = 'text';
      try { JSON.parse(input); lang = 'json'; } catch { /* raw string stays */ }
      if (this.inputCode.language !== lang) this.inputCode.language = lang;
      if (this.inputCode._code !== input) {
        const firstInput = !this.inputCode._code;
        this.inputCode.code = input;
        if (firstInput) this.inputCode.scrollToStart();
      }
      this.inputCode.hidden = false;
      this.inputNote.hidden = true;
    } else {
      this.inputCode.hidden = true;
      this.inputNote.hidden = false;
    }

    // Live chunks and persisted results are both plain tool text.
    const output = t?.output ?? '';
    if (output) {
      if (this.outputCode._code !== output) {
        this.outputCode.code = output;
        if (status !== 'streaming') this.outputCode.scrollToStart();
      }
      this.outputCode.hidden = false;
      this.outputNote.hidden = true;
    } else {
      this.outputCode.hidden = true;
      this.outputNote.hidden = false;
      this.outputNote.textContent = this._outputText(status);
    }

    const phase = toolOrbPhase(status, output);
    this.orb.set(phase);
    this.nameEl.textContent = t?.name || 'tool';
    this.callIdEl.textContent = t?.callId || '';
    this.callIdEl.hidden = !t?.callId;
    this.statusEl.textContent = status;
    this.statusEl.className = `status phase-${phase}`;
    this.details.classList.remove('phase-live', 'phase-idle', 'phase-done', 'phase-failed');
    this.details.classList.add(`phase-${phase}`);
    this.details.setAttribute('aria-busy', phase === 'live' ? 'true' : 'false');
  }

  _outputText(status) {
    switch (status) {
      case 'requested':
        return 'Awaiting tool output activity.';
      case 'streaming':
        return 'Output stream open — awaiting chunks.';
      case 'settled':
        return this._tool?.resultStored
          ? 'Completed — no output recorded.'
          : 'Settled without an exposed output marker.';
      default:
        return 'No persisted output is recorded.';
    }
  }
}

customElements.define('nusa-tool-activity', ToolActivity);
