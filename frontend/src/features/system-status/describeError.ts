import { NativeHostUnavailableError } from '../../api/errors';

/**
 * The provider-unreachable message shown on every screen's error state -
 * except now it is only shown for the cause it actually describes: a real
 * transport/HTTP/schema failure (network failure, timeout, non-2xx, or a
 * schema mismatch from `StatusSchema.parse`). A raw `error.message` from
 * `fetch` is an implementation detail an operator can't act on, so it is
 * never surfaced here - but "can't act on the detail" is not license to
 * collapse genuinely different causes into one sentence that is sometimes
 * simply wrong.
 *
 * `bridgeTransport` (see `src/api/transport/bridgeTransport.ts`) rejects
 * with a typed `NativeHostUnavailableError` for exactly one condition that
 * means something quite different from "the provider is down": the native
 * console host is entirely absent. That is branched on via
 * `error instanceof NativeHostUnavailableError` - never by inspecting
 * `error.message` - because message text is prose for a human, not a
 * contract this function should depend on.
 *
 * Through Sprint 4 there was a second, distinct branch here for
 * `error.reason === 'host-not-wired'` (the host present but its bridge not
 * yet implemented). The native bridge work wired it for real, so that state can no
 * longer occur - see `errors.ts` for the reason type it was removed from
 * alongside this branch.
 */
export function describeStatusError(error: unknown): string {
  if (error instanceof NativeHostUnavailableError) {
    return 'IA Local needs the native console host. Open the console inside cia-console.exe - this build is running outside it (a browser tab or `vite dev`), so there is no way to reach the provider from here.';
  }

  return 'IA Local is not reachable right now. Check that the provider (cia-edge) is running, then retry.';
}
