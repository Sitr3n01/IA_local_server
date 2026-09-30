import { useStatusQuery } from '../../api/queries/useStatus';
import { deriveActivity, type ActivityData } from './deriveActivity';
import { describeStatusError } from '../system-status/describeError';
import type { ViewModel } from '../system-status/types';

export type ActivityViewModel = ViewModel<ActivityData, 'no-events'>;

/**
 * Activity's one entry point into the data layer - mirrors
 * `useSystemViewModel`/`useOverviewViewModel` exactly (see those files' doc
 * comments for the dependency rule this enforces:
 * pages -> features -> api/queries -> transport). `src/pages/ActivityPage.tsx`
 * calls only this - never `useStatusQuery` or `deriveActivity` directly.
 *
 * The empty case is "no events yet" (`recent_events` is empty) - the same
 * reason System's own view model used before Sprint 6 moved the events feed
 * here. The gate and GPU memory sections still render normally from `data`
 * even when the events feed is empty, since neither is tied to event
 * history.
 */
export function useActivityViewModel(): ActivityViewModel {
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

  const activity = deriveActivity(data);

  if (activity.events.total === 0) {
    return { state: 'empty', reason: 'no-events', data: activity };
  }

  return { state: 'populated', data: activity };
}
