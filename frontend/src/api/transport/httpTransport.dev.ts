import type { Transport } from './Transport';

/**
 * Development-only transport. `vite dev` runs the console in a plain browser
 * tab with no native host to talk to, so this transport calls cia-edge's
 * real HTTP API - through Vite's own dev-server proxy
 * (`DEV_EDGE_PROXY_PREFIX`, configured in `vite.config.ts`), not by fetching
 * `http://127.0.0.1:18091` directly from the browser. A direct
 * browser-to-loopback fetch would be cross-origin from the page `vite dev`
 * serves, and cia-edge deliberately sends no `Access-Control-Allow-Origin`
 * header (see `internal/edge/server_test.go`) - no CSP relaxation changes
 * that, since CORS is enforced independently of CSP. Vite's proxy forwards
 * the request server-side (Node process to Node/Go process, no browser
 * involved), so the fetch below only ever needs to be same-origin.
 *
 * This module, and only this module, is allowed to call `fetch` for a
 * network request (enforced by the ESLint rules in eslint.config.js). It
 * must never end up in the production bundle - see
 * src/api/transport/index.ts for how selection is done at build time via
 * `import.meta.env.DEV`, and scripts/verify-prod-bundle.mjs for the CI check
 * that proves this file's code does not appear in the built output.
 */
const DEV_EDGE_PROXY_PREFIX = '/__cia-edge';

// The only operation Sprint 1's single page needs. Extend this map as more
// read operations are added in later sprints - each op name still maps to
// exactly one loopback path, kept in one place rather than scattered fetch
// calls.
const OPERATION_PATHS: Record<string, string> = {
  status: '/api/v1/status',
};

export const httpTransport: Transport = {
  async request<_T>(op: string, _params?: unknown): Promise<unknown> {
    const path = OPERATION_PATHS[op];
    if (path === undefined) {
      throw new Error(`httpTransport.dev: unsupported operation "${op}"`);
    }

    const response = await fetch(`${DEV_EDGE_PROXY_PREFIX}${path}`, { method: 'GET' });
    if (!response.ok) {
      throw new Error(`httpTransport.dev: GET ${path} responded ${response.status} ${response.statusText}`);
    }
    return (await response.json()) as unknown;
  },
};
