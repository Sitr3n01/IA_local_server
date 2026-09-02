import { useCallback, useState } from 'react';
import { NativeHostUnavailableError } from '../../api/commands/maintenance';

/**
 * The full lifecycle of one attempted maintenance command (drain/resume).
 * Mirrors `src/features/models/useModelAction.ts`'s `ModelActionOutcome`
 * exactly - same four states, same rationale for each:
 *
 *  - `idle`: nothing in flight (the starting state, and where a call
 *    returns to if it ever actually succeeds).
 *  - `pending`: the request is in flight, waiting on the native host.
 *  - `no-native-host`: a distinct, honestly-labelled refusal - the console
 *    is not running inside `cia-console.exe` (e.g. `vite dev`), so there is
 *    no native confirmation dialog to ask. Kept separate from `error` so
 *    the UI can render its own state for it.
 *  - `error`: any other rejection.
 *
 * Never a `success` state, for the same reason `useModelAction` has none:
 * `useStatusQuery`'s 5s poll (see `src/api/queries/useStatus.ts`) is what
 * picks up the resulting `status.maintenance` change - this hook does not
 * invalidate or refetch the query itself, matching `useModelAction`'s own
 * behaviour rather than inventing a second reconciliation path.
 */
export type MaintenanceActionOutcome =
  | { status: 'idle' }
  | { status: 'pending' }
  | { status: 'no-native-host'; message: string }
  | { status: 'error'; message: string };

/**
 * Drives the Activity page's one maintenance action button. `command` is
 * `drainProvider` or `resumeProvider` from
 * `src/api/commands/maintenance.ts`, passed directly - unlike
 * `useModelAction`, there is no model id to bind, since drain/resume must
 * never carry a `model_id` (see that module's doc comment).
 */
export function useMaintenanceAction(command: () => Promise<void>) {
  const [outcome, setOutcome] = useState<MaintenanceActionOutcome>({ status: 'idle' });

  const run = useCallback(async () => {
    setOutcome({ status: 'pending' });
    try {
      await command();
      setOutcome({ status: 'idle' });
    } catch (error) {
      if (error instanceof NativeHostUnavailableError) {
        setOutcome({ status: 'no-native-host', message: error.message });
      } else {
        setOutcome({
          status: 'error',
          message: error instanceof Error ? error.message : 'The request could not be completed.',
        });
      }
    }
  }, [command]);

  return [outcome, run] as const;
}
