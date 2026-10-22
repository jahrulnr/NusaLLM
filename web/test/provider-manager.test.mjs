import { test } from 'node:test';
import assert from 'node:assert/strict';

// Provider manager DOM harness — same stand-in as settings-editor plus
// <dialog> show/close state and createElementNS for inline icons.
// Globals must land BEFORE the widget module evaluates: Widget extends
// HTMLElement at class definition time.
class FakeEl {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.style = {};
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.hidden = false;
    this.open = false;
    this.listeners = {};
    this.events = [];
  }
  append(...kids) { this.children.push(...kids); }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  setAttribute(k, v) {
    if (k.startsWith('data-')) {
      const camel = k.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase());
      (this.dataset ||= {})[camel] = v;
      return;
    }
    this[k] = v;
  }
  dispatchEvent(e) { this.events.push(e); return true; }
  fire(type) { (this.listeners[type] || []).forEach((fn) => fn({ target: this })); }
  showModal() { this.open = true; }
  close() { this.open = false; }
  get firstChild() { return this.children[0] || null; }
  removeChild(c) { this.children = this.children.filter((x) => x !== c); return c; }
  querySelectorAll(selector) {
    const parts = selector.split(',').map((s) => s.trim());
    const out = [];
    const visit = (node) => {
      for (const child of node.children) {
        if (!(child instanceof FakeEl)) continue;
        if (parts.some((part) => matchSel(child, part))) out.push(child);
        visit(child);
      }
    };
    visit(this);
    return out;
  }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
}
function matchSel(node, part) {
  const m = part.match(/^([a-zA-Z]+)(?:\[([^\]]+)\])?$/);
  if (!m || node.tagName !== m[1].toUpperCase()) return false;
  return !m[2] || node[m[2]] !== undefined;
}
class FakeHTMLElement extends FakeEl {
  attachShadow() { this.shadowRoot = new FakeEl('#shadow'); return this.shadowRoot; }
}

const defined = {};
globalThis.HTMLElement = FakeHTMLElement;
globalThis.customElements = { define: (name, cls) => { defined[name] = cls; } };
globalThis.document = {
  createElement: (tag) => new FakeEl(tag),
  createElementNS: (ns, tag) => new FakeEl(tag),
  addEventListener() {},
  activeElement: null,
};

await import('../widgets/provider-manager/provider-manager.js');
const PM = defined['nusa-provider-manager'];

const GW = {
  id: 'gw1', name: 'Acme', style: 'completion', endpoint: 'https://api.acme.test',
  stream: true, keySet: true, keyState: 'vaulted', broken: false,
  models: [
    {
      id: 'model-a', name: 'Alpha', route: 'alpha-v1', contextWindow: 1000000,
      outputLimit: 8192, topP: 0.9, customFlag: 'keep-me',
      inputModalities: ['text', 'image', 'file'], outputModalities: ['text'],
      supportedEfforts: ['low', 'medium', 'high'],
      pricing: { inputPerMillionUsd: 1, outputPerMillionUsd: 2, cacheReadPerMillionUsd: 0.5, cacheWritePerMillionUsd: 0.75 },
    },
    { id: 'model-b', contextWindow: 64000 },
    { id: 'model-c' },
  ],
};

function all(node) {
  const out = [];
  const visit = (n) => {
    for (const c of n.children) {
      if (!(c instanceof FakeEl)) continue;
      out.push(c);
      visit(c);
    }
  };
  visit(node);
  return out;
}
const byClass = (node, cls) => all(node).filter((n) => (n.className || '').split(' ').includes(cls));
const byText = (node, text) => all(node).filter((n) => n.textContent === text);
const byAria = (node, label) => all(node).find((n) => n['aria-label'] === label);
const openDialog = (pm) => all(pm.root).find((n) => n.tagName === 'DIALOG' && n.open);
const saveBtn = (dialog) => byClass(dialog, 'accent').find((n) => n.tagName === 'BUTTON');

test('models view renders rows with formatted ctx meta', () => {
  const pm = new PM();
  pm.gateways = [GW];
  byClass(pm.root, 'models-btn')[0].fire('click');

  // Header: back button, gateway title, refresh, and the count.
  assert.ok(byText(pm.root, 'Gateways').length, 'back button');
  assert.ok(byText(pm.root, 'Acme').length, 'gateway title');
  assert.ok(byAria(pm.root, 'Back to gateways'), 'back aria');
  assert.ok(byText(pm.root, '3 models').length, 'model count');

  const rows = byClass(pm.root, 'model-row');
  assert.equal(rows.length, 3);
  const texts = (row) => all(row).map((n) => n.textContent).filter(Boolean);
  // Named model: name prominent, id dimmed; full meta when declared.
  assert.ok(texts(rows[0]).includes('Alpha'));
  assert.ok(texts(rows[0]).includes('model-a'));
  for (const meta of ['ctx 1M', 'out 8k', 'in:t,i,f out:t', '3 efforts']) {
    assert.ok(texts(rows[0]).includes(meta), `meta ${meta}`);
  }
  // Bare model: id only, ctx placeholder styled as undeclared.
  assert.ok(texts(rows[2]).includes('model-c'));
  const none = all(rows[2]).find((n) => n.textContent === 'ctx —');
  assert.ok(none && none.className.includes('none'), 'undeclared ctx marked');
  assert.ok(!texts(rows[1]).includes('ctx —'), '64k declared as ctx 63k');
  assert.ok(texts(rows[1]).includes('ctx 63k'));
});

