import type { Capacity } from '../../api/schemas/status';
import { computeShortfall, formatShortfallSentence } from './shortfall';

/**
 * The complete, literal set of `reason` values the admission-control plane
 * emits today (Sprint 3 brief). This is a closed list, not an inferred one -
 * every string below was handed down by the brief verbatim, not guessed
 * from behaviour.
 */
export type KnownReasonCode =
  | 'model_already_running'
  | 'resource_profile_incomplete'
  | 'insufficient_vram_budget'
  | 'insufficient_physical_memory'
  | 'commit_headroom_available'
  | 'insufficient_commit_headroom'
  | 'canary_resource_measurement_pending'
  | 'resource_measurement_required';

export interface ReasonExplanation {
  /** The raw reason string as reported by the backend (or a placeholder if none was reported). */
  code: string;
  /** `false` means `code` fell outside the known lookup - `sentence` is the raw code, not a guess. */
  recognized: boolean;
  /** The plain-language explanation to show an operator. */
  sentence: string;
}

type ReasonExplainer = (capacity: Capacity) => string;

/**
 * Exact-string lookup only - see the Sprint 3 brief: "Map from the exact
 * string via a lookup - never substring-match, never branch on a model ID."
 * Every one of the eight known reasons is mapped, including the two
 * "admission would succeed" reasons (`model_already_running`,
 * `commit_headroom_available`) - the brief asks for every value to have an
 * explanation, not only the failure cases.
 */
const REASON_EXPLANATIONS: Record<KnownReasonCode, ReasonExplainer> = {
  model_already_running: () =>
    'This model is already the one loaded and serving requests, so no new admission check was needed.',

  resource_profile_incomplete: () =>
    "This model has no recorded resource profile yet, so the admission gate cannot check whether it fits and is refusing to guess.",

  insufficient_vram_budget: (capacity) =>
    formatShortfallSentence(
      computeShortfall({
        label: 'dedicated GPU memory',
        availableGib: capacity.device_vram_gib,
        requiredGib: capacity.required_vram_gib,
        reserveGib: capacity.reserve_vram_gib,
      }),
    ),

  insufficient_physical_memory: (capacity) =>
    formatShortfallSentence(
      computeShortfall({
        label: 'physical memory',
        availableGib: capacity.physical_headroom_gib,
        requiredGib: capacity.required_physical_gib,
        reserveGib: capacity.reserve_physical_gib,
      }),
    ),

  commit_headroom_available: () => 'There is enough committed-memory headroom to load this model right now.',

  insufficient_commit_headroom: (capacity) =>
    formatShortfallSentence(
      computeShortfall({
        label: 'committed memory',
        availableGib: capacity.commit_headroom_gib,
        requiredGib: capacity.required_commit_gib,
        reserveGib: capacity.reserve_commit_gib,
      }),
    ),

  canary_resource_measurement_pending: () =>
    "This is an unmeasured canary build - its resource usage has not been profiled yet, so admission is deferred until a measurement lands.",

  resource_measurement_required: () =>
    'This model needs a resource measurement before it can be admitted, and none has been taken yet.',
};

function isKnownReasonCode(code: string): code is KnownReasonCode {
  return Object.prototype.hasOwnProperty.call(REASON_EXPLANATIONS, code);
}

/**
 * Turns one `Capacity` snapshot's `reason` into an operator-facing
 * explanation. An unrecognised reason degrades to showing the raw code
 * (`recognized: false`, `sentence === code`) rather than a wrong or invented
 * explanation - never substring-matched, never inferred from the model id.
 */
export function explainReason(capacity: Capacity): ReasonExplanation {
  const code = capacity.reason;

  if (code === null || code === undefined || code === '') {
    return {
      code: '(no reason reported)',
      recognized: false,
      sentence: 'No admission reason was reported for this capacity check.',
    };
  }

  if (!isKnownReasonCode(code)) {
    return { code, recognized: false, sentence: code };
  }

  return { code, recognized: true, sentence: REASON_EXPLANATIONS[code](capacity) };
}
