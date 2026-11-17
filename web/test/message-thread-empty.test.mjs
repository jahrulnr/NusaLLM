import { test } from 'node:test';
import assert from 'node:assert/strict';

// message-thread builds widgets through createElement; layout calls are
// stubbed so the render contract can be inspected without a browser.
class FakeEl {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.style = { setProperty() {}, height: '' };
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.hidden = false;
    this.scrollTop = 0;
    this.scrollHeight = 0;
    this.clientHeight = 0;
    this.value = '';
  }
  append(...kids) { this.children.push(...kids.filter((k) => k != null)); }
  prepend(...kids) { this.children.unshift(...kids.filter((k) => k != null)); }
  addEventListener() {}
  setAttribute(k, v) { this[k] = v; }
  getAttribute(k) { return this[k]; }
  getBoundingClientRect() { return { top: 0, bottom: 0, height: 0, width: 0 }; }
  get firstChild() { return this.children[0] || null; }
  removeChild(c) { this.children = this.children.filter((x) => x !== c); return c; }
  replaceWith() {}
  remove() {}
  focus() {}
  contains() { return false; }
  querySelector() { return null; }
  querySelectorAll() { return []; }
  scrollToEnd() {}
}
class FakeHTMLElement extends FakeEl {
  attachShadow() { this.shadowRoot = new FakeEl('#shadow'); return this.shadowRoot; }
}
const defined = {};
globalThis.HTMLElement = FakeHTMLElement;
globalThis.customElements = { define: (name, cls) => { defined[name] = cls; } };
globalThis.document = { createElement: (tag) => new FakeEl(tag), addEventListener() {} };
globalThis.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
globalThis.requestAnimationFrame = (fn) => { fn(); return 0; };
globalThis.cancelAnimationFrame = () => {};

await import('../widgets/message-thread/message-thread.js');
const MessageThread = defined['nusa-message-thread'];

test('an empty assistant message leaves no blank markdown stripe', () => {
  const thread = new MessageThread();
  thread.hasConversation = true;
  thread.messages = [{ id: 'm1', role: 'assistant', content: '', parts: [] }];
  const assistant = thread.root.children.find((n) => n.className === 'assistant-msg');
  assert.ok(assistant, 'assistant message node rendered');
  const blanks = (assistant.children || []).filter(
    (n) => n.tagName === 'NUSA-MARKDOWN-VIEW' && !n._content,
  );
  assert.equal(blanks.length, 0, 'empty response must not render an empty markdown widget');
});

// A compaction checkpoint is an automation record (role user,
// userType system), not something the owner typed. Rendering it as a
// normal user bubble exposes the compaction instruction and makes the
// summary look like user input.
test('a compaction checkpoint is not rendered as the user bubble', () => {
  const thread = new MessageThread();
  thread.hasConversation = true;
  thread.messages = [{
    id: 'm1', role: 'user', userType: 'system',
    content: '[COMPACTION CHECKPOINT]\n\n[SUMMARIES]\nprior work',
  }];
  const hasUserBubble = (node) => {
    if (!node) return false;
    if (node.className === 'user-msg') return true;
    return (node.children || []).some(hasUserBubble);
  };
  assert.equal(hasUserBubble(thread.root), false, 'checkpoint must use a system/context surface');
});

// HTTP 200 with finishReason=content_filter and no text (Gemini SAFETY,
// OpenAI content filtering) is a blocked answer, not a successful empty
// one. The thread must say so instead of leaving a blank stripe.
test('a content-filtered empty reply says it was blocked', () => {
  const thread = new MessageThread();
  thread.hasConversation = true;
  thread.messages = [{
    id: 'm1', role: 'assistant', content: '', parts: [], finishReason: 'content_filter',
  }];
  const assistant = thread.root.children.find((n) => n.className === 'assistant-msg');
  assert.ok(assistant, 'assistant message node rendered');
  assert.match(assistant.textContent || '', /blocked|filtered|refus/i);
});
