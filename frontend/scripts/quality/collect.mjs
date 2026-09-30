#!/usr/bin/env node
/**
 * Collects every deterministic quality metric for the operator console into
 * one shape, and writes it as both JSON (for the gate) and Markdown (for a
 * human).
 *
 * It is a *reader*, not a runner. Each producer writes its own artifact -
 * `npm run test:coverage:ci`, `npm run duplication:ci`, `npm run lint:report`,
 * `npm run complexity:ci`, `npm run audit:report` - and this reads what is on
 * disk. Keeping the two apart means a missing artifact is reported as missing
 * rather than silently re-measured with different flags than CI used, which is
 * the classic way a gate ends up grading something other than what shipped.
 *
 * A missing artifact is never fatal here: this script's job is to describe the
 * tree, and `check.mjs` decides what that description means.
 */
import { readFileSync, writeFileSync, mkdirSync, existsSync, readdirSync, statSync } from 'node:fs';
import { join, relative, sep } from 'node:path';

export const ROOT = process.cwd();
const REPORTS = join(ROOT, 'reports');

export function readJson(path) {
  try {
    return JSON.parse(readFileSync(path, 'utf8'));
  } catch {
    return undefined;
  }
}

function round(value) {
  return typeof value === 'number' && Number.isFinite(value) ? Math.round(value * 100) / 100 : null;
}

/** Every source file under src/, as repo-relative POSIX paths. */
function sourceFiles() {
  const out = [];
  const walk = (dir) => {
    for (const entry of readdirSync(dir)) {
      const full = join(dir, entry);
      if (statSync(full).isDirectory()) walk(full);
      else if (/\.(ts|tsx|css)$/.test(entry)) out.push(full);
    }
  };
  walk(join(ROOT, 'src'));
  return out.map((f) => relative(ROOT, f).split(sep).join('/'));
}

function collectCoverage() {
  const summary = readJson(join(ROOT, 'coverage', 'coverage-summary.json'));
  if (!summary?.total) return { available: false };
  const t = summary.total;
  return {
    available: true,
    lines: round(t.lines?.pct),
    statements: round(t.statements?.pct),
    functions: round(t.functions?.pct),
    branches: round(t.branches?.pct),
  };
}

function collectDuplication() {
  const report = readJson(join(REPORTS, 'duplication', 'jscpd-report.json'));
  const total = report?.statistics?.total;
  if (!total) return { available: false };
  return {
    available: true,
    percentage: round(total.percentage),
    percentageTokens: round(total.percentageTokens),
    clones: total.clones ?? null,
    duplicatedLines: total.duplicatedLines ?? null,
  };
}

function collectLint() {
  const eslint = readJson(join(REPORTS, 'eslint', 'eslint.json'));
  if (!Array.isArray(eslint)) return { available: false };
  return {
    available: true,
    errors: eslint.reduce((a, f) => a + (f.errorCount ?? 0), 0),
    warnings: eslint.reduce((a, f) => a + (f.warningCount ?? 0), 0),
    filesLinted: eslint.length,
  };
}

function collectComplexity() {
  const report = readJson(join(REPORTS, 'complexity', 'eslint-complexity.json'));
  if (!Array.isArray(report)) return { available: false, mode: 'missing' };
  const messages = report.flatMap((f) =>
    (f.messages ?? []).map((m) => ({
      file: relative(ROOT, f.filePath).split(sep).join('/'),
      line: m.line,
      rule: m.ruleId,
      message: m.message,
    })),
  );
  const parseNumber = (text) => {
    const match = /complexity of (\d+)/.exec(text ?? '');
    return match ? Number(match[1]) : 0;
  };
  const complexity = messages.filter((m) => m.rule === 'complexity');
  return {
    available: true,
    // "AST" rather than a heuristic: these come from ESLint's own analysis.
    mode: 'ast',
    violations: messages.length,
    complexityViolations: complexity.length,
    lengthViolations: messages.filter((m) => m.rule === 'max-lines-per-function').length,
    worst: complexity.reduce((max, m) => Math.max(max, parseNumber(m.message)), 0),
    detail: messages.slice(0, 25),
  };
}

function collectAudit() {
  const audit = readJson(join(REPORTS, 'audit', 'npm-audit.json'));
  const v = audit?.metadata?.vulnerabilities;
  if (!v) return { available: false };
  return {
    available: true,
    critical: v.critical ?? 0,
    high: v.high ?? 0,
    moderate: v.moderate ?? 0,
    low: v.low ?? 0,
  };
}

