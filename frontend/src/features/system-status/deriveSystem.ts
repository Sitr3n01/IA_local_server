import type { Deployment, RuntimeInfo, Status } from '../../api/schemas/status';

export interface SystemData {
  /** `undefined` when no release manifest is installed yet - see DeploymentSchema's `.optional()`. */
  deployment: Deployment | undefined;
  runtimes: RuntimeInfo[];
  upstream: { reachable: boolean; url: string };
  uptimeSeconds: number;
}

/**
 * Derives System's presentation-ready state from a raw, Zod-validated
 * `Status` payload. This is the "what machine is this" tier:
 * runtime/deployment identity, upstream reachability, and uptime - see the
 * Sprint 3 brief for the original field list.
 *
 * As of Sprint 6 (Activity), gate counters, GPU memory, and the
 * recent-events feed moved off this page to `src/features/activity/` - see
 * `deriveActivity.ts`. Those three are "what is the provider doing right
 * now" questions, not "what machine is this" ones, per the Sprint 6 brief's
 * split. Nothing here duplicates that derivation.
 *
 * `status.gpu_memory` and `status.recent_events` are read only in
 * `deriveActivity.ts`. `status.gate` is NOT - `deriveOverview.ts` reads its
 * active/queued counts for the summary, and `deriveModels.ts` reads them to
 * decide whether every model-lifecycle button is enabled at all. An earlier
 * version of this comment claimed all three moved to a single reader, which
 * was wrong about the one that matters most: the gate is the thing that
 * gates mutations, so it is deliberately read wherever a screen needs to
 * know whether the provider will accept work.
 */
export function deriveSystem(status: Status): SystemData {
  return {
    deployment: status.deployment,
    runtimes: status.runtimes,
    upstream: { reachable: status.upstream.reachable, url: status.upstream.url },
    uptimeSeconds: status.uptime_seconds,
  };
}
