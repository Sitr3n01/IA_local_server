/// <reference types="node" />
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

/**
 * These tests read the CSS source directly (via Node's `fs`, resolved off
 * `process.cwd()` - Vitest's module runner doesn't give test files a plain
 * `file:` `import.meta.url`, so `fileURLToPath(new URL(...))` throws "The
 * URL must be of scheme file"; `npm test`/CI always run with `frontend/` as
 * the working directory, so resolving from there is the reliable option)
 * rather than rendering it through jsdom's CSS engine: jsdom does not
 * implement `var()` substitution or `@media` evaluation for
 * `getComputedStyle` (it is not a real layout/style engine), so a
 * DOM-rendered assertion here would either be unreliable or silently test
 * nothing. Parsing the cascade's three blocks as text is the deterministic
 * way to verify each theme actually resolves the required tokens to
 * *something*, and to something *different* per theme, which is what
 * "token resolution in both themes" means for a pure-CSS token layer. The
 * `/// <reference types="node" />` directive scopes Node's ambient globals
 * to this one test file instead of every app-side source file
 * (tsconfig.app.json's `types` deliberately excludes "node").
 */
const semanticCss = readFileSync(resolve(process.cwd(), 'src/design-system/tokens/semantic.css'), 'utf8');
const foundationCss = readFileSync(resolve(process.cwd(), 'src/design-system/tokens/foundation.css'), 'utf8');

const REQUIRED_SEMANTIC_COLOR_TOKENS = [
  '--color-bg-canvas',
  '--color-bg-surface',
  '--color-bg-surface-subtle',
  '--color-bg-surface-raised',
  '--color-text-primary',
  '--color-text-secondary',
  '--color-text-muted',
  '--color-border-subtle',
  '--color-border-default',
  '--color-border-strong',
  '--color-action-primary',
  '--color-action-primary-hover',
  '--color-status-info',
  '--color-status-success',
  '--color-status-warning',
  '--color-status-danger',
  '--color-accent-ai',
];

function extractValue(block: string, token: string): string | undefined {
  const escaped = token.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = new RegExp(`${escaped}:\\s*([^;]+);`).exec(block);
  return match?.[1]?.trim();
}

function countOccurrences(block: string, token: string): number {
  const escaped = token.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const matches = block.match(new RegExp(`${escaped}:`, 'g'));
  return matches?.length ?? 0;
}

// Split semantic.css into its three cascade blocks by locating the anchors
// that are guaranteed present in the file (see semantic.css's own top
// comment describing this exact three-block structure). The anchors below
// are deliberately more specific than the prose in that top comment - the
// comment describes "@media (prefers-color-scheme: dark)" and
// `:root[data-theme="dark"]` (double-quoted) too, so a plain substring
// search without the trailing "{" (and without single quotes) would match
// the comment's mention of each, not the real rule further down the file.
const darkMediaAnchor = '@media (prefers-color-scheme: dark) {';
const explicitDarkAnchor = ":root[data-theme='dark']";

const darkMediaStart = semanticCss.indexOf(darkMediaAnchor);
const explicitDarkStart = semanticCss.indexOf(explicitDarkAnchor);

const lightBlock = semanticCss.slice(0, darkMediaStart);
const darkMediaBlock = semanticCss.slice(darkMediaStart, explicitDarkStart);
const explicitDarkBlock = semanticCss.slice(explicitDarkStart);

describe('semantic token architecture (three-layer cascade)', () => {
  it('locates all three expected cascade blocks', () => {
    expect(darkMediaStart).toBeGreaterThan(0);
    expect(explicitDarkStart).toBeGreaterThan(darkMediaStart);
  });

  it('defines every required semantic colour token exactly once in the light (:root) default', () => {
    for (const token of REQUIRED_SEMANTIC_COLOR_TOKENS) {
      expect(countOccurrences(lightBlock, token), `${token} in :root`).toBe(1);
    }
  });

  it('guards the dark override with `@media (prefers-color-scheme: dark)` + `:root:not([data-theme="light"])`, redefining every token', () => {
    expect(darkMediaBlock).toContain(":root:not([data-theme='light'])");
    for (const token of REQUIRED_SEMANTIC_COLOR_TOKENS) {
      expect(countOccurrences(darkMediaBlock, token), `${token} in the OS-dark-preference block`).toBe(1);
    }
  });

  it('redefines every token again under an explicit `:root[data-theme="dark"]`, so an explicit choice wins in both directions', () => {
    for (const token of REQUIRED_SEMANTIC_COLOR_TOKENS) {
      expect(countOccurrences(explicitDarkBlock, token), `${token} in the explicit-dark block`).toBe(1);
    }
  });

  it('resolves every required token to a different value between light and dark', () => {
    for (const token of REQUIRED_SEMANTIC_COLOR_TOKENS) {
      const light = extractValue(lightBlock, token);
      const dark = extractValue(darkMediaBlock, token);
      expect(light, `${token} light value`).toBeTruthy();
      expect(dark, `${token} dark value`).toBeTruthy();
      expect(dark, `${token} should differ between light and dark`).not.toBe(light);
    }
  });

  it('keeps the OS-preference dark block and the explicit-dark block in agreement for every token', () => {
    for (const token of REQUIRED_SEMANTIC_COLOR_TOKENS) {
      const media = extractValue(darkMediaBlock, token);
      const explicit = extractValue(explicitDarkBlock, token);
      expect(explicit, `${token} explicit-dark should match the OS-dark-preference value`).toBe(media);
    }
  });

  it('never declares a literal hex or rgb()/hsl() colour outside foundation.css - semantic.css only references palette custom properties', () => {
    // Strip comments first: semantic.css documents each token's *resolved*
    // hex value in a trailing comment for reviewers (e.g. "/* #F7F7F9 */"),
    // which is not a declaration and is not what this check cares about.
    const withoutComments = semanticCss.replace(/\/\*[\s\S]*?\*\//g, '');
    expect(withoutComments).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
    expect(withoutComments).not.toMatch(/\b(rgb|rgba|hsl|hsla)\(/);
  });
});

describe('foundation tokens (raw layer)', () => {
  it('is the only layer carrying literal palette hex values', () => {
    expect(foundationCss).toMatch(/#[0-9a-fA-F]{6}\b/);
  });

  it('never uses pure black or pure white for a palette swatch', () => {
    expect(foundationCss.toLowerCase()).not.toContain('#000000');
    expect(foundationCss.toLowerCase()).not.toContain('#ffffff');
  });
});
