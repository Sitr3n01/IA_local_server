import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // The console has no network access in production (native bridge
      // only) and a flaky loopback HTTP call in dev - one retry is enough to
      // ride out a transient hiccup without masking a real failure behind a
      // long retry storm.
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

export function QueryProvider({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}
