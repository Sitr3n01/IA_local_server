import { useStatusQuery } from '../../api/queries/useStatus';
import { deriveOverview, type OverviewData } from './deriveOverview';
import { describeStatusError } from './describeError';
import type { ViewModel } from './types';

export type OverviewViewModel = ViewModel<OverviewData, 'no-models'>;

/**
 * Overview's one entry point into the data layer. `src/pages/OverviewPage.tsx`
 * calls only this - never `useStatusQuery` directly - per the sprint's
 * dependency direction (pages -> features -> api/queries -> transport).
 */
export function useOverviewViewModel(): OverviewViewModel {
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

  const overview = deriveOverview(data);

  if (overview.totalModelCount === 0) {
    return { state: 'empty', reason: 'no-models', data: overview };
  }

  return { state: 'populated', data: overview };
}
