import { useCallback, useState } from 'react';

const STORAGE_KEY = 'ia-console:nav-rail-collapsed';

/**
 * Reads the persisted collapse choice. Wrapped in try/catch per the Sprint 3
 * brief: "Persist the collapsed/expanded choice in localStorage, wrapped in
 * try/catch, rendering correctly when it throws or returns nothing." Access
 * can throw (private browsing in some engines, a WebView2 host with storage
 * disabled) and `getItem` can validly return `null` (first run) - both fall
 * back to the same default, expanded (`false`).
 */
function readPersistedCollapsed(): boolean {
  try {
    return window.localStorage.getItem(STORAGE_KEY) === 'true';
  } catch {
    return false;
  }
}

function persistCollapsed(value: boolean): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, value ? 'true' : 'false');
  } catch {
    // Persistence is a convenience, not a requirement - a throwing storage
    // backend must not prevent the rail from collapsing/expanding in memory.
  }
}

/**
 * The nav rail's collapsed/expanded state, persisted across sessions. Only
 * `AppShell` owns this - it is UI chrome state, not application data, so it
 * never touches `src/api/*`.
 */
export function useRailCollapsed(): [boolean, (next: boolean) => void] {
  const [collapsed, setCollapsedState] = useState<boolean>(() => readPersistedCollapsed());

  const setCollapsed = useCallback((next: boolean) => {
    setCollapsedState(next);
    persistCollapsed(next);
  }, []);

  return [collapsed, setCollapsed];
}
