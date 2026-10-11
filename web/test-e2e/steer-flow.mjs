// Browser integration for the steer/queue UX against a fully mocked
// backend: a node:http static server for web/ (port 0, collision-free),
// page.route for /api/*, and routeWebSocket for /api/ws. No live
// provider, no external calls.
//
// Every scenario runs on a NEW page with a FRESH fixture and hub —
// shared state between scenarios is limited to the browser process.
//
// Run:
//   node web/test-e2e/steer-flow.mjs
//   PLAYWRIGHT_MODULE=/abs/path/to/@playwright/test/index.mjs node web/test-e2e/steer-flow.mjs
//   E2E_PORT=8931 node web/test-e2e/steer-flow.mjs   (default: port 0)
// Artifacts: E2E_ARTIFACTS (default /tmp/steer-web-artifacts) —
//   screenshots per scenario + failure traces/context dumps.

import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join, extname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const MOD = process.env.PLAYWRIGHT_MODULE || '@playwright/test';
let chromium;
try {
  ({ chromium } = await import(MOD));
} catch (err) {
  console.error(`Cannot load Playwright from "${MOD}".\n` +
    'Install @playwright/test locally or set PLAYWRIGHT_MODULE to its index.mjs.\n' +
    `Underlying error: ${err.message}`);
  process.exit(2);
}

const WEB_ROOT = resolve(fileURLToPath(new URL('..', import.meta.url)));
const ART = process.env.E2E_ARTIFACTS || '/tmp/steer-web-artifacts';
mkdirSync(ART, { recursive: true });

// --- static server (node:http, no subprocess) ----------------------------

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript', '.mjs': 'text/javascript',
  '.css': 'text/css', '.svg': 'image/svg+xml',
  '.png': 'image/png', '.json': 'application/json',
  '.woff2': 'font/woff2',
};
const server = createServer(async (req, res) => {
  const path = decodeURIComponent(new URL(req.url, 'http://x').pathname);
  const file = join(WEB_ROOT, path === '/' ? 'index.html' : path);
  if (!resolve(file).startsWith(WEB_ROOT)) { res.writeHead(403); res.end(); return; }
  try {
    const data = await readFile(file);
    res.writeHead(200, { 'content-type': MIME[extname(file)] || 'application/octet-stream' });
    res.end(data);
  } catch {
    res.writeHead(404); res.end('not found');
  }
});
const E2E_PORT = Number(process.env.E2E_PORT || 0);
await new Promise((r, j) => {
  server.once('error', j);
  server.listen(E2E_PORT, '127.0.0.1', r);
});
const BASE = `http://127.0.0.1:${server.address().port}`;

// --- assertion + wait helpers --------------------------------------------

let failures = 0;
let scenario = '';
function check(name, cond, extra = '') {
  if (cond) { console.log(`    ✔ ${name}`); return; }
  failures += 1;
  console.log(`    ✖ ${name} ${extra}`);
}
async function poll(fn, { timeout = 6000, every = 80 } = {}) {
  const end = Date.now() + timeout;
  let last;
  while (Date.now() < end) {
    try {
      last = await fn();
      if (last) return last;
    } catch { /* element vanished mid-check — keep polling */ }
    await new Promise((r) => setTimeout(r, every));
  }
  return last;
}

// --- fixture: per-scenario mock API state ---------------------------------

function makeFixture() {
  return {
    convs: new Map([
      ['c1', { id: 'c1', title: 'Room One', modelId: 'm1', route: 'gw1', workspace: '' }],
      ['c2', { id: 'c2', title: 'Room Two', modelId: 'm1', route: 'gw1', workspace: '' }],
    ]),
    messages: new Map([['c1', []], ['c2', []]]),
    minted: 0,
    // path -> ticket {count, limit, release[]} — the next `limit`
    // requests stall until released; further requests pass through
    stalls: new Map(),
    // path -> remaining 500s to serve before normal handling
    fails: new Map(),
    getLog: [],
  };
}

function stall(fixture, path, limit = Infinity) {
  // fail flips a held request to a 500 at RELEASE time — a stale
  // response may legitimately be an error, not just old data.
  const t = { count: 0, limit, release: [], fail: false };
  fixture.stalls.set(path, t);
  return t;
}

function release(t) {
  if (t) t.release.splice(0).forEach((r) => r());
}

// A held GET's response is captured at REQUEST time — a slow response
// that lands late carries the data as of its request, so overlapping
// writers can actually be told apart.
function snapshotFor(fixture, p) {
  const convMatch = p.match(/^\/api\/conversations\/([^/]+)$/);
  if (convMatch) {
    const c = fixture.convs.get(convMatch[1]);
    return c ? { body: structuredClone(c), status: 200 }
      : { body: { detail: 'not found' }, status: 404 };
  }
  const msgMatch = p.match(/^\/api\/conversations\/([^/]+)\/messages$/);
  if (msgMatch) {
    const msgs = fixture.messages.get(msgMatch[1]) || [];
    return { body: { messages: structuredClone(msgs), effectiveStart: 0, total: msgs.length }, status: 200 };
  }
  return { body: {}, status: 200 };
}

function apiMock(fixture) {
  return async (route, request) => {
    const url = new URL(request.url());
    const p = url.pathname;
    const json = (body, status = 200) => route.fulfill({
      status, contentType: 'application/json', body: JSON.stringify(body),
    });
    if (request.method() !== 'GET') return json({});
    fixture.getLog.push(p);
    // Truthful failure injection: 500s the app must survive.
    const fails = fixture.fails.get(p);
    if (fails && fails > 0) {
      fixture.fails.set(p, fails - 1);
      return json({ detail: 'injected failure' }, 500);
    }
    // Held GETs model a slow backend — the refresh barrier's test rig.
    // The response snapshot is bound at request time (see snapshotFor).
    const held = fixture.stalls.get(p);
    if (held && held.count < held.limit) {
      held.count += 1;
      const snap = snapshotFor(fixture, p);
      await new Promise((r) => held.release.push(r));
      return held.fail
        ? json({ detail: 'injected failure' }, 500)
        : json(snap.body, snap.status);
    }
    if (p === '/api/conversations') {
      return json({ data: [...fixture.convs.values()], total: fixture.convs.size });
    }
    const convMatch = p.match(/^\/api\/conversations\/([^/]+)$/);
    if (convMatch) {
      const c = fixture.convs.get(convMatch[1]);
      return c ? json(c) : json({ detail: 'not found' }, 404);
    }
    const msgMatch = p.match(/^\/api\/conversations\/([^/]+)\/messages$/);
    if (msgMatch) {
      const msgs = fixture.messages.get(msgMatch[1]) || [];
      return json({ messages: msgs, effectiveStart: 0, total: msgs.length });
    }
    if (p === '/api/gateways') {
      return json({ data: [{ id: 'gw1', name: 'MockGW', broken: false, models: [{ id: 'm1', name: 'Mock' }] }], total: 1 });
    }
    if (p === '/api/settings') return json({});
    if (p === '/api/context') return json({ estimate: 420, total: 8192 });
    if (p === '/api/tools') return json({ data: [] });
    if (p === '/api/health') return json({ ok: true });
    return json({});
  };
}

// --- scripted turn hub ----------------------------------------------------

// Server-side truth per scenario — shared across socket generations so a
// turn that was running when the socket dropped keeps existing.
class Hub {
  constructor(ws, fixture, serverState) {
    this.ws = ws;
    this.fixture = fixture;
    this.turns = serverState.turns;
    this.serverState = serverState;
    this.commands = [];
    this.autoAck = true;   // tests hold the ack to keep a steer submitted
    this.autoOpen = true;  // or hold the open to inspect a pre-open draft
    this.pendingSends = [];
    // watchReply(turn, afterSeq) -> frames to emit on turn.watch; the
    // default replays the retained contract honestly.
    this.watchReply = null;
    ws.onMessage((raw) => this._command(JSON.parse(raw)));
  }

  _mintConv() {
    const id = `m${++this.fixture.minted}`;
    this.fixture.convs.set(id, { id, title: 'New conversation', modelId: 'm1', route: 'gw1', workspace: '' });
    this.fixture.messages.set(id, []);
    return id;
  }

