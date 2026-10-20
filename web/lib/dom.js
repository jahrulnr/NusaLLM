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
// shadow root carrying its own paired stylesheet. Sheets are shared per
// class: the CSS file is fetched once and parsed into ONE constructed
// CSSStyleSheet that every instance adopts into its own shadow root.
//
// A per-instance <link> pays a fetch — under the server's unified
// no-cache policy, a revalidation round trip — for every element, so a
// widget created mid-stream (tool card, thinking block, markdown part)
// painted bare until its copy arrived. Adoption applies an already-parsed
// sheet in the same microtask, before the first paint of the new element.
// A <link>-loaded sheet cannot be adopted (constructed flag required),
// so the file text is fetched and replaceSync does the parse. Widget
// stylesheets must therefore stay free of @import and relative url().
const sheetCache = new Map(); // href -> Promise<CSSStyleSheet|null>

function sheetFor(url) {
  const href = String(url);
  let pending = sheetCache.get(href);
  if (pending) return pending;
  if (typeof CSSStyleSheet !== 'function' || typeof fetch !== 'function') {
    pending = Promise.resolve(null); // headless DOM (tests) — nothing to share
  } else {
    pending = fetch(href)
      .then((res) => (res.ok ? res.text() : Promise.reject(new Error(`stylesheet ${href}: ${res.status}`))))
      .then((text) => {
        const sheet = new CSSStyleSheet();
        sheet.replaceSync(text);
        return sheet;
      })
      .catch(() => {
        sheetCache.delete(href); // next instance retries the fetch
        return null;
      });
  }
  sheetCache.set(href, pending);
  return pending;
}

export class Widget extends HTMLElement {
  constructor() {
    super();
    this.attachShadow({ mode: 'open' });
    // The wrapper must fill the host so a widget's height:100% chain
    // (grid rows, flex children, internal scroll containers) resolves
    // against the host box, not auto content height.
    this.root = el('div', { class: 'widget' });
    this.root.style.height = '100%';
    this.shadowRoot.append(this.root);
    // `stylesheet` (or `stylesheets`, an ordered list) is set in each
    // widget module as `new URL('./name.css', import.meta.url)` —
    // import.meta resolves per-module, so the URL must be captured in
    // the widget's own file. Multiple sheets adopt in declared order.
    const urls = this.constructor.stylesheets || [this.constructor.stylesheet];
    Promise.all(urls.map(sheetFor)).then((sheets) => {
      const loaded = sheets.filter(Boolean);
      if (loaded.length) {
        if ('adoptedStyleSheets' in this.shadowRoot) {
          this.shadowRoot.adoptedStyleSheets = loaded;
        } else {
          // No adoption support — fall back to per-instance links.
          for (const u of urls) {
            this.shadowRoot.prepend(el('link', { rel: 'stylesheet', href: String(u) }));
          }
        }
      }
      // Until the sheet applies the widget renders bare, so a transition
      // the sheet declares would run from that bare layout on first paint.
      // Once the sheet is in, flush the applied state as the baseline and
      // mark the host `styled`; transitions key off `:host(.styled)` and
      // arm only then.
      if (typeof getComputedStyle === 'function') void getComputedStyle(this).transform;
      this.classList.add('styled');
    });
  }
}
