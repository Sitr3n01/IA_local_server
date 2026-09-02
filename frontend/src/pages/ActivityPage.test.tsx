import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import fixture from '../api/__fixtures__/status.sample.json';
import { ActivityPage } from './ActivityPage';

const requestMock = vi.fn();

vi.mock('../api/transport', () => ({
  getTransport: async () => ({ request: requestMock }),
}));

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ActivityPage />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  requestMock.mockReset();
});

describe('ActivityPage: loading', () => {
  it('renders a loading announcement before data arrives', () => {
    requestMock.mockImplementation(() => new Promise(() => {}));
    renderPage();
    expect(screen.getByRole('status')).toBeTruthy();
  });
});

describe('ActivityPage: error', () => {
  it('renders an operator-actionable message with no raw error text and a retry action', async () => {
    requestMock.mockRejectedValue(new Error('ECONNREFUSED'));
    renderPage();

    expect(await screen.findByRole('alert')).toBeTruthy();
    expect(screen.getByText(/IA Local is not reachable/)).toBeTruthy();
    expect(screen.queryByText('ECONNREFUSED')).toBeNull();
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy();
  });
});

describe('ActivityPage: empty', () => {
  it('shows "no requests recorded yet" when recent_events is empty, while gate and GPU memory still populate', async () => {
    requestMock.mockResolvedValue({ ...fixture, recent_events: [] });
    renderPage();

    expect(await screen.findByText('No requests recorded yet.')).toBeTruthy();
    expect(screen.getByText('Admission gate')).toBeTruthy();
    expect(screen.getByText('GPU memory')).toBeTruthy();
  });
});

describe('ActivityPage: admission gate', () => {
  it('renders wait timeout, rejected total, and timed out total - fields never shown anywhere before this screen', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    expect(await screen.findByText('Wait timeout')).toBeTruthy();
    // Fixture's gate.wait_timeout_seconds is 120.
    expect(screen.getByText('120s')).toBeTruthy();
    expect(screen.getByText('Rejected (total)')).toBeTruthy();
    expect(screen.getByText('Timed out (total)')).toBeTruthy();
  });

  it('shows the gate as Idle when active and queued are both 0 (the fixture default)', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    expect(await screen.findByText('Idle')).toBeTruthy();
    expect(screen.getByText('No requests are active or queued right now.')).toBeTruthy();
  });

  it('shows the gate as Saturated, with text pairing the icon (not colour alone), when active reaches max_active', async () => {
    requestMock.mockResolvedValue({ ...fixture, gate: { ...fixture.gate, active: 1, max_active: 1 } });
    renderPage();

    expect(await screen.findByText('Saturated')).toBeTruthy();
    expect(screen.getByText(/gate is at its limit right now/)).toBeTruthy();
  });

  it('renders active/max and queued/max saturation as text, not colour alone', async () => {
    requestMock.mockResolvedValue({ ...fixture, gate: { ...fixture.gate, active: 1, max_active: 1, queued: 2, max_queue: 4 } });
    renderPage();

    expect(await screen.findByText(/1 \/ 1/)).toBeTruthy();
    expect(screen.getByText(/2 \/ 4/)).toBeTruthy();
  });
});

describe('ActivityPage: GPU memory (moved from System)', () => {
  it('renders GPU memory fields, formatting MiB and never rendering a null field as 0', async () => {
    requestMock.mockResolvedValue({
      ...fixture,
      gpu_memory: { ...fixture.gpu_memory, dedicated_mib: null },
    });
    renderPage();

    expect(await screen.findByText('GPU memory')).toBeTruthy();
    expect(screen.getByText('not measured')).toBeTruthy();
    expect(screen.queryByText('0 MiB')).toBeNull();
  });

  it('states plainly that GPU memory is a single reading, not a history', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();
    expect(await screen.findByText(/does not report GPU memory over time/)).toBeTruthy();
  });
});

