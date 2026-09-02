import { useStatusQuery } from '../../api/queries/useStatus';

export type HealthSummary =
  | { state: 'loading' }
  | { state: 'error' }
  | { state: 'ready' }
  | { state: 'not-ready' };

/**
 * The AppShell top bar's compact health indicator. Deliberately its own
 * tiny hook (rather than reusing `useOverviewViewModel`) so the shell never
 * has to know about Overview's richer readiness-explanation shape - it only
 * needs a four-way state. TanStack Query dedupes by query key, so this and
 * `useOverviewViewModel`/`useSystemViewModel` share one cached fetch/poll
 * cycle rather than issuing three separate requests.
 */
export function useHealthSummary(): HealthSummary {
  const { data, isPending, isError } = useStatusQuery();

  if (isPending) return { state: 'loading' };
  if (isError) return { state: 'error' };
  return data.ready ? { state: 'ready' } : { state: 'not-ready' };
}
