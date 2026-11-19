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
  addEventListener(type, fn) { (this.listeners ||= {})[type] = [...(this.listeners?.[type] || []), fn]; }
  fire(type, extra = {}) { for (const fn of this.listeners?.[type] || []) fn({ target: this, ...extra }); }
  dispatchEvent(event) { (this.events ||= []).push(event); return true; }
  setAttribute(k, v) { this[k] = v; }
  getAttribute(k) { return this[k]; }
  getBoundingClientRect() { return { top: 0, bottom: 0, height: 0, width: 0 }; }
  get firstChild() { return this.children[0] || null; }
  removeChild(c) { this.children = this.children.filter((x) => x !== c); return c; }
  replaceWith() {}
  remove() {}
  focus() {}
  contains(node) {
    if (node === this) return true;
    for (const child of this.children || []) {
      if (child === node || (child && child.contains && child.contains(node))) return true;
    }
    return false;
  }
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
globalThis.document = {
  createElement: (tag) => new FakeEl(tag),
  createElementNS: (ns, tag) => new FakeEl(tag),
  addEventListener() {},
};
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

// A generated image attachment is stored as a data: URI. Using that URI
// as the chip's title writes the entire base64 blob into a DOM attribute
// and shows it as the hover text; data payloads are not human labels.
test('a data-uri attachment does not use its blob as the title', () => {
  const thread = new MessageThread();
  thread.hasConversation = true;
  const uri = 'data:image/png;base64,' + 'A'.repeat(2000);
  thread.messages = [{
    id: 'm1', role: 'assistant', content: 'generated',
    attachments: [{ uri, mediaType: 'image/png', modality: 'image', name: 'image.png' }],
  }];
  const findAttachment = (node) => {
    if (!node) return null;
    if (node.className === 'attachment') return node;
    for (const child of node.children || []) {
      const hit = findAttachment(child);
      if (hit) return hit;
    }
    return null;
  };
  const chip = findAttachment(thread.root);
  assert.ok(chip, 'attachment chip rendered');
  assert.notEqual(chip.title, uri);
  assert.ok((chip.title || '').length < 200);
});


// Enter and Escape during an IME composition belong to the candidate
// list, not to the editor. Committing on composition Enter resends the
// message mid-word; Escape cancels the whole edit. The composer already
// guards Enter this way.
test('message edit keys respect IME composition', () => {
  const thread = new MessageThread();
  thread.hasConversation = true;
  thread.messages = [{ id: 'u1', role: 'user', content: 'old' }];
  thread._editingId = 'u1';
  thread._render();
  const findInput = (node) => {
    if (!node) return null;
    if (node.className === 'edit-input') return node;
    for (const child of node.children || []) {
      const hit = findInput(child);
      if (hit) return hit;
    }
    return null;
  };
  const input = findInput(thread.root);
  assert.ok(input, 'edit input rendered');
  input.value = '新しい';
  input.fire('keydown', { key: 'Enter', isComposing: true, preventDefault() {} });
  assert.equal(thread._editingId, 'u1', 'composition Enter must not resend');
  input.fire('keydown', { key: 'Escape', isComposing: true, preventDefault() {} });
  assert.equal(thread._editingId, 'u1', 'composition Escape must not cancel the edit');
  input.fire('keydown', { key: 'Enter', isComposing: false, preventDefault() {} });
  assert.equal(thread._editingId, null);
  assert.ok(thread.events?.some((e) => e.type === 'message-edit'), 'real Enter emits the edit');
});

