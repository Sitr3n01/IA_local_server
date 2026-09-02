import { describe, expect, it } from 'vitest';
import { computeShortfall, formatShortfallSentence } from './shortfall';

describe('computeShortfall', () => {
  it('computes the exact scenario from the Sprint 3 brief: headroom 8.63, required 13.7, reserve 2', () => {
    const result = computeShortfall({
      label: 'physical memory',
      availableGib: 8.63,
      requiredGib: 13.7,
      reserveGib: 2,
    });

    expect(result.computable).toBe(true);
    expect(result.totalNeededGib).toBe(15.7);
    expect(result.shortfallGib).toBe(7.07);
  });

  it('renders the exact operator-facing sentence for that scenario', () => {
    const result = computeShortfall({
      label: 'physical memory',
      availableGib: 8.63,
      requiredGib: 13.7,
      reserveGib: 2,
    });

    expect(formatShortfallSentence(result)).toBe(
      'Not enough physical memory: 8.63 GiB available, but this model needs 13.7 GiB plus a 2 GiB safety reserve (15.7 GiB total) — short by 7.07 GiB.',
    );
  });

  it('is not computable when any operand is null ("not measured"), never treating null as 0', () => {
    const result = computeShortfall({
      label: 'VRAM',
      availableGib: null,
      requiredGib: 10.8,
      reserveGib: 3,
    });

    expect(result.computable).toBe(false);
    expect(result.totalNeededGib).toBeNull();
    expect(result.shortfallGib).toBeNull();
  });

  it('falls back to a plain, number-free sentence when not computable', () => {
    const result = computeShortfall({ label: 'committed memory', availableGib: null, requiredGib: null, reserveGib: null });
    const sentence = formatShortfallSentence(result);
    expect(sentence).not.toMatch(/null|NaN/);
    expect(sentence).toContain('committed memory');
  });

  it('avoids floating-point drift when summing already-rounded GiB figures', () => {
    const result = computeShortfall({ label: 'VRAM', availableGib: 12.67, requiredGib: 14.39, reserveGib: 3 });
    // 14.39 + 3 can drift to 17.389999999999997 in raw floating point.
    expect(result.totalNeededGib).toBe(17.39);
    expect(Number.isInteger(result.totalNeededGib! * 100)).toBe(true);
  });
});
