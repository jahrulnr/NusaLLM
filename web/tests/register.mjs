// Lets the WebUI's own modules — which import each other and the vendored
// libraries by absolute URL path (/vendor/..., /lib/...) — load under Node.
//
// Two ways in, both supported:
//   node --import ./web/tests/register.mjs --test web/tests/*.test.mjs
//   import './register.mjs' as the first line of a test file, then
//   `await import('../lib/<name>.js')` (a static import would be resolved
//   before this module installs the hook).
import { register } from 'node:module';

register('./loader.mjs', import.meta.url);
