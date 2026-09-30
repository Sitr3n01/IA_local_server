import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import fixture from '../api/__fixtures__/status.sample.json';
import { SystemPage } from './SystemPage';

const requestMock = vi.fn();

vi.mock('../api/transport', () => ({
  getTransport: async () => ({ request: requestMock }),
}));

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SystemPage />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  requestMock.mockReset();
});

describe('SystemPage: loading', () => {
  it('renders a loading announcement before data arrives', () => {
    requestMock.mockImplementation(() => new Promise(() => {}));
    renderPage();
    expect(screen.getByRole('status')).toBeTruthy();
  });
});

describe('SystemPage: error', () => {
  it('renders an operator-actionable message with no raw error text and a retry action', async () => {
    requestMock.mockRejectedValue(new Error('ECONNREFUSED'));
    renderPage();

    expect(await screen.findByRole('alert')).toBeTruthy();
    expect(screen.getByText(/IA Local is not reachable/)).toBeTruthy();
    expect(screen.queryByText('ECONNREFUSED')).toBeNull();
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy();
  });
});

describe('SystemPage: empty', () => {
  it('renders deployment/upstream/uptime normally when runtimes is empty (the new "no-runtimes" empty reason)', async () => {
    requestMock.mockResolvedValue({ ...fixture, runtimes: [] });
    renderPage();

    expect(await screen.findByText('No runtime information reported.')).toBeTruthy();
    // Deployment still renders from the same payload.
    expect(screen.getByText('canary')).toBeTruthy();
  });

  it('shows "no deployment recorded" when deployment is absent (optional field)', async () => {
    const { deployment: _deployment, ...withoutDeployment } = fixture as Record<string, unknown>;
    requestMock.mockResolvedValue(withoutDeployment);
    renderPage();

    expect(await screen.findByText('No deployment recorded yet on this machine.')).toBeTruthy();
  });
});

describe('SystemPage: populated', () => {
  it('renders deployment/release identity fields from the fixture', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    expect(await screen.findByText('canary')).toBeTruthy();
    expect(screen.getByText('claude-model-swap-20260827-1')).toBeTruthy();
    expect(screen.getByText('9fb770c8f36322ccf3775118400e222fe55f84fa')).toBeTruthy();
  });

  it('renders runtime identity for each distinct runtime', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    expect(await screen.findByText('unsloth-runtime-candidate')).toBeTruthy();
    expect(screen.getByText('amd-rocm-qwen38')).toBeTruthy();
    expect(screen.getAllByText('llama.cpp').length).toBeGreaterThan(0);
  });

  it('renders upstream reachability paired with text, not colour alone', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();
    // "Reachable" appears both as the field's <dt> label and its <span> value.
    const matches = await screen.findAllByText('Reachable');
    expect(matches.length).toBeGreaterThanOrEqual(2);
  });

  it('renders uptime in both human and raw-seconds form', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();
    expect(await screen.findByText('2h 40m 20s (9620s)')).toBeTruthy();
  });

  it('no longer renders gate counters, GPU memory, or the recent-events table - those moved to Activity in Sprint 6', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    await screen.findByText('canary');
    expect(screen.queryByText('Gate counters')).toBeNull();
    expect(screen.queryByText('GPU memory')).toBeNull();
    expect(screen.queryByText('Recent events')).toBeNull();
    expect(screen.queryByRole('columnheader', { name: 'Request ID' })).toBeNull();
  });
});
