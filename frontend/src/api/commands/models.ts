/**
 * Model-lifecycle mutations (load / unload / switch) for the Models screen.
 *
 * These go through `bridgeTransport` directly, never through `getTransport()`.
 * That dev/prod-switched selector in `src/api/transport/index.ts` resolves
 * to `httpTransport.dev` under `vite dev`). That is a deliberate,
 * security-relevant choice, not an oversight:
 *
 * ADR 0018 control 1 puts the real authorization for every load/unload/
 * switch in a native Win32 dialog owned by `cia-console.exe`, entirely
 * outside this DOM, precisely so a compromised page can never approve its
 * own mutation. That control only exists on the bridge path - cia-edge's
 * dev-only HTTP surface (what `httpTransport.dev` talks to) has no such
 * dialog and was never meant to carry a mutation. So a model-lifecycle
 * command must reach the native bridge in every environment - `vite dev`
 * included - rather than falling back to the loopback HTTP shortcut just
 * because that shortcut happens to be more convenient under `npm run dev`.
 *
 * The consequence is exactly what the Sprint 4 brief asked for: under `vite
 * dev` there is no `window.chrome.webview` for `bridgeTransport` to talk to,
 * so every call here rejects with `NativeHostUnavailableError` - an honest,
 * specific refusal, not a fake success and not a generic "not implemented".
 *
 * The op name sent for each call (`load`/`unload`/`switch`) matches
 * `cmd/cia-console/bridge.go`'s `KindLoad`/`KindUnload`/`KindSwitch`
 * constants exactly - the bridge classifies an operation kind by exact map
 * lookup, never by prefix or substring match, so anything else here would
 * fall through to `unknown_operation` (fail-closed - see bridge.go's
 * `Handle` - but silently non-functional) once `bridgeTransport` actually
 * reaches the host, which it does as of the native bridge work.
 */
import { bridgeTransport } from '../transport/bridgeTransport';
import { NativeHostUnavailableError } from '../errors';

/**
 * Re-exported so callers (see `src/features/models/useModelAction.ts`)
 * can keep importing it from here rather than
 * reaching into `src/api/errors.ts` themselves - the single home for the
 * type is `errors.ts`, but this remains the command surface's public name
 * for it.
 */
export { NativeHostUnavailableError };

async function requestModelOperation(op: string, params: { readonly model_id: string }): Promise<void> {
  try {
    await bridgeTransport.request(op, params);
  } catch (error) {
    if (error instanceof NativeHostUnavailableError) {
      throw new NativeHostUnavailableError(
        'This action needs the native console host. Open IA Local inside cia-console.exe, not a browser tab or ' +
          '`vite dev` - the native host is what shows the confirmation dialog ADR 0018 requires before a model is ' +
          'loaded, unloaded, or switched. This button never performs that confirmation itself.',
        'host-not-present',
      );
    }
    throw error;
  }
}

export interface LoadModelParams {
  readonly modelId: string;
}

/** Requests loading a model that is not currently active, when no other model is currently active either. */
export async function loadModel(params: LoadModelParams): Promise<void> {
  await requestModelOperation('load', { model_id: params.modelId });
}

export interface UnloadModelParams {
  readonly modelId: string;
}

/** Requests unloading the currently active model. */
export async function unloadModel(params: UnloadModelParams): Promise<void> {
  await requestModelOperation('unload', { model_id: params.modelId });
}

export interface SwitchModelParams {
  readonly modelId: string;
}

/** Requests replacing the currently active model with a different one in a single operation. */
export async function switchModel(params: SwitchModelParams): Promise<void> {
  await requestModelOperation('switch', { model_id: params.modelId });
}
