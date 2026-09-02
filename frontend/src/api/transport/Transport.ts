/**
 * The transport boundary. Everything the console reads from or writes to the
 * operator's machine goes through a single `Transport`, so the rest of the
 * app never has an opinion about *how* data arrives - only that it does.
 *
 * In production the only implementation is `bridgeTransport` (native WebView2
 * message bridge). In development, `httpTransport.dev` talks directly to
 * cia-edge over loopback HTTP so the console can run under `vite dev` in a
 * normal browser. Selection between the two happens at build time in
 * `src/api/transport/index.ts` via `import.meta.env.DEV`, so the dev
 * transport is excluded from the production bundle entirely rather than
 * merely unused at runtime - see verify-prod-bundle.mjs for the check that
 * proves it.
 *
 * `request` deliberately returns `Promise<unknown>`, not `Promise<T>`. The
 * type parameter (named `_T` so strict unused-parameter checks leave it
 * alone) is a documentation hint for call sites about what they expect back
 * - e.g. `transport.request<Status>('status')` reads as "I expect a
 * Status-shaped thing" - but it is NOT a runtime guarantee, and nothing here
 * validates the shape. Runtime boundary validation is the job of the Zod
 * schemas in src/api/schemas/*; every query in src/api/queries/* must parse
 * the raw result through one before treating it as trustworthy. Do not add
 * an `as T` cast anywhere and call it done - parse it.
 */
export interface Transport {
  request<_T>(op: string, params?: unknown): Promise<unknown>;
}
