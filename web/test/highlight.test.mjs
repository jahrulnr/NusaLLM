import { test } from 'node:test';
import assert from 'node:assert/strict';
import { tokenizeCode } from '../lib/highlight.js';

const join = (t) => t.map((x) => x.text).join('');
const types = (t) => new Set(t.map((x) => x.type));

test('source preservation across every supported language', () => {
  const samples = {
    go: 'package main\n\nfunc f() int { return 42 }\n',
    javascript: 'const x = `a\nb`; // c\nfunction f() { return 0x10; }\n',
    json: '{"a": [1, 2.5e3], "b": null, "c": true}\n',
    python: 'def f(x):\n    return x  # tail\n"""docstring"""\n',
    shell: '#!/bin/bash\nFOO="a b" ls -la | grep x > out\n',
    html: '<div class="a">&amp;</div>\n',
    css: '.a > b { color: #fff; /* c */ }\n',
    markdown: '# T\n**b** `c`\n',
    diff: '@@ -1,2 +1,2 @@\n-old\n+new\n ctx\n',
    unknown: 'literal raw ☃ text\n',
    '': 'no language\n',
  };
  for (const [lang, src] of Object.entries(samples)) {
    assert.equal(join(tokenizeCode(src, lang)), src, lang);
  }
});

test('go tokens', () => {
  const t = tokenizeCode('// hi\nfunc f() string { return "s" + `raw` }\nconst N = 1e3\n', 'go');
  assert.ok(types(t).has('comment'));
  assert.ok(types(t).has('keyword'));
  assert.ok(types(t).has('string'));
  assert.ok(types(t).has('number'));
});

test('javascript tokens', () => {
  const t = tokenizeCode('const s = "x"; /* b */ const r = /a+/; f(9)\n', 'javascript');
  assert.ok(types(t).has('keyword'));
  assert.ok(types(t).has('string'));
  assert.ok(types(t).has('comment'));
  assert.ok(types(t).has('number'));
});

test('json tokens — keys, values, punctuation', () => {
  const t = tokenizeCode('{"k": "v", "n": 1}\n', 'json');
  assert.ok(types(t).has('keyword')); // object keys + literals
  assert.ok(types(t).has('string'));
  assert.ok(types(t).has('number'));
  assert.ok(types(t).has('operator'));
});

test('shell tokens — comment, variable, flag', () => {
  const t = tokenizeCode('#!/bin/sh\nexport A=1\nls -la $HOME\n', 'sh');
  assert.ok(types(t).has('comment'));
  assert.ok(types(t).has('operator')); // $HOME
  assert.ok(types(t).has('keyword')); // export
});

test('diff tokens — insert/delete/header', () => {
  const t = tokenizeCode('--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n', 'diff');
  assert.ok(types(t).has('keyword'));
  assert.ok(types(t).has('insert'));
  assert.ok(types(t).has('delete'));
});

test('language aliases resolve', () => {
  for (const [alias, canon] of [['js', 'javascript'], ['ts', 'typescript'], ['tsx', 'tsx'], ['golang', 'go'], ['sh', 'shell'], ['bash', 'shell'], ['py', 'python'], ['htm', 'html'], ['md', 'markdown']]) {
    assert.equal(join(tokenizeCode('x\n', alias)), 'x\n', alias);
  }
  // An alias actually highlights, not just preserves.
  const t = tokenizeCode('function f() {}\n', 'js');
  assert.ok(types(t).has('keyword'));
});

test('empty and whitespace sources stay stable', () => {
  assert.deepEqual(tokenizeCode('', 'go'), []);
  assert.equal(join(tokenizeCode('  \n\n', 'go')), '  \n\n');
});
