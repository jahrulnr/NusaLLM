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
  focus() { globalThis.document.activeElement = this; }
  setAttribute(k, v) {
    if (k.startsWith('data-')) {
      const camel = k.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase());
      (this.dataset ||= {})[camel] = v;
      return;
    }
    this[k] = v;
  }
  getAttribute(k) { return this[k]; }
  contains(n) {
    if (n === this) return true;
    for (const c of this.children) {
      if (c === n || (c.contains && c.contains(n))) return true;
    }
    return false;
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

// Refresh-models reads the record stored on the server. While a gateway
// save is still on the wire that record is the OLD endpoint/key, and the
// import can land after (or before) the save with a stale catalog. The
// import control must be disabled for the save's duration.
test('refresh models is disabled while the gateway save is in flight', () => {
  const pm = new PM();
  pm.gateways = [GW];
  byAria(pm.root, 'Edit gw1').fire('click');
  const dialog = openDialog(pm);
  assert.ok(dialog, 'gateway editor open');
  saveBtn(dialog).fire('click');
  const refresh = byText(dialog, 'Refresh models from provider')[0];
  assert.ok(refresh, 'refresh button rendered');
  assert.equal(refresh.disabled, true);
  refresh.fire('click');
  assert.equal(pm.events.filter((e) => e.type === 'gateway-import').length, 0);
});

// The import and full-replace save are mutually exclusive server-side
// operations on the same record. A save clicked while a catalog import is
// on the wire builds its body before the merge lands, so the replacement
// can roll the imported models back. Save must stay disabled for the
// import's duration, mirroring the import's save guard.
test('gateway save is disabled while a model import is in flight', () => {
  const pm = new PM();
  pm.gateways = [GW];
  byAria(pm.root, 'Edit gw1').fire('click');
  const dialog = openDialog(pm);
  assert.ok(dialog, 'gateway editor open');
  const refresh = byText(dialog, 'Refresh models from provider')[0];
  assert.ok(refresh, 'refresh button rendered');
  refresh.fire('click');
  assert.equal(pm.events.filter((e) => e.type === 'gateway-import').length, 1);
  const save = saveBtn(dialog);
  assert.equal(save.disabled, true);
  save.fire('click');
  assert.equal(pm.events.filter((e) => e.type === 'gateway-save').length, 0);
});


// Saving a model closes its editor and rebuilds the models list. The
// save control is gone with the dialog; focus must return to the model
// row that opened the editor, not stay on a detached button or BODY.
test('saving a model returns focus to its row', () => {
  const pm = new PM();
  pm.gateways = [GW];
  pm.modelsFor = 'gw1';
  const before = document.activeElement;
  byAria(pm.root, 'Edit model-a').fire('click');
  const dialog = openDialog(pm);
  assert.ok(dialog, 'model editor open');
  const save = saveBtn(dialog);
  save.focus();
  save.fire('click');
  assert.ok(pm._pendingSaveId === 'gw1', 'save emitted');
  pm.gatewaySaveResult = { id: 'gw1', models: GW.models };
  const row = byAria(pm.root, 'Edit model-a');
  assert.ok(row, 'model row remains after save');
  assert.equal(document.activeElement, row,
    'save must return focus to the model row that opened the editor');
  document.activeElement = before;
});

// Leaving the models view for the gateway list replaces the Back
// control with the list. Focus must land on the list's first action
// (Add gateway), not stay on the detached Back button or BODY.
test('back from the models view focuses the gateway list', () => {
  const pm = new PM();
  pm.gateways = [GW];
  pm.modelsFor = 'gw1';
  const back = byAria(pm.root, 'Back to gateways');
  back.focus();
  back.fire('click');
  const add = byAria(pm.root, 'Add gateway');
  assert.ok(add, 'gateway list rendered');
  assert.equal(document.activeElement, add,
    'back navigation must land focus on the gateway list action');
});

// Opening a gateway's models view replaces the row's controls with the
// view's Back/model/refresh chrome. Focus must move into the new view
// (Back is its first control), not stay on the destroyed row button.
test('opening the models view focuses its Back control', () => {
  const pm = new PM();
  pm.gateways = [GW];
  const models = byClass(pm.root, 'models-btn')[0];
  models.focus();
  models.fire('click');
  const back = byAria(pm.root, 'Back to gateways');
  assert.ok(back, 'models view open');
  assert.equal(document.activeElement, back,
    'opening the models view must move focus into it');
});

// Saving the gateway form closes the dialog and rebuilds the gateway
// list. The Save control dies with the dialog; focus must return to the
// row/wrapper that opened it, just as it does when the model editor or
// the Back control closes.
test('saving the gateway form returns focus to its row', () => {
  const pm = new PM();
  pm.gateways = [GW];
  const edit = byAria(pm.root, 'Edit gw1');
  edit.focus();
  edit.fire('click');
  const dialog = openDialog(pm);
  assert.ok(dialog, 'gateway editor open');
  const save = saveBtn(dialog);
  save.focus();
  save.fire('click');
  pm.gatewaySaveResult = { id: 'gw1', models: GW.models };
  const row = byAria(pm.root, 'Edit gw1');
  assert.ok(row, 'gateway row remains after save');
  assert.equal(document.activeElement, row,
    'gateway save must return focus to the row that opened the editor');
});

// Refresh models is an in-place action: the gateway and view stay put,
// so the import control must retain focus when its answer rebuilds the
// models list. Losing focus to BODY strands a keyboard user after every
// catalog refresh.
test('refresh models keeps focus on the refresh control', () => {
  const pm = new PM();
  pm.gateways = [GW];
  pm.modelsFor = 'gw1';
  const refresh = byText(pm.root, 'Refresh models from provider')[0];
  refresh.focus();
  refresh.fire('click');
  pm.gatewayImportResult = { id: 'gw1', models: GW.models, added: ['model-b'] };
  const again = byText(pm.root, 'Refresh models from provider')[0];
  assert.ok(again, 'refresh control remains after import');
  assert.equal(document.activeElement, again,
    'a completed import must hand focus back to the refresh control');
});

// Deleting the last gateway replaces the whole provider list with the
// empty-state action. Focus cannot stay on the destroyed confirm
// button; it must land on Add gateway so the user can repair the setup.
test('deleting a gateway focuses the remaining list action', () => {
  const pm = new PM();
  pm.gateways = [GW];
  const del = byAria(pm.root, 'Delete gw1');
  del.focus();
  del.fire('click');
  const confirm = byClass(pm.root, 'danger-btn')[0];
  assert.ok(confirm, 'delete confirmation rendered');
  confirm.focus();
  confirm.fire('click');
  pm.gateways = [];
  const add = byAria(pm.root, 'Add gateway');
  assert.ok(add, 'empty-state Add gateway rendered');
  assert.equal(document.activeElement, add,
    'a deleted gateway must hand focus to the remaining list action');
});

// A failed catalog refresh re-enables the refresh control in place but
// does not rebuild the view. The pending-focus slot must still settle
// on the re-enabled control; otherwise the user loses keyboard focus
// exactly when the error note appears.
test('a failed refresh restores focus to the refresh control', () => {
  const pm = new PM();
  pm.gateways = [GW];
  pm.modelsFor = 'gw1';
  const refresh = byText(pm.root, 'Refresh models from provider')[0];
  refresh.focus();
  refresh.fire('click');
  // The control disables while the import is on the wire.
  document.activeElement = null;
  pm.gatewayImportResult = { id: 'gw1', error: 'catalog unavailable' };
  const again = byText(pm.root, 'Refresh models from provider')[0];
  assert.ok(again, 'refresh control remains after failure');
  assert.equal(document.activeElement, again,
    'the re-enabled refresh control must recover focus after a failed import');
});

// A failed gateway save keeps the form open with its error line. Save is
// disabled for the request and re-enabled by the answer; focus cannot
// stay on the disabled node, so the re-enabled Save must inherit it or
// the user is dropped on BODY exactly when they need to correct the form.
test('a failed gateway save keeps focus on Save', () => {
  const pm = new PM();
  pm.gateways = [GW];
  byAria(pm.root, 'Edit gw1').fire('click');
  const dialog = openDialog(pm);
  assert.ok(dialog, 'gateway editor open');
  const save = saveBtn(dialog);
  save.focus();
  save.fire('click');
  assert.equal(pm._pendingSaveId, 'gw1', 'save reached the wire');
  document.activeElement = null; // disabling the control drops focus
  pm.gatewaySaveResult = { id: 'gw1', error: 'save refused' };
  const again = saveBtn(openDialog(pm));
  assert.ok(again, 'Save remains in the open form');
  assert.ok(byText(openDialog(pm), 'save refused').length, 'failure is rendered');
  assert.equal(document.activeElement, again,
    'the re-enabled Save must recover focus after a failed gateway save');
});

// Same failure path in the model editor: the dialog stays open with the
// typed values, and its Save is the next action — focus must not fall to
// BODY when the disabled control is re-enabled.
test('a failed model save keeps focus on Save', () => {
  const pm = new PM();
  pm.gateways = [GW];
  pm.modelsFor = 'gw1';
  byAria(pm.root, 'Edit model-a').fire('click');
  const dialog = openDialog(pm);
  assert.ok(dialog, 'model editor open');
  const save = saveBtn(dialog);
  save.focus();
  save.fire('click');
  assert.equal(pm._pendingSaveId, 'gw1', 'save reached the wire');
  document.activeElement = null;
  pm.gatewaySaveResult = { id: 'gw1', error: 'model refused' };
  const again = saveBtn(openDialog(pm));
  assert.ok(again, 'Save remains in the open editor');
  assert.equal(document.activeElement, again,
    'the re-enabled Save must recover focus after a failed model save');
});

// Confirming a delete clears the widget's confirmation state but leaves
// the old row in the DOM until the server's list refresh lands. That
// still-enabled Delete button can be clicked again, issuing a second
// DELETE for the same id (and a spurious error when it 404s). A
// confirmed action must leave either no confirm row or a disabled one
// until the request resolves.
test('a confirmed gateway delete cannot be submitted twice', () => {
  const pm = new PM();
  pm.gateways = [GW];
  byAria(pm.root, 'Delete gw1').fire('click');
  const confirm = byClass(pm.root, 'danger-btn')[0];
  assert.ok(confirm, 'delete confirmation rendered');
  confirm.fire('click');
  const again = byClass(pm.root, 'danger-btn')[0];
  if (again && !again.disabled) again.fire('click');
  const sends = pm.events.filter((e) => e.type === 'gateway-delete');
  assert.equal(sends.length, 1,
    'the same confirmed delete must not be emitted twice before the row resolves');
});

// The Wave I fix retires the stale confirm row, but the normal row's
// Delete trigger is live again while the first request is still on the
// wire. A second confirm therefore sends a second DELETE for the same
// gateway. A delete needs an in-flight state, not just a fresh row.
test('gateway delete cannot be re-confirmed while in flight', () => {
  const pm = new PM();
  pm.gateways = [GW];
  byAria(pm.root, 'Delete gw1').fire('click');
  byClass(pm.root, 'danger-btn')[0].fire('click');
  const rowDelete = byAria(pm.root, 'Delete gw1');
  if (rowDelete && !rowDelete.disabled) rowDelete.fire('click');
  const secondConfirm = byClass(pm.root, 'danger-btn')[0];
  if (secondConfirm && !secondConfirm.disabled) secondConfirm.fire('click');
  const sends = pm.events.filter((e) => e.type === 'gateway-delete');
  assert.equal(sends.length, 1,
    'a gateway delete must stay single-flight until the list refresh resolves it');
});

// An external refresh can remove the gateway whose Delete control has
// focus. The row vanishes without a local confirm/delete flow, so focus
// must fall back to the always-present Add gateway action rather than the
// detached button.
test('an externally removed gateway row hands focus to Add gateway', () => {
  globalThis.document.activeElement = null;
  const pm = new PM();
  pm.gateways = [GW];
  const del = byAria(pm.root, 'Delete gw1');
  assert.ok(del, 'Delete rendered');
  del.focus();
  pm.gateways = [];
  const add = byAria(pm.root, 'Add gateway');
  assert.ok(add, 'Add gateway survives the empty list');
  assert.equal(globalThis.document.activeElement, add,
    'focus must move to the surviving list action');
});
