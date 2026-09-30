import { formatGibOrUnmeasured } from '../system-status/shortfall';

/**
 * Small presentation formatters specific to the Models screen. Kept in
 * `features/` (not inlined in the page), mirroring
 * `src/features/system-status/format.ts`.
 */

const tokenFormatter = new Intl.NumberFormat('en-US');

/** `131072` -> `"131,072 tokens"`. `context_tokens` is always a measured integer (never null on the wire), so no "not measured" branch is needed here. */
export function formatContextTokens(tokens: number): string {
  return `${tokenFormatter.format(tokens)} tokens`;
}

/**
 * `reclaimable_commit_gib`/`reclaimable_physical_gib` are `.nullable().optional()`
 * on `Capacity` (see src/api/schemas/status.ts) - two distinct absent states
 * that must render differently:
 *
 *  - `undefined` (the key itself is missing): this backend version hasn't
 *    shipped the field yet. Returns `null` here, meaning "render no row at
 *    all" - showing "not measured" for a field the payload never claims to
 *    report would overstate what the provider actually said.
 *  - `null` (the key is present, but no measurement was taken): still
 *    renders, as "not measured" - the same null discipline as every other
 *    capacity figure in this app.
 *  - a number: renders as a GiB figure via `formatGibOrUnmeasured`.
 */
export function formatReclaimableGib(value: number | null | undefined): string | null {
  if (value === undefined) {
    return null;
  }
  return formatGibOrUnmeasured(value);
}
