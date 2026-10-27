// Same-origin REST client for /api/*. Errors surface the RFC 9457
// problem `detail`/`code` — never a raw response body.

export class APIError extends Error {
  constructor(status, code, detail) {
    super(detail || code || `HTTP ${status}`);
    this.name = 'APIError';
    this.status = status;
    this.code = code;
    this.detail = detail;
  }
}

async function request(method, path, body) {
  const init = { method, headers: {} };
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(path, init);
  } catch (err) {
    throw new APIError(0, 'network', 'request failed');
  }
  if (!res.ok) {
    let problem = null;
    try { problem = await res.json(); } catch { /* non-problem error */ }
    throw new APIError(
      res.status,
      problem && problem.code ? problem.code : 'error',
      problem && problem.detail ? problem.detail : res.statusText,
    );
  }
  if (res.status === 204) return null;
  return res.json();
}

export class APIClient {
  listConversations({ offset = 0, limit = 100 } = {}) {
    return request('GET', `/api/conversations?offset=${offset}&limit=${limit}`);
  }

  getConversation(id) {
    return request('GET', `/api/conversations/${encodeURIComponent(id)}`);
  }

  getMessages(id, { start = -100, limit = 100 } = {}) {
    return request('GET', `/api/conversations/${encodeURIComponent(id)}/messages?start=${start}&limit=${limit}`);
  }

  renameConversation(id, title) {
    return request('PATCH', `/api/conversations/${encodeURIComponent(id)}`, { title });
  }

  deleteConversation(id) {
    return request('DELETE', `/api/conversations/${encodeURIComponent(id)}`);
  }

  compactConversation(id) {
    return request('POST', `/api/conversations/${encodeURIComponent(id)}/compactions`);
  }

  listGateways({ offset = 0, limit = 100 } = {}) {
    return request('GET', `/api/gateways?offset=${offset}&limit=${limit}`);
  }

  getGateway(id) {
    return request('GET', `/api/gateways/${encodeURIComponent(id)}`);
  }

  importGatewayModels(id) {
    return request('POST', `/api/gateways/${encodeURIComponent(id)}/import-models`);
  }

  discoverGatewayProviders(id, modelId) {
    const q = new URLSearchParams({ model: modelId });
    return request('GET', `/api/gateways/${encodeURIComponent(id)}/providers?${q}`);
  }

  // path '' asks the backend for its default (the owner's home).
  browseFs(path = '') {
    const q = path ? `?path=${encodeURIComponent(path)}` : '';
    return request('GET', `/api/fs/browse${q}`);
  }

  contextStats({ conversationId = '', route = '', modelId = '' } = {}) {
    const q = new URLSearchParams();
    if (conversationId) q.set('conversationId', conversationId);
    if (route) q.set('route', route);
    if (modelId) q.set('modelId', modelId);
    return request('GET', `/api/context?${q}`);
  }

  saveGateway(id, body) {
    return request('PUT', `/api/gateways/${encodeURIComponent(id)}`, body);
  }

  deleteGateway(id) {
    return request('DELETE', `/api/gateways/${encodeURIComponent(id)}`);
  }

  getSettings() {
    return request('GET', '/api/settings');
  }

  patchSetting(key, value) {
    return request('PATCH', '/api/settings', { key, value });
  }

  listTools(conversationId = '') {
    const query = conversationId ? `?conversationId=${encodeURIComponent(conversationId)}` : '';
    return request('GET', `/api/tools${query}`);
  }

  health() {
    return request('GET', '/api/health');
  }

  // Cached update status; the server re-runs the release lookup only when
  // its cache is stale.
  getUpdate() {
    return request('GET', '/api/update');
  }

  // Forces a fresh release lookup and returns the resulting status.
  checkUpdate() {
    return request('POST', '/api/update/check');
  }

  // Starts an update to the latest release. The server answers 202 with the
  // first progress state. A refusal (busy, disabled, nothing newer) rejects
  // with an APIError whose code names the reason.
  startUpdate() {
    return request('POST', '/api/update/install');
  }

  // The current install's progress; idle when none has run since start.
  getUpdateProgress() {
    return request('GET', '/api/update/install');
  }
}
