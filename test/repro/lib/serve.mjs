// A scratch `nusallm serve` for one reproduction: its own temp root, its own
// data dir, its own HOME (so `~` and the skills/root defaults cannot touch the
// operator's files), an ephemeral port, and a guaranteed teardown.
//
// Lifecycle rules this file enforces, because a harness that races itself
// produces failures that look like product bugs:
//   - one start or close at a time (a module-level lock), so a teardown can
//     never run inside another server's boot;
//   - a root is deleted only after its child has been reaped;
//   - readiness is proven against *this* child (a probe conversation that only
//     this data dir holds), never against whatever answers on the port;
//   - every failure carries the root, the port, the child state and the root's
//     listing, so a harness fault is diagnosable at a glance.
import { spawn, spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync, rmSync, readFileSync, existsSync, readdirSync } from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';

const repoRoot = path.resolve(path.dirname(new URL(import.meta.url).pathname), '../../..');

export const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// child -> root, for the global cleanup.
const liveServers = new Map();

// One lifecycle operation at a time: startServer and close() both take the
// lock, so a late close from a timed-out scenario cannot delete a root a new
// server is booting in.
let lifecycle = Promise.resolve();
function exclusive(fn) {
  const run = lifecycle.then(fn, fn);
  lifecycle = run.then(() => {}, () => {});
  return run;
}

function describeRoot(root) {
  try {
    const entries = readdirSync(root, { withFileTypes: true }).map((e) => e.name + (e.isDirectory() ? '/' : ''));
    return `root ${root} exists, entries: ${entries.join(', ') || '(empty)'}`;
  } catch (err) {
    return `root ${root} is unreadable: ${err.message}`;
  }
}

// awaitExit waits for the child's exit event, bounded.
function awaitExit(child, ms) {
  if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve(true);
  return new Promise((resolve) => {
    const timer = setTimeout(() => { child.off('exit', onExit); resolve(false); }, ms);
    const onExit = () => { clearTimeout(timer); resolve(true); };
    child.once('exit', onExit);
  });
}

// closeAllServers stops every scratch server still alive: SIGTERM, SIGKILL
// after a grace, and the root is removed only once the child is gone.
export async function closeAllServers(graceMs = 2000) {
  await Promise.all([...liveServers.keys()].map(async (child) => {
    const root = liveServers.get(child);
    child.kill('SIGTERM');
    if (!(await awaitExit(child, graceMs))) {
      child.kill('SIGKILL');
      await awaitExit(child, graceMs);
    }
    liveServers.delete(child);
    if (!process.env.QA_KEEP && (child.exitCode !== null || child.signalCode !== null)) {
      try { rmSync(root, { recursive: true, force: true }); } catch { /* best effort */ }
    }
  }));
}

// freePort asks the kernel for a port, then proves it is still free. The
// between-close-and-spawn window is where a stale listener hides; asserting
// here turns that into a clear harness error instead of a wrong-server 404.
export async function freePort(attempts = 5) {
  for (let i = 0; i < attempts; i++) {
    const port = await new Promise((resolve, reject) => {
      const srv = net.createServer();
      srv.on('error', reject);
      srv.listen(0, '127.0.0.1', () => {
        const { port: p } = srv.address();
        srv.close(() => resolve(p));
      });
    });
    const free = await new Promise((resolve) => {
      const probe = net.createServer();
      probe.once('error', () => resolve(false));
      probe.listen(port, '127.0.0.1', () => probe.close(() => resolve(true)));
    });
    if (free) return port;
  }
  throw new Error('could not reserve a loopback port after ' + attempts + ' attempts');
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

async function bootServer({ label }) {
  const root = mkdtempSync(path.join(os.tmpdir(), `nusallm-${label}-`));
  const home = path.join(root, 'home');
  const data = path.join(root, 'data');
  const workspace = path.join(root, 'ws');
  for (const dir of [home, data, workspace]) mkdirSync(dir, { recursive: true });
  if (!existsSync(home) || !existsSync(data) || !existsSync(workspace)) {
    throw new Error(`scratch root not usable before spawn: ${describeRoot(root)}`);
  }
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
      NO_COLOR: '1',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  child.stdout.on('data', (b) => logs.push(b.toString()));
  child.stderr.on('data', (b) => logs.push(b.toString()));
  liveServers.set(child, root);
  let exited = null;
  child.on('exit', (code, signal) => { exited = { code, signal }; });

  const authority = `127.0.0.1:${port}`;
  const base = `http://${authority}`;
  const failure = (why) => new Error([
    `${why} (port ${port})`,
    describeRoot(root),
    `config: ${existsSync(configPath) ? 'present' : 'missing'} · home: ${existsSync(home) ? 'present' : 'missing'}`,
    `child: ${exited ? 'exited ' + JSON.stringify(exited) : 'alive'}`,
    `logs:\n${logs.join('') || '(none)'}`,
  ].join('\n'));

  const deadline = Date.now() + 20_000;
  for (;;) {
    if (exited) throw failure('server exited during boot');
    if (Date.now() > deadline) throw failure('server did not answer /api/health');
    try {
      const res = await fetch(base + '/api/health');
      if (res.ok) break;
    } catch { /* not listening yet */ }
    await delay(50);
  }
  // A listener that answered may not be ours: the port could have been taken
  // between freePort and spawn, in which case the child is already dead and
  // the answer came from a stale server on another data dir.
  await delay(40);
  if (exited) throw failure('server exited while answering /api/health');

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

  // seedConversation writes the same two files the repository writes:
  // meta.json plus active.jsonl, one `{"type":"message"}` per line.
  const seedConversation = ({ id, title = 'seeded', route = 'stub', modelId = 'qa-model', workspace: ws = workspace, messages }) => {
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
  };

  // Ownership probe: a conversation only this data dir holds. If the answer is
  // not 200, the thing on the port is not this child — fail with the facts
  // rather than letting a later 404 masquerade as a product bug.
  const probeID = 'c_qa_probe_' + Math.random().toString(36).slice(2, 10);
  seedConversation({ id: probeID, messages: [{ id: 'm_probe', role: 'user', content: 'probe', userType: 'origin', createdAt: '2026-01-02T03:04:05Z' }] });
  const probe = await request('GET', `/api/conversations/${probeID}`);
  if (probe.status !== 200 || probe.json?.id !== probeID) {
    throw failure(`the listener is not this scratch server (probe ${probeID} → ${probe.status}: ${probe.text.slice(0, 160)})`);
  }

  const ready = {
    root, home, data, workspace, port, authority, base, configPath,
    origin: base,
    bin, logs, request,
    get exited() { return exited; },
    seedConversation,

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

    close: () => exclusive(async () => {
      if (!exited) {
        child.kill('SIGTERM');
        if (!(await awaitExit(child, 2000))) {
          child.kill('SIGKILL');
          await awaitExit(child, 2000);
        }
      }
      liveServers.delete(child);
      // A living child keeps its root: deleting under it produced the boot
      // errors this harness must never cause. The root is left for inspection.
      if (!exited) return root;
      if (!process.env.QA_KEEP) rmSync(root, { recursive: true, force: true });
      return root;
    }),
  };
  return ready;
}

export function startServer({ label = 'repro' } = {}) {
  return exclusive(() => bootServer({ label }));
}
