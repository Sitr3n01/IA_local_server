/**
 * Maintenance-lifecycle mutations (drain / resume) for the Activity screen's
 * maintenance section.
 *
 * Same routing rule as `./models.ts` (read that file's doc comment first for
 * the full ADR 0018 control 1 rationale): these go through `bridgeTransport`
 * directly, never through `getTransport()`, so a drain/resume request can
 * never reach cia-edge's dev-only HTTP surface even under `vite dev` - only
 * the native bridge shows the Win32 confirmation dialog ADR 0018 requires
 * before a mutation runs.
 *
 * CRITICAL difference from `./models.ts`: drain and resume must carry NO
 * `model_id`. `cmd/cia-console/bridge.go`'s `modelIDForbidden` map lists
 * both `KindDrain` and `KindResume` - `validateMutationModelID` rejects
 * either operation outright with `model_id_not_permitted` if a `model_id`
 * is present at all (even an empty string is fine; anything non-blank is
 * not), mirroring a check the administrative pipe itself already makes
 * rather than forwarding a malformed request and letting the pipe be the
 * only thing that notices. So this module calls `bridgeTransport.request(op)`
 * with no second argument at all. `bridgeTransport`'s own `extractModelId`
 * (see `../transport/bridgeTransport.ts`) returns `undefined` for anything
 * that isn't an object carrying a string `model_id` - `undefined` params
 * included - and the envelope it builds (`modelId !== undefined ? { ...,
 * model_id: modelId } : { id, kind: op }`) omits the `model_id` key
 * entirely in that case, rather than sending it as `undefined` or an empty
 * string. `maintenance.test.ts` asserts this directly against the wire
 * envelope rather than assuming it from reading the source.
 *
 * The op name sent for each call (`drain`/`resume`) matches
 * `cmd/cia-console/bridge.go`'s `KindDrain`/`KindResume` constants exactly -
 * same exact-map-lookup classification discipline as `./models.ts`.
 */
import { bridgeTransport } from '../transport/bridgeTransport';
import { NativeHostUnavailableError } from '../errors';

/**
 * Re-exported so callers (see `src/features/activity/useMaintenanceAction.ts`)
 * can keep importing it from here rather
 * than reaching into `src/api/errors.ts` themselves - mirrors `./models.ts`.
 */
export { NativeHostUnavailableError };

async function requestMaintenanceOperation(op: string): Promise<void> {
  try {
    await bridgeTransport.request(op);
  } catch (error) {
    if (error instanceof NativeHostUnavailableError) {
      throw new NativeHostUnavailableError(
        'This action needs the native console host. Open IA Local inside cia-console.exe, not a browser tab or ' +
          '`vite dev` - the native host is what shows the confirmation dialog ADR 0018 requires before the ' +
          'provider drains or resumes. This button never performs that confirmation itself.',
        'host-not-present',
      );
    }
    throw error;
  }
}

/**
 * Requests draining the provider: stop admitting new inference immediately,
 * while every request already active or already queued is left to finish
 * naturally - see `internal/edge/gate.go`'s `drain`. Idempotent server-side:
 * calling it again while already draining preserves the original drain
 * start time rather than resetting it.
 */
export async function drainProvider(): Promise<void> {
  await requestMaintenanceOperation('drain');
}

/**
 * Requests resuming the provider from a drain: start admitting new
 * inference again - see `internal/edge/gate.go`'s `resume`. Idempotent
 * server-side, and note that maintenance is process-local state (see that
 * function's doc comment): a restarted edge always comes back running,
 * whether or not this was ever called.
 */
export async function resumeProvider(): Promise<void> {
  await requestMaintenanceOperation('resume');
}
