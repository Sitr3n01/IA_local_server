/// <reference types="node" />
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

/**
 * Test-only helper: reads a stylesheet's raw source so tests can assert
 * against CSS text directly.
 *
 * Paths resolve from `src/`, not from `src/design-system/primitives`. The
 * narrower base was a real coverage hole rather than a detail: it made every
 * stylesheet outside the primitives directory unreadable by these tests, so
 * the focus-ring assertions silently covered three components and could not
 * reach the nav rail - the one control an operator drives most - nor the
 * text field's clear button, nor anything on a page. A green suite implied
 * a coverage it did not have.
 *
 * Uses Node's `fs`, resolved off `process.cwd()`, rather than Vite's
 * `?raw` import query or `import.meta.url`:
 *   - Vitest's SSR module pipeline stubs out `.css` imports (including
 *     `?raw`-queried ones) to an empty string by default, since CSS has no
 *     runtime meaning under Node/SSR - a `?raw` import here silently reads
 *     as `""` instead of throwing.
 *   - Vitest also doesn't give test files a plain `file:` `import.meta.url`,
 *     so `fileURLToPath(new URL(...))` throws "The URL must be of scheme
 *     file". `npm test`/CI always run with `frontend/` as the working
 *     directory, so resolving from `process.cwd()` is the reliable option.
 *
 * The `/// <reference types="node" />` above (rather than adding "node" to
 * tsconfig.app.json's `types` array) keeps Node's ambient globals scoped to
 * this one test-only helper instead of every app-side source file - app
 * code is typechecked without Node's global types, by design.
 */
export function readCss(relativePath: string): string {
  return readFileSync(resolve(process.cwd(), 'src', relativePath), 'utf8');
}
