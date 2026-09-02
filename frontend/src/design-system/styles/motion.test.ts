/// <reference types="node" />
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

/**
 * `prefers-reduced-motion` is implemented once, centrally, in motion.css -
 * no per-component media query. jsdom does not evaluate `@media` features
 * for style computation, so (as with tokens.test.ts) this reads the source
 * directly and asserts the rule actually collapses every animation and
 * transition, universally, rather than trusting it by inspection alone.
 *
 * Resolved off `process.cwd()` rather than `import.meta.url` - see the doc
 * comment in ../tokens/tokens.test.ts for why.
 */
const motionCss = readFileSync(resolve(process.cwd(), 'src/design-system/styles/motion.css'), 'utf8');
const mainTsx = readFileSync(resolve(process.cwd(), 'src/main.tsx'), 'utf8');

describe('prefers-reduced-motion is honoured centrally', () => {
  it('guards a universal-selector rule with the reduce media feature', () => {
    expect(motionCss).toMatch(/@media\s*\(prefers-reduced-motion:\s*reduce\)/);
  });

  it('forces both animation-duration and transition-duration to (near-)zero, with !important so it always wins', () => {
    const mediaBlockMatch = /@media\s*\(prefers-reduced-motion:\s*reduce\)\s*\{([\s\S]*)\}\s*$/.exec(motionCss);
    expect(mediaBlockMatch).not.toBeNull();
    const body = mediaBlockMatch![1] ?? '';

    expect(body).toMatch(/\*\s*,[\s\S]*\*::before[\s\S]*\*::after/);
    expect(body).toMatch(/animation-duration:\s*0(\.\d+)?m?s\s*!important/);
    expect(body).toMatch(/transition-duration:\s*0(\.\d+)?m?s\s*!important/);
  });

  it('is imported once, globally, from main.tsx rather than per component', () => {
    expect(mainTsx).toContain('./design-system/styles/motion.css');
  });
});
