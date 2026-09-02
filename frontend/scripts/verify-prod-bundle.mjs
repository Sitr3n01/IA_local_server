#!/usr/bin/env node
/**
 * Proves three things about the production bundle (`npm run build` output,
 * in dist/) that this console's architecture depends on:
 *
 *   1. The dev-only HTTP transport (src/api/transport/httpTransport.dev.ts)
 *      did not leak into it, and neither did its loopback base URL. This is
 *      the CI-facing half of "no HTTP transport in production" - the
 *      build-time half is `import.meta.env.DEV` in
 *      src/api/transport/index.ts, which is *supposed* to make Rollup drop
 *      httpTransport.dev.ts from the production module graph entirely. This
 *      script is what actually checks that happened, instead of trusting
 *      that it did. Checked across every built file (JS and otherwise).
 *
 *   2. The dev-only DS 0.1 primitive gallery (src/dev/Gallery.tsx, reachable
 *      at /__gallery under `vite dev`) did not leak in either. Same
 *      technique, same reasoning: `import.meta.env.DEV` gates a dynamic
 *      `import()` in src/main.tsx, and this script checks the built output
 *      for a marker string unique to Gallery.tsx's source.
 *
 *   3. index.html and any built CSS reference no external origin - no CDN
 *      script, no remote font, no external stylesheet. Checked only against
 *      index.html and *.css, not the JS bundles: React and Zod's own
 *      minified code legitimately embeds https:// documentation-link and
 *      $schema string constants (React's decode-this-error-code links,
 *      Zod's JSON Schema draft URIs) that are never fetched or rendered as
 *      a live link - flagging every URL-shaped substring in vendored JS
 *      would fail on those every time and teach reviewers to ignore this
 *      check. index.html and CSS are the actual places an external origin
 *      would cause a real network request (script/link tags, @import,
 *      url()), so that is where this script looks for one.
 *
 * Usage: node scripts/verify-prod-bundle.mjs   (run after `npm run build`)
 */
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, extname } from 'node:path';

const DIST_DIR = new URL('../dist', import.meta.url).pathname
  // Strip the leading slash Node adds on Windows for file URLs (e.g. "/C:/...").
  .replace(/^\/([A-Za-z]:)/, '$1');

/** A string that appears in httpTransport.dev.ts's source and nowhere else. */
const DEV_TRANSPORT_MARKER = 'httpTransport.dev:';
/** The dev transport's loopback base URL - must never reach production. */
const LOOPBACK_NEEDLE = '127.0.0.1';
/**
 * A string that appears only in src/dev/Gallery.tsx's source. The gallery
 * route (src/main.tsx's `/__gallery` branch) is excluded from the
 * production bundle the same way httpTransport.dev.ts is - a dynamic
 * `import()` behind `import.meta.env.DEV`, so Rollup drops it entirely
 * during `vite build`. This is the check that proves that actually
 * happened, mirroring checkNoDevTransportLeak below.
 */
const GALLERY_MARKER = 'design-system-gallery:dev-only';

function walk(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    const st = statSync(full);
    if (st.isDirectory()) {
      out.push(...walk(full));
    } else {
      out.push(full);
    }
  }
  return out;
}

function checkNoDevTransportLeak(files, failures) {
  for (const file of files) {
    const content = readFileSync(file, 'utf8');

    if (content.includes(LOOPBACK_NEEDLE)) {
      failures.push(`${file}: contains the loopback address "${LOOPBACK_NEEDLE}"`);
    }

    if (content.includes(DEV_TRANSPORT_MARKER)) {
      failures.push(
        `${file}: contains the dev-transport marker "${DEV_TRANSPORT_MARKER}" - httpTransport.dev.ts leaked into the production bundle`,
      );
    }

    if (content.includes(GALLERY_MARKER)) {
      failures.push(
        `${file}: contains the gallery marker "${GALLERY_MARKER}" - src/dev/Gallery.tsx leaked into the production bundle`,
      );
    }
  }
}

