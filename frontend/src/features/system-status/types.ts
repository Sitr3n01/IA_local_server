/**
 * The four states every screen must render, per the Sprint 3 brief:
 * loading, error, empty, and populated - built from the real query state,
 * never from a prop toggle. `empty` carries `data` too (not just a bare
 * flag) so a page can still show whatever aggregate fields remain
 * meaningful (e.g. Overview's gate/maintenance state) alongside the
 * empty-specific messaging, rather than blanking the whole screen.
 */
export type ViewModel<TData, TEmptyReason extends string> =
  | { state: 'loading' }
  | { state: 'error'; message: string; retry: () => void }
  | { state: 'empty'; reason: TEmptyReason; data: TData }
  | { state: 'populated'; data: TData };
