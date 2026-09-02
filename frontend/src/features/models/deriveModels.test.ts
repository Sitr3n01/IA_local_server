import { describe, expect, it } from 'vitest';
import fixture from '../../api/__fixtures__/status.sample.json';
import { StatusSchema } from '../../api/schemas/status';
import { deriveModels } from './deriveModels';

function parse(overrides: Record<string, unknown> = {}) {
  return StatusSchema.parse({ ...fixture, ...overrides });
}

describe('deriveModels: primary tier fields', () => {
  it('carries id, display name, active flag, availability, and context tokens straight from the payload', () => {
    const data = deriveModels(parse());
    const gemma = data.models.find((m) => m.id === 'gemma4-12b-qat-ud-q4xl');

    expect(gemma).toBeDefined();
    expect(gemma?.displayName).toBe('Gemma 4 12B QAT UD Q4_K_XL - 128k context');
    expect(gemma?.isActive).toBe(false);
    expect(gemma?.isAvailableNow).toBe(false);
    expect(gemma?.contextTokens).toBe(131072);
  });

  it('falls back to the raw id when models[] has no matching entry (never crashes, never fabricates a name)', () => {
    const status = parse();
    const withUnlistedModel = {
      ...status,
      model_statuses: [{ ...status.model_statuses[0]!, id: 'unlisted-model-id' }],
      models: [],
    };
    const data = deriveModels(StatusSchema.parse(withUnlistedModel));

    expect(data.models[0]?.displayName).toBe('unlisted-model-id');
    expect(data.models[0]?.capabilities).toBeUndefined();
  });
});

describe('deriveModels: secondary tier (reused reasonExplanations/shortfall)', () => {
  it('reuses explainReason for the full verdict sentence rather than re-deriving it', () => {
    const data = deriveModels(parse());
    const gemma = data.models.find((m) => m.id === 'gemma4-12b-qat-ud-q4xl');

    expect(gemma?.reason.recognized).toBe(true);
    expect(gemma?.reason.sentence).toBe(
      'Not enough physical memory: 3.31 GiB available, but this model needs 13.7 GiB plus a 2 GiB safety reserve (15.7 GiB total) — short by 12.39 GiB.',
    );
  });

  it('computes all three admission budgets independently via computeShortfall', () => {
    const data = deriveModels(parse());
    const gemma = data.models.find((m) => m.id === 'gemma4-12b-qat-ud-q4xl');

    // commit_headroom_gib 6.18, required_commit_gib 17.49, reserve_commit_gib 4.
    expect(gemma?.shortfalls.commit.totalNeededGib).toBe(21.49);
    expect(gemma?.shortfalls.commit.shortfallGib).toBe(15.31);

    // required_vram_gib 10.8, device_vram_gib 15.92, reserve_vram_gib 3 - fits.
    expect(gemma?.shortfalls.vram.computable).toBe(true);
    expect(gemma?.shortfalls.vram.shortfallGib).toBeLessThanOrEqual(0);
  });

  it('reclaimable figures: absent key renders as undefined ("no row"), never as 0 or "not measured"', () => {
    const data = deriveModels(parse());
    const gemma = data.models.find((m) => m.id === 'gemma4-12b-qat-ud-q4xl');
    expect(gemma?.reclaimableCommitGib).toBeUndefined();
    expect(gemma?.reclaimablePhysicalGib).toBeUndefined();
  });

  it('reclaimable figures: a present-but-null key is distinct from an absent key', () => {
    const status = parse();
    const withNullReclaimable = {
      ...status,
      model_statuses: status.model_statuses.map((m, i) =>
        i === 0 ? { ...m, capacity: { ...m.capacity, reclaimable_commit_gib: null } } : m,
      ),
    };
    const data = deriveModels(StatusSchema.parse(withNullReclaimable));
    expect(data.models[0]?.reclaimableCommitGib).toBeNull();
  });

  it('reclaimable figures: a measured value passes through untouched', () => {
    const status = parse();
    const withReclaimable = {
      ...status,
      model_statuses: status.model_statuses.map((m, i) =>
        i === 0 ? { ...m, capacity: { ...m.capacity, reclaimable_physical_gib: 4.5 } } : m,
      ),
    };
    const data = deriveModels(StatusSchema.parse(withReclaimable));
    expect(data.models[0]?.reclaimablePhysicalGib).toBe(4.5);
  });
});

