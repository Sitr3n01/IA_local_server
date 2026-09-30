/**
 * Error types shared between the transport layer and every layer above it
 * (features, pages). This file is the one place both sides may import from
 * without tripping the transport import ban in eslint.config.js:
 * `src/api/transport/*` may only be imported from within `src/api/`, but a
 * type describing *what a transport rejection means* has to be visible to
 * whatever screen renders that rejection - which is almost never inside
 * `src/api/`. `src/api/errors.ts` carries no transport code itself (no
 * `fetch`, no `window.chrome.webview`), so it is importable from anywhere.
 */

/**
 * Distinguishes why `bridgeTransport` could not get a request to - or an
 * answer from - the native console host. Callers must branch on this field,
 * never on `error.message`: message text is prose for a human, not a stable
 * contract.
 *
 *  - `host-not-present`: `window.chrome.webview` does not exist at all -
 *    this build is running outside `cia-console.exe` (a plain browser tab,
 *    or `vite dev`). There is no host on the other end, full stop.
 *
 * A second reason, `host-not-wired`, existed through Sprint 4: the host
 * object was present but the message-bridge wiring on the other end wasn't
 * implemented yet. The native bridge work wired it (see `bridgeTransport.ts`), so that
 * state can no longer occur and the reason was removed rather than kept
 * around as a value nothing ever produces - see `describeError.ts` and
 * `models.ts` for the branches that were removed alongside it.
 */
export type NativeHostUnavailableReason = 'host-not-present';

/**
 * Thrown by `bridgeTransport` (see `src/api/transport/bridgeTransport.ts`)
 * when `window.chrome.webview` is absent, and re-thrown with a UI-specific
 * message by `src/api/commands/models.ts`. `reason` is what every caller -
 * `describeStatusError`, `useModelAction` - must switch on.
 */
export class NativeHostUnavailableError extends Error {
  readonly reason: NativeHostUnavailableReason;

  constructor(message: string, reason: NativeHostUnavailableReason) {
    super(message);
    this.name = 'NativeHostUnavailableError';
    this.reason = reason;
  }
}
