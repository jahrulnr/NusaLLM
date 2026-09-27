import { Widget, el, emit, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

// The conversation surface. All remote content renders through
// textContent — nothing is ever interpreted as markup. Assistant prose
// is full-width editorial; user turns are compact bordered blocks with
// inline edit (origin messages only); tool calls are status rows.
class MessageThread extends Widget {
  static stylesheet = new URL('./message-thread.css', import.meta.url);

  constructor() {
    super();
    this._messages = [];
    this._stream = null; // {text, phase, tools:[{callId,name,output}], active}
    this._loading = false;
    this._hasConversation = false;
    this._historyNotice = false;
    this._editingId = null;
    this._render();
  }

  set messages(list) { this._messages = list || []; this._editingId = null; this._render(); }
  set hasConversation(v) { this._hasConversation = !!v; this._render(); }
  set loading(v) { this._loading = !!v; this._render(); }
  set historyNotice(v) { this._historyNotice = !!v; this._render(); }
  // stream: live turn projection — text deltas + markers, or null.
  // Renders coalesce to one per animation frame: a delta burst must
  // never rebuild the accumulated text quadratically.
  set stream(s) {
    this._stream = s; // newest snapshot always wins
    if (this._streamRaf) return;
    this._streamRaf = requestAnimationFrame(() => {
      this._streamRaf = null;
      this._renderStream();
    });
  }

  disconnectedCallback() {
    if (this._streamRaf) {
      cancelAnimationFrame(this._streamRaf);
      this._streamRaf = null;
    }
  }

  _render() {
    clear(this.root);
    this.root.classList.add('thread');
    this.root.setAttribute('role', 'log');
    this.root.setAttribute('aria-label', 'Conversation');

    if (this._loading) {
      this.root.append(el('div', { class: 'hint', text: 'Loading history…' }));
      this._mountStream();
      return;
    }
    if (!this._hasConversation) {
      this.root.append(el('div', { class: 'empty' }, [
        el('div', { class: 'empty-mark', text: 'N' }),
        el('p', { class: 'empty-title', text: 'Pick a model, write a message.' }),
        el('p', { class: 'empty-hint', text: 'A new thread starts when you send. Configure a gateway from the context panel if the picker is empty.' }),
      ]));
      this._mountStream();
      return;
    }
    if (this._historyNotice) {
      this.root.append(el('div', { class: 'notice', text: 'Earlier messages exist — showing the latest window.' }));
    }
    if (!this._messages.length) {
      this.root.append(el('div', { class: 'hint', text: 'No messages yet — say something.' }));
    }
    for (const m of this._messages) this.root.append(this._message(m));
    this._mountStream();
    this._scrollToEnd();
  }

  _message(m) {
    if (m.role === 'user') return this._userMessage(m);
    return this._assistantMessage(m);
  }

  _userMessage(m) {
    if (this._editingId === m.id) return this._editBlock(m);
    const kids = [el('div', { class: 'user-content', text: m.content })];
    if ((m.attachments || []).length) {
      kids.push(el('div', { class: 'attachments' }, m.attachments.map((a) =>
        el('span', { class: 'attachment', text: `${a.modality} · ${a.mediaType}` }))));
    }
    const block = el('div', { class: 'user-msg' }, kids);
    if (m.userType === 'origin' || m.userType === '' || !m.userType) {
      block.append(el('button', {
        class: 'edit-btn', type: 'button', 'aria-label': 'Edit message',
        onclick: () => { this._editingId = m.id; this._render(); },
      }, [icon('pencil', 11)]));
    }
    return block;
  }

  _editBlock(m) {
    const input = el('textarea', { class: 'edit-input', value: m.content, 'aria-label': 'Edit message content' });
    const cancel = () => { this._editingId = null; this._render(); };
    const commit = () => {
      const content = input.value.trim();
      if (content && content !== m.content) {
        emit(this, 'message-edit', { messageId: m.id, content });
      }
      this._editingId = null;
      this._render();
    };
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); commit(); }
      if (e.key === 'Escape') cancel();
    });
    return el('div', { class: 'user-msg editing' }, [
      input,
      el('div', { class: 'edit-actions' }, [
        el('button', { class: 'edit-save', type: 'button', onclick: commit }, [el('span', { text: 'Edit & resend' })]),
        el('button', { class: 'edit-cancel', type: 'button', onclick: cancel }, [el('span', { text: 'Cancel' })]),
      ]),
    ]);
  }

  _assistantMessage(m) {
    const kids = [el('div', { class: 'prose' })];
    this._prose(kids[0], m.content);
    for (const tc of m.toolCalls || []) {
      kids.push(el('div', { class: `tool-row ${tc.status}` }, [
        icon('tool', 12),
        el('span', { class: 'tool-name', text: tc.name || tc.id }),
        el('span', { class: 'tool-status', text: tc.status }),
      ]));
    }
    if ((m.attachments || []).length) {
      kids.push(el('div', { class: 'attachments' }, m.attachments.map((a) =>
        el('span', { class: 'attachment', text: `${a.modality} · ${a.mediaType}` }))));
    }
    if (m.finishReason) {
      kids.push(el('div', { class: 'finish', text: m.finishReason }));
    }
    return el('div', { class: 'assistant-msg' }, kids);
  }

  // Plain-text paragraphs: split on blank lines, keep line breaks.
  _prose(container, text) {
    for (const para of String(text || '').split(/\n{2,}/)) {
      const p = el('p');
      p.textContent = para;
      container.append(p);
    }
  }

  _mountStream() {
    this.streamEl = el('div', { class: 'stream', 'aria-live': 'polite', 'aria-label': 'Assistant response stream' });
    this.root.append(this.streamEl);
    this._renderStream();
  }

  _renderStream() {
    if (!this.streamEl) return;
    clear(this.streamEl);
    const s = this._stream;
    if (!s || (!s.active && !s.text)) return;
    if (s.phase === 'reasoning' && !s.text) {
      this.streamEl.append(el('div', { class: 'phase', text: 'reasoning…' }));
    }
    for (const t of s.tools || []) {
      this.streamEl.append(el('div', { class: `tool-row ${t.output ? 'completed' : 'pending'}` }, [
        icon('tool', 12),
        el('span', { class: 'tool-name', text: t.name || t.callId }),
        el('span', { class: 'tool-status', text: t.output ? 'completed' : 'running' }),
      ]));
    }
    if (s.text) {
      const prose = el('div', { class: 'prose streaming' });
      this._prose(prose, s.text);
      this.streamEl.append(prose);
    }
    if (s.active) this.streamEl.append(el('span', { class: 'caret', 'aria-hidden': 'true' }));
    this._scrollToEnd();
  }

  _scrollToEnd() {
    this.root.scrollTop = this.root.scrollHeight;
  }
}

customElements.define('nusa-message-thread', MessageThread);