// A shell veto (running turn, refresh barrier, offline) keeps the
// editor open with the typed draft so the user can retry. The next
// action is typing, so the new textarea must own focus; dropping the
// active element when the old save button is replaced strands the user
// on BODY mid-edit.
test('a vetoed edit keeps focus in the open editor', () => {
  FakeEl.prototype.focus = function focus() { globalThis.document.activeElement = this; };
  globalThis.document.activeElement = null;
  const thread = new MessageThread();
  thread.hasConversation = true;
  thread.messages = [{ id: 'u1', role: 'user', userType: 'origin', content: 'old' }];
  const findClass = (node, cls) => {
    if (!node) return null;
    if (node.className === cls) return node;
    for (const child of node.children || []) {
      const hit = findClass(child, cls);
      if (hit) return hit;
    }
    return null;
  };
  findClass(thread.root, 'edit-btn').fire('click');
  const input = findClass(thread.root, 'edit-input');
  assert.ok(input, 'editor opened');
  input.value = 'new text';
  // The shell refuses the dispatch through preventDefault/return false.
  thread.dispatchEvent = () => false;
  findClass(thread.root, 'edit-save').fire('click');
  assert.equal(thread._editingId, 'u1', 'the editor stays open after a veto');
  const reopened = findClass(thread.root, 'edit-input');
  assert.ok(reopened, 'the draft editor is still rendered');
  assert.equal(globalThis.document.activeElement, reopened,
    'focus must return to the still-open editor, not a detached button or BODY');
  delete FakeEl.prototype.focus;
});

function findThreadClass(node, cls) {
  if (!node) return null;
  if (node.className === cls) return node;
  for (const child of node.children || []) {
    const hit = findThreadClass(child, cls);
    if (hit) return hit;
  }
  return null;
}

// Loading an earlier page rebuilds the window. The Load earlier control
// is the action the user just invoked and usually survives the rebuild
// (more pages remain); it must own focus again instead of the detached
// node dropping the keyboard user on BODY.
test('loading earlier keeps focus on the rebuilt control', () => {
  const originalFocus = FakeEl.prototype.focus;
  FakeEl.prototype.focus = function focus() { globalThis.document.activeElement = this; };
  globalThis.document.activeElement = null;
  try {
    const thread = new MessageThread();
    thread.hasConversation = true;
    thread.historyNotice = true;
    thread.messages = [{ id: 'u2', role: 'user', content: 'newer' }];
    const before = findThreadClass(thread.root, 'earlier-btn');
    assert.ok(before, 'Load earlier control rendered');
    before.focus();
    thread.messages = [
      { id: 'u1', role: 'user', content: 'older' },
      { id: 'u2', role: 'user', content: 'newer' },
    ];
    const after = findThreadClass(thread.root, 'earlier-btn');
    assert.ok(after, 'Load earlier control survives the prepend');
    assert.equal(globalThis.document.activeElement, after,
      'loading earlier must return focus to the rebuilt control');
  } finally {
    FakeEl.prototype.focus = originalFocus;
    globalThis.document.activeElement = null;
  }
});

// An open edit holds text only in the textarea until commit. A window
// rewrite — the same full render a prepended earlier page forces — must
// not recreate the editor from the stored message and silently discard
// what the user typed.
test('loading earlier preserves an open message edit draft', () => {
  const thread = new MessageThread();
  thread.hasConversation = true;
  thread.messages = [{ id: 'u2', role: 'user', userType: 'origin', content: 'original' }];
  findThreadClass(thread.root, 'edit-btn').fire('click');
  const input = findThreadClass(thread.root, 'edit-input');
  assert.ok(input, 'editor opened');
  input.value = 'typed but not saved';

  thread.messages = [
    { id: 'u1', role: 'user', content: 'older' },
    { id: 'u2', role: 'user', userType: 'origin', content: 'original' },
  ];
  const reopened = findThreadClass(thread.root, 'edit-input');
  assert.ok(reopened, 'editor stays open while its message still exists');
  assert.equal(reopened.value, 'typed but not saved',
    'a window rewrite must preserve the unsaved edit draft');
});

function findThreadClasses(node, cls, out = []) {
  if (!node) return out;
  if (node.className === cls) out.push(node);
  for (const child of node.children || []) findThreadClasses(child, cls, out);
  return out;
}

