import { test } from 'node:test';
import assert from 'node:assert/strict';

// The markdown view schedules stream renders on requestAnimationFrame —
// the stand-in captures the frame so the test controls exactly when it
// runs. Global stubs must land BEFORE the widget module evaluates.
class FakeEl {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.style = {};
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.parent = null;
  }
  append(...kids) {
    for (const k of kids) { if (k && typeof k === 'object') k.parent = this; }
    this.children.push(...kids);
  }
  removeChild(c) { this.children = this.children.filter((x) => x !== c); return c; }
  get firstChild() { return this.children[0] || null; }
  before(...kids) {
    const i = this.parent.children.indexOf(this);
    for (const k of kids) { if (k && typeof k === 'object') k.parent = this.parent; }
    this.parent.children.splice(i, 0, ...kids);
  }
  replaceChildren(...kids) {
    for (const k of kids) { if (k && typeof k === 'object') k.parent = this; }
    this.children = kids;
  }
  setAttribute(k, v) { this[k] = v; }
  addEventListener() {}
}
class FakeHTMLElement extends FakeEl {
  attachShadow() { this.shadowRoot = new FakeEl('#shadow'); return this.shadowRoot; }
}

const defined = {};
globalThis.HTMLElement = FakeHTMLElement;
globalThis.customElements = { define: (name, cls) => { defined[name] = cls; } };
globalThis.document = {
  createElement: (tag) => new FakeEl(tag),
  createTextNode: (text) => ({ nodeType: 3, textContent: String(text) }),
};
let rafFn = null;
globalThis.requestAnimationFrame = (fn) => { rafFn = fn; return 1; };
globalThis.cancelAnimationFrame = () => { rafFn = null; };

await import('../widgets/markdown-view/markdown-view.js');
const View = defined['nusa-markdown-view'];

function textOf(node) {
  let out = node.textContent && node.nodeType === 3 ? node.textContent : '';
  for (const c of node.children || []) out += textOf(c);
  return out;
}

// `content =` sets _dirty so a detach between set and frame re-renders on
// reconnect; appendStreamText never sets it — a stream chunk pending in
// the canceled frame is lost: reconnecting leaves the view empty while
// _content holds the text.
test('a stream chunk pending at detach renders on reconnect', () => {
  const view = new View();
  view.isConnected = true;
  view.appendStreamText('hi');
  // Detach before the frame runs — the rAF is canceled.
  view.disconnectedCallback();
  assert.equal(rafFn, null, 'frame canceled on detach');
  // Reconnect: the pending content must come back.
  view.isConnected = true;
  view.connectedCallback();
  if (rafFn) rafFn(); // a re-armed frame also satisfies the contract
  assert.ok(textOf(view.root).includes('hi'), 'pending stream text never rendered');
});
