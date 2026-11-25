// T-26 — the WebUI's layer rules as a test (the FE half of T-20).
//
// The map is `docs/audit/boundaries-web.md`: transport (api, socket) → state
// (store) → controller (chat) → projection (turn, markdown/mdsafe) → views
// (components, app). The rules below are the ones that can be checked from the
// import graph alone, so they need no DOM and no server: this test reads the
// sources and prints every offending file:line with the import that breaks it.
//
// It is deliberately not a refactor: `settings.js` and `pickers.js` importing
// `api.js` is an accepted deviation (W-01) — the allowlist below is where that
// decision lives, with its reason, so a new exception has to be written down.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

// ---------- reading the import graph ----------

// import x from 'y'; import { a, b } from 'y'; import 'y'; export … from 'y'.
const IMPORT = /(?:^|\n)\s*(?:import|export)\s+(?:[^;]*?\bfrom\s*)?['"]([^'"]+)['"]/g;

function sources() {
  const out = ['app.js', ...readdirSync(path.join(web, 'lib')).filter((f) => f.endsWith('.js')).map((f) => 'lib/' + f),
    ...readdirSync(path.join(web, 'components')).filter((f) => f.endsWith('.js')).map((f) => 'components/' + f)];
  return out.sort();
}

// target resolves a specifier to a path under web/ (lib/x.js), or null for a
// vendored or bare module, which no rule constrains.
function target(spec, rel) {
  if (spec.startsWith('/vendor/') || spec.startsWith('node:') || !spec.startsWith('.') && !spec.startsWith('/')) return null;
  const abs = spec.startsWith('/') ? path.join(web, spec) : path.resolve(web, path.dirname(rel), spec);
  return path.relative(web, abs).split(path.sep).join('/');
}

function importsOf(rel) {
  const text = readFileSync(path.join(web, rel), 'utf8');
  const out = [];
  for (const m of text.matchAll(IMPORT)) {
    out.push({ spec: m[1], target: target(m[1], rel), line: text.slice(0, m.index).split('\n').length });
  }
  return out;
}

const files = sources();
const graph = new Map(files.map((rel) => [rel, importsOf(rel)]));

// offenders applies one rule to every import edge and answers the violations,
// each already carrying file:line and the offending specifier.
function offenders(rule) {
  const out = [];
  for (const [rel, imports] of graph) {
    for (const imp of imports) {
      const why = rule(rel, imp);
      if (why) out.push(`${rel}:${imp.line}  ${why} — import '${imp.spec}'`);
    }
  }
  return out;
}

const report = (violations) => violations.join('\n');

// ---------- the rules ----------

test('the map is built from every app source, so a moved file cannot pass silently', () => {
  for (const rel of ['app.js', 'lib/api.js', 'lib/chat.js', 'lib/store.js', 'lib/socket.js', 'lib/turn.js',
    'lib/markdown.js', 'lib/mdsafe.js', 'lib/diagram.js',
    'components/chat.js', 'components/composer.js', 'components/settings.js', 'components/pickers.js',
    'components/transcript.js', 'components/toolcard.js', 'components/sidebar.js', 'components/ui.js', 'components/icons.js']) {
    assert.ok(graph.has(rel), `boundaries: ${rel} is missing from the scan (moved or renamed?) — update this test`);
  }
});

test('api.js is the transport leaf and imports nothing from the app', () => {
  const v = offenders((rel, imp) => {
    if (rel !== 'lib/api.js' || !imp.target) return null;
    return 'the transport leaf must not import app code';
  });
  assert.deepEqual(v, [], '\n' + report(v));
});

test('store.js is the state leaf: no transport, controller or projection', () => {
  const v = offenders((rel, imp) => {
    if (rel !== 'lib/store.js') return null;
    if (!imp.target || !imp.target.startsWith('lib/')) return null;
    return 'the store must not depend on another app layer';
  });
  assert.deepEqual(v, [], '\n' + report(v));
});

test('turn.js is a pure projection: zero imports', () => {
  const v = offenders((rel, imp) => (rel === 'lib/turn.js' ? 'a pure projection imports nothing' : null));
  assert.deepEqual(v, [], '\n' + report(v));
});

test('only the controller imports the socket', () => {
  const v = offenders((rel, imp) => {
    if (imp.target !== 'lib/socket.js' || rel === 'lib/chat.js') return null;
    return 'only the controller owns the socket (chat.js:1-3)';
  });
  assert.deepEqual(v, [], '\n' + report(v));
});

test('components reach the server through the controller, except the two accepted leaves (W-01)', () => {
  // Accepted deviation, round 3: these two are admin leaves — they own no
  // shared state, are the only consumer of what they read, and take part in no
  // turn — so they call api.js directly instead of making chat.js a pass-through
  // (docs/audit/boundaries-web.md, W-01). Anything else goes through chat.js.
  const API_IMPORTERS = new Set(['components/settings.js', 'components/pickers.js']);
  const v = offenders((rel, imp) => {
    if (imp.target !== 'lib/api.js' || !rel.startsWith('components/')) return null;
    if (API_IMPORTERS.has(rel)) return null;
    return 'components call the controller, not the transport (add it to the W-01 allowlist only with a reason)';
  });
  assert.deepEqual(v, [], '\n' + report(v));
});

test('the controller never imports a view', () => {
  const v = offenders((rel, imp) => {
    if (rel !== 'lib/chat.js' || !imp.target?.startsWith('components/')) return null;
    return 'the controller must not know the DOM';
  });
  assert.deepEqual(v, [], '\n' + report(v));
});

test('the renderer knows neither state nor transport', () => {
  const FORBIDDEN = new Set(['lib/chat.js', 'lib/store.js', 'lib/api.js', 'lib/socket.js', 'lib/turn.js']);
  const v = offenders((rel, imp) => {
    if (!['lib/markdown.js', 'lib/mdsafe.js'].includes(rel)) return null;
    if (!FORBIDDEN.has(imp.target)) return null;
    return 'rendering is source → DOM; it must not read state or call the server';
  });
  assert.deepEqual(v, [], '\n' + report(v));
});

test('components never write controller-owned shared state', () => {
  // What a view may write: its own sidebar drawer and the preferences the store
  // exposes through setPref. Lists, rooms, the gateway catalog, the app status,
  // toasts and the route belong to the controller — they are shared, so one
  // writer (T-25 moved the last exception, state.update, out of settings.js).
  const OWNED = /(?:^|[^.\w])state\.(convs|rooms|gateways|update|toasts|route|conn)\s*=(?!=)/g;
  const v = [];
  for (const [rel, ] of graph) {
    if (!rel.startsWith('components/')) continue;
    const text = readFileSync(path.join(web, rel), 'utf8');
    for (const m of text.matchAll(OWNED)) {
      v.push(`${rel}:${text.slice(0, m.index).split('\n').length}  a view writes state.${m[1]} — ask the controller instead`);
    }
  }
  assert.deepEqual(v, [], '\n' + report(v));
});
