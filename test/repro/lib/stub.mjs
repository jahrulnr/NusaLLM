// A scripted, local stand-in for a provider. Every reproduction points a
// gateway at one of these instead of a real model: the wire is the
// OpenAI-shaped /chat/completions SSE that the `completion` style speaks,
// and the script decides what each provider call answers and how slowly.
import http from 'node:http';

export const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// ---- SSE chunk builders (OpenAI chat.completion.chunk) ----

const envelope = (delta, finish) => ({
  id: 'chatcmpl-qa',
  object: 'chat.completion.chunk',
  created: 1,
  model: 'qa-model',
  choices: [{ index: 0, delta, finish_reason: finish }],
});

export const text = (value) => envelope({ content: value }, null);
export const reasoning = (value) => envelope({ reasoning: value }, null);
export const finish = (reason = 'stop') => envelope({}, reason);
// streamError is an in-stream provider error: the HTTP status is already 200,
// so the adapter derives the status from the error object (type/code).
export const streamError = (message, type = 'rate_limit_error') => ({ error: { message, type } });

// toolCall is one complete call. The adapter also accepts fragments; a
// reproduction wants a deterministic, single-chunk call.
export const toolCall = (id, name, args, index = 0) => envelope({
  tool_calls: [{ index, id, type: 'function', function: { name, arguments: typeof args === 'string' ? args : JSON.stringify(args) } }],
}, null);

// usage rides the last chunk before the terminal marker, as OpenAI does.
export const usage = (prompt = 10, completion = 5) => ({
  id: 'chatcmpl-qa', object: 'chat.completion.chunk', created: 1, model: 'qa-model',
  choices: [], usage: { prompt_tokens: prompt, completion_tokens: completion, total_tokens: prompt + completion },
});

// ---- Anthropic messages wire (style: messages) ----

export const messages = {
  start: (inputTokens = 12) => ({
    type: 'message_start',
    message: { id: 'msg_qa', type: 'message', role: 'assistant', content: [], usage: { input_tokens: inputTokens, output_tokens: 0 } },
  }),
  textStart: (index = 0) => ({ type: 'content_block_start', index, content_block: { type: 'text', text: '' } }),
  text: (value, index = 0) => ({ type: 'content_block_delta', index, delta: { type: 'text_delta', text: value } }),
  // The verdict without the message_stop frame — the tail cut this case is about.
  verdict: (stopReason = 'end_turn', outputTokens = 5) => ({
    type: 'message_delta', delta: { stop_reason: stopReason }, usage: { output_tokens: outputTokens },
  }),
  stop: () => ({ type: 'message_stop' }),
  error: (type, message) => ({ type: 'error', error: { type, message } }),
};

// ---- OpenAI responses wire (style: responses) ----

export const responses = {
  text: (value, index = 0) => ({ type: 'response.output_text.delta', output_index: index, delta: value }),
  error: (code, message) => ({ type: 'error', code, message }),
};

// startStub runs handler(body, index, url) as an async generator; each yielded
// value is written as one `data:` line, then the [DONE] marker. Requests are
// recorded so a scenario can assert what a retry sent. `headers(body, index,
// url)` may return extra response headers (Retry-After, Content-Type).
const openStubs = new Set();

// closeAllStubs closes every stub still listening — the runner calls it after
// the last scenario, so a scenario that forgot its own close cannot hold the
// process open.
export async function closeAllStubs() {
  await Promise.all([...openStubs].map((s) => s.close().catch(() => {})));
}

export async function startStub(handler, { headers } = {}) {
  const requests = [];
  // A stub left open keeps Node's event loop alive and the runner hangs after
  // the report. Track every server so a scenario that forgets its close still
  // lets the process exit.
  const live = new Set();
  process.once('exit', () => { for (const srv of live) { try { srv.close(); } catch { /* gone */ } } });
  const server = http.createServer(async (req, res) => {
    const chunks = [];
    for await (const c of req) chunks.push(c);
    const raw = Buffer.concat(chunks).toString('utf8');
    let body = null;
    try { body = raw ? JSON.parse(raw) : null; } catch { /* recorded raw below */ }
    const index = requests.length;
    requests.push({ method: req.method, url: req.url, headers: req.headers, body, raw });
    if (!(req.url.endsWith('/chat/completions') || req.url.endsWith('/messages') || req.url.endsWith('/responses'))) {
      res.writeHead(404, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: { message: 'stub: no script for ' + req.url } }));
      return;
    }
    const extra = headers ? headers(body, index, req.url) || {} : {};
    const { body: unaryBody, status: unaryStatus, ...headerMap } = extra;
    const contentType = headerMap['Content-Type'] || 'text/event-stream';
    res.writeHead(unaryStatus || 200, { 'Cache-Control': 'no-cache', Connection: 'keep-alive', ...headerMap, 'Content-Type': contentType });
    try {
      if (unaryStatus || contentType !== 'text/event-stream') {
        // A unary answer (a provider that ignored stream:true, a gateway
        // error page, a non-2xx rejection) is written as-is.
        res.end(typeof unaryBody === 'string' ? unaryBody : raw);
        return;
      }
      for await (const chunk of handler(body, index, req.url)) {
        if (chunk === undefined) continue;
        res.write('data: ' + JSON.stringify(chunk) + '\n\n');
      }
      res.write('data: [DONE]\n\n');
    } catch (err) {
      res.write('data: ' + JSON.stringify({ error: { message: 'stub: ' + err.message } }) + '\n\n');
    }
    res.end();
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  live.add(server);
  const port = server.address().port;
  const handle = {
    port,
    origin: `http://127.0.0.1:${port}`,
    endpoint: `http://127.0.0.1:${port}/v1`,
    requests,
    close: () => new Promise((resolve) => {
      live.delete(server);
      openStubs.delete(handle);
      server.closeAllConnections?.();
      server.close(resolve);
    }),
  };
  openStubs.add(handle);
  return handle;
}
