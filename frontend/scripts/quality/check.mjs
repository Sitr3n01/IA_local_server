#!/usr/bin/env node
/**
 * Compares the collected metrics against `quality/baseline.json` and decides
 * whether the change is allowed to land.
 *
 * Exit code 1 means blocked. Exit code 0 with warnings means "worth a human
 * look, not a stop". Nothing here can be satisfied by editing the baseline in
 * the same change - that is a review rule, not something a script can enforce,
 * and it is written into CLAUDE.md and the workflow for that reason.
 */
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { collect, loadConfig, ROOT, readJson } from './collect.mjs';

const BASELINE = join(ROOT, 'quality', 'baseline.json');

const blocking = [];
const warnings = [];
const notes = [];

const block = (message) => blocking.push(message);
const warn = (message) => warnings.push(message);

function compareCoverage(config, current, base) {
  if (!config.coverage?.enabled) return;
  if (!current.available) return warn('coverage: no report found - run `npm run test:coverage:ci`');
  const keys = ['lines', 'statements', 'functions', 'branches'];

  if (!base) {
    warn('coverage: no baseline recorded yet, nothing to ratchet against');
  } else if (config.coverage.allowDecrease === false) {
    const tolerance = config.coverage.tolerance ?? 0;
    for (const key of keys) {
      const now = current[key];
      const then = base[key];
      if (typeof now !== 'number' || typeof then !== 'number') continue;
      if (now < then - tolerance) {
        block(`coverage.${key} fell from ${then}% to ${now}% (tolerance ${tolerance}pp)`);
      }
    }
  }

  const min = config.coverage.minimums;
  if (min?.enabled) {
    for (const key of keys) {
      const floor = min[key];
      if (typeof floor !== 'number' || typeof current[key] !== 'number') continue;
      if (current[key] < floor) {
        const message = `coverage.${key} is ${current[key]}%, below the ${floor}% floor`;
        if (min.severity === 'blocking') block(message);
        else warn(message);
      }
    }
  }
}

function compareDuplication(config, current, base) {
  if (!config.duplication?.enabled) return;
  if (!current.available) return warn('duplication: no report found - run `npm run duplication:ci`');

  if (!base) {
    warn('duplication: no baseline recorded yet, nothing to ratchet against');
  } else if (config.duplication.allowIncrease === false) {
    const tolerance = config.duplication.tolerance ?? 0;
    if (current.percentage > base.percentage + tolerance) {
      block(`duplication rose from ${base.percentage}% to ${current.percentage}% (tolerance ${tolerance}pp)`);
    }
  }

  const max = config.duplication.maximum;
  if (max?.enabled && current.percentage > max.percentage) {
    const message = `duplication is ${current.percentage}%, above the ${max.percentage}% ceiling`;
    if (max.severity === 'blocking') block(message);
    else warn(message);
  }
}

function compareLint(config, current, base) {
  if (!config.lint?.enabled) return;
  if (!current.available) return warn('lint: no report found - run `npm run lint:report`');

  if (!base) return warn('lint: no baseline recorded yet');

  if (config.lint.allowNewErrors === false && current.errors > base.errors) {
    block(`lint errors rose from ${base.errors} to ${current.errors}`);
  }
  if (config.lint.allowNewWarnings === false && current.warnings > base.warnings) {
    const message = `lint warnings rose from ${base.warnings} to ${current.warnings}`;
    if (config.lint.warningIncreaseSeverity === 'blocking') block(message);
    else warn(message);
  }
}

function compareComplexity(config, current, base) {
  if (!config.complexity?.enabled) return;
  if (!current.available) return warn('complexity: no report found - run `npm run complexity:ci`');
  if (current.mode !== 'ast') warn(`complexity: running in ${current.mode} mode, not AST analysis`);

  if (!base) return warn('complexity: no baseline recorded yet');

  if (current.violations > base.violations) {
    const message = `complexity violations rose from ${base.violations} to ${current.violations}`;
    if (config.complexity.blockOnRegression) block(message);
    else warn(message);
  }
  if (current.worst > base.worst) {
    const message = `worst function complexity rose from ${base.worst} to ${current.worst}`;
    if (config.complexity.blockOnRegression) block(message);
    else warn(message);
  }
}

function compareAudit(config, current) {
  if (!config.audit?.enabled) return;
  if (!current.available) return warn('audit: no report found - run `npm run audit:report`');

  for (const level of config.audit.blockOn ?? []) {
    if ((current[level] ?? 0) > 0) block(`${current[level]} ${level} vulnerability/vulnerabilities`);
  }
  for (const level of config.audit.warnOn ?? []) {
    if ((current[level] ?? 0) > 0) warn(`${current[level]} ${level} vulnerability/vulnerabilities`);
  }
}

function compareFileSize(config, current, base) {
  if (!config.fileSize?.enabled) return;
  const cap = config.fileSize.maxLinesNewFile;
  const baseFiles = base?.byFile ?? {};

  for (const [file, lines] of Object.entries(current.byFile)) {
    const before = baseFiles[file];
    if (before === undefined) {
      // A file the baseline has never seen.
      if (lines > cap) block(`new file ${file} is ${lines} lines, over the ${cap}-line cap`);
    } else if (before > cap && lines > before && config.fileSize.allowExistingToGrow === false) {
      block(`already-oversized ${file} grew from ${before} to ${lines} lines`);
    }
  }

  if (!base) warn('fileSize: no baseline recorded yet, only the new-file cap applies');
  else notes.push(`${current.overCap} file(s) over the ${cap}-line cap, none grown`);
}

async function main() {
  const config = await loadConfig();
  const current = await collect();
  const baseline = readJson(BASELINE);
  const base = baseline?.metrics;

  if (!base) warn('no baseline found at quality/baseline.json - run `npm run quality:baseline` on main');

  compareCoverage(config, current.coverage, base?.coverage);
  compareDuplication(config, current.duplication, base?.duplication);
  compareLint(config, current.lint, base?.lint);
  compareComplexity(config, current.complexity, base?.complexity);
  compareAudit(config, current.audit);
  compareFileSize(config, current.fileSize, base?.fileSize);

  const verdict = blocking.length > 0 ? 'BLOCKED' : warnings.length > 0 ? 'PASS WITH WARNINGS' : 'PASS';
  const lines = [`quality gate (frontend): ${verdict}`, ''];
  for (const item of blocking) lines.push(`  BLOCKING  ${item}`);
  for (const item of warnings) lines.push(`  warning   ${item}`);
  for (const item of notes) lines.push(`  note      ${item}`);

  const output = lines.join('\n');
  console.log(output);

  mkdirSync(join(ROOT, 'reports'), { recursive: true });
  writeFileSync(
    join(ROOT, 'reports', 'quality-check.json'),
    `${JSON.stringify({ verdict, blocking, warnings, notes, metrics: current }, null, 2)}\n`,
  );

  if (blocking.length > 0) {
    console.error('\nThe baseline is not the fix. Raise the metric, or justify the change to a human reviewer.');
    process.exit(1);
  }
}

await main();
