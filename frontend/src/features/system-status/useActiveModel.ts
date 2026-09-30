import { useStatusQuery } from '../../api/queries/useStatus';
import { deriveActiveModel, describeActiveModel, type ActiveModelSummary } from './activeModel';

export type ActiveModelState =
  /** No snapshot has arrived yet - the shell shows a placeholder, never a name. */
  | { state: 'unknown' }
  /** The provider reports no loaded model (`active_model === ""`). */
  | { state: 'none' }
  | { state: 'loaded'; model: ActiveModelSummary; detail: string | null };

/**
 * What the app shell's header names: which model this console is currently
 * driving.
 *
 * Sprint 10 promoted this from a line on one screen to a permanent fact of
 * the shell. The header used to lead with the deployment environment, which
 * answered "which provider am I acting on" but not "what is it running" -
 * and every screen in the console is about a provider that is either
 * serving a model or is not. The environment is still there, one step
 * quieter, beside the readiness indicator.
 *
 * Branches on `data`, not on `isError`, for the same reason every view
 * model here does: a five-second poll makes transient failures routine, and
 * blanking the model's name out of the chrome on one of them would be
 * worse than briefly naming a model that is still loaded. The shell's
 * stale-data banner is what says the figures may no longer be current.
 *
 * Shares the single cached poll with every other `useStatusQuery` consumer.
 */
export function useActiveModel(): ActiveModelState {
  const { data } = useStatusQuery();

  if (data === undefined) {
    return { state: 'unknown' };
  }

  const model = deriveActiveModel(data);
  if (model === null) {
    return { state: 'none' };
  }

  return { state: 'loaded', model, detail: describeActiveModel(model) };
}