function checkNoExternalOrigin(files, failures) {
  const urlPattern = /https?:\/\/[^\s"'<>)]+/g;
  for (const file of files) {
    const content = readFileSync(file, 'utf8');
    const matches = content.match(urlPattern) ?? [];
    for (const url of matches) {
      failures.push(`${file}: references an external origin "${url}"`);
    }
  }
}

// The shipped Content-Security-Policy must be the restrictive one from the
// source index.html, not the relaxed variant vite.config.ts's dev-only
// plugin substitutes for `vite dev`. That plugin loosens `connect-src` (so
// the dev HTTP transport can reach the loopback status endpoint) and
// `style-src` (so Vite's HMR can inject <style> elements). Neither may ever
// reach a built page: the console runs inside a WebView2 host with no
// network access from the document and no HMR.
const REQUIRED_CSP_DIRECTIVES = ["connect-src 'none'", "style-src 'self'"];
const FORBIDDEN_CSP_SUBSTRINGS = ["'unsafe-inline'", "'unsafe-eval'", '127.0.0.1'];

function checkProductionCsp(files, failures) {
  const htmlFiles = files.filter((file) => file.endsWith('.html'));
  if (htmlFiles.length === 0) {
    failures.push('dist/ contains no HTML file to check the CSP on');
    return;
  }
  for (const file of htmlFiles) {
    const html = readFileSync(file, 'utf8');
    const match = html.match(
      /<meta[^>]+http-equiv=["']Content-Security-Policy["'][^>]*>/i,
    );
    if (!match) {
      failures.push(`${file}: has no Content-Security-Policy meta tag`);
      continue;
    }
    const tag = match[0];
    for (const directive of REQUIRED_CSP_DIRECTIVES) {
      if (!tag.includes(directive)) {
        failures.push(`${file}: CSP is missing the required directive "${directive}"`);
      }
    }
    for (const forbidden of FORBIDDEN_CSP_SUBSTRINGS) {
      if (tag.includes(forbidden)) {
        failures.push(
          `${file}: CSP contains "${forbidden}" - the dev-only relaxation leaked into the build`,
        );
      }
    }
  }
}

function main() {
  let distStat;
  try {
    distStat = statSync(DIST_DIR);
  } catch {
    console.error(`verify-prod-bundle: dist/ not found at ${DIST_DIR}. Run "npm run build" first.`);
    process.exit(1);
    return;
  }
  if (!distStat.isDirectory()) {
    console.error(`verify-prod-bundle: ${DIST_DIR} is not a directory.`);
    process.exit(1);
    return;
  }

  const allFiles = walk(DIST_DIR).filter((f) => ['.js', '.mjs', '.html', '.css'].includes(extname(f)));
  if (allFiles.length === 0) {
    console.error('verify-prod-bundle: no built .js/.html/.css files found - build output looks empty.');
    process.exit(1);
    return;
  }

  const assetSurfaceFiles = allFiles.filter((f) => ['.html', '.css'].includes(extname(f)));
  if (assetSurfaceFiles.length === 0) {
    console.error('verify-prod-bundle: no built .html/.css files found - expected at least index.html.');
    process.exit(1);
    return;
  }

  const failures = [];
  checkNoDevTransportLeak(allFiles, failures);
  checkNoExternalOrigin(assetSurfaceFiles, failures);
  checkProductionCsp(allFiles, failures);

  if (failures.length > 0) {
    console.error('verify-prod-bundle: FAILED\n');
    for (const failure of failures) {
      console.error(`  - ${failure}`);
    }
    process.exit(1);
    return;
  }

  console.log(
    `verify-prod-bundle: OK - scanned ${allFiles.length} file(s) in dist/ ` +
      `(loopback address + dev-transport marker + gallery marker) and ${assetSurfaceFiles.length} HTML/CSS file(s) ` +
      '(external origins) and asserted the shipped CSP. Found none.',
  );
}

main();