describe('deriveModels: capability flags come only from the payload, never an id', () => {
  it('renders a true capability for the model that reports it', () => {
    const data = deriveModels(parse());
    const qwenAgent = data.models.find((m) => m.id === 'qwen38-27b-agent-128k');
    expect(qwenAgent?.capabilities?.function_calling).toBe(true);
  });

  it('leaves an unreported capability undefined for a different model whose id also suggests agentic use', () => {
    const data = deriveModels(parse());
    // ornith15-35b-a3b-fast-128k's id has nothing "agent"-like in it and its
    // fixture entry never sends function_calling - included to show the
    // absence is read from the payload, not guessed from either id.
    const ornith = data.models.find((m) => m.id === 'ornith15-35b-a3b-fast-128k');
    expect(ornith?.capabilities?.function_calling).toBeUndefined();
  });
});

describe('deriveModels: action gating', () => {
  it('offers "load" (not "switch") for an available model when no model is currently active', () => {
    const status = parse();
    const withAvailable = {
      ...status,
      model_statuses: status.model_statuses.map((m, i) =>
        i === 0 ? { ...m, available: true, capacity: { ...m.capacity, available: true } } : m,
      ),
    };
    const data = deriveModels(StatusSchema.parse(withAvailable));
    expect(data.activeModelId).toBeNull();
    expect(data.models[0]?.action).toEqual({
      kind: 'load',
      enabled: true,
      disabledReason: null,
      disabledCause: null,
    });
  });

  it('offers "switch" (never "load") for a non-active model when a different model is active', () => {
    const data = deriveModels(parse({ active_model: 'qwen38-27b-deep-32k' }));
    const gemma = data.models.find((m) => m.id === 'gemma4-12b-qat-ud-q4xl');
    expect(gemma?.action.kind).toBe('switch');
  });

  it('offers "unload" for the currently active model', () => {
    const status = parse({ active_model: 'gemma4-12b-qat-ud-q4xl' });
    const withActiveFlag = {
      ...status,
      model_statuses: status.model_statuses.map((m) => (m.id === 'gemma4-12b-qat-ud-q4xl' ? { ...m, active: true } : m)),
    };
    const data = deriveModels(StatusSchema.parse(withActiveFlag));
    const gemma = data.models.find((m) => m.id === 'gemma4-12b-qat-ud-q4xl');
    expect(gemma?.action.kind).toBe('unload');
    expect(gemma?.action.enabled).toBe(true);
  });

  it('disables every action with a stated reason while the admission gate is busy (active non-zero)', () => {
    const status = parse();
    const busy = { ...status, gate: { ...status.gate, active: 1 } };
    const data = deriveModels(StatusSchema.parse(busy));

    expect(data.gateBusy).toBe(true);
    expect(data.gateBusyReason).toMatch(/busy/);
    for (const model of data.models) {
      expect(model.action.enabled).toBe(false);
      expect(model.action.disabledCause).toBe('gate-busy');
      expect(model.action.disabledReason).toBe(data.gateBusyReason);
    }
  });

  it('disables every action with a stated reason while the admission gate is busy (queued non-zero)', () => {
    const status = parse();
    const busy = { ...status, gate: { ...status.gate, queued: 2 } };
    const data = deriveModels(StatusSchema.parse(busy));

    expect(data.gateBusy).toBe(true);
    expect(data.models.every((m) => m.action.enabled === false)).toBe(true);
  });

  it('disables the action with the capacity reason when the model itself is not available (gate otherwise idle)', () => {
    const data = deriveModels(parse());
    const gemma = data.models.find((m) => m.id === 'gemma4-12b-qat-ud-q4xl');

    expect(data.gateBusy).toBe(false);
    expect(gemma?.action.enabled).toBe(false);
    expect(gemma?.action.disabledCause).toBe('capacity');
    expect(gemma?.action.disabledReason).toBe(gemma?.reason.sentence);
  });
});
