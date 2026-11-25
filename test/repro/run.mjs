// Runs the black-box contract checks: one scratch server, one local provider
// stub, and a pass/fail per check with the bytes that were observed. No
// screenshots, no assertions hidden in a framework — a scenario returns its
// evidence and the runner only formats it.
//
// What a scenario may assume (QA rule): only the stable surfaces — websocket
// frames, HTTP/JSON payloads, the DOM, and process exit codes. Never an
// internal variable name, a cosmetic wording of output, or a key the DTO does
// not carry. A check that some code is wired goes through a module's public
// API (call the projection), never a regex over source text.
//
//   node test/repro/run.mjs                 # every scenario
//   node test/repro/run.mjs t01 t04         # by prefix
//   node test/repro/run.mjs --strict        # a pending fix counts as failure
//
// Every scenario asserts the CONTRACT (what the fix makes true), not the
// round-0 defect. `expected.json` says whether the production fix has landed:
//   pending — the contract is known and the fix is not in the tree yet; a red
//             check is reported as PENDING, never as a pass.
//   landed  — the fix is in; a red check is a regression (exit 2).
// A pending scenario whose checks all pass is reported as PASS plus a note to
// flip the manifest — the status is never silently assumed.
//
// Exit status: 0 every selected scenario passed or is pending, 1 a scenario
// could not run (harness error or timeout), 2 a landed contract regressed (or
// any failure under --strict). The runner always tears down and exits itself:
// a scenario that hangs is reported as TIMEOUT and never holds the process.
import { readFileSync } from 'node:fs';
import { closeAllServers, startServer } from './lib/serve.mjs';
import { closeAllStubs, startStub } from './lib/stub.mjs';
import { closeAllSockets } from './lib/ws.mjs';

const scenarios = {
  t01: () => import('./scenarios/t01-path.mjs'),
  t01b: () => import('./scenarios/t01b-path-regressions.mjs'),
  t02: () => import('./scenarios/t02-retry.mjs'),
  t03a: () => import('./scenarios/t03a-messages-verdict.mjs'),
  t03b: () => import('./scenarios/t03b-responses-retry-after.mjs'),
  t03c: () => import('./scenarios/t03c-error-envelope.mjs'),
  t03d: () => import('./scenarios/t03d-usage-merge.mjs'),
  t03e: () => import('./scenarios/t03e-truncated-call.mjs'),
  t03f: () => import('./scenarios/t03f-provider-code.mjs'),
  t04: () => import('./scenarios/t04-watch.mjs'),
  t08: () => import('./scenarios/t08-markdown-split.mjs'),
  t10: () => import('./scenarios/t10-late-watch.mjs'),
  t11: () => import('./scenarios/t11-diagram.mjs'),
  t15: () => import('./scenarios/t15-refresh.mjs'),
  t16a: () => import('./scenarios/t16a-crlf-patch.mjs'),
  t16b: () => import('./scenarios/t16b-invalid-utf8.mjs'),
  t16c: () => import('./scenarios/t16c-exec-no-output.mjs'),
  t17: () => import('./scenarios/t17-replay-bound.mjs'),
  t18: () => import('./scenarios/t18-steer-input-id.mjs'),
  t30: () => import('./scenarios/t30-exec-session.mjs'),
  t27: () => import('./scenarios/t27-tool-outcome.mjs'),
  t31: () => import('./scenarios/t31-compact-400.mjs'),
  t33: () => import('./scenarios/t33-signal.mjs'),
  t34: () => import('./scenarios/t34-tolerant-read.mjs'),
  t34b: () => import('./scenarios/t34b-role-tolerance.mjs'),
  t34c: () => import('./scenarios/t34c-patch-stream.mjs'),
  tc: () => import('./scenarios/terminal-contract.mjs'),
  tp: () => import('./scenarios/persist-order.mjs'),
  tr: () => import('./scenarios/replay-exact-fit.mjs'),
};

const expected = JSON.parse(readFileSync(new URL('./expected.json', import.meta.url), 'utf8'));
const argv = process.argv.slice(2);
const strict = argv.includes('--strict');
const args = argv.filter((a) => !a.startsWith('-'));
const selected = args.length ? Object.keys(scenarios).filter((k) => args.some((a) => k.startsWith(a))) : Object.keys(scenarios);

const paint = (code) => (s) => (process.stdout.isTTY ? `\x1b[${code}m${s}\x1b[0m` : s);
const red = paint(31), green = paint(32), yellow = paint(33), dim = paint(2), bold = paint(1), cyan = paint(36);

const SCENARIO_TIMEOUT_MS = 30_000;

// exitNow flushes the report before leaving: process.exit() alone can truncate
// a piped summary. Every path out of main() goes through it, so a lingering
// handle can never turn a finished run into a hang.
async function exitNow(code) {
  process.exitCode = code;
  // Flush with a ceiling: waiting forever on a blocked stdout was itself a
  // way for the runner to hang after printing its summary.
  await new Promise((resolve) => {
    const timer = setTimeout(resolve, 500);
    process.stdout.write('', () => { clearTimeout(timer); resolve(); });
  });
  process.exit(code);
}

