// Runs the black-box contract checks: one scratch server, one local provider
// stub, and a pass/fail per check with the bytes that were observed. No
// screenshots, no assertions hidden in a framework — a scenario returns its
// evidence and the runner only formats it.
//
//   node test/repro/run.mjs                 # every scenario
//   node test/repro/run.mjs t01 t04         # by prefix
//   node test/repro/run.mjs --strict        # a pending fix counts as failure
//
// Round 1 semantics: every scenario asserts the CONTRACT (what the fix makes
// true), not the round-0 defect. `expected.json` says whether the production
// fix has landed:
//   pending — the contract is known and the fix is not in the tree yet; a red
//             check is reported as PENDING, never as a pass.
//   landed  — the fix is in; a red check is a regression (exit 2).
// A pending scenario whose checks all pass is reported as PASS plus a note to
// flip the manifest — the status is never silently assumed.
//
// Exit status: 0 every selected scenario passed or is pending, 1 a scenario
// could not run (harness error), 2 a landed contract regressed (or any failure
// under --strict).
import { readFileSync } from 'node:fs';
import { startServer } from './lib/serve.mjs';
import { startStub } from './lib/stub.mjs';

const scenarios = {
  t01: () => import('./scenarios/t01-path.mjs'),
  t01b: () => import('./scenarios/t01b-path-regressions.mjs'),
  t02: () => import('./scenarios/t02-retry.mjs'),
  t03: () => import('./scenarios/t03-stream.mjs'),
  t04: () => import('./scenarios/t04-watch.mjs'),
  t08: () => import('./scenarios/t08-markdown-split.mjs'),
  t10: () => import('./scenarios/t10-late-watch.mjs'),
  t11: () => import('./scenarios/t11-diagram.mjs'),
  t16: () => import('./scenarios/t16-code.mjs'),
  t17: () => import('./scenarios/t17-replay-bound.mjs'),
  t18: () => import('./scenarios/t18-steer-input-id.mjs'),
  tc: () => import('./scenarios/terminal-contract.mjs'),
  tp: () => import('./scenarios/persist-order.mjs'),
  tr: () => import('./scenarios/replay-exact-fit.mjs'),
};

const expected = JSON.parse(readFileSync(new URL('./expected.json', import.meta.url), 'utf8'));
const argv = process.argv.slice(2);
const strict = argv.includes('--strict');
const args = argv.filter((a) => !a.startsWith('-'));
const selected = args.length ? Object.keys(scenarios).filter((k) => args.some((a) => k.startsWith(a))) : Object.keys(scenarios);
if (!selected.length) {
  console.error('no scenario matches ' + args.join(' '));
  process.exit(2);
}

const paint = (code) => (s) => (process.stdout.isTTY ? `\x1b[${code}m${s}\x1b[0m` : s);
const red = paint(31), green = paint(32), yellow = paint(33), dim = paint(2), bold = paint(1), cyan = paint(36);

let harnessErrors = 0;
let regressions = 0;
let pendingChecks = 0;
const table = [];
for (const key of selected) {
  const mod = await scenarios[key]();
  const scenario = mod.default;
  const want = expected[key] || { status: 'pending', owner: '?', fix: 'unlisted scenario' };
  console.log('');
  console.log(bold(`${key.toUpperCase()} · ${scenario.ticket} — ${scenario.title}`) + dim(`  [${want.owner} · fix ${want.status}]`));
  let result;
  try {
    result = await scenario.run({ startServer, startStub });
  } catch (err) {
    harnessErrors++;
    table.push([key, 'ERROR', err.message]);
    console.log(red('  ERROR ') + (err && err.stack ? err.stack.split('\n').slice(0, 8).join('\n         ') : err));
    continue;
  }
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
console.log('');
if (harnessErrors) {
  console.log(red(`${harnessErrors} scenario(s) could not run`));
  process.exit(1);
}
if (regressions || (strict && pendingChecks)) {
  console.log(red(regressions
    ? `${regressions} landed contract(s) regressed`
    : `${pendingChecks} check(s) pending under --strict`));
  process.exit(2);
}
const pending = table.filter(([, state]) => state === 'PENDING').length;
if (pending) console.log(yellow(`${pending} scenario(s) pending — the production fix is not in the tree yet`));
else console.log(green('every selected contract holds'));
