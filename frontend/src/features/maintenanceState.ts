/**
 * The canonical short label for each `status.maintenance.state` the provider
 * reports, shared by every screen that shows it.
 *
 * This module exists because the same state was being named two different
 * things at once. Overview called `maintenance` "Maintenance" (Sprint 3);
 * Activity, which gained the drain and resume controls in Sprint 7, called
 * the same state "Drained". An operator moving between the two screens saw
 * one provider state under two names, with no way to know they were the same
 * thing - a hardening defect found in Sprint 8 by auditing for exactly this
 * kind of quiet divergence.
 *
 * Only the LABEL is shared. Each screen keeps its own description, because
 * the surrounding context genuinely differs: Overview is a one-line summary
 * of provider health, while Activity sits beside the counters that show
 * whether a drain has actually finished. Sharing the sentence too would make
 * one of the two read as a non-sequitur.
 *
 * `maintenance` is labelled "Drained" rather than "Maintenance". The backend
 * constant is `maintenance`, but that word names a *mode* and tells an
 * operator nothing about what happened; "Drained" names the condition the
 * provider is actually in - the drain ran and completed - and matches the
 * control and counters that produce it.
 *
 * `Record<string, ...>` rather than a closed union: the state string is
 * whatever cia-edge sends, and `StatusSchema` types it as `z.string()`
 * precisely so an unrecognized future state degrades rather than throwing.
 * Callers must handle a miss - see `maintenanceLabel`.
 */
export const MAINTENANCE_LABELS: Record<string, string> = {
  running: 'Running',
  draining: 'Draining',
  maintenance: 'Drained',
};

/**
 * The label for a state, falling back to the raw value the provider sent.
 *
 * Showing the unknown state verbatim is deliberate: an operator faced with a
 * state this console does not recognize is better served by seeing exactly
 * what the provider called it - which they can search for, or quote in a bug
 * report - than by a smoothed-over "Unknown" that discards the one piece of
 * information that would identify it.
 */
export function maintenanceLabel(state: string): string {
  return MAINTENANCE_LABELS[state] ?? state;
}
