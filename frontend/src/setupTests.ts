/**
 * Vitest global test setup (wired via vite.config.ts's `test.setupFiles`).
 *
 * `@testing-library/react` only auto-registers its `afterEach(cleanup)`
 * hook when it detects Vitest's *global* test APIs (`test.globals: true`),
 * which this project deliberately doesn't enable elsewhere - so without
 * this file, `render()` output from one test stays mounted into the next
 * test in the same file, and a second `render()` of the same component
 * (e.g. two Tooltips, two Dividers) makes `getByRole` fail with "found
 * multiple elements" for reasons that have nothing to do with the
 * component under test.
 */
import { afterEach } from 'vitest';
import { cleanup } from '@testing-library/react';

afterEach(() => {
  cleanup();
});