// A rebuild under a focused Edit control must return focus to the SAME
// message. Every edit button shares the class "edit-btn"; a class-only
// focus key lands on the first message instead, silently moving the
// keyboard user's place after a prepend/refresh.
test('a full refresh keeps focus on the same message edit control', () => {
  const originalFocus = FakeEl.prototype.focus;
  FakeEl.prototype.focus = function focus() { globalThis.document.activeElement = this; };
  globalThis.document.activeElement = null;
  try {
    const thread = new MessageThread();
    thread.hasConversation = true;
    thread.messages = [
      { id: 'u1', role: 'user', userType: 'origin', content: 'one' },
      { id: 'u2', role: 'user', userType: 'origin', content: 'two' },
    ];
    findThreadClasses(thread.root, 'edit-btn')[1].focus();
    thread.messages = [
      { id: 'u0', role: 'user', userType: 'origin', content: 'zero' },
      { id: 'u1', role: 'user', userType: 'origin', content: 'one' },
      { id: 'u2', role: 'user', userType: 'origin', content: 'two' },
    ];
    const edits = findThreadClasses(thread.root, 'edit-btn');
    assert.equal(globalThis.document.activeElement, edits[2],
      'focus must follow the same message, not the first edit button');
  } finally {
    FakeEl.prototype.focus = originalFocus;
    globalThis.document.activeElement = null;
  }
});

// The preserved edit draft belongs to the message being edited. After a
// cancel/commit leaves the old textarea cached, opening another message
// must not seed that editor with the previous message's draft.
test('switching edit targets does not inherit the previous draft', () => {
  const thread = new MessageThread();
  thread.hasConversation = true;
  thread.messages = [
    { id: 'u1', role: 'user', userType: 'origin', content: 'one' },
    { id: 'u2', role: 'user', userType: 'origin', content: 'two' },
  ];
  findThreadClasses(thread.root, 'edit-btn')[0].fire('click');
  const first = findThreadClass(thread.root, 'edit-input');
  assert.ok(first, 'first editor opened');
  first.value = 'draft for one';
  findThreadClass(thread.root, 'edit-cancel').fire('click');

  findThreadClasses(thread.root, 'edit-btn')[1].fire('click');
  const second = findThreadClass(thread.root, 'edit-input');
  assert.ok(second, 'second editor opened');
  assert.equal(second.value, 'two',
    'the second message must open with its own content, not the previous draft');
});

// A refresh can update the failed-turn card's note while the user has
// focus on Retry. The card rebuild replaces the button; focus must move to
// the rebuilt Retry, not a detached node.
test('a changed failed card keeps focus on Retry', () => {
  FakeEl.prototype.focus = function focus() { globalThis.document.activeElement = this; };
  globalThis.document.activeElement = null;
  try {
    const thread = new MessageThread();
    thread.hasConversation = true;
    thread.messages = [{ id: 'u1', role: 'user', userType: 'origin', content: 'hi' }];
    thread.failedTurn = { title: 'Turn failed', note: 'first', retryable: true };
    const retry = findThreadClass(thread.root, 'fail-retry');
    assert.ok(retry, 'Retry rendered');
    retry.focus();
    thread.failedTurn = { title: 'Turn failed', note: 'second', retryable: true };
    const rebuilt = findThreadClass(thread.root, 'fail-retry');
    assert.ok(rebuilt, 'Retry survives the card rebuild');
    assert.equal(globalThis.document.activeElement, rebuilt,
      'focus must move to the rebuilt Retry control');
  } finally {
    delete FakeEl.prototype.focus;
    globalThis.document.activeElement = null;
  }
});

// Dismiss removes the focused control with the whole card. Leaving focus
// on the detached button is just as broken as losing it to BODY; a
// surviving control (or the composer, via the shell) must take over.
test('dismissing the failed card moves focus off the removed control', () => {
  FakeEl.prototype.focus = function focus() { globalThis.document.activeElement = this; };
  globalThis.document.activeElement = null;
  try {
    const thread = new MessageThread();
    thread.hasConversation = true;
    thread.messages = [{ id: 'u1', role: 'user', userType: 'origin', content: 'hi' }];
    thread.failedTurn = { title: 'Turn failed', note: 'boom', retryable: true };
    const dismiss = findThreadClass(thread.root, 'fail-dismiss');
    assert.ok(dismiss, 'Dismiss rendered');
    dismiss.focus();
    thread.failedTurn = null;
    assert.notEqual(globalThis.document.activeElement, dismiss,
      'focus must not stay on the removed Dismiss control');
  } finally {
    delete FakeEl.prototype.focus;
    globalThis.document.activeElement = null;
  }
});