// withWatchdog bounds one scenario. A hung scenario is reported as TIMEOUT and
// the run continues; the cleanup after the loop still stops whatever it left
// running, so one bad scenario cannot hang CI.
function withWatchdog(run, ms = SCENARIO_TIMEOUT_MS) {
  let timer;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(`scenario exceeded ${ms / 1000}s`)), ms);
  });
  return Promise.race([run(), timeout]).finally(() => clearTimeout(timer));
}

async function main() {
  if (!selected.length) {
    console.error('no scenario matches ' + args.join(' '));
    await exitNow(2); return;
  }

  let harnessErrors = 0;
  let regressions = 0;
  let pendingChecks = 0;
  let timeouts = 0;
  let checksRun = 0;
  const table = [];

  for (const key of selected) {
    const mod = await scenarios[key]();
    const scenario = mod.default;
    const want = expected[key] || { status: 'pending', owner: '?', fix: 'unlisted scenario' };
    console.log('');
    console.log(bold(`${key.toUpperCase()} · ${scenario.ticket} — ${scenario.title}`) + dim(`  [${want.owner} · fix ${want.status}]`));
    let result;
    try {
      result = await withWatchdog(() => scenario.run({ startServer, startStub }));
    } catch (err) {
      if (String(err?.message || '').includes('scenario exceeded')) {
        timeouts++;
        table.push([key, 'TIMEOUT', err.message]);
        console.log(red('  TIMEOUT ') + err.message);
        // The scenario keeps running after Promise.race rejects; its resources
        // must not leak into the next one.
        await closeAllSockets();
        await closeAllStubs();
        await closeAllServers();
        continue;
      }
      harnessErrors++;
      table.push([key, 'ERROR', err.message]);
      console.log(red('  ERROR ') + (err && err.stack ? err.stack.split('\n').slice(0, 8).join('\n         ') : err));
      continue;
    }
    checksRun += result.checks.length;
    for (const check of result.checks) {
      const mark = check.ok ? green('  ✓ ') : red('  ✗ ');
      console.log(mark + check.label);
      for (const line of check.observed || []) console.log(dim('      ' + line));
    }
    const failed = result.checks.filter((c) => !c.ok);
    const passed = result.checks.length - failed.length;
    let state;
    if (!failed.length) {
      state = 'PASS';
      console.log('  ' + green('PASS') + dim(` (${passed}/${result.checks.length} checks)`));
      if (want.status === 'pending') console.log(cyan('  note: the contract already holds — flip expected.json to "landed"'));
    } else if (want.status === 'pending') {
      state = 'PENDING';
      pendingChecks += failed.length;
      console.log('  ' + yellow('PENDING') + dim(` (${passed}/${result.checks.length} checks) — fix not landed: ${want.fix}`));
    } else {
      state = 'FAIL';
      regressions++;
      console.log('  ' + red('FAIL') + dim(` (${passed}/${result.checks.length} checks) — landed fix regressed`));
    }
    if (result.notes) for (const line of result.notes) console.log(dim('  note: ' + line));
    table.push([key, state, `${passed}/${result.checks.length}`]);
  }

  console.log('');
  console.log(dim('scenario   state     checks'));
  for (const [key, state, checks] of table) console.log(`${key.padEnd(11)}${state.padEnd(10)}${checks}`);
  const count = (state) => table.filter(([, s]) => s === state).length;
  console.log(dim(`summary    scenarios=${table.length} pass=${count('PASS')} pending=${count('PENDING')} fail=${count('FAIL')} timeout=${count('TIMEOUT')} error=${count('ERROR')} checks=${checksRun}`));

  // Stop everything the scenarios left behind before the verdict: sockets
  // first (their close frames), then stubs, then child servers. This is what
  // keeps a finished run from hanging on an open handle.
  await closeAllSockets();
  await closeAllStubs();
  await closeAllServers();

  console.log('');
  if (timeouts || harnessErrors) {
    console.log(red(`${harnessErrors} scenario(s) could not run` + (timeouts ? `, ${timeouts} timed out` : '')));
    await exitNow(1); return;
  }
  if (regressions || (strict && pendingChecks)) {
    console.log(red(regressions
      ? `${regressions} landed contract(s) regressed`
      : `${pendingChecks} check(s) pending under --strict`));
    await exitNow(2); return;
  }
  const pending = table.filter(([, state]) => state === 'PENDING').length;
  if (pending) console.log(yellow(`${pending} scenario(s) pending — the production fix is not in the tree yet`));
  else console.log(green('every selected contract holds'));
  await exitNow(0); return;
}

main().catch((err) => {
  console.error(err && err.stack ? err.stack : err);
  exitNow(1);
});
