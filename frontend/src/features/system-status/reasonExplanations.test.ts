import { describe, expect, it } from 'vitest';
import { explainReason, type KnownReasonCode } from './reasonExplanations';
import type { Capacity } from '../../api/schemas/status';

const BASE_CAPACITY: Capacity = {
  admission: 'commit-headroom',
  model: 'test-model',
  model_running: false,
  commit_headroom_gib: 28.44,
  required_commit_gib: 17.49,
  reserve_commit_gib: 4,
  physical_headroom_gib: 14.88,
  required_physical_gib: 13.7,
  reserve_physical_gib: 2,
  required_vram_gib: 10.8,
  device_vram_gib: 15.92,
  reserve_vram_gib: 3,
  measured: true,
  available: true,
  reason: 'commit_headroom_available',
};

function withReason(reason: string, overrides: Partial<Capacity> = {}): Capacity {
  return { ...BASE_CAPACITY, ...overrides, reason };
}

// Every one of the eight literal reason strings from the Sprint 3 brief maps
// to its own explanation, and each explanation is distinct from every other.
const ALL_KNOWN_REASONS: KnownReasonCode[] = [
  'model_already_running',
  'resource_profile_incomplete',
  'insufficient_vram_budget',
  'insufficient_physical_memory',
  'commit_headroom_available',
  'insufficient_commit_headroom',
  'canary_resource_measurement_pending',
  'resource_measurement_required',
];

describe('explainReason', () => {
  it.each(ALL_KNOWN_REASONS)('recognizes "%s" and produces a non-empty, non-raw-code sentence', (reason) => {
    const result = explainReason(withReason(reason));
    expect(result.recognized).toBe(true);
    expect(result.code).toBe(reason);
    expect(result.sentence.length).toBeGreaterThan(0);
  });

  it('maps every known reason to its own distinct explanation (no two share a sentence)', () => {
    const sentences = ALL_KNOWN_REASONS.map((reason) => explainReason(withReason(reason)).sentence);
    expect(new Set(sentences).size).toBe(sentences.length);
  });

  it('degrades an unrecognized reason to the raw code, not a wrong explanation', () => {
    const result = explainReason(withReason('some_future_reason_v2'));
    expect(result.recognized).toBe(false);
    expect(result.code).toBe('some_future_reason_v2');
    expect(result.sentence).toBe('some_future_reason_v2');
  });

  it('never substring-matches: a reason merely containing a known substring is still unrecognized', () => {
    const result = explainReason(withReason('insufficient_physical_memory_but_actually_something_else'));
    expect(result.recognized).toBe(false);
    expect(result.sentence).toBe('insufficient_physical_memory_but_actually_something_else');
  });

  it('treats a missing reason as "no reason reported" rather than throwing', () => {
    const result = explainReason({ ...BASE_CAPACITY, reason: null });
    expect(result.recognized).toBe(false);
    expect(result.sentence).toContain('No admission reason was reported');
  });

  it('renders the exact insufficient_physical_memory sentence for the brief\'s scenario (headroom 8.63, required 13.7, reserve 2)', () => {
    const result = explainReason(
      withReason('insufficient_physical_memory', {
        physical_headroom_gib: 8.63,
        required_physical_gib: 13.7,
        reserve_physical_gib: 2,
      }),
    );
    expect(result.sentence).toBe(
      'Not enough physical memory: 8.63 GiB available, but this model needs 13.7 GiB plus a 2 GiB safety reserve (15.7 GiB total) — short by 7.07 GiB.',
    );
  });
});
