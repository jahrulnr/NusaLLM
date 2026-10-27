import { test } from 'node:test';
import assert from 'node:assert/strict';

// model-picker evaluates its widget class at load time; isStaleSelection is a
// pure function, so the element stand-ins only need to satisfy the definition.
globalThis.HTMLElement = class {};
globalThis.customElements = { define() {} };
globalThis.document = { createElement: () => ({}) };

const { isStaleSelection } = await import('../widgets/model-picker/model-picker.js');

const gateways = [
  { id: 'groq', models: [{ id: 'llama' }] },
  { id: 'old', models: [{ id: 'm1' }], broken: true },
];

test('a binding on a usable gateway and model is not stale', () => {
  assert.equal(isStaleSelection({ route: 'groq', modelId: 'llama' }, gateways), false);
});

test('a binding whose gateway was deleted is stale', () => {
  assert.equal(isStaleSelection({ route: 'broken', modelId: 'm1' }, gateways), true);
});

test('a binding on a broken gateway or a missing model is stale', () => {
  assert.equal(isStaleSelection({ route: 'old', modelId: 'm1' }, gateways), true);
  assert.equal(isStaleSelection({ route: 'groq', modelId: 'gone' }, gateways), true);
});

test('no selection, or a gateway list not loaded yet, is never stale', () => {
  assert.equal(isStaleSelection(null, gateways), false);
  assert.equal(isStaleSelection({ route: 'groq', modelId: 'llama' }, undefined), false);
});
