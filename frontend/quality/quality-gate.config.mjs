/**
 * Frontend quality-gate policy.
 *
 * Scoped to `frontend/` on purpose. This repository keeps the Go control plane
 * and the operator console apart at every layer - separate trees, separate CI
 * jobs (`go`/`race`/`validate` on one side, `frontend` on the other), separate
 * toolchains - and the gate follows that seam rather than cutting across it.
 * Nothing here reads or judges a Go file, and the Go jobs stay unaware of it.
 *
 * The policy is ratchet-first. Absolute floors and ceilings are opt-in; what
 * blocks by default is *getting worse than the recorded baseline*. That is the
 * only rule that works on a tree nine sprints deep: it does not demand the
 * project already be perfect, it demands no change makes it worse.
 */
export default {
  coverage: {
    enabled: true,
    // Ratchet: any drop against baseline blocks, independent of the floors.
    allowDecrease: false,
    // A small tolerance, in percentage points, so a one-line refactor that
    // moves a covered line between files cannot fail the build on noise.
    tolerance: 0.5,
    minimums: {
      // Enabled as a *warning* rather than blocking: the ratchet is what
      // holds the line. A floor that blocks would only ever fire on a change
      // that already failed the ratchet, and would fire misleadingly on a
      // legitimate deletion of well-covered code.
      enabled: true,
      severity: 'warning',
      lines: 85,
      statements: 85,
      functions: 85,
      branches: 80,
    },
  },

  duplication: {
    enabled: true,
    mode: 'ratchet',
    allowIncrease: false,
    tolerance: 0.25,
    maximum: {
      enabled: true,
      severity: 'warning',
      percentage: 4.0,
    },
  },

  lint: {
    enabled: true,
    // ESLint and Stylelint both currently report zero. Any error at all is a
    // regression from that, and the codebase's own rules (the transport
    // boundary, the HTML-sink ban) are expressed as ESLint errors - so a new
    // error is frequently a security-relevant one.
    allowNewErrors: false,
    allowNewWarnings: false,
    warningIncreaseSeverity: 'warning',
  },

  fileSize: {
    enabled: true,
    // Applies to source, not tests: a long test file is usually a thorough
    // one, and capping it pushes people to write fewer cases.
    maxLinesNewFile: 300,
    // Existing files over the cap are grandfathered and must not grow.
    allowExistingToGrow: false,
    ignore: ['**/*.test.ts', '**/*.test.tsx'],
  },

  complexity: {
    enabled: true,
    // Cyclomatic complexity per function. The current worst is TextField at
    // 22; the baseline records that and the ratchet stops it climbing.
    max: 10,
    maxLinesPerFunction: 80,
    blockOnRegression: true,
  },

  audit: {
    enabled: true,
    blockOn: ['critical'],
    warnOn: ['high', 'moderate'],
  },
};
