import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { createElement, type ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import fixture from '../../api/__fixtures__/status.sample.json';
import { useLiveAnnouncement } from './useLiveAnnouncement';

const requestMock = vi.fn();

vi.mock('../../api/transport', () => ({
  getTransport: async () => ({ request: requestMock }),
}));

afterEach(() => {
  requestMock.mockReset();
});

function harness() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, refetchInterval: false } } });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  return { client, wrapper };
}

describe('useLiveAnnouncement: silence is the normal state', () => {
  it('says nothing on the first successful poll - the operator just arrived, nothing changed', async () => {
    requestMock.mockResolvedValue(fixture);
    const { wrapper } = harness();
    const { result } = renderHook(() => useLiveAnnouncement(), { wrapper });

    await waitFor(() => expect(requestMock).toHaveBeenCalled());
    expect(result.current).toBe('');
  });

  it('says nothing when successive polls report the same state', async () => {
    requestMock.mockResolvedValue(fixture);
    const { client, wrapper } = harness();
    const { result } = renderHook(() => useLiveAnnouncement(), { wrapper });

    await waitFor(() => expect(requestMock).toHaveBeenCalled());
    await client.refetchQueries();
    await client.refetchQueries();
    expect(result.current).toBe('');
  });
});

describe('useLiveAnnouncement: announces semantic transitions only', () => {
  it('announces a readiness change, once, when ready flips', async () => {
    requestMock.mockResolvedValue({ ...fixture, ready: false });
    const { client, wrapper } = harness();
    const { result } = renderHook(() => useLiveAnnouncement(), { wrapper });

    await waitFor(() => expect(requestMock).toHaveBeenCalled());
    expect(result.current).toBe('');

    requestMock.mockResolvedValue({ ...fixture, ready: true });
    await client.refetchQueries();
    await waitFor(() => expect(result.current).toMatch(/ready to serve/));
  });

  it('announces a maintenance change using the shared vocabulary, not the raw backend state', async () => {
    requestMock.mockResolvedValue(fixture);
    const { client, wrapper } = harness();
    const { result } = renderHook(() => useLiveAnnouncement(), { wrapper });
    await waitFor(() => expect(requestMock).toHaveBeenCalled());

    requestMock.mockResolvedValue({
      ...fixture,
      maintenance: { state: 'maintenance', draining: true, drained: true, active: 0, queued: 0, rejected_total: 0 },
    });
    await client.refetchQueries();
    // "Drained", the label both screens show - never the backend constant
    // `maintenance`, which names a mode and tells an operator nothing.
    await waitFor(() => expect(result.current).toBe('Maintenance state changed to Drained.'));
  });
});

describe('useLiveAnnouncement: never speaks quantities', () => {
  it('stays silent when only the gate counters move', async () => {
    // The gate changes constantly under load. Spoken in isolation these
    // numbers mean nothing, and at a 5s poll they would bury everything else.
    requestMock.mockResolvedValue(fixture);
    const { client, wrapper } = harness();
    const { result } = renderHook(() => useLiveAnnouncement(), { wrapper });
    await waitFor(() => expect(requestMock).toHaveBeenCalled());

    requestMock.mockResolvedValue({
      ...fixture,
      gate: { ...fixture.gate, active: 1, queued: 3, rejected_total: 7, timed_out_total: 2 },
    });
    await client.refetchQueries();
    expect(result.current).toBe('');
  });

  it('stays silent when only the request log grows', async () => {
    requestMock.mockResolvedValue(fixture);
    const { client, wrapper } = harness();
    const { result } = renderHook(() => useLiveAnnouncement(), { wrapper });
    await waitFor(() => expect(requestMock).toHaveBeenCalled());

    requestMock.mockResolvedValue({ ...fixture, recent_events: [], uptime_seconds: 999_999 });
    await client.refetchQueries();
    expect(result.current).toBe('');
  });
});

describe('useLiveAnnouncement: stale and recovery', () => {
  it('announces when the provider stops answering, and again when it comes back', async () => {
    requestMock.mockResolvedValue(fixture);
    const { client, wrapper } = harness();
    const { result } = renderHook(() => useLiveAnnouncement(), { wrapper });
    await waitFor(() => expect(requestMock).toHaveBeenCalled());
    expect(result.current).toBe('');

    requestMock.mockRejectedValue(new Error('ECONNREFUSED'));
    await client.refetchQueries();
    await waitFor(() => expect(result.current).toMatch(/stopped answering/));
    // It says the figures are stale, not merely that something failed - the
    // operator is still looking at numbers and needs to know they are frozen.
    expect(result.current).toMatch(/no longer updating/);

    requestMock.mockResolvedValue(fixture);
    await client.refetchQueries();
    await waitFor(() => expect(result.current).toMatch(/answering again/));
  });

  it('never leaks a raw error string into speech', async () => {
    requestMock.mockResolvedValue(fixture);
    const { client, wrapper } = harness();
    const { result } = renderHook(() => useLiveAnnouncement(), { wrapper });
    await waitFor(() => expect(requestMock).toHaveBeenCalled());

    requestMock.mockRejectedValue(new Error('ECONNREFUSED 127.0.0.1:18091'));
    await client.refetchQueries();
    await waitFor(() => expect(result.current).not.toBe(''));
    expect(result.current).not.toMatch(/ECONNREFUSED/);
    expect(result.current).not.toMatch(/127\.0\.0\.1/);
  });
});
