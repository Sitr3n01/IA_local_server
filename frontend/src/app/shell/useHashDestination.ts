import { useCallback, useSyncExternalStore } from 'react';
import { NAV_DESTINATIONS, type DestinationId } from './navigation';

export const DEFAULT_DESTINATION: DestinationId = NAV_DESTINATIONS[0]?.id ?? 'overview';

/**
 * Which destination is selected, kept in the URL fragment.
 *
 * Before this, the selected destination lived in `useState` and nothing else.
 * A reload always landed on Overview no matter where the operator had been,
 * the browser's Back button did nothing, and there was no way to point someone
 * at a screen. The fragment fixes all three and costs no dependency.
 *
 * The fragment rather than a path, for two reasons that are specific to how
 * this console is served. It is loaded from a WebView2 virtual-host mapping
 * over a folder - there is no server behind it to answer `GET /models`, so a
 * real path would 404 on reload. And `main.tsx` already routes the dev-only
 * gallery off `location.pathname`, which stays untouched this way.
 *
 * `useSyncExternalStore` rather than `useState` + an effect: the URL is an
 * external mutable source, and this is exactly the subscription React
 * provides for one. It removes the class of bug where an effect writes state
 * a render later and the two disagree in between.
 *
 * An unrecognised fragment renders the default destination rather than an
 * error screen. It is deliberately not rewritten to match: `history.replaceState`
 * does not fire `hashchange`, so rewriting would leave this hook's snapshot
 * stale behind the URL it just changed - trading a cosmetic mismatch for a
 * real state bug. It stays cosmetic in practice because the shipped console
 * runs in a WebView2 window with no address bar, so the only way to reach a
 * bad fragment is to type one in `vite dev`.
 */
const HASH_PREFIX = '#/';

function subscribe(onStoreChange: () => void): () => void {
  window.addEventListener('hashchange', onStoreChange);
  return () => window.removeEventListener('hashchange', onStoreChange);
}

function readHash(): string {
  return window.location.hash;
}

/** Server snapshot: no DOM, so nothing is selected and the default stands. */
function readEmpty(): string {
  return '';
}

export function parseDestination(hash: string): DestinationId | undefined {
  if (!hash.startsWith(HASH_PREFIX)) return undefined;
  const candidate = hash.slice(HASH_PREFIX.length);
  // Whole-string match against the closed union's own source of truth, never a
  // prefix or substring test - `#/models-experimental` must not resolve to
  // `models`.
  return NAV_DESTINATIONS.find((destination) => destination.id === candidate)?.id;
}

export function destinationHash(destination: DestinationId): string {
  return `${HASH_PREFIX}${destination}`;
}

export function useHashDestination(): [DestinationId, (destination: DestinationId) => void] {
  const hash = useSyncExternalStore(subscribe, readHash, readEmpty);
  const destination = parseDestination(hash) ?? DEFAULT_DESTINATION;

  const navigate = useCallback((next: DestinationId) => {
    // Assigning `location.hash` pushes a history entry, which is what makes
    // Back return to the previous screen.
    window.location.hash = destinationHash(next);
  }, []);

  return [destination, navigate];
}
