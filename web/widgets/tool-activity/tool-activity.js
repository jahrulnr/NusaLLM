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
    this._hasInitialTool = false;
    this._outToggled = false;   // Output section
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
    if (t && !this._hasInitialTool) {
      this.details.open = t.status === 'requested' || t.status === 'streaming';
      this._hasInitialTool = true;
    }
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
    this.root.append(this.details);

    // Persistent section elements — nested details keep their open
    // state across property updates; code widgets keep theirs.
    this.inputCode = document.createElement('nusa-code-block');
    this.inputNote = el('p', { class: 'note', text: 'No input was recorded.' });
    this.inputSec = this._section('Input', [this.inputNote, this.inputCode]);

    this.outputCode = document.createElement('nusa-code-block');
    this.outputCode.language = 'text';
    this.outputNote = el('p', { class: 'note' });
    this.outputSec = this._section('Output', [this.outputNote, this.outputCode], (d) => {
      this._outDet = d;
      d.querySelector('summary').addEventListener('click', () => { this._outToggled = true; });
      d.querySelector('summary').addEventListener('keydown', (e) => {
        if (e.key === 'Enter' || e.key === ' ') this._outToggled = true;
      });
    });

    this.bodyEl.append(this.inputSec, this.outputSec);
  }

  _section(title, content, onBuilt) {
    const d = el('details', { class: 'sec' });
    d.append(
      el('summary', { class: 'sec-summary', text: title }),
      el('div', { class: 'sec-body' }, content),
    );
    if (onBuilt) onBuilt(d);
    return d;
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
      if (this.inputCode._code !== input) this.inputCode.code = input;
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
      if (this.outputCode._code !== output) this.outputCode.code = output;
      this.outputCode.hidden = false;
      this.outputNote.hidden = true;
    } else {
      this.outputCode.hidden = true;
      this.outputNote.hidden = false;
      this.outputNote.textContent = this._outputText(status);
    }
    if (!this._outToggled) this._outDet.open = status === 'streaming';
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
