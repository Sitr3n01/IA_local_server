import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import js from '@eslint/js';
import { ESLint } from 'eslint';
import globals from 'globals';

const root = new URL('../../', import.meta.url);
const eslint = new ESLint({
  cwd: fileURLToPath(root),
  overrideConfigFile: true,
  overrideConfig: [{
    files: ['**/*.js'],
    languageOptions: { ecmaVersion: 2022, sourceType: 'script', globals: globals.browser },
    rules: {
      ...js.configs.recommended.rules,
      'no-unused-vars': ['error', { args: 'after-used', caughtErrors: 'none' }],
    },
  }],
});
const results = [];
for (const name of ['app.js', 'theme.js']) {
  const file = new URL(`internal/monitor/web/assets/${name}`, root);
  results.push(...await eslint.lintText(readFileSync(file, 'utf8'), { filePath: fileURLToPath(file) }));
}
const formatter = await eslint.loadFormatter('stylish');
const output = formatter.format(results);
if (output) process.stdout.write(output);
if (results.some((result) => result.errorCount > 0 || result.warningCount > 0)) process.exitCode = 1;
