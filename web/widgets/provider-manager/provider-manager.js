import { Widget, el, emit, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

const STYLES = ['completion', 'responses', 'messages', 'generatecontent'];
const EFFORTS = ['default', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];
// 'default' means "provider decides" — it is a sentinel, never a level a
// model declares support for.
const SUPPORTED_EFFORTS = EFFORTS.filter((e) => e !== 'default');
const MODEL_TYPES = ['text', 'embedding', 'video', 'image', 'speech', 'transcription', 'audio', 'rerank', 'decision'];
const MODALITIES = ['text', 'image', 'audio', 'video', 'file'];
const MODALITY_LETTERS = { text: 't', image: 'i', audio: 'a', video: 'v', file: 'f' };

// Catalog token counts compact to M/k; 0 means "not declared" and the
// caller renders the placeholder instead.
function fmtCount(n) {
  if (!n) return null;
  if (n >= 1_000_000) return `${+(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1024) return `${Math.round(n / 1024)}k`;
  return String(n);
}

// Provider CRUD. The editor keeps the wire body honest: apiKey is a
// tri-state (Keep omits it, Replace sends the string, Clear sends ""),
// and editing preserves every advanced field of surviving models.
// The server merges the provider's advertised model ids into the
// record on save and on Refresh — the stored key or ref is never
// rendered.
class ProviderManager extends Widget {
  static stylesheet = new URL('./provider-manager.css', import.meta.url);

  constructor() {
    super();
    this._gateways = [];
    this._editing = null;   // existing gateway object being edited
    this._creating = false;
    this._confirmId = null; // gateway id awaiting delete confirmation
    this._modelsOf = null;  // gateway whose models view is open
    this._editingModel = null; // model object open in the model editor
    this._viewQuery = '';   // models-view search text, survives re-renders
    this._importing = false;
    this._saving = false;
    this._pendingSaveId = null;
    this._render();
  }

  set gateways(list) {
    this._gateways = list || [];
    // Re-resolve the open models view against the fresh list — a stale
    // object must never become an edit target.
    if (this._modelsOf) {
      this._modelsOf = this._gateways.find((g) => g.id === this._modelsOf.id) || null;
    }
    // A pending cross-widget target applies once the data arrives.
    if (this._pendingModelsFor) {
      const g = this._gateways.find((x) => x.id === this._pendingModelsFor);
      this._pendingModelsFor = null;
      if (g) { this._openModels(g); return; }
    }
    if (!this._editing && !this._creating && !this._editingModel) this._render();
  }

  // Cross-widget entry point: open the models view for a gateway id.
  // Before the gateways load the id waits — a dropped navigation is a
  // dead click for the user.
  set modelsFor(id) {
    const g = this._gateways.find((x) => x.id === id);
    if (g) this._openModels(g);
    else this._pendingModelsFor = id;
  }

  set gatewaySaveResult(result) {
    if (!result || result.id !== this._pendingSaveId) return;
    this._saving = false;
    this._pendingSaveId = null;
    if (this._modelsOf && this._modelsOf.id === result.id) {
      // A model-editor save keeps the models view open: the returned
      // catalog replaces the gateway's list in place, and a failure
      // lands on the open dialog's error line (mirrored into the view).
      if (result.models) this._modelsOf = { ...this._modelsOf, models: result.models };
      const failure = result.error || result.catalogError || '';
      if (failure) {
        if (this._modelFormError) this._modelFormError.textContent = failure;
        this._noteImport(failure, true);
        if (result.models) this._refreshModelsView();
        return;
      }
      this._editingModel = null;
      this._render();
      return;
    }
    if (result.error) {
      if (this._formError) this._formError.textContent = result.error;
      return;
    }
    // The record saved even when the provider catalog would not merge —
    // adopt the stored catalog, keep the dialog open, and show the
    // failure where save errors render.
    if (result.models) {
      this._modelIds = new Set(result.models.map((m) => m.id));
      if (this._editing) this._editing = { ...this._editing, models: result.models };
      this._renderModels();
    }
    if (result.catalogError) {
      if (this._formError) this._formError.textContent = result.catalogError;
      return;
    }
    this._editing = null;
    this._creating = false;
    this._render();
  }

  // Refresh answer: the import runs server-side against the stored
  // endpoint and key. The response's post-merge catalog replaces the
  // form's id list in place — the dialog stays open. In the models view
  // the same outcome refreshes the visible list behind any open editor.
  set gatewayImportResult(result) {
    if (!result) return;
    // One import flight at a time — any answer clears the busy state,
    // even when it lands after the user navigated away mid-flight.
    this._importing = false;
    this._syncImportState();
    if (this._modelsOf && result.id === this._modelsOf.id) {
      if (result.error) {
        this._noteImport(result.error, true);
        return;
      }
      if (result.models) {
        this._modelsOf = { ...this._modelsOf, models: result.models };
        this._refreshModelsView();
      }
      if (result.catalogError) {
        this._noteImport(result.catalogError, true);
        return;
      }
      const n = (result.added || []).length;
      this._noteImport(n
        ? `Imported ${n} new model${n === 1 ? '' : 's'} from the provider catalog.`
        : 'No new models from the provider.', false);
      return;
    }
    if (!this._editing || result.id !== this._editing.id) return;
    if (result.error) {
      this._noteImport(result.error, true);
      return;
    }
    if (result.models) {
      this._editing = { ...this._editing, models: result.models };
      this._modelIds = new Set(result.models.map((m) => m.id));
      this._renderModels();
    }
    if (result.catalogError) {
      this._noteImport(result.catalogError, true);
      return;
    }
    const n = (result.added || []).length;
    this._noteImport(n
      ? `Imported ${n} new model${n === 1 ? '' : 's'} from the provider catalog.`
      : 'No new models from the provider.', false);
  }

  _render() {
    if (this._dialog?.open) this._dialog.close();
    this._dialog = null;
    this._formError = null;
    this._modelFormError = null;
    this._importBtn = null;
    this._importNote = null;
    this._modelList = null;
    this._modelSearch = null;
    this._modelsListEl = null;
    this._modelsCount = null;
    clear(this.root);
    this.root.classList.add('providers');

    if (this._modelsOf) {
      this._renderModelsView();
      return;
    }

    this.root.append(el('div', { class: 'row-head' }, [
      el('span', { class: 'count', text: `${this._gateways.length} gateway${this._gateways.length === 1 ? '' : 's'}` }),
      el('button', {
        class: 'btn accent', type: 'button',
        onclick: () => { this._creating = true; this._render(); },
      }, [icon('plus', 12), el('span', { text: 'Add gateway' })]),
    ]));

    if (!this._gateways.length) {
      this.root.append(el('div', { class: 'hint', text: 'No gateways yet — add one to enable sending.' }));
    }
    for (const g of this._gateways) {
      if (this._confirmId === g.id) {
        // Deleting a gateway also drops its credential record — make it
        // a real confirmation, never a single click.
        this.root.append(el('div', { class: 'row confirm', role: 'alertdialog', 'aria-label': `Delete ${g.id}?` }, [
          el('span', { class: 'confirm-text', text: `Delete ${g.name || g.id} and its key?` }),
          el('button', {
            class: 'btn danger-btn', type: 'button',
            onclick: () => {
              this._confirmId = null;
              emit(this, 'gateway-delete', { id: g.id });
            },
          }, [el('span', { text: 'Delete' })]),
          el('button', {
            class: 'btn', type: 'button',
            onclick: () => { this._confirmId = null; this._render(); },
          }, [el('span', { text: 'Keep' })]),
        ]));
        continue;
      }
      this.root.append(el('div', { class: 'row' }, [
        el('div', { class: 'row-main' }, [
          el('span', { class: 'row-id', text: g.name || g.id }),
          el('span', { class: 'row-meta', text: `${g.style} · ${g.endpoint || 'no endpoint'}` }),
          el('span', { class: 'row-meta', text: `${(g.models || []).length} model(s) · key ${g.keyState || 'unset'}${g.broken ? ' · broken' : ''}` }),
        ]),
        el('div', { class: 'row-actions' }, [
          el('button', {
            class: 'btn models-btn', type: 'button',
            'aria-label': `Manage ${(g.models || []).length} models of ${g.id}`,
            onclick: () => this._openModels(g),
          }, [el('span', { text: `${(g.models || []).length} models` }), icon('chevron', 12)]),
          el('button', {
            class: 'icon-btn', type: 'button', 'aria-label': `Edit ${g.id}`,
            onclick: () => { this._editing = g; this._importing = false; this._render(); },
          }, [icon('pencil', 12)]),
          el('button', {
            class: 'icon-btn danger', type: 'button', 'aria-label': `Delete ${g.id}`,
            onclick: () => { this._confirmId = g.id; this._render(); },
          }, [icon('trash', 12)]),
        ]),
      ]));
    }
    if (this._editing || this._creating) this._renderForm();
  }

  _openModels(g) {
    this._modelsOf = g;
    this._editing = null;
    this._creating = false;
    this._editingModel = null;
    this._confirmId = null;
    this._viewQuery = '';
    this._render();
  }

  // Second view level: the per-gateway model catalog. A model editor
  // dialog opens over it; the refresh button reuses the same
  // server-side import as the gateway editor.
  _renderModelsView() {
    const g = this._modelsOf;
    this.root.append(el('div', { class: 'models-head' }, [
      el('button', {
        class: 'btn back', type: 'button', 'aria-label': 'Back to gateways',
        onclick: () => { this._modelsOf = null; this._editingModel = null; this._render(); },
      }, [icon('chevron', 12), el('span', { text: 'Gateways' })]),
      el('span', { class: 'models-title', text: g.name || g.id }),
      this._importBtn = el('button', {
        class: 'btn', type: 'button', disabled: this._importing,
        text: this._importing ? 'Importing…' : 'Refresh models from provider',
        onclick: () => this._importModels(g.id),
      }),
      this._modelsCount = el('span', { class: 'count' }),
    ]));
    this.root.append(this._importNote = el('div'));
    const search = el('input', {
      class: 'in models-search', type: 'search',
      placeholder: 'Search models by id or name', 'aria-label': 'Search models',
      value: this._viewQuery,
    });
    search.addEventListener('input', () => {
      this._viewQuery = search.value;
      this._renderModelsList();
    });
    this.root.append(search);
    this._modelsListEl = el('div', { class: 'models-list', 'aria-label': 'Gateway models' });
    this.root.append(this._modelsListEl);
    this._renderModelsList();
    if (this._editingModel) this._renderModelEditor();
  }

  // List refresh that leaves an open model editor untouched — the
  // dialog lives on root, outside the list element.
  _refreshModelsView() {
    if (this._editingModel) this._renderModelsList();
    else this._render();
  }

  _renderModelsList() {
    const g = this._modelsOf;
    if (!g || !this._modelsListEl) return;
    const models = g.models || [];
    const q = this._viewQuery.trim().toLowerCase();
    const visible = q
      ? models.filter((m) => m.id.toLowerCase().includes(q) || (m.name || '').toLowerCase().includes(q))
      : models;
    if (this._modelsCount) {
      this._modelsCount.textContent = q
        ? `${visible.length} of ${models.length}`
        : `${models.length} model${models.length === 1 ? '' : 's'}`;
    }
    clear(this._modelsListEl);
    if (!visible.length) {
      this._modelsListEl.append(el('div', {
        class: 'model-empty',
        text: q ? 'No matching models.' : 'No models on this gateway yet.',
      }));
      return;
    }
    for (const m of visible) {
      // Compact right-aligned meta: ctx always shows ('—' when the
      // catalog never declared it); the rest appear only when declared.
      const ctx = fmtCount(m.contextWindow);
      const meta = [el('span', { class: ctx ? 'mm' : 'mm none', text: `ctx ${ctx || '—'}` })];
      const out = fmtCount(m.outputLimit);
      if (out) meta.push(el('span', { class: 'mm', text: `out ${out}` }));
      if (m.maxImages) meta.push(el('span', { class: 'mm', text: `img ≤${m.maxImages}` }));
      const mods = [
        (m.inputModalities || []).map((v) => MODALITY_LETTERS[v] || v[0]).join(','),
        (m.outputModalities || []).map((v) => MODALITY_LETTERS[v] || v[0]).join(','),
      ];
      const modText = [mods[0] && `in:${mods[0]}`, mods[1] && `out:${mods[1]}`].filter(Boolean).join(' ');
      if (modText) meta.push(el('span', { class: 'mm', text: modText }));
      const nEff = (m.supportedEfforts || []).length;
      if (nEff) meta.push(el('span', { class: 'mm', text: `${nEff} effort${nEff === 1 ? '' : 's'}` }));
      this._modelsListEl.append(el('button', {
        class: 'model-row', type: 'button', 'aria-label': `Edit ${m.id}`,
        onclick: () => { this._editingModel = m; this._renderModelEditor(); },
      }, [
        el('span', { class: 'model-row-main' }, [
          el('span', { class: 'model-row-id', text: m.name || m.id }),
          m.name ? el('span', { class: 'model-row-sub', text: m.id }) : null,
        ]),
        el('span', { class: 'model-row-meta' }, meta),
      ]));
    }
  }

  // Per-model editor: a full replace of the model's readable fields.
  // The save starts from the stored object so keys the editor does not
  // expose survive the round trip; empty optional inputs drop the key
  // entirely (JSON.stringify omits undefined — the wire's "unset").
  _renderModelEditor() {
    const m = this._editingModel;
    const g = this._modelsOf;
    if (!m || !g) return;
    const dialog = el('dialog', { class: 'editor-dialog', 'aria-label': `Edit ${m.name || m.id}` });
    const close = () => {
      if (this._saving) return;
      this._editingModel = null;
      this._render();
    };
    dialog.addEventListener('cancel', (event) => {
      event.preventDefault();
      close();
    });
    const field = (label, node) => el('label', { class: 'field' }, [
      el('span', { class: 'field-label', text: label }), node,
    ]);
    // Same visual as field() but without <label> semantics — for chip
    // groups that already contain their own labels.
    const group = (label, node) => el('div', { class: 'field' }, [
      el('span', { class: 'field-label', text: label }), node,
    ]);
    const sec = (label, inner) => el('div', { class: 'sec' }, [
      el('span', { class: 'field-label', text: label }), inner,
    ]);
    const ro = (value, aria) => el('input', { class: 'in', value: value || '', disabled: true, 'aria-label': aria });
    const txt = (value, aria, placeholder) => el('input', { class: 'in', value: value || '', 'aria-label': aria, placeholder });
    // Token counts and rates are non-negative; gen params keep their
    // sign (penalties run -2..2 on some providers).
    const num = (value, aria, step, min = '0') =>
      el('input', {
        class: 'in', type: 'number', step: step || '1', min,
        // Trim binary-float tails (0.023940000000000003) the per-token →
        // per-million conversion leaves behind.
        value: typeof value === 'number' ? +value.toFixed(10) : value,
        'aria-label': aria,
      });
    const chips = (values, selected, aria) => {
      const sel = new Set(selected || []);
      const boxes = values.map((v) => ({
        v,
        input: el('input', { type: 'checkbox', value: v, checked: sel.has(v), 'aria-label': v }),
      }));
      for (const b of boxes) b.node = el('label', { class: 'chip' }, [b.input, el('span', { text: b.v })]);
      return { boxes, row: el('div', { class: 'chips', role: 'group', 'aria-label': aria }, boxes.map((b) => b.node)) };
    };

    const f = {};
    const identity = el('div', { class: 'sec-cols' }, [field('id', ro(m.id, 'Model id'))]);
    if (m.canonical) identity.append(field('canonical', ro(m.canonical, 'Canonical')));
    if (m.aliasOf) identity.append(field('alias of', ro(m.aliasOf, 'Alias of')));

    f.name = txt(m.name, 'Model name');
    f.route = txt(m.route, 'Route');

    f.ctx = num(m.contextWindow || '', 'Context window');
    f.outLimit = num(m.outputLimit || '', 'Output limit');
    f.maxOut = num(m.maxOutputTokens ?? '', 'Max output tokens');
    f.maxImages = num(m.maxImages || '', 'Max images');

    f.effort = el('select', { class: 'in', 'aria-label': 'Effort' },
      EFFORTS.map((e) => el('option', { value: e, text: e, selected: (m.effort || 'default') === e })));
    f.modelType = el('select', { class: 'in', 'aria-label': 'Model type' }, [
      el('option', { value: '', text: 'unset', selected: !m.modelType }),
      ...MODEL_TYPES.map((t) => el('option', { value: t, text: t, selected: m.modelType === t })),
    ]);
    f.supEff = chips(SUPPORTED_EFFORTS, m.supportedEfforts, 'Supported efforts');
    f.inMod = chips(MODALITIES, m.inputModalities, 'Input modalities');
    f.outMod = chips(MODALITIES, m.outputModalities, 'Output modalities');

    const p = m.pricing || {};
    f.priceIn = num(p.inputPerMillionUsd || '', 'Input USD per million tokens', 'any');
    f.priceOut = num(p.outputPerMillionUsd || '', 'Output USD per million tokens', 'any');
    f.priceCR = num(p.cacheReadPerMillionUsd || '', 'Cache read USD per million tokens', 'any');
    f.priceCW = num(p.cacheWritePerMillionUsd || '', 'Cache write USD per million tokens', 'any');

    f.temperature = num(m.temperature ?? '', 'Temperature', 'any', null);
    f.topP = num(m.topP ?? '', 'Top P', 'any', null);
    f.topK = num(m.topK ?? '', 'Top K');
    f.presence = num(m.presencePenalty ?? '', 'Presence penalty', 'any', null);
    f.frequency = num(m.frequencyPenalty ?? '', 'Frequency penalty', 'any', null);
    // Seed keeps its sign — no min bound.
    f.seed = num(m.seed ?? '', 'Seed', '1', null);
    f.stop = txt((m.stopSequences || []).join(', '), 'Stop sequences', 'stop1, stop2');

    const error = el('div', { class: 'form-error' });

    const save = () => {
      if (this._saving) return;
      // Resolve against the LIVE gateway: a refresh/import landing while
      // this dialog sits open replaces the object it opened from — a PUT
      // built on the captured snapshot would roll that merge back and
      // clobber concurrent gateway-field edits.
      const g = this._modelsOf;
      const base = g && (g.models || []).find((x) => x.id === m.id);
      if (!base) {
        error.textContent = g
          ? 'model is no longer in this gateway — close and reopen'
          : 'gateway was removed';
        return;
      }
      const next = { ...base };
      // '' = unset; null marks an unparsable number.
      const take = (input, int) => {
        const v = String(input.value ?? '').trim();
        if (!v) return undefined;
        const n = Number(v);
        return Number.isFinite(n) ? (int ? Math.trunc(n) : n) : null;
      };
      const apply = (key, input, int, label) => {
        const v = take(input, int);
        if (v === null) { error.textContent = `${label} must be a number`; return false; }
        if (v === undefined) delete next[key]; else next[key] = v;
        return true;
      };
      // Whole-document ints send 0 for "auto" rather than omitting.
      const applyZero = (key, input, label) => {
        const v = take(input, true);
        if (v === null) { error.textContent = `${label} must be a number`; return false; }
        next[key] = v || 0;
        return true;
      };
      const pick = (set) => set.boxes.filter((b) => b.input.checked).map((b) => b.v);

      next.name = f.name.value.trim();
      next.route = f.route.value.trim();
      if (!applyZero('contextWindow', f.ctx, 'context window')) return;
      if (!applyZero('outputLimit', f.outLimit, 'output limit')) return;
      if (!apply('maxOutputTokens', f.maxOut, true, 'max output tokens')) return;
      if (!applyZero('maxImages', f.maxImages, 'max images')) return;
      if (f.effort.value === 'default') delete next.effort; else next.effort = f.effort.value;
      if (f.modelType.value) next.modelType = f.modelType.value; else delete next.modelType;
      const sup = pick(f.supEff);
      if (sup.length) next.supportedEfforts = sup; else delete next.supportedEfforts;
      const inM = pick(f.inMod);
      if (inM.length) next.inputModalities = inM; else delete next.inputModalities;
      const outM = pick(f.outMod);
      if (outM.length) next.outputModalities = outM; else delete next.outputModalities;
      const rates = [f.priceIn, f.priceOut, f.priceCR, f.priceCW].map((i) => take(i, false));
      if (rates.some((r) => r === null)) { error.textContent = 'pricing must be numbers'; return; }
      next.pricing = {
        inputPerMillionUsd: rates[0] || 0,
        outputPerMillionUsd: rates[1] || 0,
        cacheReadPerMillionUsd: rates[2] || 0,
        cacheWritePerMillionUsd: rates[3] || 0,
      };
      if (!apply('temperature', f.temperature, false, 'temperature')) return;
      if (!apply('topP', f.topP, false, 'topP')) return;
      if (!apply('topK', f.topK, true, 'topK')) return;
      if (!apply('presencePenalty', f.presence, false, 'presence penalty')) return;
      if (!apply('frequencyPenalty', f.frequency, false, 'frequency penalty')) return;
      if (!apply('seed', f.seed, true, 'seed')) return;
      const stops = f.stop.value.split(',').map((s) => s.trim()).filter(Boolean);
      if (stops.length) next.stopSequences = stops; else delete next.stopSequences;

      // Full upsert body: every readable field is authoritative, apiKey
      // stays absent so the stored credential is kept (tri-state Keep).
      const body = {
        name: g.name || '',
        style: g.style,
        endpoint: g.endpoint || '',
        stream: g.stream,
        models: (g.models || []).map((x) => (x.id === m.id ? next : x)),
      };
      this._saving = true;
      this._pendingSaveId = g.id;
      error.textContent = 'Saving…';
      emit(this, 'gateway-save', { id: g.id, body });
    };

    const form = el('div', { class: 'editor-form' }, [
      sec('identity', identity),
      sec('basics', el('div', { class: 'sec-cols' }, [field('name', f.name), field('route', f.route)])),
      sec('limits', el('div', { class: 'stack' }, [
        el('div', { class: 'sec-cols' }, [
          field('context window', f.ctx), field('output limit', f.outLimit), field('max output tokens', f.maxOut),
          field('max images', f.maxImages),
        ]),
        el('div', { class: 'hint', text: 'empty = provider catalog decides; max images = per-request cap this serving enforces' }),
      ])),
      sec('behavior', el('div', { class: 'stack' }, [
        el('div', { class: 'sec-cols' }, [field('effort', f.effort), field('model type', f.modelType)]),
        group('supported efforts', f.supEff.row),
      ])),
      sec('modalities', el('div', { class: 'stack' }, [
        group('input', f.inMod.row), group('output', f.outMod.row),
      ])),
      sec('pricing (usd per 1m tokens)', el('div', { class: 'sec-cols' }, [
        field('input', f.priceIn), field('output', f.priceOut), field('cache read', f.priceCR), field('cache write', f.priceCW),
      ])),
      sec('generation', el('div', { class: 'stack' }, [
        el('div', { class: 'sec-cols' }, [
          field('temperature', f.temperature), field('top p', f.topP), field('top k', f.topK),
          field('presence penalty', f.presence), field('frequency penalty', f.frequency), field('seed', f.seed),
        ]),
        field('stop sequences (comma-separated)', f.stop),
      ])),
      error,
    ]);
    dialog.append(
      el('div', { class: 'dialog-head' }, [
        el('div', { class: 'dialog-title', text: `Edit ${m.name || m.id}` }),
        el('button', { class: 'icon-btn dialog-close', type: 'button', 'aria-label': 'Close model editor', onclick: close }, [icon('x', 16)]),
      ]),
      form,
      el('div', { class: 'form-actions' }, [
        el('button', { class: 'btn accent', type: 'button', onclick: save }, [el('span', { text: 'Save' })]),
        el('button', { class: 'btn', type: 'button', onclick: close }, [el('span', { text: 'Cancel' })]),
      ]),
    );
    this.root.append(dialog);
    this._dialog = dialog;
    this._modelFormError = error;
    dialog.showModal();
  }

  _renderForm() {
    const g = this._editing; // null when creating
    const dialog = el('dialog', { class: 'editor-dialog', 'aria-label': g ? `Edit ${g.id}` : 'New gateway' });
    const form = el('div', { class: 'editor-form' });
    const close = () => {
      if (this._saving) return;
      this._editing = null;
      this._creating = false;
      this._importing = false;
      this._render();
    };
    dialog.addEventListener('cancel', (event) => {
      event.preventDefault();
      close();
    });
    const field = (label, node) => el('label', { class: 'field' }, [
      el('span', { class: 'field-label', text: label }), node,
    ]);

    const id = el('input', { class: 'in', value: g ? g.id : '', disabled: !!g, 'aria-label': 'Gateway id' });
    const name = el('input', { class: 'in', value: g ? g.name : '', 'aria-label': 'Name' });
    const style = el('select', { class: 'in', 'aria-label': 'API style' },
      STYLES.map((s) => el('option', { value: s, text: s, selected: g && g.style === s })));
    const endpoint = el('input', { class: 'in', value: g ? g.endpoint : '', 'aria-label': 'Endpoint URL' });
    const stream = el('input', { type: 'checkbox', checked: g ? !!g.stream : true, 'aria-label': 'Streaming' });
    this._modelIds = new Set((g?.models || []).map((m) => m.id));
    this._modelSearch = el('input', {
      class: 'in', type: 'search', placeholder: 'Search configured models',
      'aria-label': 'Search configured models',
    });
    this._modelList = el('div', { class: 'model-list', role: 'list', 'aria-label': 'Configured models', tabindex: '0' });
    this._modelCount = el('span', { class: 'count' });
    this._modelSearch.addEventListener('input', () => this._renderModels());

    // API key tri-state — the current value never renders, only a mode.
    const keyMode = el('select', { class: 'in', 'aria-label': 'API key mode' }, [
      el('option', { value: 'keep', text: g && g.keySet ? 'Keep stored key' : 'No key', selected: true }),
      el('option', { value: 'replace', text: 'Replace key…' }),
      g && g.keySet ? el('option', { value: 'clear', text: 'Clear stored key' }) : null,
    ]);
    const keyValue = el('input', {
      class: 'in', type: 'password', autocomplete: 'off',
      placeholder: 'new api key', 'aria-label': 'New API key',
    });
    const keyField = field('new api key', keyValue);
    keyField.hidden = true;
    keyMode.addEventListener('change', () => {
      keyField.hidden = keyMode.value !== 'replace';
    });

    const error = el('div', { class: 'form-error' });

    const save = () => {
      if (this._saving) return;
      const gid = g ? g.id : id.value.trim();
      if (!gid) {
        error.textContent = 'gateway id is required';
        return;
      }
      // Preserve every advanced field for surviving ids; defaults for new.
      const byId = new Map((g ? g.models || [] : []).map((m) => [m.id, m]));
      const body = {
        name: name.value.trim(),
        style: style.value,
        endpoint: endpoint.value.trim(),
        stream: stream.checked,
        models: [...this._modelIds].map((mid) => byId.get(mid) || {
          id: mid, route: '', contextWindow: 0,
          pricing: { inputPerMillionUsd: 0, outputPerMillionUsd: 0, cacheReadPerMillionUsd: 0, cacheWritePerMillionUsd: 0 },
        }),
      };
      // Replace must carry a real key — an empty string only crosses
      // via the explicit Clear mode.
      if (keyMode.value === 'replace') {
        if (!keyValue.value) {
          error.textContent = 'new API key is empty — use Clear to remove the stored key';
          return;
        }
        body.apiKey = keyValue.value;
      }
      if (keyMode.value === 'clear') body.apiKey = '';
      this._saving = true;
      this._pendingSaveId = gid;
      error.textContent = 'Saving…';
      emit(this, 'gateway-save', { id: gid, body });
    };

    form.append(
      field('id', id),
      field('name', name),
      field('api style', style),
      field('endpoint', endpoint),
      field('stream', el('span', { class: 'check' }, [stream])),
      el('div', { class: 'field' }, [
        el('div', { class: 'model-head' }, [
          el('span', { class: 'field-label', text: 'models' }), this._modelCount,
        ]),
        this._modelSearch,
        this._modelList,
      ]),
      el('div', { class: 'import-actions' }, [
        this._importBtn = el('button', {
          class: 'btn', type: 'button', disabled: !g || this._importing,
          text: this._importing ? 'Importing…' : 'Refresh models from provider',
          onclick: () => this._importModels(g.id),
        }),
        g
          ? el('span', { class: 'hint', text: 'Re-reads the provider catalog with the saved endpoint and key.' })
          : el('span', { class: 'hint', text: 'Save the gateway to import its models.' }),
      ]),
      this._importNote = el('div'),
      field('api key', keyMode),
      keyField,
      error,
    );
    dialog.append(
      el('div', { class: 'dialog-head' }, [
        el('div', { class: 'dialog-title', text: g ? `Edit ${g.id}` : 'New gateway' }),
        el('button', { class: 'icon-btn dialog-close', type: 'button', 'aria-label': 'Close gateway editor', onclick: close }, [icon('x', 16)]),
      ]),
      form,
      el('div', { class: 'form-actions' }, [
        el('button', { class: 'btn accent', type: 'button', onclick: save }, [el('span', { text: 'Save' })]),
        el('button', { class: 'btn', type: 'button', onclick: close }, [el('span', { text: 'Cancel' })]),
      ]),
    );
    this.root.append(dialog);
    this._dialog = dialog;
    dialog.showModal();
    this._renderModels();
    this._formError = error;
  }

  _renderModels() {
    if (!this._modelSearch || !this._modelList) return; // no form on screen
    const ids = [...this._modelIds];
    const q = this._modelSearch.value.trim().toLowerCase();
    const visible = ids.filter((id) => id.toLowerCase().includes(q));
    this._modelCount.textContent = q ? `${visible.length} of ${ids.length}` : `${ids.length} configured`;
    clear(this._modelList);
    if (!visible.length) {
      this._modelList.append(el('div', { class: 'model-empty', text: q ? 'No matching models.' : 'No models configured yet.' }));
      return;
    }
    for (const id of visible) this._modelList.append(el('div', { class: 'model-item', role: 'listitem', text: id }));
  }

  // The import runs server-side against the stored record — a field the
  // user edited but never saved (endpoint, key) does NOT apply here.
  _importModels(id) {
    this._importing = true;
    this._syncImportState();
    this._noteImport('Importing the provider catalog…', false);
    emit(this, 'gateway-import', { id });
  }

  _syncImportState() {
    if (!this._importBtn) return;
    this._importBtn.disabled = !(this._editing || this._modelsOf) || this._importing;
    this._importBtn.textContent = this._importing
      ? 'Importing…' : 'Refresh models from provider';
  }

  _noteImport(text, isError) {
    if (!this._importNote) return;
    this._importNote.textContent = text;
    this._importNote.className = isError ? 'form-error' : 'hint';
  }
}

customElements.define('nusa-provider-manager', ProviderManager);
