// A scratch `nusallm serve` for one reproduction: its own temp root, its own
// data dir, its own HOME (so `~` and the skills/root defaults cannot touch
// the operator's files), an ephemeral port, and a guaranteed teardown.
import { spawn, spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync, rmSync, readFileSync, existsSync } from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';

const repoRoot = path.resolve(path.dirname(new URL(import.meta.url).pathname), '../../..');

export const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// Every spawned server is tracked so a harness error (a scenario throwing
// between spawn and teardown) can never leave a scratch server running after
// the runner exits.
const liveServers = new Set();
process.once('exit', () => {
  for (const child of liveServers) {
    try { child.kill('SIGKILL'); } catch { /* already gone */ }
  }
});

export function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.on('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const { port } = srv.address();
      srv.close(() => resolve(port));
    });
  });
}

// buildBinary compiles cmd/nusallm once per run unless NUSALLM_BIN names a
// binary. A stale binary is the classic false negative in a black-box kit.
function buildBinary(dest) {
  if (process.env.NUSALLM_BIN) return process.env.NUSALLM_BIN;
  const out = spawnSync('go', ['build', '-o', dest, './cmd/nusallm'], {
    cwd: repoRoot,
    encoding: 'utf8',
  });
  if (out.status !== 0) throw new Error('go build failed:\n' + out.stderr);
  return dest;
}

export async function startServer({ label = 'repro' } = {}) {
  const root = mkdtempSync(path.join(os.tmpdir(), `nusallm-${label}-`));
  const home = path.join(root, 'home');
  const data = path.join(root, 'data');
  const workspace = path.join(root, 'ws');
  for (const dir of [home, data, workspace]) mkdirSync(dir, { recursive: true });
  const port = await freePort();
  const configPath = path.join(root, 'config.json');
  writeFileSync(configPath, JSON.stringify({
    server: { port, hosts: ['127.0.0.1', 'localhost'] },
    storage: { dir: data },
  }, null, 2));
  const bin = buildBinary(path.join(root, 'nusallm'));

  const logs = [];
  const child = spawn(bin, ['serve'], {
    cwd: root,
    env: {
      ...process.env,
      HOME: home,
      CONFIG_PATH: configPath,
      NUSALLM_QA_KEY: 'qa-key',
      // The update check and the model catalog are both network calls this
      // kit must not make.
      NO_COLOR: '1',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  child.stdout.on('data', (b) => logs.push(b.toString()));
  child.stderr.on('data', (b) => logs.push(b.toString()));
  liveServers.add(child);
  child.on('exit', () => { liveServers.delete(child); });

  const authority = `127.0.0.1:${port}`;
  const base = `http://${authority}`;
  let exited = null;
  child.on('exit', (code, signal) => { exited = { code, signal }; });

  const deadline = Date.now() + 20_000;
  for (;;) {
    if (exited) throw new Error(`server exited during boot (${JSON.stringify(exited)}):\n${logs.join('')}`);
    if (Date.now() > deadline) throw new Error(`server did not answer /api/health:\n${logs.join('')}`);
    try {
      const res = await fetch(base + '/api/health');
      if (res.ok) break;
    } catch { /* not listening yet */ }
    await delay(50);
  }

  const request = async (method, url, body) => {
    const headers = {};
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    // Unsafe methods need an exact Origin: the same gate a browser meets.
    if (method !== 'GET') headers.Origin = base;
    const res = await fetch(base + url, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
    const text = await res.text();
    let json = null;
    try { json = text ? JSON.parse(text) : null; } catch { /* problem text */ }
    return { status: res.status, headers: res.headers, text, json };
  };

  const ready = {
    root, home, data, workspace, port, authority, base, configPath,
    origin: base,
    bin, logs, request,
    get exited() { return exited; },

    // seedConversation writes the same two files the repository writes:
    // meta.json plus active.jsonl, one `{"type":"message"}` per line. A
    // reproduction seeds storage instead of driving a provider to produce
    // history it then has to undo.
    seedConversation({ id, title = 'seeded', route = 'stub', modelId = 'qa-model', workspace: ws = workspace, messages }) {
      const dir = path.join(data, 'conversations', id);
      mkdirSync(dir, { recursive: true });
      const at = new Date('2026-01-02T03:04:05Z').toISOString();
      const meta = {
        id, title, route, modelId, workspace: ws, environment: 'local',
        usage: { inputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, outputTokens: 0 },
        messageCount: messages.length, activeCount: messages.length, sealedSegments: 0,
        createdAt: at, updatedAt: at,
      };
      writeFileSync(path.join(dir, 'meta.json'), JSON.stringify(meta, null, 2));
      const lines = messages.map((m) => JSON.stringify({ type: 'message', data: m })).join('\n') + '\n';
      writeFileSync(path.join(dir, 'active.jsonl'), lines);
      return dir;
    },

    // addGateway writes one gateway record. The key is an env ref, so the
    // file stays credential-free and the server resolves it at load. `style`
    // picks the wire the adapter speaks: completion (default), messages, or
    // responses.
    addGateway({ id = 'stub', endpoint, style = 'completion', models = [{ id: 'qa-model' }] }) {
      const dir = path.join(data, 'gateways');
      mkdirSync(dir, { recursive: true });
      const record = { id, name: 'QA stub', style, endpoint, apiKey: 'env:NUSALLM_QA_KEY', stream: true, models };
      writeFileSync(path.join(dir, id + '.json'), JSON.stringify(record, null, 2));
    },

    readConversationFile(id, name) {
      const file = path.join(data, 'conversations', id, name);
      return existsSync(file) ? readFileSync(file, 'utf8') : null;
    },

    async close() {
      if (!exited) {
        child.kill('SIGTERM');
        const until = Date.now() + 5000;
        while (!exited && Date.now() < until) await delay(50);
        if (!exited) child.kill('SIGKILL');
      }
      // Kept when the run failed so the caller can print the tail; the
      // scenarios print their own temp root instead of hiding it.
      if (process.env.QA_KEEP) return root;
      rmSync(root, { recursive: true, force: true });
      return root;
    },
  };
  return ready;
}

// message helpers keep the seeded records close to the storage schema.
export function userMessage(id, content, at = '2026-01-02T03:04:05Z') {
  return { id, role: 'user', content, userType: 'origin', createdAt: at };
}
export function assistantMessage(id, { content = '', parts = [], toolCalls = [], finishReason = 'stop', at = '2026-01-02T03:04:06Z' } = {}) {
  return { id, role: 'assistant', content, parts, toolCalls, finishReason, createdAt: at };
}
export function textPart(value) { return { kind: 'text', text: value }; }
export function toolCallPart(id) { return { kind: 'tool_call', toolCallId: id }; }
export function storedToolCall(id, name, input, output) {
  return { id, name, input, output, outputSet: true, callAt: '2026-01-02T03:04:05Z' };
}
