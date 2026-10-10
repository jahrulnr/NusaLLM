// Shared DOM mechanics for widgets: element building that never touches
// innerHTML, event emission that crosses shadow boundaries, and the
// shadow-root + stylesheet bootstrap every widget repeats.

export function el(tag, attrs = {}, children = []) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (value == null || value === false) continue;
    if (key === 'class') node.className = value;
    else if (key === 'text') node.textContent = value;
    else if (key === 'value') node.value = value;
    else if (key === 'checked') node.checked = !!value;
    else if (key.startsWith('on') && typeof value === 'function') node.addEventListener(key.slice(2), value);
    else if (value === true) node.setAttribute(key, '');
    else node.setAttribute(key, String(value));
  }
  for (const child of [].concat(children)) {
    if (child == null) continue;
    node.append(child);
  }
  return node;
}

export function emit(host, type, detail = {}, options = {}) {
  // options.cancelable lets a listener veto the emit — dispatchEvent
  // then returns false to the caller.
  return host.dispatchEvent(new CustomEvent(type, {
    detail, bubbles: true, composed: true,
    cancelable: !!options.cancelable,
  }));
}

export function clear(node) {
  while (node.firstChild) node.removeChild(node.firstChild);
}

// Widget base: every widget is an autonomous custom element with a
// shadow root carrying its own paired stylesheet. The sheet is a plain
// <link> — CSP-friendly ('self') and browser-cached per element.
export class Widget extends HTMLElement {
  constructor() {
    super();
    this.attachShadow({ mode: 'open' });
    // `stylesheet` is set in each widget module as
    // `new URL('./name.css', import.meta.url)` — import.meta resolves
    // per-module, so the URL must be captured in the widget's own file.
    this.shadowRoot.append(el('link', { rel: 'stylesheet', href: this.constructor.stylesheet }));
    // The wrapper must fill the host so a widget's height:100% chain
    // (grid rows, flex children, internal scroll containers) resolves
    // against the host box, not auto content height.
    this.root = el('div', { class: 'widget' });
    this.root.style.height = '100%';
    this.shadowRoot.append(this.root);
  }
}