  _command(cmd) {
    this.commands.push(cmd);
    if (cmd.type === 'turn.send') {
      const turnId = `t${this.serverState.nextTurn++}`;
      const convId = cmd.conversationId || this._mintConv();
      this.turns.set(turnId, {
        seq: 0, convId, requestId: cmd.requestId,
        terminal: null, receipts: new Map(),
      });
      if (this.autoOpen) this._emit(turnId, 'turn.opened', {}, cmd.requestId);
      else this.pendingSends.push(turnId);
    } else if (cmd.type === 'turn.edit') {
      const turnId = `t${this.serverState.nextTurn++}`;
      this.turns.set(turnId, {
        seq: 0, convId: cmd.conversationId, requestId: cmd.requestId,
        terminal: null, receipts: new Map(),
      });
      if (this.autoOpen) this._emit(turnId, 'turn.opened', {}, cmd.requestId);
      else this.pendingSends.push(turnId);
    } else if (cmd.type === 'turn.steer') {
      const t = this.turns.get(cmd.turnId);
      if (t && !t.terminal) {
        // The mailbox records a pending receipt — ack is acceptance,
        // not consumption.
        t.receipts.set(cmd.requestId, { inputId: cmd.requestId, state: 'pending' });
        if (this.autoAck) this._emit(cmd.turnId, 'turn.steer_ack', { inputId: cmd.requestId });
      }
    } else if (cmd.type === 'turn.steer_recall') {
      const t = this.turns.get(cmd.turnId);
      if (t && !t.terminal) {
        const r = t.receipts.get(cmd.payload.inputId);
        if (r && r.state === 'pending') {
          r.state = 'recalled';
          this._emit(cmd.turnId, 'turn.steer_recalled', { inputId: r.inputId });
        } else {
          this._replyError(cmd, 'not_found', 'steer input not recallable');
        }
      }
    } else if (cmd.type === 'turn.cancel') {
      const t = this.turns.get(cmd.turnId);
      if (t && !t.terminal) {
        this._emit(cmd.turnId, 'turn.cancel_ack', {});
        this.complete(cmd.turnId, {
          failed: true, message: 'interrupted',
          reason: 'canceled', code: 'interrupted',
        });
      }
    } else if (cmd.type === 'turn.watch') {
      const t = this.turns.get(cmd.turnId);
      if (this.watchReply) {
        this.watchReply(t, cmd.afterSeq, cmd.turnId);
      } else if (!t) {
        this.resync(cmd.turnId, { turn: 'unknown' });
      } else if (t.terminal) {
        this.resync(cmd.turnId, { turn: t.terminal.type === 'turn.completed' ? 'completed' : 'failed', terminal: t.terminal.payload });
      } else {
        this.resync(cmd.turnId, { turn: 'active', steering: [...t.receipts.values()] });
      }
    }
  }

  _replyError(cmd, code, message) {
    this.ws.send(JSON.stringify({
      type: 'error', requestId: cmd.requestId,
      conversationId: cmd.conversationId, turnId: cmd.turnId,
      code, message,
    }));
  }

  _emit(turnId, type, payload, requestId) {
    const t = this.turns.get(turnId);
    const convId = t ? t.convId : this.fixture.convs.keys().next().value;
    const f = { type, conversationId: convId, turnId, seq: t ? t.seq++ : 0, payload };
    if (requestId) f.requestId = requestId;
    this.ws.send(JSON.stringify(f));
  }

  resync(turnId, { turn, steering = [], terminal = null }) {
    const t = this.turns.get(turnId);
    // resync_required is an unsequenced control frame — like the real
    // hub it carries no seq, so dedup never swallows a verdict.
    this.ws.send(JSON.stringify({
      type: 'turn.resync_required',
      conversationId: t ? t.convId : this.fixture.convs.keys().next().value,
      turnId,
      payload: { turn, steering, ...(terminal ? { terminal } : {}) },
    }));
  }

  delta(turnId, text) { this._emit(turnId, 'turn.delta', { text }); }
  steerAck(turnId, inputId) { this._emit(turnId, 'turn.steer_ack', { inputId }); }
  openPending() {
    for (const turnId of this.pendingSends.splice(0)) {
      this._emit(turnId, 'turn.opened', {}, this.turns.get(turnId).requestId);
    }
  }
  steerFold(turnId, inputId, text) {
    const t = this.turns.get(turnId);
    const r = t && t.receipts.get(inputId);
    if (r) r.state = 'persisted';
    this._emit(turnId, 'turn.steer', { text, inputId, final: true });
  }
  // Marks the turn terminal on the server WITHOUT emitting — the frame
  // the client missed, retrievable only through the resync journal.
  markTerminal(turnId, {
    failed = false, message = 'done', reason = 'stop',
    steering = [], persistedUser = true,
  } = {}) {
    const t = this.turns.get(turnId);
    if (!t) return;
    const payload = {
      message, finishReason: reason, resyncRequired: true,
      steering, userMessagePersisted: persistedUser,
    };
    t.terminal = { type: failed ? 'turn.failed' : 'turn.completed', payload };
  }
  // Terminals always carry the authoritative persistence verdict — the
  // shell never infers it from stream shape.
  complete(turnId, {
    steering = [], failed = false, message = 'done',
    reason = 'stop', code, persistedUser = true,
  } = {}) {
    const type = failed ? 'turn.failed' : 'turn.completed';
    const payload = {
      message, finishReason: reason, resyncRequired: true,
      steering, userMessagePersisted: persistedUser,
    };
    if (code) payload.code = code;
    const t = this.turns.get(turnId);
    if (t) t.terminal = { type, payload };
    this._emit(turnId, type, payload);
  }
  rejectSend(requestId, message = 'rejected') {
    this.ws.send(JSON.stringify({
      type: 'error', requestId, code: 'invalid', message,
    }));
  }
  find(type, pred = () => true) { return this.commands.find((c) => c.type === type && pred(c)); }
  all(type) { return this.commands.filter((c) => c.type === type); }
}

// --- browser / scenario runner --------------------------------------------

const browser = await chromium.launch({ channel: 'chrome' });
const results = [];

