import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import fixture from '../api/__fixtures__/status.sample.json';
import { OverviewPage } from './OverviewPage';

// Mirrors src/pages/StatusPage.test.tsx's Sprint 1 pattern: a full-module
// mock of the transport boundary, so OverviewPage never knows or cares
// which transport implementation would really be selected.
const requestMock = vi.fn();

vi.mock('../api/transport', () => ({
  getTransport: async () => ({ request: requestMock }),
}));

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <OverviewPage />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  requestMock.mockReset();
});

describe('OverviewPage: loading', () => {
  it('renders a loading announcement before data arrives', () => {
    requestMock.mockImplementation(() => new Promise(() => {}));
    renderPage();
    expect(screen.getByRole('status')).toBeTruthy();
    expect(screen.queryByText('Ready to serve')).toBeNull();
    expect(screen.queryByText('Not ready to serve')).toBeNull();
  });
});

describe('OverviewPage: error', () => {
  it('renders an operator-actionable message with no raw error text and a retry action', async () => {
    requestMock.mockRejectedValue(new Error('fetch failed: ECONNREFUSED 127.0.0.1:18091'));
    renderPage();

    expect(await screen.findByRole('alert')).toBeTruthy();
    expect(screen.getByText(/IA Local is not reachable/)).toBeTruthy();
    expect(screen.queryByText(/ECONNREFUSED/)).toBeNull();
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy();
  });
});

describe('OverviewPage: empty', () => {
  it('shows "no models deployed" when model_statuses/models are empty, while still showing gate and maintenance', async () => {
    requestMock.mockResolvedValue({ ...fixture, model_statuses: [], models: [] });
    renderPage();

    expect(await screen.findByText('No models are deployed on this provider yet.')).toBeTruthy();
    expect(screen.getByText('Admission gate')).toBeTruthy();
    expect(screen.getByText('Maintenance state')).toBeTruthy();
  });
});

describe('OverviewPage: populated', () => {
  it('renders "Not ready to serve" with the exact insufficient_physical_memory sentence from the fixture', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    expect(await screen.findByText('Not ready to serve')).toBeTruthy();
    // Fixture: physical_headroom_gib 3.31, required_physical_gib 13.7, reserve_physical_gib 2.
    expect(
      screen.getByText(
        'Not enough physical memory: 3.31 GiB available, but this model needs 13.7 GiB plus a 2 GiB safety reserve (15.7 GiB total) — short by 12.39 GiB.',
      ),
    ).toBeTruthy();
  });

  it('shows "None loaded" for an empty active_model', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();
    expect(await screen.findByText('None loaded')).toBeTruthy();
  });

  it('shows gate occupancy as active/max_active and queued/max_queue', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();
    expect(await screen.findByText('0 / 1 active · 0 / 4 queued')).toBeTruthy();
  });

  it('shows the active model\'s display name (looked up by exact id, never a substring match) when one is loaded', async () => {
    requestMock.mockResolvedValue({ ...fixture, active_model: 'gemma4-12b-qat-ud-q4xl' });
    renderPage();
    expect(await screen.findByText('Gemma 4 12B QAT UD Q4_K_XL - 128k context')).toBeTruthy();
  });

  it('shows "Ready to serve" and no failure arithmetic when ready is true', async () => {
    requestMock.mockResolvedValue({
      ...fixture,
      ready: true,
      capacity: { ...fixture.capacity, available: true, reason: 'commit_headroom_available' },
    });
    renderPage();
    expect(await screen.findByText('Ready to serve')).toBeTruthy();
    expect(screen.queryByText(/Not enough/)).toBeNull();
  });

  it('degrades an unrecognized reason to the raw code rather than a wrong sentence', async () => {
    requestMock.mockResolvedValue({
      ...fixture,
      capacity: { ...fixture.capacity, reason: 'a_future_reason_v9' },
    });
    renderPage();
    expect(await screen.findByText('Reason reported by the provider: a_future_reason_v9')).toBeTruthy();
  });

  it('renders a draining maintenance state prominently, not buried', async () => {
    requestMock.mockResolvedValue({
      ...fixture,
      maintenance: { ...fixture.maintenance, state: 'draining', draining: true },
    });
    renderPage();
    expect(await screen.findByText('Draining')).toBeTruthy();
    expect(screen.getByText(/not accepting new ones/)).toBeTruthy();
  });
});
