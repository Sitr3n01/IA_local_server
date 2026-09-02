import { describe, expect, it } from 'vitest';
import fixture from '../__fixtures__/status.sample.json';
import { StatusSchema } from './status';

describe('StatusSchema', () => {
  it('parses the captured GET /api/v1/status fixture without loss', () => {
    const result = StatusSchema.safeParse(fixture);
    expect(result.success).toBe(true);
  });

  it('keeps measured capacity headroom as a number, never defaulted to 0', () => {
    const parsed = StatusSchema.parse(fixture);
    expect(parsed.capacity.commit_headroom_gib).not.toBeNull();
    expect(typeof parsed.capacity.commit_headroom_gib).toBe('number');
  });

  it('models null checkpoint fields as null, not as a missing/defaulted value', () => {
    const parsed = StatusSchema.parse(fixture);
    const first = parsed.model_statuses[0];
    expect(first).toBeDefined();
    expect(first?.checkpoints.checkpoint_min_step).toBeNull();
    expect(first?.checkpoints.ctx_checkpoints).toBeNull();
  });

  it('treats an omitted capability flag as undefined, not false', () => {
    const parsed = StatusSchema.parse(fixture);
    const gemma = parsed.models.find((m) => m.id === 'gemma4-12b-qat-ud-q4xl');
    expect(gemma).toBeDefined();
    expect(gemma?.capabilities.function_calling).toBeUndefined();

    const qwenAgent = parsed.models.find((m) => m.id === 'qwen38-27b-agent-128k');
    expect(qwenAgent?.capabilities.function_calling).toBe(true);
  });

  it('makes deployment optional rather than required', () => {
    const { deployment: _deployment, ...withoutDeployment } = fixture as Record<string, unknown>;
    const result = StatusSchema.safeParse(withoutDeployment);
    expect(result.success).toBe(true);
  });

  it('rejects a payload missing a required top-level field', () => {
    const { service: _service, ...withoutService } = fixture as Record<string, unknown>;
    const result = StatusSchema.safeParse(withoutService);
    expect(result.success).toBe(false);
  });

  it('passes unknown future top-level fields through instead of rejecting them', () => {
    const result = StatusSchema.safeParse({ ...fixture, totally_new_field_from_a_future_backend: 'x' });
    expect(result.success).toBe(true);
  });

  it('passes unknown future capability flags through the capabilities object', () => {
    const withNewCapability = {
      ...fixture,
      models: [
        {
          id: 'x',
          object: 'model',
          owned_by: 'local',
          display_name: 'x',
          capabilities: { chat_completions: true, vision: true },
        },
      ],
    };
    const result = StatusSchema.safeParse(withNewCapability);
    expect(result.success).toBe(true);
  });
});
