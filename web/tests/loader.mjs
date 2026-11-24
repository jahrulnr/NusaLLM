// Resolves the WebUI's absolute URL imports against the repository's web/
// directory, which is the root the server serves them from. A real absolute
// file path (or a bare package specifier) is left to Node's own resolver.
import { existsSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

export function resolve(specifier, context, next) {
  if (specifier.startsWith('/') && !specifier.startsWith('//')) {
    const file = path.join(root, specifier);
    if (existsSync(file)) return next(pathToFileURL(file).href, context);
  }
  return next(specifier, context);
}
