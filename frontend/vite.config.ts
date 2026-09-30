// `vitest/config` re-exports Vite's `defineConfig` merged with the `test`
// option's types, so this one file can carry both the Vite build config and
// the Vitest test config without a second config file or an extra dependency.
import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

/**
 * The prefix `httpTransport.dev.ts` fetches under `vite dev`, forwarded to
 * cia-edge by the dev-server proxy configured below. Kept as a shared
 * constant so the proxy's `target`/`rewrite` and the transport's fetch path
 * can't drift apart.
 */
const DEV_EDGE_PROXY_PREFIX = '/__cia-edge';
const DEV_EDGE_ORIGIN = 'http://127.0.0.1:18091';

/**
 * Development-only relaxation of the production Content-Security-Policy meta
 * tag baked into index.html. `vite dev` runs the console in a plain browser
 * tab (there is no native WebView2 host yet, and no bridge to talk to), so
 * the dev HTTP transport (src/api/transport/httpTransport.dev.ts) needs
 * `connect-src` to allow its own fetches. Those fetches now stay same-origin
 * (see the proxy doc comment below), so `connect-src 'self'` is enough -
 * no loopback origin needs naming here at all.
 *
 * It also relaxes `style-src`. Vite's dev server injects CSS by creating
 * `<style>` elements from JavaScript for hot module replacement, which
 * `style-src 'self'` blocks - so with the production policy in force, `vite
 * dev` renders the console entirely unstyled. That was invisible while the
 * app had almost no CSS and became obvious the moment the design system
 * landed. The relaxation is confined to the dev server for the same reason
 * the `connect-src` one is: the shipped page must not need it.
 *
 * This plugin is only ever added to the `serve` command's plugin list (see
 * the conditional spread in `defineConfig` below) - `vite build` never loads
 * it, so the production index.html is emitted with the restrictive
 * `connect-src 'none'` and `style-src 'self'` from the source file,
 * untouched. scripts/verify-prod-bundle.mjs asserts that.
 *
 * The replacement is scoped to the CSP `<meta>` tag's own `content`
 * attribute via a regex, not a blind whole-document string `.replace()`.
 * index.html's doc comment directly above that tag explains this override
 * in prose and itself contains the literal substring `connect-src 'none'`
 * (backtick-quoted) - a plain `.replace("connect-src 'none'", ...)` matches
 * that comment text first (it appears earlier in the document) and never
 * reaches the real attribute, so `vite dev` would silently keep serving the
 * production `connect-src 'none'` and every dev-transport request would be
 * CSP-blocked. Scoping to the attribute value is what makes this override
 * actually take effect instead of failing silently. The same regex also had
 * to stop only at the closing *double* quote, not at any quote character -
 * the CSP value is full of single-quoted keywords ('none', 'self', ...)
 * inside its double-quoted HTML attribute.
 */
function devCspOverride() {
  const cspMetaTag = /(<meta[^>]+http-equiv=["']Content-Security-Policy["'][^>]*content=")([^"]+)(")/i;

  return {
    name: 'ia-local-dev-csp-override',
    transformIndexHtml(html: string): string {
      return html.replace(cspMetaTag, (_match, prefix: string, content: string, suffix: string) => {
        const relaxed = content
          .replace("connect-src 'none'", "connect-src 'self'")
          .replace(
            "style-src 'self'",
            // HMR injects <style> elements from JS. Dev server only.
            "style-src 'self' 'unsafe-inline'",
          );
        return `${prefix}${relaxed}${suffix}`;
      });
    },
  };
}

export default defineConfig(({ command }) => ({
  plugins: [react(), ...(command === 'serve' ? [devCspOverride()] : [])],
  // `exactOptionalPropertyTypes` (tsconfig) forbids assigning `server:
  // undefined` explicitly - the key must be omitted entirely for `vite
  // build`, not set to `undefined`, hence the conditional spread rather
  // than a ternary assigned directly to `server`.
  ...(command === 'serve'
    ? {
        server: {
          proxy: {
            // Forwards the dev transport's requests to the real cia-edge
            // instance server-side (Vite's Node process talking to another
            // local server), so the browser's own fetch stays same-origin.
            //
            // A direct browser fetch to `http://127.0.0.1:18091` (a
            // different origin than the page vite serves) cannot work no
            // matter how `connect-src` is relaxed: cia-edge deliberately
            // sends no `Access-Control-Allow-Origin` header - see
            // `internal/edge/server_test.go`'s assertion that the data
            // plane "unexpectedly enabled CORS" - so a real browser refuses
            // the response at the CORS layer, entirely separately from CSP.
            // Proxying server-side sidesteps browser CORS altogether
            // without touching cia-edge's own (intentionally strict)
            // response headers.
            [DEV_EDGE_PROXY_PREFIX]: {
              target: DEV_EDGE_ORIGIN,
              changeOrigin: true,
              rewrite: (path: string) => path.replace(new RegExp(`^${DEV_EDGE_PROXY_PREFIX}`), ''),
            },
          },
        },
      }
    : {}),
  build: {
    outDir: 'dist',
    // Keep the production bundle inspectable by the CI assertion in
    // scripts/verify-prod-bundle.mjs - source maps are not shipped, and the
    // built JS is not minified into a single opaque blob per-module.
    sourcemap: false,
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.{ts,tsx}'],
    // Registers `afterEach(cleanup)` from @testing-library/react - see
    // src/setupTests.ts for why this is required (globals: true isn't set).
    setupFiles: ['./src/setupTests.ts'],
    coverage: {
      provider: 'v8',
      // `json-summary` is what the quality gate reads; `text-summary` is for
      // a human running it locally. No HTML report - it is a large tree of
      // generated files nobody reviews and it would have to be gitignored.
      reporter: ['text-summary', 'json-summary'],
      reportsDirectory: 'coverage',
      include: ['src/**/*.{ts,tsx}'],
      // Excluded from the *denominator*, not from the checks: a barrel file
      // is re-exports with no branches, the dev-only gallery never ships, and
      // type-only declarations compile to nothing. Counting them would move
      // the coverage number without any test having been written.
      exclude: [
        'src/**/*.test.{ts,tsx}',
        'src/**/index.ts',
        'src/dev/**',
        'src/main.tsx',
        'src/vite-env.d.ts',
        'src/setupTests.ts',
        'src/design-system/primitives/test-utils/**',
      ],
    },
  },
}));
