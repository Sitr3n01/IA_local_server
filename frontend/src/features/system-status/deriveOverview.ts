import type { Status } from '../../api/schemas/status';
import { explainReason, type ReasonExplanation } from './reasonExplanations';

export interface OverviewData {
  ready: boolean;
  readiness: ReasonExplanation;
  availableModelCount: number;
  totalModelCount: number;
  /** `null` means "no model is currently loaded" (`active_model === ""`). */
  activeModelLabel: string | null;
  gate: {
    active: number;
    queued: number;
    maxActive: number;
    maxQueue: number;
  };
  maintenance: {
    state: string;
    draining: boolean;
    drained: boolean;
  };
}

/**
 * Derives Overview's presentation-ready state from a raw, Zod-validated
 * `Status` payload. Nothing downstream of this function touches
 * `capacity.reason`, `model_statuses`, or `active_model` directly - pages
 * render only what this returns, per the sprint's dependency-direction rule
 * (pages -> features -> api/queries -> transport).
 */
export function deriveOverview(status: Status): OverviewData {
  const availableModelCount = status.model_statuses.filter((model) => model.available).length;
  const totalModelCount = status.model_statuses.length;

  const activeModelLabel =
    status.active_model === ''
      ? null
      : (status.models.find((model) => model.id === status.active_model)?.display_name ?? status.active_model);

  return {
    ready: status.ready,
    readiness: explainReason(status.capacity),
    availableModelCount,
    totalModelCount,
    activeModelLabel,
    gate: {
      active: status.gate.active,
      queued: status.gate.queued,
      maxActive: status.gate.max_active,
      maxQueue: status.gate.max_queue,
    },
    maintenance: {
      state: status.maintenance.state,
      draining: status.maintenance.draining,
      drained: status.maintenance.drained,
    },
  };
}
