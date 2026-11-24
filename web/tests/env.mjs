// A just-enough browser for the WebUI's controller modules (lib/*.js):
// location with hashchange, localStorage, animation frames, a scriptable
// fetch, and a WebSocket the test drives by hand. Import this file before
// any module under test — store.js reads localStorage when it loads.
//
// The DOM itself is not shimmed: components need Preact + a document, and
// the projection/controller logic does not. A test that must render markup
// drives the browser instead.

const realSetTimeout = globalThis.setTimeout;

// Timers the code under test leaves running (toasts, the hourly update
// check, debounced refreshes) must not keep the test process alive.
for (const name of ['setTimeout', 'setInterval']) {
  const original = globalThis[name];
  globalThis[name] = (...args) => {
    const timer = original(...args);
    timer?.unref?.();
    return timer;
  };
}

// sleep waits on a timer that does keep the process alive.
export const sleep = (ms) => new Promise((resolve) => realSetTimeout(resolve, ms));

const listeners = new Map();
globalThis.window = globalThis;
globalThis.addEventListener = (type, fn) => {
  if (!listeners.has(type)) listeners.set(type, new Set());
  listeners.get(type).add(fn);
};
globalThis.removeEventListener = (type, fn) => listeners.get(type)?.delete(fn);
const fire = (type) => { for (const fn of listeners.get(type) || []) fn({ type }); };

const loc = { protocol: 'http:', host: 'localhost', pathname: '/', search: '', _hash: '' };
Object.defineProperty(loc, 'hash', {
  get() { return this._hash === '#' ? '' : this._hash; },
  set(value) {
    const next = value.startsWith('#') ? value : '#' + value;
    if (next === this._hash) return;
    this._hash = next;
    queueMicrotask(() => fire('hashchange'));
  },
});
globalThis.location = loc;

// history.replaceState rewrites the address without a navigation or a
// hashchange, which is how the router can take over a path URL.
globalThis.history = {
  replaceState(_state, _title, url) {
    const next = new URL(String(url), 'http://localhost');
    loc.pathname = next.pathname;
    loc.search = next.search;
    loc._hash = next.hash;
  },
};

const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => { store.set(k, String(v)); },
  removeItem: (k) => { store.delete(k); },
  clear: () => store.clear(),
};
globalThis.requestAnimationFrame = (fn) => realSetTimeout(fn, 0);
globalThis.document = { visibilityState: 'visible', addEventListener: globalThis.addEventListener };
globalThis.matchMedia = () => ({ matches: true });

// ---------- fetch ----------

let routes = [];
export const calls = [];

// mockApi replaces the fetch routes. Each key is "METHOD /path" (a path
// prefix); a value is a body, [status, body], or a function (request) →
// either. Anything unrouted is a 404 problem+json, like the server's.
export function mockApi(table = {}) {
  routes = Object.entries(table).map(([key, value]) => {
    const [method, ...rest] = key.split(' ');
    return { method, path: rest.join(' '), value };
  });
  calls.length = 0;
}

globalThis.fetch = async (url, init = {}) => {
  const method = init.method || 'GET';
  const call = { method, url, body: init.body ? JSON.parse(init.body) : undefined, headers: init.headers || {} };
  calls.push(call);
  const hit = routes.find((r) => r.method === method && url.startsWith(r.path));
  let out = hit ? hit.value : [404, { code: 'not_found', detail: 'not found' }];
  if (typeof out === 'function') out = await out(call); // a route may answer late
  const [status, body] = Array.isArray(out) ? out : [200, out];
  return new Response(body === null || status === 204 ? null : JSON.stringify(body), { status });
};

// ---------- WebSocket ----------

export const sockets = [];

class FakeWebSocket {
  static OPEN = 1;
  constructor(url) {
    this.url = url;
    this.readyState = 0;
    this.sent = [];
    sockets.push(this);
  }
  send(data) { this.sent.push(JSON.parse(data)); }
  // open and serve are the test's side of the wire.
  open() { this.readyState = 1; this.onopen?.(); }
  close() { this.readyState = 3; this.onclose?.(); }
  serve(frame) { this.onmessage?.({ data: JSON.stringify(frame) }); }
}
globalThis.WebSocket = FakeWebSocket;
