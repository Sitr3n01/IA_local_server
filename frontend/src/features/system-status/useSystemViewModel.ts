import { useStatusQuery } from '../../api/queries/useStatus';
import { deriveSystem, type SystemData } from './deriveSystem';
import { describeStatusError } from './describeError';
import type { ViewModel } from './types';

export type SystemViewModel = ViewModel<SystemData, 'no-runtimes'>;

/**
 * System's one entry point into the data layer - mirrors
 * `useOverviewViewModel`, see that file's doc comment for the dependency
 * rule this enforces.
 *
 * As of Sprint 6, `SystemData` no longer carries `recent_events` (moved to
 * Activity - see `deriveSystem.ts`), so "no events yet" no longer applies
 * here. The empty case is now "no runtimes reported" (`runtimes` is empty):
 * the rest of the page (deployment, upstream, uptime) still renders
 * normally from `data` even when the runtimes list is empty, since none of
 * those fields depend on it.
 */
export function useSystemViewModel(): SystemViewModel {
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

  const system = deriveSystem(data);

  if (system.runtimes.length === 0) {
    return { state: 'empty', reason: 'no-runtimes', data: system };
  }

  return { state: 'populated', data: system };
}
