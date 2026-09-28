import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseMarkdown, parseInline, sanitizeLink } from '../lib/markdown.js';

// Recursively collect all literal text of an inline/block node list —
// used to prove no source characters are silently dropped.
function flatText(nodes) {
  let s = '';
  for (const n of nodes) {
    if (n.text != null) s += n.text;
    if (n.content != null) s += n.content;
    if (n.children) s += flatText(n.children);
    if (n.inline) s += flatText(n.inline);
    if (n.blocks) s += flatText(n.blocks);
    if (n.type === 'softbreak' || n.type === 'hardbreak') s += '\n';
    if (n.type === 'image') s += `![${n.alt}](${n.src})`;
    if (n.items) for (const it of n.items) s += flatText(it.inline);
    if (n.header) for (const c of n.header) s += flatText(c);
    if (n.rows) for (const r of n.rows) for (const c of r) s += flatText(c);
  }
  return s;
}

test('blocks: headings, hr, paragraph, blank-line separation', () => {
  const ast = parseMarkdown('# Title\n\nPara one.\n\n---\n\n###### deep\n####### not a heading\n');
  assert.equal(ast[0].type, 'heading');
  assert.equal(ast[0].level, 1);
  assert.equal(ast[1].type, 'paragraph');
  assert.equal(ast[2].type, 'hr');
  assert.equal(ast[3].level, 6);
  assert.equal(ast[4].type, 'paragraph'); // 7 hashes are literal text
});

test('inline: strong/em/strike/code/escape', () => {
  const ast = parseInline('**b _i_** and `a ** b` \\*lit\\* ~~gone~~ _unclosed');
  const strong = ast.find((n) => n.type === 'strong');
  assert.equal(strong.children[1].type, 'em');
  assert.equal(flatText(strong.children), 'b i');
  assert.equal(ast.find((n) => n.type === 'code').text, 'a ** b');
  assert.equal(ast.find((n) => n.type === 'strike').children[0].text, 'gone');
  assert.ok(flatText(ast).includes('*lit*'));   // escapes unescape
  assert.ok(flatText(ast).includes('_unclosed')); // unmatched stays literal
});

test('nested syntax degrades to literal without losing characters', () => {
  const ast = parseInline('**a `code` tail** and **unclosed');
  assert.equal(ast[0].type, 'strong');
  assert.ok(ast[0].children.some((n) => n.type === 'code'));
  assert.ok(flatText(ast).includes('**unclosed'));
});

test('links: safe schemes parse; autolinks; images become placeholder nodes', () => {
  const ast = parseInline('[a](https://x.test) <https://y.test> ![alt](https://i.test/x.png) ![empty]()');
  const links = ast.filter((n) => n.type === 'link');
  assert.equal(links.length, 2);
  assert.equal(links[0].children[0].text, 'a');
  assert.equal(links[1].children[0].text, 'https://y.test');
  const img = ast.find((n) => n.type === 'image');
  assert.equal(img.alt, 'alt');
  assert.ok(ast.filter((n) => n.type === 'image').length === 2);
});

test('unsafe link destinations fall back to literal source text', () => {
  for (const bad of ['javascript:alert(1)', 'data:x', '//evil', 'java\tscript:x']) {
    const ast = parseInline(`[t](${bad})`);
    assert.ok(!ast.some((n) => n.type === 'link'), bad);
    assert.ok(flatText(ast).includes(`[t](${bad})`), bad);
  }
});

test('hard break and soft break nodes', () => {
  const ast = parseInline('one  \ntwo\nthree');
  assert.deepEqual(ast.map((n) => n.type), ['text', 'hardbreak', 'text', 'softbreak', 'text']);
});

test('raw HTML is literal text, never markup', () => {
  const ast = parseMarkdown('<script>alert(1)</script>\ninline <b>bold</b>');
  assert.equal(ast[0].type, 'paragraph');
  assert.equal(flatText(ast), '<script>alert(1)</script>\ninline <b>bold</b>');
});

test('fenced code: backticks, tildes, language, unterminated stream', () => {
  let ast = parseMarkdown('```go\nfmt.Println()\n```\n');
  assert.equal(ast[0].type, 'code');
  assert.equal(ast[0].lang, 'go');
  assert.equal(ast[0].content, 'fmt.Println()');
  assert.equal(ast[0].closed, true);

  ast = parseMarkdown('~~~py\nprint(1)\n~~~');
  assert.equal(ast[0].lang, 'py');
  assert.equal(ast[0].content, 'print(1)');
  assert.equal(ast[0].closed, true);

  // Unterminated fence (mid-stream) still yields a code block and is
  // marked open — diagram engines must never see it.
  ast = parseMarkdown('text\n```js\nlet x = 1');
  assert.equal(ast.at(-1).type, 'code');
  assert.equal(ast.at(-1).lang, 'js');
  assert.equal(ast.at(-1).content, 'let x = 1');
  assert.equal(ast.at(-1).closed, false);
});

