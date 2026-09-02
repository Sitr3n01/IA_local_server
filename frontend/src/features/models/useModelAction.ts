import { useCallback, useState } from 'react';
import { NativeHostUnavailableError } from '../../api/commands/models';

/**
 * The full lifecycle of one attempted model-lifecycle command:
 *
 *  - `idle`: nothing in flight (the starting state, and where a call
 *    returns to if it ever actually succeeds).
 *  - `pending`: the request is in flight, waiting on the native host.
 *  - `no-native-host`: a distinct, honestly-labelled refusal - the console
 *    is not running inside `cia-console.exe` (e.g. `vite dev`), so there is
 *    no native confirmation dialog to ask. Kept separate from `error` so
 *    the UI can render its own state for it, per the Sprint 4 brief.
 *  - `error`: any other rejection - a refused approval, or an error the
 *    administrative pipe reported. See `src/api/commands/models.ts`.
 *
 * Never a `success` state, and that is still right now that mutations do
 * complete. An earlier version of this comment said no mutation could
 * complete at all and spoke of a bridge that did not exist yet; the bridge
 * has since been wired end to end, and the reasoning survived the change
 * intact: a resolved call falls back to `idle`, because the only evidence
 * worth showing an operator is the provider's own next status, which
 * `useStatusQuery`'s poll brings in on its own. Rendering "Loaded!" from the
 * fact that a call returned would be asserting an outcome this layer never
 * actually observed - the "fake success" the Sprint 4 brief forbids.
 */
export type ModelActionOutcome =
  | { status: 'idle' }
  | { status: 'pending' }
  | { status: 'no-native-host'; message: string }
  | { status: 'error'; message: string };

/**
 * Drives one model card's action button. `command` is one of
 * `loadModel`/`unloadModel`/`switchModel` from `src/api/commands/models.ts`,
 * pre-bound by the caller to the model id it targets - this hook itself
 * never imports the transport layer or knows which op it is calling.
 */
export function useModelAction(command: (modelId: string) => Promise<void>) {
  const [outcome, setOutcome] = useState<ModelActionOutcome>({ status: 'idle' });

  const run = useCallback(
    async (modelId: string) => {
      setOutcome({ status: 'pending' });
      try {
        await command(modelId);
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
    },
    [command],
  );

  return [outcome, run] as const;
}