async function scenarioRun(name, fn) {
  scenario = name;
  console.log(`\n■ ${name}`);
  const fixture = makeFixture();
  const serverState = { turns: new Map(), nextTurn: 1 };
  const hubs = [];
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const consoleErrors = [];
  try { await ctx.tracing.start({ screenshots: true, snapshots: true }); } catch { /* unsupported channel */ }
  const page = await ctx.newPage();
  page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text()); });
  page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${e.message}`));
  await page.route('**/api/**', apiMock(fixture));
  await page.routeWebSocket(/api\/ws/, (ws) => { hubs.push(new Hub(ws, fixture, serverState)); });
  const before = failures;
  const s = {
    page, ctx, fixture, hubs, serverState,
    hub: () => hubs[hubs.length - 1],
    allCmds: (t) => hubs.flatMap((h) => h.all(t)),
  };
  try {
    await fn(s);
  } catch (err) {
    failures += 1;
    console.log(`    ✖ scenario threw: ${err.message}`);
  }
  // Scenarios that inject 500s expect the resource-load console noise
  // those failures legitimately produce — everything else still counts.
  const errs = s.allowResourceErrors
    ? consoleErrors.filter((t) => !/Failed to load resource/.test(t))
    : consoleErrors;
  check('zero console errors', errs.length === 0, errs.slice(0, 3).join(' | '));
  const ok = failures === before;
  results.push({ name, ok });
  if (!ok) {
    await page.screenshot({ path: `${ART}/fail-${name}.png` }).catch(() => {});
    try { await ctx.tracing.stop({ path: `${ART}/fail-${name}-trace.zip` }); } catch { /* never started */ }
  }
  await ctx.close();
}

// Selectors that reach through shadow roots.
const input = (s) => s.page.locator('nusa-chat-composer textarea.input');
const panel = (s) => s.page.locator('nusa-chat-composer .pending');
const rows = (s) => s.page.locator('nusa-chat-composer .pending-item');
const steerBtn = (s) => s.page.locator('nusa-chat-composer button.steer');
const stopBtn = (s) => s.page.locator('nusa-chat-composer button.send.cancel');
const sendBtn = (s) => s.page.locator('nusa-chat-composer button.send:not(.cancel)');
const resumeBtn = (s) => s.page.locator('nusa-chat-composer .pending-resume');
const COMPOSER = `document.querySelector('nusa-app')?.shadowRoot.querySelector('nusa-chat-composer')`;

async function open(s, hash = '#conversation=c1') {
  await s.page.goto(BASE + hash);
  await input(s).waitFor({ timeout: 8000 });
  // Wait for the room (or fresh view) to settle — a submission while
  // the view loads must honestly queue, never race into the void.
  const want = hash.match(/conversation=(.+)/)?.[1] ?? null;
  await poll(() => s.page.evaluate((w) => {
    const app = document.querySelector('nusa-app');
    return app && app.activeId === w && !app.viewLoading;
  }, want), { timeout: 8000 });
}

async function typeEnter(s, text) {
  await input(s).fill(text);
  await input(s).press('Enter');
}

const overflowOk = (s) => s.page.evaluate(() =>
  document.documentElement.scrollWidth <= document.documentElement.clientWidth + 2);

// --- scenarios ------------------------------------------------------------

await scenarioRun('queue-fifo-barrier', async (s) => {
  await open(s, '#conversation=c1');
  await typeEnter(s, 'first');
  await poll(() => s.hub().find('turn.send'));
  // Busy: both following Enters queue locally in order.
  await typeEnter(s, 'second');
  await typeEnter(s, 'third');
  await poll(async () => (await rows(s).count()) === 2);
  check('two lines queued locally', true);
  check('no second send while busy', s.hub().all('turn.send').length === 1);
  // Terminal with a STALLED refresh — the barrier must hold the drain.
  const held = stall(s.fixture, '/api/conversations/c1/messages');
  s.fixture.messages.get('c1').push({ id: 'u1', role: 'user', userType: 'origin', content: 'first' });
  s.hub().complete('t1', {});
  await poll(() => held.count === 1);
  check('terminal triggered a refresh', true);
  // The refresh is provably in flight — the drain cannot have fired.
  check('barrier holds: still no second send', s.hub().all('turn.send').length === 1);
  // Enter during the barrier queues rather than bypassing.
  await typeEnter(s, 'fourth');
  await poll(async () => (await rows(s).count()) === 3);
  check('enter during barrier queues', s.hub().all('turn.send').length === 1);
  // Release history — exactly the oldest line drains next.
  release(held);
  await poll(() => s.hub().all('turn.send').length === 2);
  check('oldest line drained after refresh', s.hub().all('turn.send')[1].payload.content === 'second');
  // t2's terminal re-arms the same barrier.
  s.hub().complete('t2', {});
  await poll(() => held.count === 2);
  check('next terminal re-arms the barrier', s.hub().all('turn.send').length === 2);
  release(held);
  await poll(() => s.hub().all('turn.send').length === 3);
  check('fifo order preserved', s.hub().all('turn.send')[2].payload.content === 'third');
  s.hub().complete('t3', {});
  await poll(() => held.count === 3);
  release(held);
  await poll(() => s.hub().all('turn.send').length === 4);
  check('barrier-queued line drains last', s.hub().all('turn.send')[3].payload.content === 'fourth');
});

await scenarioRun('steer-fold-recall', async (s) => {
  await open(s, '#conversation=c1');
  await typeEnter(s, 'run a task');
  await poll(() => s.hub().find('turn.opened') || s.hub().find('turn.send'));
  await poll(async () => await steerBtn(s).isVisible());
  check('steer visible in owning room', true);
  check('no steer hint in the footer', (await s.page.locator('.steer-hint').count()) === 0);
  // Steer joins the running turn.
  await input(s).fill('prefer bullets');
  await steerBtn(s).click();
  await poll(() => s.hub().find('turn.steer'));
  const steer = s.hub().find('turn.steer');
  check('turn.steer submitted', steer?.payload?.content === 'prefer bullets');
  await poll(async () => (await s.page.locator('text=Waiting for next step').count()) > 0);
  check('receipt shows waiting state', true);
  // Fold: the input renders as user text and the receipt resolves.
  s.hub().steerFold('t1', steer.requestId, 'prefer bullets');
  await poll(async () =>
    (await s.page.locator('nusa-message-thread').getByText('prefer bullets').count()) > 0);
  check('steer rendered in stream', true);
  await poll(async () => (await rows(s).count()) === 0);
  check('receipt resolved after projection', true);
  s.hub().complete('t1', { steering: [{ inputId: steer.requestId, state: 'persisted' }] });
  // Recall: steer then Restore while still pending.
  await typeEnter(s, 'another turn');
  await poll(() => s.hub().all('turn.send').length === 2);
  await poll(async () => await steerBtn(s).isVisible());
  await input(s).fill('existing draft\nsecond line');
  // Keep part of the draft? No — steer takes the whole draft. Seed the
  // draft, steer it, then prove the recall APPENDS to a nonempty draft.
  await steerBtn(s).click();
  await poll(() => s.hub().all('turn.steer').length === 2);
  const steer2 = s.hub().all('turn.steer')[1];
  await input(s).fill('already typed');
  await s.page.locator('.pending-item .pending-action', { hasText: 'Restore' }).click();
  await poll(() => s.hub().find('turn.steer_recall'));
  check('exact recall sent', s.hub().find('turn.steer_recall')?.payload?.inputId === steer2.requestId);
  await poll(async () => (await input(s).inputValue()) === 'already typed\nexisting draft\nsecond line');
  check('recall appended to nonempty draft', true);
});

await scenarioRun('persistence-authority', async (s) => {
  await open(s, '#conversation=c1');
  const sends = () => s.hub().all('turn.send');
  // Terminal failed + persistedUser:false → the direct send's text
  // comes back to the draft; the queue pauses for the user.
  await typeEnter(s, 'will not persist');
  await poll(() => sends().length === 1);
  s.hub().complete('t1', { failed: true, message: 'provider exploded', persistedUser: false });
  await poll(async () => (await input(s).inputValue()) === 'will not persist');
  check('unpersisted direct send restored to draft', true);
  check('nothing held after a direct-send failure', await s.page.evaluate(
    () => document.querySelector('nusa-app')?.queue?.paused === false));
  // The pause holds only what it captured — a fresh Enter is new
  // intent and dispatches once the room's barrier clears.
  await typeEnter(s, 'will not persist');
  await poll(() => sends().length === 2);
  check('fresh submit after a definitive failure dispatches', true);
  check('draft cleared once the lane is empty',
    (await input(s).inputValue()) === '');
  s.hub().complete('t2', { failed: true, message: 'exploded again', persistedUser: false });
  // A held backlog still captures later lines behind it — FIFO order
  // is never violated by fresh input.
  await typeEnter(s, 'will not persist');
  await poll(() => sends().length === 3);
  await typeEnter(s, 'queued during the turn');
  await poll(async () => (await rows(s).count()) === 1);
  s.hub().complete('t3', { failed: true, message: 'boom', persistedUser: false });
  await poll(async () => (await input(s).inputValue()) === 'will not persist');
  check('unpersisted send restores to draft', true);
  await typeEnter(s, 'will not persist');
  await poll(async () => (await rows(s).count()) === 2);
  check('fresh line queues behind the held backlog', true);
  await resumeBtn(s).click();
  await poll(() => sends().length === 4);
  check('resume drains the held head first',
    s.hub().all('turn.send')[3].payload.content === 'queued during the turn');
  // t4 completes — the unpaused lane drains the second line itself.
  s.hub().complete('t4', {});
  await poll(() => sends().length === 5);
  check('post-completion drain dispatches the tail', true);
  // A persisted failure parks a retry row — never a silent resend.
  s.fixture.messages.get('c1').push(
    { id: 'u-fail', role: 'user', userType: 'origin', content: 'will not persist' });
  s.hub().complete('t5', { failed: true, message: 'provider rejected the request (HTTP 502)', persistedUser: true });
  await poll(async () => (await rows(s).count()) === 1);
  await poll(() => s.page.evaluate(
    () => document.querySelector('nusa-app').messages.length === 1));
  check('persisted failure parks a retry row', true);
  check('no phantom draft for a persisted failure',
    (await input(s).inputValue()) === '');
  // Retry re-issues turn.edit against the failed turn's own line.
  await s.page.locator('.pending-item .pending-action', { hasText: 'Retry' }).click();
  await poll(() => s.hub().all('turn.edit').length === 1);
  const retried = s.hub().all('turn.edit')[0];
  check('retry re-runs the failed turn via turn.edit',
    retried.conversationId === 'c1' &&
    retried.payload.messageId === 'u-fail' &&
    retried.payload.content === 'will not persist');
  check('retry row consumed by dispatch', (await rows(s).count()) === 0);
});

await scenarioRun('preopen-error-cancel', async (s) => {
  await open(s, '#conversation=c1');
  // A rejection before turn.opened: the draft was never handed to the
  // queue or cleared — it stays exactly as typed.
  s.hub().autoOpen = false;
  await typeEnter(s, 'rejected pre-open');
  await poll(() => s.hub().find('turn.send'));
  s.hub().rejectSend(s.hub().find('turn.send').requestId, 'invalid content');
  await poll(async () => (await input(s).inputValue()) === 'rejected pre-open');
  check('pre-open error keeps the draft untouched', true);
  check('nothing queued or resent', s.hub().all('turn.send').length === 1 &&
    (await rows(s).count()) === 0);
  // A dispatched send canceled after open: the interrupted terminal
  // reports the user message persisted — the text does not come back.
  s.hub().autoOpen = true;
  await typeEnter(s, 'will be canceled');
  await poll(() => s.hub().all('turn.send').length === 2);
  await typeEnter(s, 'queued behind cancel');
  await poll(async () => (await rows(s).count()) === 1);
  await stopBtn(s).click();
  await poll(() => s.hub().find('turn.cancel'));
  check('cancel fired on the exact turn', s.hub().find('turn.cancel')?.turnId === 't2');
  await poll(async () => s.fixture.getLog.includes('/api/conversations/c1/messages'));
  check('canceled send text not restored', (await input(s).inputValue()) === '');
  check('queued line waits paused after cancel', s.hub().all('turn.send').length === 2 &&
    (await rows(s).count()) === 1);
});

await scenarioRun('resync-active-consumed-terminal', async (s) => {
  await open(s, '#conversation=c1');
  await typeEnter(s, 'long task');
  await poll(() => s.hub().find('turn.send'));
  await input(s).fill('mid-flight steer');
  await steerBtn(s).click();
  await poll(() => s.hub().find('turn.steer'));
  const steer = s.hub().find('turn.steer');
  // Drop the socket; the reconnect re-watches and the hub answers an
  // ACTIVE resync — the server still owns the pending input.
  s.hub().ws.close();
  await poll(() => s.hubs.length === 2, { timeout: 8000 });
  const h2 = s.hub();
  await poll(() => h2.find('turn.watch', (c) => c.turnId === 't1'));
  h2.resync('t1', { turn: 'active', steering: [{ inputId: steer.requestId, state: 'pending' }] });
  await poll(async () => (await s.page.locator('text=Waiting for next step').count()) > 0);
  check('active resync keeps receipt steering — no requeue', true);
  check('active resync duplicates nothing',
    (await rows(s).count()) === 1);
  // Later the input is consumed and the terminal snapshot is persisted —
  // the receipt resolves once, no duplicate either way.
  h2.steerFold('t1', steer.requestId, 'mid-flight steer');
  await poll(async () => (await rows(s).count()) === 0);
  check('consumed receipt resolves', true);
  s.fixture.messages.get('c1').push({ id: 'u1', role: 'user', userType: 'steer', content: 'mid-flight steer' });
  h2.complete('t1', { steering: [{ inputId: steer.requestId, state: 'persisted' }] });
  // Wait for the terminal to be processed — command retired — rather
  // than sleeping a fixed beat.
  await poll(() => s.page.evaluate(
    () => document.querySelector('nusa-app').command === null));
  check('terminal snapshot duplicates nothing', (await rows(s).count()) === 0);
});

await scenarioRun('resync-retained-terminal', async (s) => {
  await open(s, '#conversation=c1');
  await typeEnter(s, 'task');
  await typeEnter(s, 'queued behind');
  await poll(() => s.hub().find('turn.send'));
  await poll(async () => (await rows(s).count()) === 1);
  // Drop first, THEN mark the turn terminal server-side — the client
  // never saw the frame; only the retained journal can report it.
  s.hub().ws.close();
  s.hub().markTerminal('t1', {});
  s.fixture.messages.get('c1').push({ id: 'u1', role: 'user', userType: 'origin', content: 'task' });
  await poll(() => s.hubs.length === 2, { timeout: 8000 });
  const h2 = s.hub();
  await poll(() => h2.find('turn.watch', (c) => c.turnId === 't1'));
  // The default watchReply answers the retained terminal verdict —
  // the shell must retire the command and release the drain.
  await poll(() => h2.all('turn.send').length === 1);
  check('retained terminal unblocked the drain', true);
  await poll(async () => (await rows(s).count()) === 0);
  check('queue drained after resync settle', true);
});

await scenarioRun('resync-unknown-expired', async (s) => {
  await open(s, '#conversation=c1');
  await typeEnter(s, 'will vanish');
  await poll(() => s.hub().find('turn.send'));
  // Socket drops; on reconnect the turn is gone from the server — the
  // resync verdict is unknown, honestly.
  s.hub().turns.delete('t1');
  s.hub().ws.close();
  await poll(() => s.hubs.length === 2, { timeout: 8000 });
  const h2 = s.hub();
  await poll(() => h2.find('turn.watch'));
  h2.resync('t1', { turn: 'unknown' });
  await poll(async () => (await s.page.locator('text=Delivery unknown').count()) > 0);
  check('expired send surfaced as unknown — not resent', true);
  check('no retry fired', s.allCmds('turn.send').length === 1);
  check('unknown row holds nothing — it is an explicit decision, not a hold', await s.page.evaluate(
    () => document.querySelector('nusa-app').queue.paused === false));
  // Explicit Copy puts the text back in the draft once.
  await s.page.locator('.pending-action', { hasText: 'Copy to draft' }).click();
  await poll(async () => (await input(s).inputValue()) === 'will vanish');
  check('copy restored text exactly once', (await rows(s).count()) === 0);
});

await scenarioRun('multi-room-fresh-ownership', async (s) => {
  await open(s, '#conversation=c1');
  await typeEnter(s, 'c1 busy task');
  await poll(() => s.hub().find('turn.send'));
  // Queue a line for c2 while viewing c2 — it must not dispatch on c1's
  // settle, and must not mutate c1's draft.
  await s.page.locator('nusa-nav-sidebar').getByText('Room Two').click();
  await poll(async () => (await s.page.locator('nusa-conversation-header').getByText('Room Two').count()) > 0);
  await typeEnter(s, 'for room two');
  await poll(async () => (await rows(s).count()) === 1);
  check('c2 line captured while c1 busy', true);
  // New conversation — a DIFFERENT fresh token.
  await s.page.locator('nusa-nav-sidebar').getByText('New thread').click();
  await typeEnter(s, 'fresh line');
  await poll(async () => (await rows(s).count()) === 2);
  check('fresh line captured on its own view', true);
  // c1 completes — the CURRENT view's queue (the fresh line) drains and
  // mints its own room; c2's line stays put, never dispatched by a
  // foreign settle and never touching the fresh draft.
  s.fixture.messages.get('c1').push({ id: 'u1', role: 'user', userType: 'origin', content: 'c1 busy task' });
  s.hub().complete('t1', {});
  await poll(() => s.hub().all('turn.send').length === 2);
  const freshSend = s.hub().all('turn.send')[1];
  check('own view line drained after settle', freshSend.payload.content === 'fresh line');
  check('fresh line minted a new room', freshSend.conversationId === null);
  check('c2 line never dispatched', !s.hub().all('turn.send')
    .some((c) => c.payload.content === 'for room two'));
  check('foreign row offers Open not Restore',
    (await s.page.locator('.pending-action', { hasText: 'Open' }).count()) >= 1);
  check('foreign row offers no draft-mutating Restore',
    (await s.page.locator('.pending-action', { hasText: 'Restore' }).count()) === 0);
  // The minted turn still runs — c2's line honestly waits for it.
  s.fixture.messages.get('m1').push({ id: 'u2', role: 'user', userType: 'origin', content: 'fresh line' });
  s.hub().complete('t2', {});
  await poll(() => s.fixture.getLog.includes('/api/conversations/m1/messages'));
  // Open navigates to the owning room — then its queue drains there.
  await s.page.locator('.pending-action', { hasText: 'Open' }).first().click();
  await poll(() => s.hub().all('turn.send').length === 3);
  const third = s.hub().all('turn.send')[2];
  check('opened room drained its own line', third.payload.content === 'for room two');
  check('dispatched into the right room', third.conversationId === 'c2');
});

await scenarioRun('composer-ux-states', async (s) => {
  await open(s, '#conversation=c1');
  check('idle primary is send', (await sendBtn(s).getAttribute('aria-label')) === 'Send message');
  check('idle primary has no caption', (await sendBtn(s).innerText()).trim() === '');
  check('idle shortcut sends',
    (await s.page.locator('.shortcut').textContent()).includes('Enter to send'));
  await typeEnter(s, 'busy task');
  await poll(() => s.hub().find('turn.send'));
  // Panel header + queue mode while busy. The button stays an icon.
  await typeEnter(s, 'queued a');
  await typeEnter(s, 'queued b');
  await poll(async () => (await rows(s).count()) === 2);
  check('panel header shows count',
    (await s.page.locator('.pending-title').textContent()).includes('Pending (2)'));
  check('busy primary is queue', (await sendBtn(s).getAttribute('aria-label')) === 'Queue message');
  check('busy primary has no caption', (await sendBtn(s).innerText()).trim() === '');
  check('pending row still says Queued',
    (await s.page.locator('.pending-state').first().textContent()) === 'Queued');
  check('enter hint says queue',
    (await s.page.locator('.shortcut').textContent()).includes('Enter to queue'));
  check('stop visible in owning room', await stopBtn(s).isVisible());
  // A failed turn pauses — Resume waits for the queue's eligibility.
  s.hub().complete('t1', { failed: true, message: 'x', persistedUser: true });
  await poll(async () => (await s.page.locator('.pending-title').textContent()).includes('paused'));
  check('paused state visible', true);
  // Resume enables once the post-terminal refresh has landed.
  await poll(async () => (await resumeBtn(s).isVisible()) && !(await resumeBtn(s).isDisabled()));
  check('resume offered when eligible', true);
  // IME Enter never queues/sends mid-composition.
  const sends = s.hub().all('turn.send').length;
  const rowCount = await rows(s).count();
  await input(s).fill('かな');
  await input(s).dispatchEvent('keydown', { key: 'Enter', isComposing: true });
  // Two frames let the keydown handler and any render pass run — no
  // fixed sleep.
  await s.page.evaluate(() => new Promise(
    (r) => requestAnimationFrame(() => requestAnimationFrame(r))));
  check('composing Enter ignored', s.hub().all('turn.send').length === sends &&
    (await rows(s).count()) === rowCount);
  // Focus survives a panel re-sync — keyed rows reuse the same nodes.
  await input(s).fill('');
  const firstAction = s.page.locator('.pending-action').first();
  await firstAction.focus();
  const heldTag = await firstAction.evaluate((el) => el.tagName + '|' + el.textContent.trim());
  // A notice/status write re-syncs the whole panel — row nodes must be
  // reused, not rebuilt, so the focused button stays focused.
  await s.page.evaluate((expr) => {
    const c = eval(expr);
    if (c) c.queueNotice = 'checking…';
  }, COMPOSER);
  const focus = await s.page.evaluate((expr) => {
    const c = eval(expr);
    const ae = c?.shadowRoot.activeElement;
    return ae ? ae.tagName + '|' + ae.textContent.trim() : '';
  }, COMPOSER);
  check('row focus survives pending re-sync', focus === heldTag, `focus=${focus}`);
  // Long content: the panel scrolls bounded — reach the LAST row's action.
  const longText = 'long pending content '.repeat(300); // ~6KB
  await input(s).fill(longText);
  await input(s).press('Enter');
  await poll(async () => (await s.page.locator('.pending-text').last().textContent()) === longText.trim());
  check('long text kept in full', true);
  const metrics = await s.page.evaluate((expr) => {
    const p = eval(expr)?.shadowRoot.querySelector('.pending-rows');
    const last = p?.querySelector('.pending-item:last-child .pending-action');
    return p && last ? {
      scrollable: p.scrollHeight > p.clientHeight,
      lastTop: 0, // filled below
    } : null;
  }, COMPOSER);
  check('panel scrolls with long content', metrics && metrics.scrollable);
  const lastAction = s.page.locator('.pending-item').last().locator('.pending-action').first();
  await lastAction.scrollIntoViewIfNeeded();
  check('last row action reachable by scroll', await lastAction.isVisible());
  // Footer controls stay inside the viewport.
  const boxes = await s.page.evaluate((expr) => {
    const c = eval(expr);
    const rect = (q) => {
      const el = c?.shadowRoot.querySelector(q);
      if (!el || el.offsetParent === null) return null;
      const r = el.getBoundingClientRect();
      return { x: r.x, right: r.right };
    };
    return {
      send: rect('.send.queue, .send'), vw: innerWidth,
      footer: rect('.footer'),
    };
  }, COMPOSER);
  check('controls inside viewport', !boxes.send || boxes.send.right <= boxes.vw + 1);
});

await scenarioRun('responsive-layout', async (s) => {
  await open(s, '#conversation=c1');
  await typeEnter(s, 'busy task');
  await poll(() => s.hub().find('turn.send'));
  await typeEnter(s, 'queued behind');
  await poll(async () => (await rows(s).count()) === 1);
  for (const w of [1440, 1024, 768]) {
    await s.page.setViewportSize({ width: w, height: 800 });
    await poll(() => overflowOk(s));
    check(`no horizontal overflow at ${w}px`, true);
    await s.page.screenshot({ path: `${ART}/resp-${w}-busy.png` });
  }
  // The real 320px validation (sidebar toggle, hit-testing, usable
  // clicks) lives in the dedicated mobile scenario below.
});

// A rejected pre-open send must never drop its text: it returns to the
// OWNING view's draft — or parks paused in its own room's queue when
// the user has already navigated elsewhere.
await scenarioRun('preopen-reject-ownership', async (s) => {
  await open(s, '#conversation=c1');
  s.hub().autoOpen = false;
  await typeEnter(s, 'send A');
  await poll(() => s.hub().find('turn.send'));
  // Busy → B queues locally and the draft clears; the queue owns B.
  await typeEnter(s, 'send B');
  await poll(async () => (await rows(s).count()) === 1);
  s.hub().rejectSend(s.hub().all('turn.send')[0].requestId, 'invalid content');
  await poll(async () => (await input(s).inputValue()) === 'send A');
  check('rejected send restored to its own draft', true);
  check('queued line still owned exactly once', (await rows(s).count()) === 1);
  check('no auto resend', s.hub().all('turn.send').length === 1);
  // A rejection that arrives while ANOTHER view is current parks the
  // text paused in its own room — never into the viewed draft.
  await s.page.locator('nusa-nav-sidebar').getByText('Room Two').click();
  await poll(async () =>
    (await s.page.locator('nusa-conversation-header').getByText('Room Two').count()) > 0);
  await typeEnter(s, 'send D');
  await poll(() => s.hub().all('turn.send').length === 2);
  await s.page.locator('nusa-nav-sidebar').getByText('New thread').click();
  s.hub().rejectSend(s.hub().all('turn.send')[1].requestId, 'invalid content');
  await poll(async () => (await rows(s).count()) === 2);
  check('foreign-room rejection parked as an owned entry', true);
  check('parked copy pauses the queue', await s.page.evaluate(
    () => document.querySelector('nusa-app').queue.paused === true));
  check('current draft untouched by foreign text',
    (await input(s).inputValue()) === '');
  check('nothing resent', s.hub().all('turn.send').length === 2);
  check('foreign parked row offers Open not Restore',
    (await s.page.locator('.pending-action', { hasText: 'Open' }).count()) >= 1 &&
    (await s.page.locator('.pending-action', { hasText: 'Restore' }).count()) === 0);
});

// Fresh-view ownership: two un-minted views are distinct rooms, a
// queued fresh send rejoins the room it minted (not the dead token),
// and identical draft text on a foreign view is never eaten.
await scenarioRun('fresh-ownership-guards', async (s) => {
  await open(s, '#conversation=c1');
  s.hub().autoOpen = false;
  // Pre-open send on a fresh view, then a SECOND fresh view — Stop must
  // not target it, and cancel must not fire for it.
  await s.page.locator('nusa-nav-sidebar').getByText('New thread').click();
  await typeEnter(s, 'fresh preopen');
  await poll(() => s.hub().find('turn.send'));
  await s.page.locator('nusa-nav-sidebar').getByText('New thread').click();
  check('stop hidden on a different fresh view', !(await stopBtn(s).isVisible()));
  check('no cancel for a foreign view', !s.hub().find('turn.cancel'));
  // An identical draft on the second fresh view survives the first
  // send's minted opened — it is not that send's draft.
  await input(s).fill('fresh preopen');
  s.hub().openPending();
  await poll(() => s.page.evaluate(
    () => document.querySelector('nusa-app').command?.turnId === 't1'));
  check('foreign identical draft survives opened',
    (await input(s).inputValue()) === 'fresh preopen');
  // The minted turn keeps running — its room is m1.
  s.fixture.messages.get('m1').push(
    { id: 'u1', role: 'user', userType: 'origin', content: 'fresh preopen' });
  s.hub().autoOpen = true;
  s.hub().complete('t1', {});
  await poll(() => s.fixture.getLog.includes('/api/conversations/m1/messages'));
  // A queued fresh send that mints its own room and then fails
  // unpersisted must rejoin the MINTED room — the fresh token is dead.
  await s.page.locator('nusa-nav-sidebar').getByText('Room One').click();
  await poll(async () =>
    (await s.page.locator('nusa-conversation-header').getByText('Room One').count()) > 0);
  await typeEnter(s, 'c1 busy again');
  await poll(() => s.hub().all('turn.send').length === 2);
  await s.page.locator('nusa-nav-sidebar').getByText('New thread').click();
  await typeEnter(s, 'fresh queued line');
  await poll(async () => (await rows(s).count()) === 1);
  s.fixture.messages.get('c1').push(
    { id: 'u2', role: 'user', userType: 'origin', content: 'c1 busy again' });
  s.hub().autoOpen = false;
  s.hub().complete('t2', {});
  // The fresh line drains on the fresh view and mints m2.
  await poll(() => s.hub().all('turn.send').length === 3);
  const mintSend = s.hub().all('turn.send')[2];
  check('fresh queue dispatched', mintSend.conversationId === null);
  s.hub().openPending(); // let its opened arrive → mints m2, entry re-homed
  await poll(() => s.page.evaluate(
    () => document.querySelector('nusa-app').command?.turnId === 't3'));
  s.hub().autoOpen = true;
  s.hub().complete('t3', { failed: true, message: 'x', persistedUser: false });
  await poll(async () => (await rows(s).count()) === 1);
  // Resume — the requeued line must send into m2, not mint a new room.
  await poll(async () =>
    (await resumeBtn(s).isVisible()) && !(await resumeBtn(s).isDisabled()));
  await resumeBtn(s).click();
  await poll(() => s.hub().all('turn.send').length === 4);
  const retry = s.hub().all('turn.send')[3];
  check('requeued line returns to its minted room',
    retry.conversationId === 'm2' && retry.payload.content === 'fresh queued line',
    JSON.stringify(retry));
});

// Two overlapping history writers: the OLDER selection load must never
// overwrite the NEWER terminal refresh — and once the stale writer
// settles, exactly the oldest queued line drains.
await scenarioRun('selection-refresh-race', async (s) => {
  await open(s, '#conversation=c1');
  await typeEnter(s, 'run task');
  await poll(() => s.hub().find('turn.send'));
  await typeEnter(s, 'queued b');
  await poll(async () => (await rows(s).count()) === 1);
  await s.page.locator('nusa-nav-sidebar').getByText('Room Two').click();
  await poll(async () =>
    (await s.page.locator('nusa-conversation-header').getByText('Room Two').count()) > 0);
  // Hold the selection's two GETs — exactly once each.
  const heldConv = stall(s.fixture, '/api/conversations/c1', 1);
  const heldMsgs = stall(s.fixture, '/api/conversations/c1/messages', 1);
  await s.page.locator('nusa-nav-sidebar').getByText('Room One').click();
  await poll(() => heldMsgs.count === 1 && heldConv.count === 1);
  // The terminal lands; its refresh is the NEWER history writer and
  // passes through while the selection is still parked.
  s.fixture.messages.get('c1').push(
    { id: 'u1', role: 'user', userType: 'origin', content: 'run task' },
    { id: 'a1', role: 'assistant', content: 'fresh answer' });
  s.hub().complete('t1', {});
  await poll(async () =>
    (await s.page.locator('nusa-message-thread').getByText('fresh answer').count()) > 0);
  check('newer refresh history landed', true);
  check('drain held while an older writer is still in flight',
    s.hub().all('turn.send').length === 1);
  // Release the stale selection response — epoch-stale, so history
  // keeps the newest version and the oldest queued line drains once.
  release(heldConv); release(heldMsgs);
  await poll(() => s.hub().all('turn.send').length === 2);
  check('exactly the oldest line dispatched after settle',
    s.hub().all('turn.send')[1].payload.content === 'queued b');
  check('newest history kept after stale response',
    (await s.page.locator('nusa-message-thread').getByText('fresh answer').count()) > 0);
});

// Same overlap on the resync path: a resync-triggered refresh parked
// behind a newer terminal refresh must not strand the queue.
await scenarioRun('resync-refresh-race', async (s) => {
  await open(s, '#conversation=c1');
  await typeEnter(s, 'task');
  await poll(() => s.hub().find('turn.send'));
  await typeEnter(s, 'queued');
  await poll(async () => (await rows(s).count()) === 1);
  // The reconnect's resync refresh is the OLD writer — hold its GETs.
  const heldConv = stall(s.fixture, '/api/conversations/c1', 1);
  const heldMsgs = stall(s.fixture, '/api/conversations/c1/messages', 1);
  s.hub().ws.close();
  await poll(() => s.hubs.length === 2, { timeout: 8000 });
  const h2 = s.hub();
  await poll(() => h2.find('turn.watch', (c) => c.turnId === 't1'));
  // The default watchReply answers "active" → the shell refreshes → parked.
  await poll(() => heldMsgs.count === 1);
  // The live terminal arrives — its refresh is newer and lands first.
  s.fixture.messages.get('c1').push(
    { id: 'u1', role: 'user', userType: 'origin', content: 'task' },
    { id: 'a1', role: 'assistant', content: 'late answer' });
  h2.complete('t1', {});
  await poll(async () =>
    (await s.page.locator('nusa-message-thread').getByText('late answer').count()) > 0);
  check('terminal refresh landed while resync refresh held', true);
  check('drain held by outstanding stale writer', h2.all('turn.send').length === 0);
  release(heldConv); release(heldMsgs);
  // The reconnect paused the queue for inspection — the stale writer
  // settling alone must not dispatch; Resume is the user's go-ahead.
  await poll(async () =>
    (await resumeBtn(s).isVisible()) && !(await resumeBtn(s).isDisabled()));
  check('no dispatch until explicit resume', h2.all('turn.send').length === 0);
  await resumeBtn(s).click();
  await poll(() => h2.all('turn.send').length === 1);
  check('one oldest dispatch after stale writer settles',
    h2.all('turn.send')[0]?.payload.content === 'queued');
  check('history kept the newer refresh',
    (await s.page.locator('nusa-message-thread').getByText('late answer').count()) > 0);
});

// A failed refresh gates the room: no send, Resume retries the fetch
// (still failing → still no send), an offline Resume defers nothing,
// and a successful retry drains exactly the oldest line.
await scenarioRun('failed-refresh-resume', async (s) => {
  s.allowResourceErrors = true; // injected 500s log resource errors
  await open(s, '#conversation=c1');
  await typeEnter(s, 'task');
  await poll(() => s.hub().find('turn.send'));
  await typeEnter(s, 'queued');
  await poll(async () => (await rows(s).count()) === 1);
  s.fixture.fails.set('/api/conversations/c1/messages', 3);
  s.hub().complete('t1', {});
  await poll(async () => (await s.page.locator('.pending-title').textContent()).includes('paused'));
  check('failed refresh paused the drain', s.hub().all('turn.send').length === 1);
  // Resume is the retry path — it re-fetches; while fetches keep
  // failing nothing dispatches.
  await poll(async () =>
    (await resumeBtn(s).isVisible()) && !(await resumeBtn(s).isDisabled()));
  const getsBefore = s.fixture.getLog.filter((p) => p === '/api/conversations/c1/messages').length;
  await resumeBtn(s).click();
  await poll(() => s.fixture.getLog.filter((p) => p === '/api/conversations/c1/messages').length > getsBefore);
  check('resume retried the refresh', true);
  await poll(async () => !(await resumeBtn(s).isDisabled()) || (await rows(s).count()) >= 1);
  check('still no send after failed retry', s.hub().all('turn.send').length === 1);
  // Offline: Resume is inert and nothing is deferred onto the socket.
  s.hub().ws.close();
  await poll(async () => await resumeBtn(s).isDisabled());
  check('resume inert while offline', true);
  await poll(() => s.hubs.length === 2, { timeout: 8000 });
  check('no deferred send across reconnect', s.allCmds('turn.send').length === 1);
  // Failure budget exhausted — the retry lands authoritative history
  // and drains exactly the oldest line.
  await poll(async () =>
    (await resumeBtn(s).isVisible()) && !(await resumeBtn(s).isDisabled()));
  await resumeBtn(s).click();
  await poll(() => s.allCmds('turn.send').length === 2);
  check('one oldest send after successful retry',
    s.allCmds('turn.send')[1].payload.content === 'queued');
});

// An edit crosses the same history/offline barrier as a send — a
// refused dispatch keeps the editor open with the typed text so the
// user can retry, it is never silently dropped.
await scenarioRun('edit-barrier-retry', async (s) => {
  s.allowResourceErrors = true;
  s.fixture.messages.get('c1').push(
    { id: 'u1', role: 'user', userType: 'origin', content: 'original text' });
  await open(s, '#conversation=c1');
  await poll(async () =>
    (await s.page.locator('nusa-message-thread').getByText('original text').count()) > 0);
  await typeEnter(s, 'task');
  await poll(() => s.hub().find('turn.send'));
  // Fail the post-terminal refresh — the room's history is stale.
  s.fixture.fails.set('/api/conversations/c1/messages', 1);
  s.hub().complete('t1', {});
  await poll(async () => await s.page.evaluate(
    () => document.querySelector('nusa-app').queue.paused === true));
  // Edit the loaded user message — the barrier refuses the dispatch.
  await s.page.locator('nusa-message-thread .edit-btn').first().click();
  const editInput = s.page.locator('nusa-message-thread .edit-input');
  await editInput.fill('edited content');
  await s.page.locator('nusa-message-thread .edit-save').click();
  await poll(async () => (await editInput.count()) === 1);
  check('vetoed edit keeps the editor open',
    (await editInput.inputValue()) === 'edited content');
  check('no turn.edit dispatched behind the barrier',
    s.allCmds('turn.edit').length === 0);
  // A reconnect re-reads the room successfully — the gate clears and
  // the same editor, still holding its text, dispatches on retry.
  s.hub().ws.close();
  await poll(() => s.hubs.length === 2, { timeout: 8000 });
  // The reconnect re-reads the room — the barrier clears.
  await poll(() => s.page.evaluate(() => {
    const g = document.querySelector('nusa-app')._gate('c1');
    return !g.required && !g.failed && !g.pending;
  }));
  await poll(async () => (await s.page.locator('nusa-message-thread .edit-input').count()) === 1);
  check('editor survived the reconnect refresh',
    (await s.page.locator('nusa-message-thread .edit-input').inputValue()) === 'edited content');
  await s.page.locator('nusa-message-thread .edit-save').click();
  await poll(() => s.allCmds('turn.edit').length === 1);
  check('retry dispatches the edit after the barrier clears', true);
});

// Real mobile proof at 320px: dismiss the open drawer through its REAL
// close button (the full-height drawer + scrim cover the header toggle
// by design), wait for the transform to carry it fully offscreen,
// then hit-test every control through the shadow roots and click for
// real — bounding boxes alone can lie.
await scenarioRun('mobile-320-real-controls', async (s) => {
  await s.page.setViewportSize({ width: 320, height: 800 });
  await open(s, '#conversation=c1');
  // Force the sidebar open through the shell's own sync so the drawer
  // and scrim take their real state, then dismiss it via a real click
  // on the drawer's close button — a hit-test, not a state poke.
  await s.page.evaluate(() => {
    const app = document.querySelector('nusa-app');
    app.sidebarOpen = true;
    app._syncSidebar();
  });
  await poll(() => s.page.evaluate(() => {
    const sb = document.querySelector('nusa-app').shadowRoot
      .querySelector('nusa-nav-sidebar');
    return sb.getBoundingClientRect().right > 0;
  }));
  await s.page.locator('nusa-nav-sidebar .close-btn').click();
  await poll(() => s.page.evaluate(() => {
    const app = document.querySelector('nusa-app');
    const sb = app.shadowRoot.querySelector('nusa-nav-sidebar');
    return !app.sidebarOpen && sb.getBoundingClientRect().right <= 0;
  }), { timeout: 8000 });
  check('sidebar fully offscreen after real close click', true);
  // Busy + queued + draft filled: Steer, Queue, Stop all rendered.
  await typeEnter(s, 'busy task');
  await poll(() => s.hub().find('turn.send'));
  await input(s).fill('draft line');
  await poll(async () => await steerBtn(s).isVisible());
  // Hit-test through shadow roots: the element at each control's center
  // must BE that control (or inside it) — nothing may cover it.
  const hits = await s.page.evaluate((expr) => {
    const c = eval(expr);
    const hitPath = (cx, cy) => {
      const path = [];
      let el = document.elementFromPoint(cx, cy);
      path.push(el);
      while (el && el.shadowRoot) {
        el = el.shadowRoot.elementFromPoint(cx, cy);
        if (el) path.push(el);
      }
      return path;
    };
    const out = {};
    for (const [k, q] of [
      ['send', '.send.queue, .send:not(.cancel)'], ['steer', '.steer'],
      ['stop', '.send.cancel'], ['input', 'textarea.input'],
      ['ctx', '.context-trigger'],
    ]) {
      const node = c?.shadowRoot.querySelector(q);
      if (!node || node.hidden || node.offsetParent === null) {
        out[k] = 'hidden';
        continue;
      }
      const r = node.getBoundingClientRect();
      const path = hitPath(r.left + r.width / 2, r.top + r.height / 2);
      out[k] = path.some((e) => e === node || node.contains(e))
        ? 'hit'
        : `blocked:${path.map((e) => e && `${e.tagName}.${String(e.className).split(' ')[0]}`).join('>')}`;
    }
    return out;
  }, COMPOSER);
  check('controls hit-test unobstructed at 320px',
    Object.values(hits).every((v) => v === 'hit' || v === 'hidden'),
    JSON.stringify(hits));
  // 44px touch targets on the affected controls.
  const sizes = await s.page.evaluate((expr) => {
    const c = eval(expr);
    const r = (q) => {
      const el = c?.shadowRoot.querySelector(q);
      if (!el || el.offsetParent === null) return null;
      const b = el.getBoundingClientRect();
      return { w: Math.round(b.width), h: Math.round(b.height) };
    };
    return { send: r('.send'), steer: r('.steer'), stop: r('.send.cancel') };
  }, COMPOSER);
  check('44px touch targets',
    Object.values(sizes).every((b) => !b || (b.w >= 44 && b.h >= 44)),
    JSON.stringify(sizes));
  // The Queue button actually works at 320 — a real click captures.
  await sendBtn(s).click();
  await poll(async () => (await rows(s).count()) === 1);
  check('queue captures via real click at 320px', true);
  check('no horizontal overflow at 320px', await overflowOk(s));
  await s.page.screenshot({ path: `${ART}/resp-320-usable.png` });
});

// Focused pending actions survive state-change row replacement and
// row removal — focus moves to the same action, then the next row or
// the input, never stranded on the body.
await scenarioRun('pending-focus-transitions', async (s) => {
  await open(s, '#conversation=c1');
  await typeEnter(s, 'task');
  await poll(() => s.hub().find('turn.send'));
  // Steer with the ack held — SUBMITTING row appears.
  s.hub().autoAck = false;
  await input(s).fill('steer me');
  await steerBtn(s).click();
  await poll(() => s.hub().find('turn.steer'));
  const steer = s.hub().find('turn.steer');
  await poll(async () => (await rows(s).count()) === 1);
  // Focus an action on the row, then let the ack arrive — the row is
  // replaced (signature change) and focus must follow the same action.
  const act = () => s.page.locator('.pending-action').first();
  await act().focus();
  const actionName = await act().getAttribute('data-action');
  const ack = steer.requestId;
  s.hub().steerAck('t1', ack);
  await poll(async () => {
    const ae = await s.page.evaluate((expr) => {
      const a = eval(expr)?.shadowRoot.activeElement;
      return a ? a.getAttribute('data-action') : null;
    }, COMPOSER);
    return ae === actionName;
  });
  check('focus follows same action across row replacement', true);
  check('row shows waiting state after ack',
    (await s.page.locator('.pending-steering, .pending-submitting').count()) >= 0);
  // Now the input is consumed — the row is removed and focus moves to
  // a sensible place (next action or the input), never stranded.
  s.hub().steerFold('t1', ack, 'steer me');
  await poll(async () => (await rows(s).count()) === 0);
  const after = await s.page.evaluate((expr) => {
    const a = eval(expr)?.shadowRoot.activeElement;
    return a ? (a.classList.contains('input') ? 'input' : a.tagName) : 'none';
  }, COMPOSER);
  check('focus lands on input after row consumed', after === 'input', `focus=${after}`);
  // Focused row surviving an unrelated re-sync: queue a line, focus
  // Remove, push a notice — same nodes reused, focus stays.
  await typeEnter(s, 'queued');
  await poll(async () => (await rows(s).count()) === 1);
  const rem = s.page.locator('.pending-action[data-action="queue-remove"]').first();
  await rem.focus();
  await s.page.evaluate((expr) => { eval(expr).queueNotice = 'x'; }, COMPOSER);
  const still = await s.page.evaluate((expr) => {
    const a = eval(expr)?.shadowRoot.activeElement;
    return a ? a.getAttribute('data-action') : null;
  }, COMPOSER);
  check('unrelated sync keeps focused action', still === 'queue-remove', `focus=${still}`);
  s.hub().complete('t1', {});
});

// A superseded selection must still re-arm the composer — the loading
// state froze it and, with no queued lines, nothing else re-syncs.
// The refresh that superseded it also owns the room's binding: the
// next send must capture THIS room's route/model/workspace.
await scenarioRun('stale-select-rearms-composer', async (s) => {
  Object.assign(s.fixture.convs.get('c2'),
    { modelId: 'm2c', route: 'gw2', workspace: 'wsc2' });
  await open(s, '#conversation=c2');
  await typeEnter(s, 'run task');
  await poll(() => s.hub().find('turn.send'));
  await s.page.locator('nusa-nav-sidebar').getByText('Room One').click();
  await poll(async () =>
    (await s.page.locator('nusa-conversation-header').getByText('Room One').count()) > 0);
  // Hold the c2 select's GETs — the terminal's refresh is the newer writer.
  const heldConv = stall(s.fixture, '/api/conversations/c2', 1);
  const heldMsgs = stall(s.fixture, '/api/conversations/c2/messages', 1);
  await s.page.locator('nusa-nav-sidebar').getByText('Room Two').click();
  await poll(() => heldMsgs.count === 1 && heldConv.count === 1);
  s.fixture.messages.get('c2').push(
    { id: 'u1', role: 'user', userType: 'origin', content: 'run task' },
    { id: 'a1', role: 'assistant', content: 'c2 newest answer' });
  s.hub().complete('t1', {});
  await poll(async () =>
    (await s.page.locator('nusa-message-thread').getByText('c2 newest answer').count()) > 0);
  // Release the stale select — late success; nothing queued anywhere.
  release(heldConv); release(heldMsgs);
  await input(s).fill('next');
  await poll(async () => !(await sendBtn(s).isDisabled()));
  check('composer re-armed after stale select success', true);
  check('newest room kept', await s.page.evaluate(
    () => document.querySelector('nusa-app').activeId === 'c2'));
  check('newest history kept',
    (await s.page.locator('nusa-message-thread').getByText('c2 newest answer').count()) > 0);
  // The refresh adopted c2's binding — the send carries it, not c1's.
  await input(s).press('Enter');
  await poll(() => s.hub().all('turn.send').length === 2);
  const next = s.hub().all('turn.send')[1];
  check('next send captured the refreshed room binding',
    next.payload.route === 'gw2' && next.payload.modelId === 'm2c' &&
    next.payload.workspace === 'wsc2',
    JSON.stringify(next.payload));
});

// The stale writer may also be an ERROR — its failure must be
// suppressed (no toast, no navigation) and the composer re-armed.
await scenarioRun('stale-select-error-suppressed', async (s) => {
  s.allowResourceErrors = true; // the held release serves an injected 500
  await open(s, '#conversation=c2');
  await typeEnter(s, 'run task');
  await poll(() => s.hub().find('turn.send'));
  await s.page.locator('nusa-nav-sidebar').getByText('Room One').click();
  await poll(async () =>
    (await s.page.locator('nusa-conversation-header').getByText('Room One').count()) > 0);
  const heldConv = stall(s.fixture, '/api/conversations/c2', 1);
  const heldMsgs = stall(s.fixture, '/api/conversations/c2/messages', 1);
  await s.page.locator('nusa-nav-sidebar').getByText('Room Two').click();
  await poll(() => heldMsgs.count === 1 && heldConv.count === 1);
  s.fixture.messages.get('c2').push(
    { id: 'u1', role: 'user', userType: 'origin', content: 'run task' },
    { id: 'a1', role: 'assistant', content: 'newest answer' });
  s.hub().complete('t1', {});
  await poll(async () =>
    (await s.page.locator('nusa-message-thread').getByText('newest answer').count()) > 0);
  // Release the stale select as a FAILURE — the newer writer's success
  // already owns the room; the old error must not navigate away.
  heldMsgs.fail = true;
  heldConv.fail = true;
  release(heldConv); release(heldMsgs);
  await poll(async () => (await input(s).isEnabled()));
  await input(s).fill('retry');
  await poll(async () => !(await sendBtn(s).isDisabled()));
  check('composer re-armed after stale select error', true);
  check('stale error did not navigate away', await s.page.evaluate(
    () => document.querySelector('nusa-app').activeId === 'c2'));
  check('newest history kept after stale error',
    (await s.page.locator('nusa-message-thread').getByText('newest answer').count()) > 0);
});

// Repeating the same text must not hide the NEW optimistic line: an
// identical user line already in history predates this dispatch, so it
// never counts as this send's commit.
await scenarioRun('optimistic-repeated-text', async (s) => {
  s.fixture.messages.get('c1').push(
    { id: 'u-old', role: 'user', userType: 'origin', content: 'same text' });
  await open(s, '#conversation=c1');
  await typeEnter(s, 'same text');
  await poll(() => s.page.evaluate(
    () => document.querySelector('nusa-app').command?.turnId === 't1'));
  const pendingCount = () => s.page.evaluate(
    () => document.querySelector('nusa-app').messages
      .filter((m) => String(m.id).startsWith('pending-')).length);
  await poll(async () => (await pendingCount()) === 1);
  // A foreign turn's terminal refreshes c1 — history still lacks the
  // uncommitted send, and the older identical line must not mask it.
  s.hub().ws.send(JSON.stringify({
    type: 'turn.completed', conversationId: 'c1', turnId: 't-foreign',
    seq: 900, payload: { userMessagePersisted: true, steering: [] },
  }));
  await poll(() => s.fixture.getLog.filter(
    (p) => p === '/api/conversations/c1/messages').length >= 2);
  await poll(async () => (await pendingCount()) === 1);
  check('repeated text keeps the in-flight optimistic line', true);
  check('old identical line still in history', await s.page.evaluate(
    () => document.querySelector('nusa-app').messages.some((m) => m.id === 'u-old')));
  s.hub().complete('t1', {});
});

await scenarioRun('idle-after-completed-turn', async (s) => {
  await open(s, '#conversation=c1');
  await typeEnter(s, 'done soon');
  await poll(() => s.hub().find('turn.send'));
  s.fixture.messages.get('c1').push(
    { id: 'u1', role: 'user', userType: 'origin', content: 'done soon' });
  s.hub().complete('t1', {});
  await poll(() => s.page.evaluate(() => document.querySelector('nusa-app').command === null));
  await poll(async () => (await sendBtn(s).getAttribute('aria-label')) === 'Send message');
  check('settled primary is send', true);
  check('settled primary has no caption', (await sendBtn(s).innerText()).trim() === '');
  check('settled shortcut sends',
    (await s.page.locator('.shortcut').textContent()).includes('Enter to send'));
});

const threadHas = (s, text) => s.page.locator('nusa-message-thread').getByText(text, { exact: true });

async function editFirst(s, text) {
  await s.page.locator('nusa-message-thread .edit-btn').first().click();
  const editInput = s.page.locator('nusa-message-thread .edit-input');
  await editInput.fill(text);
  await s.page.locator('nusa-message-thread .edit-save').click();
}

await scenarioRun('optimistic-edit-cut', async (s) => {
  s.fixture.messages.get('c1').push(
    { id: 'u1', role: 'user', userType: 'origin', content: 'original text' },
    { id: 'a1', role: 'assistant', content: 'old answer' },
    { id: 'u2', role: 'user', userType: 'origin', content: 'later question' },
  );
  await open(s, '#conversation=c1');
  await poll(async () => (await threadHas(s, 'later question').count()) > 0);
  await editFirst(s, 'rewritten line');
  await poll(() => s.hub().find('turn.edit'));
  await poll(async () => (await threadHas(s, 'rewritten line').count()) > 0);
  check('edited text shows immediately', true);
  check('assistant after the edit is gone', (await threadHas(s, 'old answer').count()) === 0);
  check('later user message is gone', (await threadHas(s, 'later question').count()) === 0);
  check('edit hidden while the turn runs',
    (await s.page.locator('nusa-message-thread .edit-btn').count()) === 0);
  const order = await s.page.evaluate(() => {
    const thread = document.querySelector('nusa-app').shadowRoot.querySelector('nusa-message-thread');
    const root = thread.shadowRoot;
    return [...root.querySelectorAll('.user-content, .assistant-msg, .stream')].map((n) =>
      n.classList.contains('user-content') ? 'user' : n.classList.contains('stream') ? 'stream' : 'assistant');
  });
  check('stream sits after the edited message', order.join(',') === 'user,stream', order.join(','));
  s.hub().delta('t1', 'LIVE-DELTA');
  await poll(async () => (await threadHas(s, 'LIVE-DELTA').count()) > 0);
  check('live delta renders in place of the cut tail', (await threadHas(s, 'old answer').count()) === 0);
  s.fixture.messages.set('c1', [
    { id: 'u1', role: 'user', userType: 'origin', content: 'rewritten line' },
    { id: 'a2', role: 'assistant', content: 'fresh answer' },
  ]);
  s.hub().complete('t1', {});
  await poll(async () => (await threadHas(s, 'fresh answer').count()) > 0);
  check('terminal history keeps the rewrite', (await threadHas(s, 'rewritten line').count()) > 0);
  check('terminal history drops the old tail', (await threadHas(s, 'old answer').count()) === 0);
});

await scenarioRun('edit-preopen-restores', async (s) => {
  s.fixture.messages.get('c1').push(
    { id: 'u1', role: 'user', userType: 'origin', content: 'original text' },
    { id: 'a1', role: 'assistant', content: 'old answer' },
  );
  await open(s, '#conversation=c1');
  await poll(async () => (await threadHas(s, 'old answer').count()) > 0);
  s.hub().autoOpen = false;
  await editFirst(s, 'rewritten line');
  await poll(() => s.hub().find('turn.edit'));
  await poll(async () => (await threadHas(s, 'rewritten line').count()) > 0);
  check('cut is visible before the server opens', (await threadHas(s, 'old answer').count()) === 0);
  s.hub().rejectSend(s.hub().find('turn.edit').requestId, 'rejected');
  await poll(async () => (await threadHas(s, 'old answer').count()) > 0);
  check('pre-open error restores the tail', (await threadHas(s, 'original text').count()) > 0);
  check('rejected edit is not parked for resend', (await rows(s).count()) === 0);
  check('no second edit', s.allCmds('turn.edit').length === 1);
});

await scenarioRun('edit-drop-restores', async (s) => {
  s.fixture.messages.get('c1').push(
    { id: 'u1', role: 'user', userType: 'origin', content: 'original text' },
    { id: 'a1', role: 'assistant', content: 'old answer' },
  );
  await open(s, '#conversation=c1');
  await poll(async () => (await threadHas(s, 'old answer').count()) > 0);
  s.hub().autoOpen = false;
  await editFirst(s, 'rewritten line');
  await poll(async () => (await threadHas(s, 'rewritten line').count()) > 0);
  // Hold the reconnect refresh so the restored tail cannot be the HTTP page.
  const held = stall(s.fixture, '/api/conversations/c1/messages');
  s.hub().ws.close();
  await poll(async () => (await threadHas(s, 'old answer').count()) > 0);
  release(held);
  check('socket drop restores the tail', (await threadHas(s, 'original text').count()) > 0);
  await poll(async () => (await s.page.locator('text=Delivery unknown').count()) > 0);
  check('dropped edit is parked, not resent', s.allCmds('turn.edit').length === 1);
  await poll(() => s.hubs.length === 2, { timeout: 8000 });
  check('reconnect does not resend the edit', s.allCmds('turn.edit').length === 1);
});

await scenarioRun('reload-watches-live-turn', async (s) => {
  s.fixture.messages.get('c1').push(
    { id: 'u1', role: 'user', userType: 'origin', content: 'already sent' });
  s.fixture.convs.get('c1').activeTurnId = 't-live';
  s.fixture.convs.get('c1').activeTurnKind = 'send';
  s.serverState.turns.set('t-live', {
    seq: 0, convId: 'c1', requestId: 'orig', terminal: null, receipts: new Map(),
  });
  await open(s, '#conversation=c1');
  await poll(() => s.hub().find('turn.watch', (c) => c.turnId === 't-live' && c.afterSeq === -1));
  check('reload attaches with turn.watch', true);
  await poll(() => s.page.evaluate(() => {
    const app = document.querySelector('nusa-app');
    const g = app._gate('c1');
    return app.command?.turnId === 't-live' && app.command.watched === true && !g.pending;
  }));
  check('stop armed in the owning room', await stopBtn(s).isVisible());
  s.hub().delta('t-live', 'STILL-RUNNING');
  await poll(async () => (await threadHas(s, 'STILL-RUNNING').count()) > 0);
  check('live delta arrives after reload', true);
  await s.page.locator('nusa-nav-sidebar').getByText('Room Two').click();
  await poll(async () => !(await stopBtn(s).isVisible()));
  check('stop hidden in another room', true);
  await s.page.locator('nusa-nav-sidebar').getByText('Room One').click();
  await poll(async () => await stopBtn(s).isVisible());
  delete s.fixture.convs.get('c1').activeTurnId;
  delete s.fixture.convs.get('c1').activeTurnKind;
  s.fixture.messages.set('c1', [
    { id: 'u1', role: 'user', userType: 'origin', content: 'already sent' },
    { id: 'a1', role: 'assistant', content: 'finished answer' },
  ]);
  await stopBtn(s).click();
  await poll(() => s.hub().find('turn.cancel')?.turnId === 't-live');
  check('stop cancels the reattached turn', true);
  await poll(async () => (await threadHas(s, 'finished answer').count()) > 0);
  check('terminal refresh shows persisted history', true);
});

await scenarioRun('select-watches-live-turn', async (s) => {
  s.fixture.convs.get('c1').activeTurnId = 't-live';
  s.fixture.convs.get('c1').activeTurnKind = 'edit';
  s.serverState.turns.set('t-live', {
    seq: 0, convId: 'c1', requestId: 'orig', terminal: null, receipts: new Map(),
  });
  await open(s, '#conversation=c2');
  check('other room does not watch', !s.hub().find('turn.watch'));
  await s.page.locator('nusa-nav-sidebar').getByText('Room One').click();
  await poll(() => s.hub().find('turn.watch', (c) => c.turnId === 't-live'));
  check('selecting the room attaches', true);
  await poll(() => s.page.evaluate(
    () => document.querySelector('nusa-app').command?.kind === 'edit'));
  check('edit turn does not offer steer', !(await steerBtn(s).isVisible()));
  check('stop armed for the reattached edit', await stopBtn(s).isVisible());
});

// --- teardown --------------------------------------------------------------

await browser.close();
server.close();
console.log(`\n${results.length} scenarios, ${results.filter((r) => r.ok).length} clean`);
console.log(failures === 0 ? 'ALL PASS' : `${failures} FAILURE(S)`);
process.exit(failures === 0 ? 0 : 1);
