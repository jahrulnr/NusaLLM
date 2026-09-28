import { Widget, el, clear } from '../../lib/dom.js';
import { tokenizeCode } from '../../lib/highlight.js';

// Framed terminal/editor code block: language badge, copy button, a
// line-number gutter, and token spans — every byte renders as a text
// node. Whitespace is preserved; long lines scroll inside the block,
// never widening the page.
class CodeBlock extends Widget {
  static stylesheet = new URL('./code-block.css', import.meta.url);

  constructor() {
    super();
    this._lang = '';
    this._code = '';
    this._render();
  }

  set language(v) { this._lang = v || ''; this._render(); }
  set code(v) { this._code = v == null ? '' : String(v); this._render(); }

  _render() {
    clear(this.root);
    this.root.classList.add('codeblock');

    const badge = el('span', { class: 'lang', text: this._lang || 'text' });
    const copyBtn = el('button', {
      class: 'copy', type: 'button', 'aria-label': 'Copy code',
    }, [el('span', { class: 'copy-label', text: 'Copy' })]);
    copyBtn.addEventListener('click', async () => {
      const label = copyBtn.querySelector('.copy-label');
      try {
        await navigator.clipboard.writeText(this._code);
        label.textContent = 'Copied';
      } catch {
        label.textContent = 'Copy failed';
      }
      setTimeout(() => { if (label.isConnected) label.textContent = 'Copy'; }, 1400);
    });

    this.root.append(el('div', { class: 'chrome' }, [
      badge, el('span', { class: 'chrome-gap' }), copyBtn,
    ]));

    // Tokens may span newlines — split each token's text at line
    // boundaries and emit same-class spans inside each line so the
    // gutter counter lines up and text is preserved exactly. Lines are
    // block-level spans so <pre><code> stays valid phrasing content.
    const codeEl = el('code', { class: 'lines' });
    let line = el('span', { class: 'line' });
    for (const tok of tokenizeCode(this._code, this._lang)) {
      const parts = tok.text.split('\n');
      parts.forEach((part, idx) => {
        if (idx > 0) { codeEl.append(line); line = el('span', { class: 'line' }); }
        if (part) line.append(el('span', { class: `tok-${tok.type}`, text: part }));
      });
    }
    codeEl.append(line);
    this.root.append(el('pre', {
      class: 'scroll',
      'aria-label': `${this._lang || 'plain'} code block`,
    }, [codeEl]));
  }
}

customElements.define('nusa-code-block', CodeBlock);