describe('ActivityPage: maintenance (Sprint 7)', () => {
  afterEach(() => {
    delete (window as typeof window & { chrome?: unknown }).chrome;
  });

  it('offers "Request drain" (never "Request resume") when running, and shows the drain-progress counters', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    expect(await screen.findByText('Maintenance')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Request drain' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Request resume' })).toBeNull();

    // The three fields no earlier screen has ever rendered.
    expect(screen.getByText('Active requests')).toBeTruthy();
    expect(screen.getByText('Queued requests')).toBeTruthy();
    expect(screen.getByText('Rejected while draining (total)')).toBeTruthy();
  });

  it('offers "Request resume" (never "Request drain") while draining', async () => {
    requestMock.mockResolvedValue({
      ...fixture,
      maintenance: { state: 'draining', draining: true, drained: false, active: 2, queued: 1, rejected_total: 3 },
    });
    renderPage();

    expect(await screen.findByRole('button', { name: 'Request resume' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Request drain' })).toBeNull();
  });

  it('offers "Request resume" (never "Request drain") once fully drained', async () => {
    requestMock.mockResolvedValue({
      ...fixture,
      maintenance: { state: 'maintenance', draining: true, drained: true, active: 0, queued: 0, rejected_total: 5 },
    });
    renderPage();

    expect(await screen.findByRole('button', { name: 'Request resume' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Request drain' })).toBeNull();
  });

  it('renders the actual drain-progress counter values from the payload, not zeros', async () => {
    requestMock.mockResolvedValue({
      ...fixture,
      maintenance: { state: 'draining', draining: true, drained: false, active: 4, queued: 6, rejected_total: 9 },
    });
    renderPage();

    await screen.findByText('Maintenance');
    const activeValue = screen.getByText('Active requests').parentElement?.querySelector('dd');
    const queuedValue = screen.getByText('Queued requests').parentElement?.querySelector('dd');
    const rejectedValue = screen.getByText('Rejected while draining (total)').parentElement?.querySelector('dd');
    expect(activeValue?.textContent).toBe('4');
    expect(queuedValue?.textContent).toBe('6');
    expect(rejectedValue?.textContent).toBe('9');
  });

  it('states all three consequences plainly next to the control, not in a footnote', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    await screen.findByText('Maintenance');
    expect(screen.getByText(/stops readiness routing/)).toBeTruthy();
    expect(screen.getByText(/reports not-ready/)).toBeTruthy();
    expect(screen.getByText(/does not unblock model control/)).toBeTruthy();
    expect(screen.getByText(/stay refused \(409\)/)).toBeTruthy();
    expect(screen.getByText(/does not survive a restart/)).toBeTruthy();
    expect(screen.getByText(/process-local state/)).toBeTruthy();
  });

  it('clicking the offered action, under jsdom (no native host), surfaces the honest no-native-host refusal', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    const button = await screen.findByRole('button', { name: 'Request drain' });
    fireEvent.click(button);

    const outcome = await screen.findByText(/needs the native console host/);
    expect(outcome.closest('[role="alert"]')).toBeTruthy();
    expect(document.querySelector('.activity-maintenance__action-outcome--no-native-host')).toBeTruthy();
    expect(document.querySelector('.activity-maintenance__action-outcome--error')).toBeNull();
  });
});

describe('ActivityPage: request timeline - default calm, details on demand', () => {
  it('shows the calm summary (requests/errors/slowest/span) by default, with the full table collapsed', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    expect(await screen.findByText('Request timeline')).toBeTruthy();
    expect(screen.getByText('Requests')).toBeTruthy();
    expect(screen.getByText('Errors')).toBeTruthy();
    expect(screen.getByText('Slowest')).toBeTruthy();
    expect(screen.getByText('Span')).toBeTruthy();
    // Fixture has 18 recent_events.
    expect(screen.getByText('18')).toBeTruthy();

    // The full per-request table is not rendered until expanded.
    expect(screen.queryByRole('columnheader', { name: 'Request ID' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Show request details' })).toBeTruthy();
  });

  it('expands to reveal the full per-request table when the disclosure is toggled, and can be collapsed again', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    const toggle = await screen.findByRole('button', { name: 'Show request details' });
    expect(toggle.getAttribute('aria-expanded')).toBe('false');

    fireEvent.click(toggle);

    expect(screen.getByRole('columnheader', { name: 'Request ID' })).toBeTruthy();
    expect(screen.getAllByRole('row').length).toBeGreaterThan(1);
    const hideButton = screen.getByRole('button', { name: 'Hide request details' });
    expect(hideButton.getAttribute('aria-expanded')).toBe('true');

    fireEvent.click(hideButton);
    expect(screen.queryByRole('columnheader', { name: 'Request ID' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Show request details' })).toBeTruthy();
  });

  it('counts errors (status >= 400) in the summary using the fixture\'s real mix of statuses', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    await screen.findByText('Request timeline');
    // Fixture recent_events: four 503s and one 401 => 5 errors.
    const errorsLabel = screen.getByText('Errors');
    const errorsValue = errorsLabel.parentElement?.querySelector('dd');
    expect(errorsValue?.textContent).toBe('5');
  });

  it('never shows the raw table by default even though events exist', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();
    await screen.findByText('Request timeline');

    // The panel stays mounted while collapsed so the toggle's aria-controls
    // always resolves to a real element (see its render site). "Not shown"
    // is therefore asserted as "not in the accessibility tree" - the
    // property that actually matters - rather than "not in the DOM", which
    // is no longer the mechanism. Role queries exclude hidden subtrees;
    // a text query would find it and would be asserting the wrong thing.
    expect(screen.queryByRole('region', { name: 'Full request log' })).toBeNull();

    const toggle = screen.getByRole('button', { name: /Show request details/ });
    const panel = document.getElementById(toggle.getAttribute('aria-controls') as string);
    expect(panel).not.toBeNull();
    expect((panel as HTMLElement).hidden).toBe(true);
  });

  it('keeps the aria-controls target present in both collapsed and expanded states', async () => {
    // The defect this pins: while the panel was conditionally rendered, the
    // toggle advertised aria-controls pointing at an id that existed only
    // when open, so a screen reader following that reference while collapsed
    // found nothing at all.
    requestMock.mockResolvedValue(fixture);
    renderPage();
    const toggle = await screen.findByRole('button', { name: /Show request details/ });
    const controls = toggle.getAttribute('aria-controls');
    expect(controls).toBeTruthy();
    expect(document.getElementById(controls as string)).not.toBeNull();

    fireEvent.click(toggle);
    const panel = document.getElementById(controls as string);
    expect(panel).not.toBeNull();
    expect((panel as HTMLElement).hidden).toBe(false);
    expect(screen.getByRole('region', { name: 'Full request log' })).toBeTruthy();
  });
});