// An edit/refresh can shrink the visible history and remove the focused
// message's controls. Focus must follow to a surviving message action,
// not stay on the detached edit button.
test('a removed message hands focus to the nearest surviving edit', () => {
  FakeEl.prototype.focus = function focus() { globalThis.document.activeElement = this; };
  globalThis.document.activeElement = null;
  try {
    const thread = new MessageThread();
    thread.hasConversation = true;
    thread.messages = [
      { id: 'u1', role: 'user', userType: 'origin', content: 'one' },
      { id: 'u2', role: 'user', userType: 'origin', content: 'two' },
    ];
    const edits = [];
    const walk = (node) => {
      if (node.className === 'edit-btn') edits.push(node);
      for (const child of node.children || []) walk(child);
    };
    walk(thread.root);
    assert.equal(edits.length, 2);
    edits[1].focus();
    thread.messages = [{ id: 'u1', role: 'user', userType: 'origin', content: 'one' }];
    const after = [];
    const walkAfter = (node) => {
      if (node.className === 'edit-btn') after.push(node);
      for (const child of node.children || []) walkAfter(child);
    };
    walkAfter(thread.root);
    assert.ok(after.length >= 1, 'surviving edit button rendered');
    assert.equal(globalThis.document.activeElement, after[0],
      'focus must move to the surviving message action');
  } finally {
    delete FakeEl.prototype.focus;
    globalThis.document.activeElement = null;
  }
});

// A full thread rebuild recreates the failed-card container. The
// signature cache must not suppress painting into the new container —
// otherwise toggling loading (or any full render) silently drops an
// existing failed-turn card.
test('a full render repaints an existing failed card', () => {
  const thread = new MessageThread();
  thread.hasConversation = true;
  thread.messages = [{ id: 'u1', role: 'user', userType: 'origin', content: 'hi' }];
  thread.failedTurn = { title: 'Turn failed', note: 'boom', retryable: true };
  assert.ok(findThreadClass(thread.root, 'fail-retry'), 'card initially rendered');
  thread.loading = true;
  thread.loading = false;
  assert.ok(findThreadClass(thread.root, 'fail-retry'),
    'failed card disappeared after a full render');
});

// A failed card can exist without a surviving editable user line (a
// watched failure, a history-derived tail, or a busy command). Dismiss
// still removes the focused control; the browser fallback must not be the
// only hand-off.
test('dismissing a failed card with no editable line moves focus off the removed control', () => {
  FakeEl.prototype.focus = function focus() { globalThis.document.activeElement = this; };
  globalThis.document.activeElement = null;
  try {
    const thread = new MessageThread();
    thread.hasConversation = true;
    thread.messages = [{ id: 'a1', role: 'assistant', content: 'answer' }];
    thread.failedTurn = { title: 'Turn failed', note: 'boom', retryable: true };
    const dismiss = findThreadClass(thread.root, 'fail-dismiss');
    assert.ok(dismiss, 'Dismiss rendered');
    dismiss.focus();
    thread.failedTurn = null;
    assert.notEqual(globalThis.document.activeElement, dismiss,
      'focus must not stay on the removed Dismiss control');
  } finally {
    delete FakeEl.prototype.focus;
    globalThis.document.activeElement = null;
  }
});

// Retryability can change while Retry is focused (a new user line arrives).
// The rebuilt Retry is disabled; focus must move to the surviving Dismiss
// action, not stay on the detached old control.
test('a Retry that becomes disabled hands focus to Dismiss', () => {
  FakeEl.prototype.focus = function focus() { if (this.disabled) return; globalThis.document.activeElement = this; };
  globalThis.document.activeElement = null;
  try {
    const thread = new MessageThread();
    thread.hasConversation = true;
    thread.messages = [{ id: 'u1', role: 'user', userType: 'origin', content: 'hi' }];
    thread.failedTurn = { title: 'Turn failed', note: 'boom', retryable: true };
    const retry = findThreadClass(thread.root, 'fail-retry');
    retry.focus();
    thread.failedTurn = { title: 'Turn failed', note: 'boom', retryable: false };
    const dismiss = findThreadClass(thread.root, 'fail-dismiss');
    assert.ok(dismiss, 'Dismiss survives');
    assert.equal(globalThis.document.activeElement, dismiss,
      'focus must move to the enabled Dismiss action');
  } finally {
    delete FakeEl.prototype.focus;
    globalThis.document.activeElement = null;
  }
});
