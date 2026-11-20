// HTTP client for the loopback API. Every failure becomes an ApiError with
// the server's problem+json `code`/`detail`, or a client-side code for
// network failure and timeout, so callers never parse provider text.

// The server sets no HTTP write timeout; upstream calls are bounded by
// service.header_timeout (30s default) plus streaming idle time. Plain
// reads/writes answer from local storage, so 30s is a generous ceiling.
// Calls that reach a provider (gateway save/import, compaction) get the
// long budget instead of failing before the backend does.
export const TIMEOUT_MS = 30_000;
export const LONG_TIMEOUT_MS = 10 * 60_000;

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function request(method, path, { body, timeout = TIMEOUT_MS, signal } = {}) {
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(new DOMException('timeout', 'TimeoutError')), timeout);
  const onAbort = () => ctl.abort(signal.reason);
  signal?.addEventListener('abort', onAbort, { once: true });
  let res;
  try {
    res = await fetch(path, {
      method,
      signal: ctl.signal,
      headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (err) {
    if (signal?.aborted) throw err;
    if (ctl.signal.aborted) throw new ApiError(0, 'timeout', 'The server took too long to answer.');
    throw new ApiError(0, 'network', 'Cannot reach the NusaLLM server. Is `nusallm serve` running?');
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener('abort', onAbort);
  }
  if (res.status === 204) return null;
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { /* non-JSON */ }
  if (!res.ok) {
    throw new ApiError(res.status, data?.code || 'http_' + res.status, data?.detail || data?.title || `Request failed (HTTP ${res.status})`);
  }
  return data;
}

const q = (params) => {
  const s = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== '') s.set(k, v);
  const str = s.toString();
  return str ? '?' + str : '';
};
const id = encodeURIComponent;

export const api = {
  conversations: (offset = 0, limit = 30) => request('GET', '/api/conversations' + q({ offset, limit })),
  conversation: (cid) => request('GET', `/api/conversations/${id(cid)}`),
  messages: (cid, start, limit = 60) => request('GET', `/api/conversations/${id(cid)}/messages` + q({ start, limit })),
  rename: (cid, title) => request('PATCH', `/api/conversations/${id(cid)}`, { body: { title } }),
  remove: (cid) => request('DELETE', `/api/conversations/${id(cid)}`),
  compact: (cid) => request('POST', `/api/conversations/${id(cid)}/compactions`, { timeout: LONG_TIMEOUT_MS }),
  context: (params) => request('GET', '/api/context' + q(params)),

  gateways: () => request('GET', '/api/gateways?limit=100'),
  gateway: (gid) => request('GET', `/api/gateways/${id(gid)}`),
  saveGateway: (gid, doc) => request('PUT', `/api/gateways/${id(gid)}`, { body: doc, timeout: LONG_TIMEOUT_MS }),
  importModels: (gid) => request('POST', `/api/gateways/${id(gid)}/import-models`, { timeout: LONG_TIMEOUT_MS }),
  deleteGateway: (gid) => request('DELETE', `/api/gateways/${id(gid)}`),
  providers: (gid, model) => request('GET', `/api/gateways/${id(gid)}/providers` + q({ model })),

  settings: () => request('GET', '/api/settings'),
  setSetting: (key, value) => request('PATCH', '/api/settings', { body: { key, value } }),
  tools: (conversationId) => request('GET', '/api/tools' + q({ conversationId })),
  browse: (path) => request('GET', '/api/fs/browse' + q({ path })),

  update: () => request('GET', '/api/update'),
  checkUpdate: () => request('POST', '/api/update/check', { timeout: 60_000 }),
  installUpdate: () => request('POST', '/api/update/install'),
  installProgress: () => request('GET', '/api/update/install'),
};

export function errorText(err) {
  if (err instanceof ApiError) return err.message;
  return err?.message || String(err);
}
