import { useStatusQuery } from '../../api/queries/useStatus';
import { deriveModels, type ModelsData } from './deriveModels';
import { describeStatusError } from '../system-status/describeError';
import type { ViewModel } from '../system-status/types';

export type ModelsViewModel = ViewModel<ModelsData, 'no-models'>;

/**
 * Models' one entry point into the data layer - mirrors
 * `useOverviewViewModel`/`useSystemViewModel` (see those files' doc
 * comments for the dependency rule this enforces, and
 * `src/features/system-status/types.ts` for the shared `ViewModel` shape
 * reused here rather than re-declared). The empty case is "no models
 * configured at all" (`model_statuses` is empty), matching Overview's
 * `no-models` reason.
 */
export function useModelsViewModel(): ModelsViewModel {
  const { data, isError, error, refetch } = useStatusQuery();

  // A poll failure must not blank a screen that already has a good snapshot.
  //
  // `refetchInterval` is 5s, so any transient hiccup - the provider
  // restarting, a momentary hang - flips `isError` within seconds. Returning
  // `error` there unmounted the whole page: every expanded disclosure closed,
  // and the keyboard operator's focus went with it, several times an hour on
  // a machine under memory pressure. The data itself was never gone; TanStack
  // Query still held the last successful response in `data`.
  //
  // So the branch is on `data`, not on `isError`: with no snapshot at all
  // there is genuinely nothing to show and the error state is right, but once
  // one exists the screen keeps rendering it. The operator is told it is
  // stale in one place rather than four - the top bar's health indicator
  // switches to "Unreachable" on the same condition (see
  // `useHealthSummary`), and AppShell carries the banner explaining that the
  // figures on screen are the last ones received.
  if (data === undefined) {
    if (isError) {
      return { state: 'error', message: describeStatusError(error), retry: () => void refetch() };
    }
    return { state: 'loading' };
  }

  const models = deriveModels(data);

  if (models.models.length === 0) {
    return { state: 'empty', reason: 'no-models', data: models };
  }

  return { state: 'populated', data: models };
}
