import { describe, expect, it } from 'vitest';
import { readCss } from './test-utils/readCss';

/**
 * "Visible :focus-visible styling that is not merely a colour change" is
 * checked here at the CSS-source level: jsdom does not implement the
 * `:focus-visible` heuristic (keyboard vs. pointer interaction) that real
 * browsers use, so simulating focus in a rendered test would not actually
 * exercise the pseudo-class the way a browser does. Reading each
 * interactive primitive's stylesheet and asserting a `:focus-visible` rule
 * exists *and* sets a geometry property (`outline`) - not just `color` or
 * `border-color` - is the reliable way to verify this for every primitive
 * in this repo, in CI, without a real browser.
 */

/** Every `:focus-visible { ... }` (or `:has(...:focus-visible)`) rule body found in the source. */
function focusVisibleRuleBodies(css: string): string[] {
  const bodies: string[] = [];
  const pattern = /:focus-visible[^{]*\{([^}]*)\}/g;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(css)) !== null) {
    const body = match[1];
    if (body !== undefined) bodies.push(body);
  }
  return bodies;
}

describe('focus-visible: every interactive primitive defines a real geometry ring, not just a colour swap', () => {
  // Every stylesheet in the app that styles something an operator can focus.
  // The nav rail and the text field's clear button were unreachable by this
  // test until `readCss` was widened from the primitives directory to `src/`,
  // which is how the rail - the control used most - went uncovered while the
  // suite stayed green.
  it.each([
    ['Button', 'design-system/primitives/Button/Button.css'],
    ['IconButton', 'design-system/primitives/IconButton/IconButton.css'],
    ['TextField', 'design-system/primitives/TextField/TextField.css'],
    ['NavRail', 'app/shell/NavRail.css'],
  ])('%s.css has at least one :focus-visible rule that sets `outline`', (_name, path) => {
    const css = readCss(path);
    const bodies = focusVisibleRuleBodies(css);
    expect(bodies.length, 'expected at least one :focus-visible rule').toBeGreaterThan(0);

    const hasOutlineGeometry = bodies.some(
      (body) => /\boutline\s*:/.test(body) && !/\boutline\s*:\s*none\b/.test(body),
    );
    expect(hasOutlineGeometry, 'expected a :focus-visible rule that sets a real outline, not just none/colour').toBe(
      true,
    );
  });

  it('every :focus-visible ring colour comes from --color-action-primary (a semantic token, never a raw value)', () => {
    for (const path of [
      'design-system/primitives/Button/Button.css',
      'design-system/primitives/IconButton/IconButton.css',
      'design-system/primitives/TextField/TextField.css',
      'app/shell/NavRail.css',
    ]) {
      const css = readCss(path);
      const bodies = focusVisibleRuleBodies(css);
      for (const body of bodies) {
        const outlineDecl = /outline\s*:\s*([^;]+);/.exec(body)?.[1];
        if (!outlineDecl || outlineDecl.trim() === 'none') continue;
        expect(outlineDecl, `${path}: outline should reference a --color-* custom property`).toContain(
          'var(--color-action-primary)',
        );
      }
    }
  });

  it('Surface, Divider, and Skeleton are non-interactive and intentionally define no focus-visible rule', () => {
    for (const path of [
      'design-system/primitives/Surface/Surface.css',
      'design-system/primitives/Divider/Divider.css',
      'design-system/primitives/Skeleton/Skeleton.css',
    ]) {
      const css = readCss(path);
      expect(focusVisibleRuleBodies(css).length, `${path} should have no :focus-visible rule`).toBe(0);
    }
  });
});
