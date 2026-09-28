import { Widget, el, emit, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

// The conversation surface. Assistant content renders through
// nusa-markdown-view; user messages stay escaped plain text. Tool calls —
// persisted and live — render as nusa-tool-activity with their full
// input and output. Provider reasoning renders in nusa-thinking-block.
// Stream rerenders keep widget instances: details/open state must
// survive every delta.
class MessageThread extends Widget {
  static stylesheet = new URL('./message-thread.css', import.meta.url);

  constructor() {
    super();
    this._messages = [];
    this._stream = null; // turn projection: {id, active, segments, announcements, finishReason}
    this._streamId = null;
    this._loading = false;
    this._hasConversation = false;
    this._historyNotice = false;
    this._compactState = null;
    this._editingId = null;
    this._pinned = true; // pinned to bottom until the user scrolls up
    this._lastScrollTop = 0;
    // Live-stream widget instances keyed by segment.id — preserved
    // across frames so user expansion/scroll state survives deltas.
    this._segmentWidgets = new Map();
    this._toolExpanded = new Map();
    this._annCursor = 0;
    this._caret = el('span', { class: 'caret', 'aria-hidden': 'true' });
    this._contentResize = new ResizeObserver(() => {
      if (this._pinned) this._scrollToEnd();
    });
    this.root.addEventListener('scroll', () => {
      const top = this.root.scrollTop;
      const atEnd = top + this.root.clientHeight >= this.root.scrollHeight - 2;
      if (atEnd) this._pinned = true;
      else if (top < this._lastScrollTop) this._pinned = false;
      this._lastScrollTop = top;
    });
    this._render();
  }

  set messages(list) {
    const next = list || [];
    const previous = this._messages;
    this._messages = next;
    this._editingId = null;
    // A completed turn usually replaces one pending user record and
    // appends assistant records. Keep the existing conversation DOM.
    if (!this._loading && this._hasConversation && this.streamEl &&
        this._messageNodes?.length === previous.length &&
        next.length >= previous.length && previous.length > 0) {
      let appendOnly = true;
      const replaced = [];
      for (let i = 0; i < previous.length; i++) {
        const before = previous[i], after = next[i];
        if (JSON.stringify(before) === JSON.stringify(after)) continue;
        if (before.id?.startsWith('pending-') && before.role === 'user' &&
            after.role === 'user' && before.content === after.content) {
          replaced.push(i);
        } else {
          appendOnly = false;
          break;
        }
      }
      if (appendOnly) {
        if (this._stream && !this._stream.active) this._renderStream();
        this._handoffSegments = this._stream && !this._stream.active ? this._stream.segments : null;
        this._handoffIndex = 0;
        for (const i of replaced) {
          const node = this._message(next[i]);
          this._contentResize.unobserve(this._messageNodes[i]);
          this._messageNodes[i].replaceWith(node);
          this._messageNodes[i] = node;
          this._contentResize.observe(node);
        }
        for (let i = previous.length; i < next.length; i++) {
          const node = this._message(next[i]);
          this.streamEl.before(node);
          this._messageNodes.push(node);
          this._contentResize.observe(node);
        }
        this._handoffSegments = null;
        if (this._pinned) this._scrollToEnd();
        return;
      }
    }
    this._render();
  }
  set hasConversation(v) { this._hasConversation = !!v; this._render(); }
  set loading(v) { this._loading = !!v; this._render(); }
  set historyNotice(v) {
    if (this._historyNotice === !!v) return;
    this._historyNotice = !!v;
    this._render();
  }
  set compactState(state) {
    this._compactState = state;
    if (this.compactEl) this._renderCompactState();
  }

  _renderCompactState() {
    const state = this._compactState;
    this.compactEl.hidden = !state;
    this.compactEl.className = `compact-status${state === 'running' ? ' running' : ''}`;
    this.compactText.textContent = state === 'running'
      ? 'Context compacting'
      : 'Context compacted';
    if (state && this._pinned) this._scrollToEnd();
  }
  // stream: a live turn projection, or null. A different projection id
  // resets all segment widget instances and the announcement cursor —
  // details state must never leak across turns. Renders coalesce to one
  // per animation frame; the newest snapshot always wins.
  set stream(s) {
    const id = (s && s.id) || null;
    if (id !== this._streamId) {
      for (const w of this._segmentWidgets.values()) {
        if (w.localName === 'nusa-tool-activity' && w._tool?.callId) {
          this._toolExpanded.set(w._tool.callId, w.expanded);
        }
      }
      this._streamId = id;
      this._segmentWidgets.clear();
      this._annCursor = 0;
      if (this.streamEl) clear(this.streamEl);
      if (this.liveEl) clear(this.liveEl);
    }
    this._stream = s;
    if (this._streamRaf) return;
    this._streamRaf = requestAnimationFrame(() => {
      this._streamRaf = null;
      this._renderStream();
    });
  }

  disconnectedCallback() {
    this._contentResize.disconnect();
    if (this._streamRaf) {
      cancelAnimationFrame(this._streamRaf);
      this._streamRaf = null;
    }
  }

  connectedCallback() {
    for (const node of this._messageNodes || []) this._contentResize.observe(node);
    if (this.streamEl) this._contentResize.observe(this.streamEl);
  }

  _render() {
    this._contentResize.disconnect();
    clear(this.root);
    this._messageNodes = [];
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
        el('p', { class: 'empty-hint', text: 'Choose a model and send a message. Add a gateway in Settings if the picker is empty.' }),
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
    for (const m of this._messages) {
      const node = this._message(m);
      this.root.append(node);
      this._messageNodes.push(node);
      this._contentResize.observe(node);
    }
    this._mountStream();
    // A fresh conversation load lands at the end unconditionally.
    this._pinned = true;
    this._scrollToEnd();
  }

  _message(m) {
    if (m.role === 'user') return this._userMessage(m);
    return this._assistantMessage(m);
  }

  // Attachment chip — the stored reference (uri, or its display name
  // when set) is owner-visible. The full uri rides the tooltip; both
  // are untrusted text inserted via textContent only.
  _attachmentChip(a) {
    return el('span', { class: 'attachment', title: a.uri || '' }, [
      el('span', { class: 'att-kind', text: `${a.modality} · ${a.mediaType}` }),
      el('span', { class: 'att-name', text: a.name || a.uri || 'attachment' }),
    ]);
  }

  _userMessage(m) {
    if (this._editingId === m.id) return this._editBlock(m);
    const kids = [el('div', { class: 'user-content', text: m.content })];
    if ((m.attachments || []).length) {
      kids.push(el('div', { class: 'attachments' }, m.attachments.map((a) => this._attachmentChip(a))));
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
    const kids = [];
    const calls = m.toolCalls || [];
    if (m.parts && m.parts.length) {
      const callsByID = new Map(calls.map((tc) => [tc.id, tc]));
      const renderedCalls = new Set();
      for (const part of m.parts) {
        if (part.kind === 'text') {
          const md = this._liveWidgetFor('markdown', part) || document.createElement('nusa-markdown-view');
          md.classList.remove('streaming-md');
          md.finishStream();
          if (md._content !== (part.text || '')) md.content = part.text || '';
          kids.push(md);
        } else if (part.kind === 'reasoning' && part.text) {
          const thinking = this._liveWidgetFor('thinking', part) || document.createElement('nusa-thinking-block');
          if (thinking._state?.text !== (part.text || '')) {
            thinking.state = { status: 'done', startedAt: null, elapsedMs: null, text: part.text || '' };
          }
          kids.push(thinking);
        } else if (part.kind === 'tool_call') {
          const tc = callsByID.get(part.toolCallId);
          if (tc) {
            kids.push(this._persistedTool(tc, this._liveWidgetFor('tool', part)));
            renderedCalls.add(tc.id);
          }
        }
      }
      for (const tc of calls) {
        if (!renderedCalls.has(tc.id)) kids.push(this._persistedTool(tc));
      }
    } else {
      const md = document.createElement('nusa-markdown-view');
      md.content = m.content;
      kids.push(md);
      for (const tc of calls) kids.push(this._persistedTool(tc));
    }
    if ((m.attachments || []).length) {
      kids.push(el('div', { class: 'attachments' }, m.attachments.map((a) => this._attachmentChip(a))));
    }
    return el('div', { class: 'assistant-msg' }, kids);
  }

  _liveWidgetFor(type, part) {
    if (!this._handoffSegments) return null;
    for (let i = this._handoffIndex; i < this._handoffSegments.length; i++) {
      const seg = this._handoffSegments[i];
      if (seg.type !== type) continue;
      if (type === 'tool' && seg.tool.callId !== part.toolCallId) continue;
      this._handoffIndex = i + 1;
      return this._segmentWidgets.get(seg.id) || null;
    }
    return null;
  }

  // Persisted calls carry the exact input string and the stored evidence
  // envelope (when completed) — the owner sees what the agent saw.
  _persistedTool(tc, existing = null) {
    const settled = tc.status === 'completed';
    const w = existing || document.createElement('nusa-tool-activity');
    w.tool = {
      callId: tc.id, name: tc.name,
      status: settled ? 'settled' : 'pending',
      resultStored: settled,
      input: tc.input || '',
      output: settled ? (tc.output ?? '') : '',
    };
    if (this._toolExpanded.has(tc.id)) w.expanded = this._toolExpanded.get(tc.id);
    return w;
  }

  _mountStream() {
    this.streamEl = el('div', { class: 'stream', 'aria-label': 'Assistant response stream' });
    // The announcer is a stable, visually-hidden live region — the
    // stream subtree itself is NOT live (a live subtree re-reads the
    // whole accumulated response on every delta).
    this.liveEl = el('div', {
      class: 'sr-only', 'aria-live': 'polite', 'aria-atomic': 'false',
    });
    this.compactText = el('span');
    this.compactEl = el('div', { class: 'compact-status', role: 'status' }, [
      el('span', { class: 'compact-symbol', 'aria-hidden': 'true', text: '⇥' }),
      this.compactText,
    ]);
    this.root.append(this.streamEl, this.compactEl, this.liveEl);
    this._contentResize.observe(this.streamEl);
    this._renderCompactState();
    this._renderStream();
  }

  _renderStream() {
    if (!this.streamEl) return;
    const s = this._stream;
    if (!s || (!s.active && !(s.segments || []).length)) {
      clear(this.streamEl);
      this._segmentWidgets.clear();
      return;
    }

    // Segments only append. Keep existing DOM in place across deltas;
    // moving every widget forces style/layout work and visible flicker.
    const present = new Set();
    const segments = s.segments || [];
    for (const [index, seg] of segments.entries()) {
      present.add(seg.id);
      let w = this._segmentWidgets.get(seg.id);
      if (!w) {
        w = seg.type === 'markdown' ? document.createElement('nusa-markdown-view')
          : seg.type === 'thinking' ? document.createElement('nusa-thinking-block')
          : document.createElement('nusa-tool-activity');
        if (seg.type === 'markdown') w.classList.add('streaming-md');
        this._segmentWidgets.set(seg.id, w);
        this.streamEl.append(w);
      }
      if (w.parentNode !== this.streamEl) this.streamEl.append(w);
      if (w._segVersion !== seg.version) {
        w._segVersion = seg.version;
        if (seg.type === 'markdown') {
          const previous = w._streamedText || '';
          if (seg.text.startsWith(previous)) {
            w.appendStreamText(seg.text.slice(previous.length));
          } else {
            w.content = seg.text;
          }
          w._streamedText = seg.text;
        } else if (seg.type === 'thinking') {
          w.state = {
            status: seg.status, startedAt: seg.startedAt,
            elapsedMs: seg.elapsedMs, text: seg.text || '',
          };
        } else w.tool = seg.tool;
      }
      if (seg.type === 'markdown' && (!s.active || index < segments.length - 1)) {
        w.finishStream();
      }
    }
    for (const [id] of this._segmentWidgets) {
      if (!present.has(id)) this._segmentWidgets.delete(id);
    }

    if (s.active) this.streamEl.append(this._caret);
    else this._caret.remove();

    // Announce only the newly appended markers, never the subtree.
    const anns = s.announcements || [];
    if (this.liveEl) {
      const from = Math.min(this._annCursor, anns.length);
      for (let i = from; i < anns.length; i++) {
        this.liveEl.append(el('div', { text: anns[i] }));
      }
      while (this.liveEl.childElementCount > 200) {
        this.liveEl.firstElementChild.remove();
      }
      this._annCursor = anns.length;
    }

    if (this._pinned) this._scrollToEnd();
  }

  _scrollToEnd() {
    this.root.scrollTop = this.root.scrollHeight;
    this._lastScrollTop = this.root.scrollTop;
  }
}

customElements.define('nusa-message-thread', MessageThread);
