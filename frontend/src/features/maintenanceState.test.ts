import { describe, expect, it } from 'vitest';
import { MAINTENANCE_LABELS, maintenanceLabel } from './maintenanceState';

// The defect this file exists to prevent from recurring: Overview and
// Activity each carried their own maintenance vocabulary, and the `maintenance`
// state drifted to "Maintenance" on one screen and "Drained" on the other -
// one provider state under two names, on two screens an operator moves
// between. Both now read from this module, and these pin that it stays the
// single source.
describe('maintenance vocabulary', () => {
  it('names every state cia-edge can report', () => {
    // The three states internal/edge/gate.go's maintenanceSnapshot produces.
    expect(Object.keys(MAINTENANCE_LABELS).sort()).toEqual(['draining', 'maintenance', 'running']);
  });

  it('labels the drained state "Drained", not "Maintenance"', () => {
    // The backend constant is `maintenance`, but that names a mode and tells
    // an operator nothing about what happened. "Drained" names the condition
    // the provider is in, and matches the control and counters that produce it.
    expect(maintenanceLabel('maintenance')).toBe('Drained');
  });

  it('gives every state exactly one label', () => {
    const labels = Object.values(MAINTENANCE_LABELS);
    expect(new Set(labels).size).toBe(labels.length);
  });

  it('falls back to the provider raw value for a state it does not recognise', () => {
    // Deliberately not "Unknown": an operator faced with an unrecognised
    // state is better served seeing exactly what the provider called it -
    // searchable, quotable in a bug report - than a smoothed-over word that
    // discards the only identifying detail.
    expect(maintenanceLabel('some-future-state')).toBe('some-future-state');
    expect(maintenanceLabel('')).toBe('');
  });
});