function collectFileSizes(config) {
  const ignore = config.fileSize?.ignore ?? [];
  const isIgnored = (file) =>
    ignore.some((pattern) => new RegExp(`^${pattern.replace(/\./g, '\\.').replace(/\*\*/g, '.*').replace(/\*/g, '[^/]*')}$`).test(file));

  const files = sourceFiles()
    .filter((file) => !isIgnored(file))
    .map((file) => ({ file, lines: readFileSync(join(ROOT, file), 'utf8').split('\n').length }))
    .sort((a, b) => b.lines - a.lines);

  const cap = config.fileSize?.maxLinesNewFile ?? 300;
  return {
    available: true,
    cap,
    total: files.length,
    overCap: files.filter((f) => f.lines > cap).length,
    largest: files.slice(0, 10),
    // The whole map, so `check.mjs` can tell "this existing oversized file
    // grew" from "a new file arrived already oversized".
    byFile: Object.fromEntries(files.map((f) => [f.file, f.lines])),
  };
}

export function loadConfig() {
  return import(new URL('../../quality/quality-gate.config.mjs', import.meta.url)).then((m) => m.default);
}

export async function collect() {
  const config = await loadConfig();
  return {
    // No timestamp. It would make every report differ from every other report
    // and turn a "did anything change" diff into noise.
    scope: 'frontend',
    coverage: collectCoverage(),
    duplication: collectDuplication(),
    lint: collectLint(),
    complexity: collectComplexity(),
    audit: collectAudit(),
    fileSize: collectFileSizes(config),
  };
}

function renderMarkdown(metrics) {
  const row = (name, value, note = '') => `| ${name} | ${value} | ${note} |`;
  const missing = '_not collected_';
  const c = metrics.coverage;
  const d = metrics.duplication;
  const l = metrics.lint;
  const x = metrics.complexity;
  const a = metrics.audit;
  const f = metrics.fileSize;

  return [
    '# Frontend quality report',
    '',
    'Scope: `frontend/` only. The Go control plane is graded by its own CI jobs.',
    '',
    '| Metric | Value | Note |',
    '| --- | --- | --- |',
    row('Coverage — lines', c.available ? `${c.lines}%` : missing),
    row('Coverage — statements', c.available ? `${c.statements}%` : missing),
    row('Coverage — functions', c.available ? `${c.functions}%` : missing),
    row('Coverage — branches', c.available ? `${c.branches}%` : missing),
    row('Duplication — lines', d.available ? `${d.percentage}%` : missing, d.available ? `${d.clones} clones` : ''),
    row('Duplication — tokens', d.available ? `${d.percentageTokens}%` : missing),
    row('Lint errors', l.available ? l.errors : missing, l.available ? `${l.filesLinted} files` : ''),
    row('Lint warnings', l.available ? l.warnings : missing),
    row('Complexity violations', x.available ? x.complexityViolations : missing, x.available ? `worst: ${x.worst}` : ''),
    row('Over-long functions', x.available ? x.lengthViolations : missing),
    row('Files over cap', `${f.overCap} of ${f.total}`, `cap ${f.cap} lines, tests excluded`),
    row('Vulnerabilities', a.available ? `${a.critical} critical / ${a.high} high / ${a.moderate} moderate` : missing),
    '',
  ].join('\n');
}

async function main() {
  const metrics = await collect();
  mkdirSync(REPORTS, { recursive: true });
  writeFileSync(join(REPORTS, 'quality-gate.json'), `${JSON.stringify(metrics, null, 2)}\n`);
  writeFileSync(join(REPORTS, 'quality-gate.md'), renderMarkdown(metrics));
  console.log(renderMarkdown(metrics));

  const absent = Object.entries(metrics)
    .filter(([, value]) => value && typeof value === 'object' && value.available === false)
    .map(([name]) => name);
  if (absent.length > 0) {
    console.warn(`\nquality: ${absent.length} metric(s) not collected: ${absent.join(', ')}`);
    console.warn('Run `npm run quality:collect` to produce every artifact first.');
  }
}

if (import.meta.url === `file://${process.argv[1]}` || process.argv[1]?.endsWith('collect.mjs')) {
  await main();
}

export { existsSync };
