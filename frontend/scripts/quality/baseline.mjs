#!/usr/bin/env node
/**
 * Rewrites `quality/baseline.json` from the current metrics.
 *
 * This is the one script in the gate that can make a failing check pass, so it
 * is deliberately not wired into CI and prints what it is about to accept. Run
 * it on `main` after a change has already been reviewed and merged - never on
 * a feature branch to get that branch green, which is the exact move the
 * ratchet exists to prevent.
 */
import { writeFileSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { collect, ROOT, readJson } from './collect.mjs';

const BASELINE = join(ROOT, 'quality', 'baseline.json');

const metrics = await collect();
const previous = readJson(BASELINE)?.metrics;

const unavailable = Object.entries(metrics)
  .filter(([, value]) => value && typeof value === 'object' && value.available === false)
  .map(([name]) => name);

if (unavailable.length > 0) {
  console.error(`refusing to write a baseline with missing metrics: ${unavailable.join(', ')}`);
  console.error('Run `npm run quality:collect` first so every metric is real rather than absent.');
  process.exit(1);
}

if (previous) {
  const moved = [];
  if (previous.coverage?.lines !== metrics.coverage.lines)
    moved.push(`coverage.lines ${previous.coverage?.lines} -> ${metrics.coverage.lines}`);
  if (previous.duplication?.percentage !== metrics.duplication.percentage)
    moved.push(`duplication ${previous.duplication?.percentage}% -> ${metrics.duplication.percentage}%`);
  if (previous.lint?.errors !== metrics.lint.errors)
    moved.push(`lint errors ${previous.lint?.errors} -> ${metrics.lint.errors}`);
  if (previous.complexity?.violations !== metrics.complexity.violations)
    moved.push(`complexity violations ${previous.complexity?.violations} -> ${metrics.complexity.violations}`);
  console.log(moved.length > 0 ? `accepting:\n  ${moved.join('\n  ')}` : 'no metric changed');
}

mkdirSync(join(ROOT, 'quality'), { recursive: true });
writeFileSync(BASELINE, `${JSON.stringify({ version: 1, scope: 'frontend', metrics }, null, 2)}\n`);
console.log(`\nwrote ${BASELINE}`);
console.log('Commit this on its own, with a message saying why the numbers moved.');
