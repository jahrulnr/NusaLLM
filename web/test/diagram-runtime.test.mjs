import test from 'node:test';
import assert from 'node:assert/strict';
import { validateDiagramSource, mountDiagram } from '../lib/diagram-runtime.js';

// Allowed: ordinary diagram families and arrow/label shapes.
const OK = [
  'flowchart TD\n  A[Parse] --> B{Valid?}\n  B -- yes --> C[Render]',
  'flowchart LR\n  A <--> B\n  C <' + '|-- D',
  'flowchart TD\n  A -.-> B\n  A ==> B\n  A o-- B',
  'sequenceDiagram\n  participant U as User\n  U->>S: request\n  S-->>U: response',
  'stateDiagram-v2\n  [*] --> Idle\n  Idle --> Done: finish',
  'classDiagram\n  Animal <' + '|-- Duck\n  class Shape {\n    <<interface>>\n    +draw()\n  }',
  'erDiagram\n  CUSTOMER ||--o{ ORDER : places',
  'gantt\n  title Plan\n  section S\n  Task :2024-01-01, 3d',
  'flowchart TD\n  A["an image of a cat"] --> B["width < height"]',
  'flowchart TD\n  A["x < y and a>b"] --> B',
];

for (const src of OK) {
  test(`accepts safe source: ${src.split('\n')[0]}`, () => {
    assert.equal(validateDiagramSource(src), true);
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
    assert.equal(validateDiagramSource(src), false);
  });
}

// A stubbed global proves invalid sources never reach pele.mount —
// the engine is never touched — and that the mount options stay
// locked (no links, no images, sanitize wired through).
test('mount gate and locked options', () => {
  let mountCalls = 0;
  let seenOptions = null;
  globalThis.pele = {
    mount(el, source, options) {
      mountCalls++;
      seenOptions = options;
      return { update() {}, refresh() {}, destroy() {}, result: { svg: '<svg/>' } };
    },
  };
  const el = {};
  assert.throws(
    () => mountDiagram(el, 'flowchart TD\n A["<img src=x>"] --> B'),
    /Diagram could not be rendered/,
  );
  assert.equal(mountCalls, 0);

  const hooks = { sanitize: () => null, onError() {}, onRender() {} };
  const handle = mountDiagram(el, 'flowchart TD\n A-->B', hooks);
  assert.equal(mountCalls, 1);
  assert.equal(handle.result.svg, '<svg/>');
  assert.equal(seenOptions.links, false);
  assert.equal(seenOptions.images, false);
  assert.equal(seenOptions.limit, 100 * 1024);
  assert.equal(seenOptions.zoom.always, true);
  assert.equal(typeof seenOptions.sanitize, 'function');
  // The widget's hooks pass through untouched — verdict reporting is
  // pele's onError/onRender, not thrown errors.
  assert.equal(typeof seenOptions.onError, 'function');
  assert.equal(typeof seenOptions.onRender, 'function');
  delete globalThis.pele;
});

// Without the vendored global the seam fails clearly.
test('missing pele global throws unavailable', () => {
  delete globalThis.pele;
  assert.throws(
    () => mountDiagram({}, 'flowchart TD\n A-->B'),
    /Diagram runtime is unavailable/,
  );
});
