import { Widget, el, clear } from '../../lib/dom.js';

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
    this._tool = null;
    this._build();
    this._sync();
  }

  // tool = {callId, name, status:'requested'|'streaming'|'settled'|'pending',
  //         input?, output?, resultStored?}
  // `input`/`output` are exact strings from the wire record — live
  // chunks accumulate into `output`; a persisted `completed` record
  // lands as resultStored+output (the evidence envelope).
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

    clear(this.summaryEl);
    this.summaryEl.append(
      el('span', { class: 'glyph', 'aria-hidden': 'true', text: '⚒' }),
      el('span', { class: 'name', text: t?.name || 'tool' }),
      t?.callId ? el('span', { class: 'callid', text: t.callId }) : null,
      el('span', { class: `status ${status}`, text: status }),
      el('span', { class: 'chev', 'aria-hidden': 'true', text: '›' }),
    );

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

    // The stored tool record includes transport delimiters. Show and
    // copy the tool's own output, without that envelope.
    let output = t?.output ?? '';
    if (t?.resultStored) {
      const wrapped = /^<untrusted_tool(?:\s[^>]*)?>(?:\\n|\n)([\s\S]*)\n<\/untrusted_tool>$/.exec(output);
      if (wrapped) output = wrapped[1];
    }
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
