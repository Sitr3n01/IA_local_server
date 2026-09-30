import type { Capacity, ModelCapabilities, ModelStatus, Status } from '../../api/schemas/status';
import { explainReason, type ReasonExplanation } from '../system-status/reasonExplanations';
import { computeShortfall, type ShortfallResult } from '../system-status/shortfall';

/**
 * The single action this console currently offers a given model. Never
 * both `load` and `switch` at once: which one applies is determined below
 * by whether *another* model is already active, per the Sprint 4 brief -
 * "load is refused when another model is already loaded (switch is the
 * operation for that)" - so this console simply never offers `load` in that
 * situation rather than offering a `load` button that would always be
 * refused.
 */
export type ModelActionKind = 'load' | 'unload' | 'switch';

export interface ModelActionState {
  kind: ModelActionKind;
  /**
   * Whether *this console's own derived state* currently allows attempting
   * the action - independent of whether the native host will actually
   * approve it (see `src/api/commands/models.ts` and
   * `src/features/models/useModelAction.ts` for that separate, later gate).
   */
  enabled: boolean;
  /**
   * Present only when `enabled` is `false` - the full operator-facing
   * reason this console is not offering the action right now. This is
   * deliberately *not* rendered verbatim on the always-visible primary
   * surface (see `ModelsPage.tsx`'s `actionCaption`) - the capacity
   * sentence in particular is exactly "the capacity verdict in full" the
   * Sprint 4 brief reserves for the secondary tier, so the primary tier
   * shows a short, generic pointer instead and relies on `disabledCause`
   * to pick which one.
   */
  disabledReason: string | null;
  /** `null` when `enabled`; otherwise which gate this console's own logic applied, so the UI can choose short wording without parsing `disabledReason`'s prose. */
  disabledCause: 'gate-busy' | 'capacity' | null;
}

export interface ModelCardData {
  id: string;
  displayName: string;
  /** Primary tier: "whether it is the loaded model". */
  isActive: boolean;
  /** Primary tier: "whether it can be admitted right now" - `model_statuses[].available`, never re-derived from `capacity` fields directly. */
  isAvailableNow: boolean;
  /** Primary tier: "its context window". */
  contextTokens: number;

  /** Secondary tier: the capacity verdict in full, reused (not re-derived) from `reasonExplanations.ts`. */
  reason: ReasonExplanation;
  /** Secondary tier: "the numbers behind the verdict" for all three admission budgets, reused (not re-derived) from `shortfall.ts`. */
  shortfalls: {
    commit: ShortfallResult;
    physical: ShortfallResult;
    vram: ShortfallResult;
  };
  /** Secondary tier: `null` = "render no row" (key absent); see `format.ts`'s `formatReclaimableGib`. */
  reclaimableCommitGib: number | null | undefined;
  reclaimablePhysicalGib: number | null | undefined;
  /** Secondary tier: capability flags, verbatim from the payload - `undefined` when `models[]` has no matching entry for this id at all (never fabricated). */
  capabilities: ModelCapabilities | undefined;

  /** Advanced tier. */
  profile: ModelStatus['profile'];
  checkpoints: ModelStatus['checkpoints'];
  runtime: ModelStatus['runtime'];

  action: ModelActionState;
}

export interface ModelsData {
  /** `true` when the admission gate itself refuses every mutation right now (`gate.active` or `gate.queued` non-zero). */
  gateBusy: boolean;
  /** Populated iff `gateBusy` - the shared, operator-facing reason surfaced by every disabled action below. */
  gateBusyReason: string | null;
  /** `null` means no model is currently active (`active_model === ""`), mirroring `deriveOverview.ts`'s `activeModelLabel` convention. */
  activeModelId: string | null;
  models: ModelCardData[];
}

function buildShortfalls(capacity: Capacity): ModelCardData['shortfalls'] {
  return {
    commit: computeShortfall({
      label: 'committed memory',
      availableGib: capacity.commit_headroom_gib,
      requiredGib: capacity.required_commit_gib,
      reserveGib: capacity.reserve_commit_gib,
    }),
    physical: computeShortfall({
      label: 'physical memory',
      availableGib: capacity.physical_headroom_gib,
      requiredGib: capacity.required_physical_gib,
      reserveGib: capacity.reserve_physical_gib,
    }),
    vram: computeShortfall({
      label: 'dedicated GPU memory',
      availableGib: capacity.device_vram_gib,
      requiredGib: capacity.required_vram_gib,
      reserveGib: capacity.reserve_vram_gib,
    }),
  };
}

function deriveAction(
  model: ModelStatus,
  gateBusy: boolean,
  gateBusyReason: string | null,
  activeModelId: string | null,
): ModelActionState {
  if (model.active) {
    return gateBusy
      ? { kind: 'unload', enabled: false, disabledReason: gateBusyReason, disabledCause: 'gate-busy' }
      : { kind: 'unload', enabled: true, disabledReason: null, disabledCause: null };
  }

  // Never `load` while a *different* model is active - `switch` is the
  // operation for that, per the Sprint 4 brief. This model is a candidate
  // for `load` only when nothing at all is currently active.
  const kind: ModelActionKind = activeModelId === null ? 'load' : 'switch';

  if (gateBusy) {
    return { kind, enabled: false, disabledReason: gateBusyReason, disabledCause: 'gate-busy' };
  }

  if (!model.available) {
    // Reuse the same reason explanation the secondary tier shows in full,
    // rather than re-deriving a second, possibly-inconsistent sentence.
    return {
      kind,
      enabled: false,
      disabledReason: explainReason(model.capacity).sentence,
      disabledCause: 'capacity',
    };
  }

  return { kind, enabled: true, disabledReason: null, disabledCause: null };
}

function deriveCard(
  model: ModelStatus,
  status: Status,
  gateBusy: boolean,
  gateBusyReason: string | null,
  activeModelId: string | null,
): ModelCardData {
  const modelInfo = status.models.find((entry) => entry.id === model.id);

  return {
    id: model.id,
    displayName: modelInfo?.display_name ?? model.id,
    isActive: model.active,
    isAvailableNow: model.available,
    contextTokens: model.context_tokens,
    reason: explainReason(model.capacity),
    shortfalls: buildShortfalls(model.capacity),
    reclaimableCommitGib: model.capacity.reclaimable_commit_gib,
    reclaimablePhysicalGib: model.capacity.reclaimable_physical_gib,
    capabilities: modelInfo?.capabilities,
    profile: model.profile,
    checkpoints: model.checkpoints,
    runtime: model.runtime,
    action: deriveAction(model, gateBusy, gateBusyReason, activeModelId),
  };
}

/**
 * Derives the Models screen's presentation-ready state from a raw,
 * Zod-validated `Status` payload. Nothing downstream of this function reads
 * `model_statuses`, `models`, or `gate` directly - `src/pages/ModelsPage.tsx`
 * renders only what this returns, per the app's established dependency
 * direction (pages -> features -> api/queries -> transport).
 */
export function deriveModels(status: Status): ModelsData {
  const gateBusy = status.gate.active > 0 || status.gate.queued > 0;
  const gateBusyReason = gateBusy
    ? `The admission gate is busy right now (${status.gate.active} active, ${status.gate.queued} queued) - every load, unload, and switch is refused while inference is in flight.`
    : null;
  const activeModelId = status.active_model === '' ? null : status.active_model;

  return {
    gateBusy,
    gateBusyReason,
    activeModelId,
    models: status.model_statuses.map((model) => deriveCard(model, status, gateBusy, gateBusyReason, activeModelId)),
  };
}
