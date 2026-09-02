import type { Transport } from './Transport';

export type { Transport } from './Transport';

/**
 * Build-time transport selection.
 *
 * `import.meta.env.DEV` is a compile-time constant: Vite inlines it as the
 * literal `true` in `vite dev` / `vite build --mode development` and as the
 * literal `false` in the production build. With the branch behind a dynamic
 * `import()`, a `false` condition lets Rollup's dead-code elimination remove
 * the entire `if` body - including its `import()` call - during the
 * production build. Once nothing in the module graph imports
 * `httpTransport.dev.ts` any more, Rollup drops it from the bundle instead of
 * emitting it as a reachable (if unused) chunk.
 *
 * scripts/verify-prod-bundle.mjs asserts this actually happened by scanning
 * the built `dist/` output for the dev transport's loopback URL and a marker
 * string unique to it - see that script for the check.
 */
let cached: Promise<Transport> | undefined;

export function getTransport(): Promise<Transport> {
  cached ??= import.meta.env.DEV
    ? import('./httpTransport.dev').then((mod) => mod.httpTransport)
    : import('./bridgeTransport').then((mod) => mod.bridgeTransport);
  return cached;
}
