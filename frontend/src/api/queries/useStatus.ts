import { useQuery } from '@tanstack/react-query';
import { getTransport } from '../transport';
import { StatusSchema, type Status } from '../schemas/status';

export const STATUS_QUERY_KEY = ['status'] as const;

/**
 * The only place in the app allowed to treat a status response as trustworthy
 * data: it goes transport -> Zod parse -> typed `Status`. Nothing upstream of
 * this function (pages, features) may call the transport directly or skip
 * the parse - see eslint.config.js for the rule that enforces the import
 * boundary.
 */
async function fetchStatus(): Promise<Status> {
  const transport = await getTransport();
  const raw = await transport.request<Status>('status');
  return StatusSchema.parse(raw);
}

export function useStatusQuery() {
  return useQuery({
    queryKey: STATUS_QUERY_KEY,
    queryFn: fetchStatus,
    // The status endpoint is cheap and operator-facing; poll it rather than
    // requiring a manual refresh. Sprint-later work may replace this with a
    // push channel over the native bridge instead of polling.
    refetchInterval: 5_000,
  });
}