test('mermaid/mmd fences parse like any code block; closed flag drives rendering', () => {
  let ast = parseMarkdown('```mermaid\nflowchart TD\n A-->B\n```\n');
  assert.equal(ast[0].type, 'code');
  assert.equal(ast[0].lang, 'mermaid');
  assert.equal(ast[0].closed, true);

  ast = parseMarkdown('```mmd\nsequenceDiagram\n A->>B: hi\n```\n');
  assert.equal(ast[0].lang, 'mmd');
  assert.equal(ast[0].closed, true);

  ast = parseMarkdown('```mermaid\nflowchart TD\n A-->');
  assert.equal(ast[0].lang, 'mermaid');
  assert.equal(ast[0].closed, false);
});

test('lists: unordered, ordered start, task markers', () => {
  const ast = parseMarkdown('- a\n- [x] done\n- [ ] todo\n\n3. three\n4. four\n');
  const ul = ast[0];
  assert.equal(ul.type, 'list');
  assert.equal(ul.ordered, false);
  assert.equal(ul.items[0].task, null);
  assert.equal(ul.items[1].task, true);
  assert.equal(ul.items[2].task, false);
  const ol = ast[1];
  assert.equal(ol.ordered, true);
  assert.equal(ol.start, 3);
});

test('blockquote contains nested blocks', () => {
  const ast = parseMarkdown('> quoted\n> more\n\nnot quoted');
  assert.equal(ast[0].type, 'blockquote');
  assert.equal(ast[0].blocks[0].type, 'paragraph');
  assert.equal(flatText(ast[0].blocks), 'quoted\nmore');
  assert.equal(ast[1].type, 'paragraph');
});

test('pipe table: headers, alignment row, ragged rows', () => {
  const ast = parseMarkdown('| a | b |\n|---|--:|\n| 1 | 2 |\n| 3 |\n');
  const t = ast[0];
  assert.equal(t.type, 'table');
  assert.deepEqual(t.align, ['', 'right']); // bare --- is default align
  assert.equal(t.rows.length, 2);
  assert.equal(flatText(t.header[0]), 'a');
});

test('soft line breaks stay inside one paragraph', () => {
  const ast = parseMarkdown('line one\nline two\n');
  assert.equal(ast.length, 1);
  assert.equal(ast[0].type, 'paragraph');
  assert.equal(flatText(ast[0].inline), 'line one\nline two');
});

test('sanitizeLink allows only safe destinations', () => {
  for (const ok of ['https://a.test', 'http://a.test', 'mailto:x@y.z', '/path', './rel', '../up']) {
    assert.equal(sanitizeLink(ok), ok, ok);
  }
  for (const bad of [
    'javascript:alert(1)', 'JaVaScRiPt:x', 'data:text/html,x', 'file:///etc',
    'blob:x', '//evil.test', 'https://x test', 'java\tscript:alert(1)',
    'jav\nascript:x', 'java\vscript:x', 'https://a.test/\u0007path', '', null,
    // Fragments are reserved for the app's own hash router.
    '#fragment', '#',
    // Backslash path tricks — plain, leading, and mixed.
    '/\\evil.example/x', '\\evil', 'https:\\\\evil.test', '/a\\b',
  ]) {
    assert.equal(sanitizeLink(bad), null, String(bad));
  }
});

test('blockquote depth is bounded — 20000 nested markers degrade to literal text', () => {
  const deep = `${'> '.repeat(20000)}bottom`;
  const ast = parseMarkdown(deep);
  let node = ast[0];
  let depth = 0;
  while (node && node.type === 'blockquote') {
    depth++;
    node = node.blocks[0];
  }
  assert.equal(depth, 32);
  // Beyond the cap the remaining markers render literally, not lost.
  assert.equal(node.type, 'paragraph');
  assert.ok(flatText([node]).includes('>'));
  assert.ok(flatText([node]).includes('bottom'));
});

test('no character loss on malformed markdown', () => {
  const weird = '**_~~` [x]( unclosed <notalink ![img]( \n```\nno close\n';
  const ast = parseMarkdown(weird);
  const flat = flatText(ast);
  assert.ok(flat.includes('**_~~` [x]( unclosed <notalink ![img]( '), flat);
  assert.ok(flat.includes('no close'));
});
