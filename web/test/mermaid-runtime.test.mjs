import test from 'node:test';
import assert from 'node:assert/strict';
import { validateMermaidSource, renderMermaid } from '../lib/mermaid-runtime.js';

// Allowed: ordinary diagram families and arrow/label shapes.
const OK = [
  'flowchart TD\n  A[Parse] --> B{Valid?}\n  B -- yes --> C[Render]',
  'flowchart LR\n  A <--> B\n  C <|-- D',
  'flowchart TD\n  A -.-> B\n  A ==> B\n  A o-- B',
  'sequenceDiagram\n  participant U as User\n  U->>S: request\n  S-->>U: response',
  'stateDiagram-v2\n  [*] --> Idle\n  Idle --> Done: finish',
  'classDiagram\n  Animal <|-- Duck\n  class Shape {\n    <<interface>>\n    +draw()\n  }',
  'erDiagram\n  CUSTOMER ||--o{ ORDER : places',
  'gantt\n  title Plan\n  section S\n  Task :2024-01-01, 3d',
  'flowchart TD\n  A["an image of a cat"] --> B["width < height"]',
  'flowchart TD\n  A["x < y and a>b"] --> B',
];

for (const src of OK) {
  test(`accepts safe source: ${src.split('\n')[0]}`, () => {
    assert.equal(validateMermaidSource(src), true);
  });
}

const BAD = [
  ['html img tag', 'flowchart TD\n  A["<img src=x onerror=alert(1)>"] --> B'],
  ['html div tag', 'flowchart TD\n  A<div>x</div> --> B'],
  ['closing tag', 'flowchart TD\n  A["x"] --> B\n  </script>'],
  ['encoded tag', 'flowchart TD\n  A["&lt;img src=x&gt;"] --> B'],
  ['numeric entity tag', 'flowchart TD\n  A["&#60;img src=x&#62;"] --> B'],
  ['stereotype smuggling tag', 'flowchart TD\n  A<<img src=x onerror=alert(1)>> --> B'],
  ['click href', 'flowchart TD\n  A-->B\n  click A href "https://evil.example/"'],
  ['click call', 'flowchart TD\n  A-->B\n  click B call alert(1)'],
  ['indented click', 'flowchart TD\n  A-->B\n   click A href "/local"'],
  ['img shape', 'flowchart TD\n  A@{img: "./local.png"}'],
  ['image shape', 'flowchart TD\n  A@{image: "pic.png"}'],
  ['https url', 'flowchart TD\n  A["see https://evil.example"] --> B'],
  ['http url', 'flowchart TD\n  A["see http://evil.example"] --> B'],
  ['protocol-relative', 'flowchart TD\n  A["//evil.example/x"] --> B'],
  ['javascript scheme', 'flowchart TD\n  A["javascript:alert(1)"] --> B'],
  ['data scheme', 'flowchart TD\n  A["data:text/html,x"] --> B'],
  ['file scheme', 'flowchart TD\n  A["file:///etc/passwd"] --> B'],
  ['blob scheme', 'flowchart TD\n  A["blob:abc"] --> B'],
  ['css url()', 'flowchart TD\n  A["url(evil)"] --> B'],
  ['@import', 'flowchart TD\n  A["@import x"] --> B'],
  ['init directive', '%%{init: {"securityLevel":"loose"}}%%\nflowchart TD\n  A-->B'],
  ['frontmatter', '---\ntitle: hi\n---\nflowchart TD\n  A-->B'],
  ['control char', 'flowchart TD\n  A["x\u0001y"] --> B'],
  ['oversize', `flowchart TD\n  A${'x'.repeat(101 * 1024)}-->B`],
  ['empty', ''],
];

for (const [name, src] of BAD) {
  test(`rejects ${name}`, () => {
    assert.equal(validateMermaidSource(src), false);
  });
}

// A stubbed global proves invalid jobs never reach mermaid.render —
// the scratch DOM (where side effects happen) is never touched.
test('invalid source rejects before mermaid.render is called', async () => {
  let renderCalls = 0;
  let initCalls = 0;
  globalThis.mermaid = {
    initialize() { initCalls++; },
    render() { renderCalls++; return Promise.resolve({ svg: '<svg/>' }); },
  };
  await assert.rejects(
    () => renderMermaid('t-invalid', 'flowchart TD\n A["<img src=x>"] --> B'),
    /Diagram could not be rendered/,
  );
  assert.equal(renderCalls, 0);

  // And a valid source still goes through (queue unpoisoned).
  const { svg } = await renderMermaid('t-valid', 'flowchart TD\n A-->B');
  assert.equal(svg, '<svg/>');
  assert.equal(renderCalls, 1);
  delete globalThis.mermaid;
});
