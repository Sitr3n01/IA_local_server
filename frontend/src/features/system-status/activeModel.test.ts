import { describe, expect, it } from 'vitest';
import fixture from '../../api/__fixtures__/status.sample.json';
import { StatusSchema, type Status } from '../../api/schemas/status';
import { deriveActiveModel, describeActiveModel } from './activeModel';

function statusWith(overrides: Record<string, unknown>): Status {
  return StatusSchema.parse({ ...fixture, ...overrides });
}

describe('deriveActiveModel', () => {
  it('returns null when the provider reports no loaded model', () => {
    // The payload states this as `active_model: ""`, not as a missing key
    // and not as null - the fixture's own resting state.
    expect(deriveActiveModel(statusWith({ active_model: '' }))).toBeNull();
  });

  it('resolves the display name, weights and context window for the loaded model', () => {
    const model = deriveActiveModel(statusWith({ active_model: 'qwen36-35b-a3b-huge-256k' }));

    expect(model?.id).toBe('qwen36-35b-a3b-huge-256k');
    expect(model?.displayName).toBe('Qwen3.6 35B-A3B Huge 256k - UD-Q2_K_XL / KV q4_0 - long-context MoE');
    expect(model?.weights).toBe('Qwen3.6-35B-A3B-UD-Q2_K_XL');
    expect(model?.contextTokens).toBe(262_144);
  });

  it('matches on the exact id, never a prefix - two fixture models differ only by a "-256k" suffix', () => {
    // `gemma4-12b-qat-ud-q4xl` is a strict prefix of
    // `gemma4-12b-qat-ud-q4xl-256k`. A `startsWith`/`includes` lookup would
    // confidently attribute one model's 131,072-token window to the other's
    // 262,144-token one, and an operator would have no way to tell.
    const shorter = deriveActiveModel(statusWith({ active_model: 'gemma4-12b-qat-ud-q4xl' }));
    const longer = deriveActiveModel(statusWith({ active_model: 'gemma4-12b-qat-ud-q4xl-256k' }));

    expect(shorter?.contextTokens).toBe(131_072);
    expect(longer?.contextTokens).toBe(262_144);
    expect(shorter?.displayName).not.toBe(longer?.displayName);
  });

  it('falls back to the raw id, and to null facts, when no entry describes the loaded model', () => {
    // An id is an honest, if ugly, name for the thing that is loaded.
    // Inventing a prettier one would name something the provider never
    // described, and reporting 0 tokens would be a measurement nobody took.
    const model = deriveActiveModel(statusWith({ active_model: 'a-model-nothing-describes' }));

    expect(model?.displayName).toBe('a-model-nothing-describes');
    expect(model?.weights).toBeNull();
    expect(model?.contextTokens).toBeNull();
  });
});

describe('describeActiveModel', () => {
  it('joins the weights and the formatted context window', () => {
    const model = deriveActiveModel(statusWith({ active_model: 'qwen36-35b-a3b-huge-256k' }));
    expect(describeActiveModel(model!)).toBe('Qwen3.6-35B-A3B-UD-Q2_K_XL · 262,144 tokens context');
  });

  it('omits whichever half the payload did not report', () => {
    expect(describeActiveModel({ id: 'x', displayName: 'X', weights: 'W', contextTokens: null })).toBe('W');
    expect(describeActiveModel({ id: 'x', displayName: 'X', weights: null, contextTokens: 4096 })).toBe(
      '4,096 tokens context',
    );
  });

  it('returns null - not an empty string - when neither fact was reported, so the shell renders no line at all', () => {
    expect(describeActiveModel({ id: 'x', displayName: 'X', weights: null, contextTokens: null })).toBeNull();
  });
});
