#!/usr/bin/env node
/**
 * Writes `reports/audit/npm-audit.json`.
 *
 * In Node rather than `npm audit --json > file` because that shell redirection
 * behaves differently under cmd.exe and sh - this project's CI runs the
 * frontend on ubuntu while every developer here is on Windows - and because
 * `npm audit` exits non-zero the moment it finds anything, which would abort
 * the collect chain before the gate ever got to decide whether those findings
 * matter. The advisory data is on stdout either way; the exit code is the
 * gate's business, not the collector's.
 */
import { spawnSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const out = join(process.cwd(), 'reports', 'audit');
mkdirSync(out, { recursive: true });

// One literal command string, no `args` array. Node's DEP0190 warns when
// arguments are passed alongside `shell: true`, because the shell concatenates
// rather than escapes them; with nothing to concatenate there is nothing to
// escape. A shell is needed at all only because `npm` is `npm.cmd` on Windows
// and bare `npm.cmd` does not resolve under Git Bash, which is what every
// developer on this project runs.
//
// `--omit=dev` scopes the gate to the dependencies that ship in the bundle.
// CI reports the development tooling separately, as a non-blocking warning.
const result = spawnSync('npm audit --json --omit=dev', {
  encoding: 'utf8',
  shell: true,
  maxBuffer: 32 * 1024 * 1024,
});

const raw = result.stdout?.trim();
if (!raw) {
  console.error('audit: npm produced no output');
  console.error(result.stderr ?? '(no stderr)');
  process.exit(1);
}

let parsed;
try {
  parsed = JSON.parse(raw);
} catch (error) {
  console.error(`audit: npm output was not JSON: ${error.message}`);
  process.exit(1);
}

writeFileSync(join(out, 'npm-audit.json'), `${JSON.stringify(parsed, null, 2)}\n`);

const v = parsed.metadata?.vulnerabilities ?? {};
console.log(
  `audit: ${v.critical ?? 0} critical, ${v.high ?? 0} high, ${v.moderate ?? 0} moderate, ${v.low ?? 0} low`,
);
