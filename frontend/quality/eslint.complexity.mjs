/**
 * A second, narrow ESLint configuration used only by the quality gate to
 * measure complexity. Kept apart from `eslint.config.js` deliberately.
 *
 * `eslint.config.js` is the *build gate*: everything in it is an error and
 * must be zero, because those rules encode real invariants (the transport
 * boundary, the ban on every HTML sink). Complexity is not that kind of rule -
 * it is a trend to hold flat, not a line nobody may cross. Mixing the two
 * would either turn six existing functions into build errors overnight or
 * force the complexity threshold up to whatever the worst function already is,
 * which measures nothing.
 *
 * So it lives here, is reported rather than enforced, and the ratchet in
 * `check.mjs` is what stops the numbers climbing.
 */
import tseslint from 'typescript-eslint';

export default tseslint.config({
  files: ['src/**/*.{ts,tsx}'],
  ignores: ['src/**/*.test.{ts,tsx}', 'src/dev/**'],
  languageOptions: {
    parser: tseslint.parser,
    parserOptions: { ecmaFeatures: { jsx: true } },
  },
  rules: {
    complexity: ['warn', 10],
    'max-depth': ['warn', 4],
    'max-lines-per-function': ['warn', { max: 80, skipBlankLines: true, skipComments: true }],
  },
});