test('models view search filters by id and name', () => {
  const pm = new PM();
  pm.gateways = [GW];
  pm.modelsFor = 'gw1';
  const search = byAria(pm.root, 'Search models');
  search.value = 'alpha';
  search.fire('input');
  let rows = byClass(pm.root, 'model-row');
  assert.equal(rows.length, 1, 'name match');
  search.value = 'model-';
  search.fire('input');
  rows = byClass(pm.root, 'model-row');
  assert.equal(rows.length, 3, 'id match');
  assert.ok(byText(pm.root, '3 of 3').length, 'count reports filter coverage');
});

test('model editor save emits gateway-save with the full catalog', () => {
  const pm = new PM();
  pm.gateways = [GW];
  pm.modelsFor = 'gw1';
  byClass(pm.root, 'model-row')[0].fire('click');
  const dialog = openDialog(pm);
  assert.ok(dialog, 'editor dialog open');
  assert.ok(byText(dialog, 'Edit Alpha').length, 'dialog title');

  byAria(dialog, 'Model name').value = 'Renamed';
  byAria(dialog, 'Context window').value = '2000000';
  byAria(dialog, 'Max images').value = '8';
  byAria(dialog, 'Effort').value = 'high';
  byAria(dialog, 'Temperature').value = '0.2';
  byAria(dialog, 'Top P').value = '';            // clears the stored 0.9
  byAria(dialog, 'Stop sequences').value = 'a, b ,';
  saveBtn(dialog).fire('click');

  const evt = pm.events.find((e) => e.type === 'gateway-save');
  assert.ok(evt, 'gateway-save emitted');
  assert.equal(evt.detail.id, 'gw1');
  const body = evt.detail.body;
  assert.equal(body.name, 'Acme');
  assert.equal(body.style, 'completion');
  assert.equal(body.endpoint, 'https://api.acme.test');
  assert.equal(body.stream, true);
  assert.ok(!('apiKey' in body), 'apiKey omitted — tri-state Keep');
  assert.equal(body.models.length, 3);
  // Untouched models ride through as the exact stored objects.
  assert.deepEqual(body.models[1], GW.models[1]);
  assert.deepEqual(body.models[2], GW.models[2]);
  const m = body.models[0];
  assert.equal(m.id, 'model-a');
  assert.equal(m.name, 'Renamed');
  assert.equal(m.route, 'alpha-v1');
  assert.equal(m.contextWindow, 2000000);
  assert.equal(m.maxImages, 8);
  assert.equal(m.effort, 'high');
  assert.equal(m.temperature, 0.2);
  assert.deepEqual(m.stopSequences, ['a', 'b']);
  assert.ok(!('topP' in m), 'emptied pointer field drops the key');
  assert.equal(m.customFlag, 'keep-me', 'unexposed keys preserved');
  assert.deepEqual(m.pricing, GW.models[0].pricing);
  assert.deepEqual(m.supportedEfforts, ['low', 'medium', 'high']);
});

test('model api style: select pin, inherit reset, and row chip', () => {
  const gw = { ...GW, models: [{ id: 'model-a', style: 'messages' }, { id: 'model-b' }] };
  const pm = new PM();
  pm.gateways = [gw];
  pm.modelsFor = 'gw1';

  // A pinned model shows its override as a meta chip; an inheriting one does not.
  const rows = byClass(pm.root, 'model-row');
  const texts = (row) => all(row).map((n) => n.textContent).filter(Boolean);
  assert.ok(texts(rows[0]).includes('messages'), 'pin rendered as chip');
  assert.ok(!texts(rows[1]).includes('messages'), 'no chip without a pin');

  // The editor labels the empty choice with the gateway's own style and
  // marks the pinned option selected.
  rows[0].fire('click');
  let dialog = openDialog(pm);
  const styleSel = byAria(dialog, 'API style');
  const optByValue = (sel, v) =>
    all(dialog).find((n) => n.tagName === 'OPTION' && n.value === v && sel.children.includes(n));
  assert.ok(optByValue(styleSel, 'messages'), 'style option exists');
  assert.notEqual(optByValue(styleSel, 'messages').selected, undefined, 'pinned option selected');
  assert.ok(optByValue(styleSel, ''), 'inherit option exists');
  assert.ok(all(dialog).some((n) => n.tagName === 'OPTION' && n.textContent === 'gateway default (completion)'),
    'inherit option names the gateway style');

  // Switching back to the default drops the key — inheritance, not "".
  styleSel.value = '';
  saveBtn(dialog).fire('click');
  let evt = pm.events.find((e) => e.type === 'gateway-save');
  assert.ok(!('style' in evt.detail.body.models[0]), 'reset deletes the style key');

  // Pinning writes the style key on the wire.
  const pm2 = new PM();
  pm2.gateways = [gw];
  pm2.modelsFor = 'gw1';
  byClass(pm2.root, 'model-row')[1].fire('click');
  dialog = openDialog(pm2);
  const sel2 = byAria(dialog, 'API style');
  const opt2 = (v) => all(dialog).find((n) => n.tagName === 'OPTION' && n.value === v && sel2.children.includes(n));
  assert.notEqual(opt2('').selected, undefined, 'unpinned starts at the gateway default');
  assert.equal(opt2('responses').selected, undefined, 'no override marked');
  sel2.value = 'responses';
  saveBtn(dialog).fire('click');
  evt = pm2.events.find((e) => e.type === 'gateway-save');
  assert.equal(evt.detail.body.models[1].style, 'responses');
});

