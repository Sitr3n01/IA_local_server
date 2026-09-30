// @ts-check
import js from '@eslint/js';
import globals from 'globals';
import reactHooks from 'eslint-plugin-react-hooks';
import tseslint from 'typescript-eslint';

// Sprint 1's architecture rule: the console must not be able to reach the
// network except through the transport boundary in src/api/transport/. Two
// ESLint rules enforce the two halves of that boundary:
//
//   1. `no-restricted-globals` bans the bare `fetch` global everywhere except
//      inside src/api/transport/ itself - that's the only place allowed to
//      make an HTTP call at all (and only in the dev-only transport; the
//      production bridge transport never calls fetch).
//   2. `no-restricted-imports` bans importing anything from
//      src/api/transport/* from outside src/api/ - only api/queries and
//      api/commands (both under src/api/) may reach into the transport
//      layer. Pages and features go through them instead.
const noFetchGlobal = [
  'error',
  {
    name: 'fetch',
    message:
      'Call fetch only from src/api/transport/*. Everything else must go through src/api/queries or src/api/commands.',
  },
];

const noTransportImport = [
  'error',
  {
    patterns: [
      {
        group: ['**/api/transport/*', '**/api/transport'],
        message:
          'Import the transport layer only from within src/api/ (queries/commands). Pages and features must call api/queries or api/commands instead.',
      },
    ],
  },
];

// ADR 0018 control 4: no HTML injection path exists, and the build proves it.
// The console renders operator-supplied and backend-supplied strings - model
// display names, capacity reasons, event paths, manifest fields. None of them
// may reach an HTML sink, because the native bridge behind this page performs
// privileged model-lifecycle operations and script execution in this document
// is the thing that control exists to prevent.
//
// Expressed as AST selectors rather than eslint-plugin-react's `no-danger` so
// the ban costs no dependency and also covers the plain-DOM sinks a React
// rule would miss entirely.
const noHtmlSink = [
  'error',
  {
    selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']",
    message:
      'ADR 0018 control 4: dangerouslySetInnerHTML is forbidden. Render text as text; if markup is genuinely required, it needs an ADR and a sanitiser allowlist.',
  },
  {
    selector:
      "AssignmentExpression > MemberExpression[property.name=/^(inner|outer)HTML$/]",
    message:
      'ADR 0018 control 4: assigning innerHTML/outerHTML is forbidden. Use textContent, or React.',
  },
  {
    selector: "CallExpression > MemberExpression[property.name='insertAdjacentHTML']",
    message:
      'ADR 0018 control 4: insertAdjacentHTML is forbidden. Use textContent, or React.',
  },
];

export default tseslint.config(
  { ignores: ['dist', 'node_modules', 'coverage'] },

  {
    files: ['**/*.{ts,tsx}'],
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    languageOptions: {
      ecmaVersion: 2022,
      sourceType: 'module',
      globals: globals.browser,
    },
    plugins: {
      'react-hooks': reactHooks,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      'no-restricted-globals': noFetchGlobal,
      'no-restricted-syntax': noHtmlSink,
      // Leading-underscore names are the project's convention for
      // intentionally unused bindings - a transport stub's unused params, a
      // destructured field dropped only to omit it from the rest, or (per
      // Transport.ts) a type parameter kept purely as a call-site
      // documentation hint. `@typescript-eslint/no-unused-vars` extends
      // TypeScript's own unused-binding check (which already honours this
      // convention) to type parameters too, so mirror it here explicitly.
      '@typescript-eslint/no-unused-vars': [
        'error',
        {
          args: 'after-used',
          argsIgnorePattern: '^_',
          varsIgnorePattern: '^_',
          caughtErrorsIgnorePattern: '^_',
        },
      ],
    },
  },

  // Node-context config/script files run outside the browser and outside the
  // transport boundary rule entirely - they are build tooling, not console
  // code that ships to the operator.
  {
    files: ['vite.config.ts', 'scripts/**/*.mjs'],
    languageOptions: {
      globals: globals.node,
    },
    rules: {
      'no-restricted-globals': 'off',
    },
  },

  // The transport layer itself is exempt from the fetch ban (that's its job)
  // but is exactly where the import ban does NOT apply reflexively - other
  // files importing *it* are what's restricted, handled by the block below.
  {
    files: ['src/api/transport/**/*.{ts,tsx}'],
    rules: {
      'no-restricted-globals': 'off',
    },
  },

  // Forbid importing the transport layer from everywhere except src/api/
  // itself (api/queries, api/commands, and the transport layer's own
  // internal files).
  {
    files: ['**/*.{ts,tsx}'],
    ignores: ['src/api/**'],
    rules: {
      'no-restricted-imports': noTransportImport,
    },
  },
);
