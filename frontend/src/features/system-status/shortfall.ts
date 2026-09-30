/**
 * Shortfall arithmetic for a single admission budget (commit, physical, or
 * VRAM). Every capacity figure the backend sends is a GiB measurement or
 * `null` ("not measured" - see src/api/schemas/status.ts's doc comment), so
 * this module never assumes a number is present; `computable` is `false`
 * whenever any operand is missing, and callers must check it before trusting
 * `totalNeededGib`/`shortfallGib`.
 *
 * Rounding happens at 2 decimal places at every step (matching the
 * precision the backend itself reports headroom/requirement/reserve at) so
 * floating-point drift from summing two already-rounded GiB figures never
 * surfaces as a value like `15.700000000000001` in the rendered sentence.
 */

export interface BudgetFigures {
  /** Human label for the budget this arithmetic is about, e.g. "physical memory". */
  label: string;
  availableGib: number | null;
  requiredGib: number | null;
  reserveGib: number | null;
}

export interface ShortfallResult {
  label: string;
  availableGib: number | null;
  requiredGib: number | null;
  reserveGib: number | null;
  /** `requiredGib + reserveGib`, rounded to 2dp. `null` unless every operand is measured. */
  totalNeededGib: number | null;
  /** `totalNeededGib - availableGib`, rounded to 2dp. `null` unless every operand is measured. */
  shortfallGib: number | null;
  /** `true` only when all three inputs are non-null numbers. */
  computable: boolean;
}

function round2(value: number): number {
  return Math.round(value * 100) / 100;
}

export function computeShortfall(figures: BudgetFigures): ShortfallResult {
  const { label, availableGib, requiredGib, reserveGib } = figures;
  const computable = availableGib !== null && requiredGib !== null && reserveGib !== null;

  if (!computable) {
    return {
      label,
      availableGib,
      requiredGib,
      reserveGib,
      totalNeededGib: null,
      shortfallGib: null,
      computable: false,
    };
  }

  const totalNeededGib = round2(requiredGib + reserveGib);
  const shortfallGib = round2(totalNeededGib - availableGib);

  return { label, availableGib, requiredGib, reserveGib, totalNeededGib, shortfallGib, computable: true };
}

/** Renders a GiB figure without a spurious trailing `.00` (`2` -> "2 GiB", `13.7` -> "13.7 GiB"). */
export function formatGib(value: number): string {
  const rounded = round2(value);
  const text = Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(2).replace(/0+$/, '').replace(/\.$/, '');
  return `${text} GiB`;
}

/**
 * Null-safe wrapper around `formatGib` for callers that render one figure in
 * isolation rather than a full sentence (e.g. `ResourceMeter`, which must
 * show "not measured" for a null operand instead of an empty/zero-looking
 * bar - see the Models sprint brief). Mirrors
 * `src/features/system-status/format.ts`'s `formatMib` convention.
 */
export function formatGibOrUnmeasured(value: number | null): string {
  return value === null ? 'not measured' : formatGib(value);
}

/**
 * Renders the operator-facing sentence: which budget failed, what is
 * available, what is required, what the reserve is, and the shortfall - in
 * that order, per the Sprint 3 brief. Falls back to a plain statement
 * (no numbers) when a measurement is missing rather than printing `null`
 * or `NaN` anywhere.
 */
export function formatShortfallSentence(result: ShortfallResult): string {
  if (!result.computable) {
    return `Not enough ${result.label} to load this model, but the measurement needed to show the exact numbers is not available.`;
  }

  const { label, availableGib, requiredGib, reserveGib, totalNeededGib, shortfallGib } = result;
  const shortfallPhrase =
    shortfallGib !== null && shortfallGib > 0
      ? `short by ${formatGib(shortfallGib)}`
      : 'the reported figures do not show a shortfall';

  return (
    `Not enough ${label}: ${formatGib(availableGib!)} available, but this model needs ` +
    `${formatGib(requiredGib!)} plus a ${formatGib(reserveGib!)} safety reserve ` +
    `(${formatGib(totalNeededGib!)} total) — ${shortfallPhrase}.`
  );
}