test('a clean save result applies models and keeps the models view', () => {
  const pm = new PM();
  pm.gateways = [GW];
  pm.modelsFor = 'gw1';
  byClass(pm.root, 'model-row')[0].fire('click');
  saveBtn(openDialog(pm)).fire('click');

  pm.gatewaySaveResult = {
    id: 'gw1', added: [],
    models: [{ id: 'model-a', contextWindow: 512 }, { id: 'model-b', contextWindow: 64000 }, { id: 'model-c' }],
  };
  assert.ok(!openDialog(pm), 'editor closed on success');
  assert.ok(byClass(pm.root, 'model-row').length, 'still in models view');
  assert.ok(byText(pm.root, 'ctx 512').length, 'saved catalog rendered');

  // A failure keeps the dialog open with the message on its error line.
  byClass(pm.root, 'model-row')[0].fire('click');
  saveBtn(openDialog(pm)).fire('click');
  pm.gatewaySaveResult = { id: 'gw1', error: 'boom' };
  const dialog = openDialog(pm);
  assert.ok(dialog, 'editor stays open on error');
  assert.ok(byText(dialog, 'boom').length, 'error text on the dialog');
});

test('gateways refresh re-resolves the open models view by id', () => {
  const pm = new PM();
  pm.gateways = [GW];
  pm.modelsFor = 'gw1';
  assert.ok(byClass(pm.root, 'model-row').length === 3);
  // A fresh list replaces the object — the view re-renders fresh data.
  pm.gateways = [{ ...GW, models: [{ id: 'model-z', contextWindow: 512 }] }];
  const rows = byClass(pm.root, 'model-row');
  assert.equal(rows.length, 1);
  assert.ok(byText(pm.root, 'model-z').length);
  assert.ok(byText(pm.root, 'ctx 512').length);
  // Unknown ids are ignored; a vanished gateway drops back to the list.
  pm.modelsFor = 'nope';
  assert.ok(byClass(pm.root, 'model-row').length, 'still models view');
  pm.gateways = [{ ...GW, id: 'other' }];
  assert.equal(byClass(pm.root, 'model-row').length, 0, 'back to gateway list');
  assert.ok(byText(pm.root, '1 gateway').length);
});

test('editor save uses the live gateway doc when a refresh lands mid-edit', () => {
  const pm = new PM();
  pm.gateways = [GW];
  pm.modelsFor = 'gw1';
  byClass(pm.root, 'model-row')[0].fire('click');
  assert.ok(openDialog(pm), 'editor open');

  // An import result lands while the dialog is open: it merges a new
  // model AND carries a renamed gateway — the save must not roll either back.
  pm.gatewayImportResult = {
    id: 'gw1', added: ['model-c'],
    models: [...GW.models, { id: 'model-c', contextWindow: 64_000 }],
  };
  pm.gateways = [{ ...GW, name: 'Acme Renamed', models: [...GW.models, { id: 'model-c', contextWindow: 64_000 }] }];

  saveBtn(openDialog(pm)).fire('click');
  const evt = pm.events.find((e) => e.type === 'gateway-save');
  assert.equal(evt.detail.body.name, 'Acme Renamed', 'fresh gateway fields, not the snapshot');
  assert.equal(evt.detail.body.models.length, 4, 'imported merge not rolled back');
  assert.ok(evt.detail.body.models.some((x) => x.id === 'model-c'));

  // A model removed while its editor sits open must refuse the PUT —
  // sending would resurrect the dropped id via full-replace.
  const pm2 = new PM();
  pm2.gateways = [GW];
  pm2.modelsFor = 'gw1';
  byClass(pm2.root, 'model-row')[0].fire('click');
  const d2 = openDialog(pm2);
  assert.ok(d2, 'editor open');
  // The refresh does not re-render while the dialog is open — the live
  // _modelsOf is already the fresh doc at save time.
  pm2.gateways = [{ ...GW, models: GW.models.slice(1) }];
  saveBtn(d2).fire('click');
  assert.equal(pm2.events.filter((e) => e.type === 'gateway-save').length, 0,
    'no save emitted for a removed model');
  assert.ok(byText(d2, 'model is no longer in this gateway — close and reopen').length,
    'refusal message on the dialog');
});
