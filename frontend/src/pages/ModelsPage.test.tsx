import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import fixture from '../api/__fixtures__/status.sample.json';
import { ModelsPage } from './ModelsPage';

/*
 * `.model-entry`, not `.model-card`. Sprint 10 removed the card: a model is
 * an entry in a list now, separated from its neighbours by a hairline
 * rather than wrapped in a bordered, rounded surface of its own. The class
 * was renamed with the thing it names, and these queries follow it.
 *
 * Nothing about what is asserted changed. Every scoping query below still
 * resolves to the one element that contains exactly one model's name,
 * facts, action and disclosures - which is the property these tests
 * actually depend on, and the reason they scope at all (six models share
 * the label "Request load").
 */

const requestMock = vi.fn();

vi.mock('../api/transport', () => ({
  getTransport: async () => ({ request: requestMock }),
}));

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ModelsPage />
    </QueryClientProvider>,
  );
}

/** Fixture override: makes model_statuses[0] (gemma) admittable right now, so its action button is enabled and clickable. */
function withGemmaAvailable(overrides: Record<string, unknown> = {}) {
  return {
    ...fixture,
    ...overrides,
    model_statuses: fixture.model_statuses.map((model, index) =>
      index === 0 ? { ...model, available: true } : model,
    ),
  };
}

afterEach(() => {
  requestMock.mockReset();
  delete (window as typeof window & { chrome?: unknown }).chrome;
});

describe('ModelsPage: loading', () => {
  it('renders a loading announcement before data arrives', () => {
    requestMock.mockImplementation(() => new Promise(() => {}));
    renderPage();
    expect(screen.getByRole('status')).toBeTruthy();
  });
});

describe('ModelsPage: error', () => {
  it('renders an operator-actionable message with no raw error text and a retry action', async () => {
    requestMock.mockRejectedValue(new Error('fetch failed: ECONNREFUSED'));
    renderPage();

    expect(await screen.findByRole('alert')).toBeTruthy();
    expect(screen.getByText(/IA Local is not reachable/)).toBeTruthy();
    expect(screen.queryByText(/ECONNREFUSED/)).toBeNull();
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy();
  });
});

describe('ModelsPage: empty', () => {
  it('shows "no models configured" when model_statuses/models are empty', async () => {
    requestMock.mockResolvedValue({ ...fixture, model_statuses: [], models: [] });
    renderPage();

    expect(await screen.findByText('No models are configured on this provider yet.')).toBeTruthy();
  });
});

describe('ModelsPage: primary tier', () => {
  it('shows display name, loaded state, availability, and context window for every model - and nothing more by default', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    expect(await screen.findByText('Gemma 4 12B QAT UD Q4_K_XL - 128k context')).toBeTruthy();
    // Fixture: all six models report active: false, available: false.
    expect(screen.getAllByText('Not loaded').length).toBe(6);
    expect(screen.getAllByText('Not available now').length).toBe(6);
    // Several fixture models share the same context_tokens value (e.g. 131072
    // for gemma, the agent Qwen, and the MoE fixture) - assert presence, not uniqueness.
    expect(screen.getAllByText('131,072 tokens').length).toBeGreaterThan(0);
    expect(screen.getAllByText('262,144 tokens').length).toBeGreaterThan(0);

    // Secondary/advanced content is not rendered until expanded - in
    // particular, the primary surface never carries "the capacity verdict
    // in full" (that full sentence is secondary-tier only, reserved for
    // "Show capacity details"). The always-visible action caption instead
    // states a short, generic reason.
    // Both disclosure panels stay mounted while collapsed so each toggle's
    // aria-controls resolves to a real element in both states (see their
    // render sites). "Not rendered by default" is therefore asserted against
    // the accessibility tree, which is the property that matters - role and
    // *ByRole queries skip hidden subtrees, while a bare text query would
    // find the hidden content and assert the wrong thing.
    expect(screen.queryByRole('region', { name: /advanced detail/ })).toBeNull();
    expect(screen.queryByRole('region', { name: /capacity details/ })).toBeNull();
    expect(
      screen.getAllByText('Not offered right now because this model cannot be admitted - see "Show capacity details" below for why.')
        .length,
    ).toBe(6);
  });

  it('renders "Loaded" for whichever model has active: true', async () => {
    const status = {
      ...fixture,
      active_model: 'gemma4-12b-qat-ud-q4xl',
      model_statuses: fixture.model_statuses.map((model) =>
        model.id === 'gemma4-12b-qat-ud-q4xl' ? { ...model, active: true } : model,
      ),
    };
    requestMock.mockResolvedValue(status);
    renderPage();

    expect(await screen.findByText('Loaded')).toBeTruthy();
    expect(screen.getAllByText('Not loaded').length).toBe(5);
  });
});

describe('ModelsPage: the exact experience for a model reporting insufficient_physical_memory', () => {
  it('shows the full reused shortfall sentence once capacity details are expanded', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    const gemmaHeading = await screen.findByText('Gemma 4 12B QAT UD Q4_K_XL - 128k context');
    const card = gemmaHeading.closest('.model-entry') as HTMLElement;
    fireEvent.click(within(card).getByRole('button', { name: 'Show capacity details' }));

    // The full sentence renders exactly once - as the secondary tier's
    // verdict - never duplicated onto the always-visible action caption.
    const matches = within(card).getAllByText(
      'Not enough physical memory: 3.31 GiB available, but this model needs 13.7 GiB plus a 2 GiB safety reserve (15.7 GiB total) — short by 12.39 GiB.',
    );
    expect(matches.length).toBe(1);
  });
});

describe('ModelsPage: capacity ResourceMeter null handling', () => {
  it('renders "not measured" (never 0) for a null capacity operand, once expanded', async () => {
    const status = {
      ...fixture,
      model_statuses: fixture.model_statuses.map((model, index) =>
        index === 0 ? { ...model, capacity: { ...model.capacity, physical_headroom_gib: null } } : model,
      ),
    };
    requestMock.mockResolvedValue(status);
    renderPage();

    const gemmaHeading = await screen.findByText('Gemma 4 12B QAT UD Q4_K_XL - 128k context');
    const card = gemmaHeading.closest('.model-entry') as HTMLElement;
    fireEvent.click(within(card).getByRole('button', { name: 'Show capacity details' }));

    // "Not measured" (the verdict badge + the track's own label) appears
    // twice for the one meter (physical) whose operand was nulled above.
    expect(within(card).getAllByText('Not measured').length).toBeGreaterThan(0);
    expect(within(card).queryByText('0 GiB')).toBeNull();
  });
});

describe('ModelsPage: capability flags come from the payload, never a model id', () => {
  it('shows Function calling for the model whose payload reports it, and omits it for one whose id also looks agentic but never sent the flag', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    const agentHeading = await screen.findByText('Qwen3.8 Agent 128k - Q3_K_XL / KV q4_0 - daily agentic coding');
    const agentCard = agentHeading.closest('.model-entry') as HTMLElement;
    fireEvent.click(within(agentCard).getByRole('button', { name: 'Show capacity details' }));
    expect(within(agentCard).getByText('Function calling')).toBeTruthy();

    // fixture-moe-35b-128k's fixture entry never sends function_calling,
    // even though "agent" language appears in its own display name too.
    const moeHeading = screen.getByText(
      'Fixture MoE 35B-A3B | IQ2_M | 128k context',
    );
    const moeCard = moeHeading.closest('.model-entry') as HTMLElement;
    fireEvent.click(within(moeCard).getByRole('button', { name: 'Show capacity details' }));
    expect(within(moeCard).queryByText('Function calling')).toBeNull();
  });
});

describe('ModelsPage: action gating', () => {
  it('disables every action with the gate-busy reason when the admission gate is active', async () => {
    // No model is active in the fixture, so every one of the six models
    // resolves to the "load" action kind - all six buttons share the label
    // "Request load", and the gate-busy state must disable every single one.
    requestMock.mockResolvedValue({ ...fixture, gate: { ...fixture.gate, active: 1 } });
    renderPage();

    const buttons = await screen.findAllByRole('button', { name: 'Request load' });
    expect(buttons.length).toBe(6);
    for (const button of buttons) {
      expect((button as HTMLButtonElement).disabled).toBe(true);
    }
    expect(screen.getAllByText(/admission gate is busy/).length).toBeGreaterThan(0);
  });

  it('offers "Request switch" (never "Request load") for a non-active model when a different model is already active', async () => {
    requestMock.mockResolvedValue({ ...fixture, active_model: 'qwen38-27b-deep-32k' });
    renderPage();

    const gemmaHeading = await screen.findByText('Gemma 4 12B QAT UD Q4_K_XL - 128k context');
    const card = gemmaHeading.closest('.model-entry') as HTMLElement;
    expect(within(card).getByRole('button', { name: 'Request switch' })).toBeTruthy();
    expect(within(card).queryByRole('button', { name: 'Request load' })).toBeNull();
  });

  it('offers "Request unload" for the currently active model, enabled', async () => {
    const status = {
      ...fixture,
      active_model: 'gemma4-12b-qat-ud-q4xl',
      model_statuses: fixture.model_statuses.map((model) =>
        model.id === 'gemma4-12b-qat-ud-q4xl' ? { ...model, active: true } : model,
      ),
    };
    requestMock.mockResolvedValue(status);
    renderPage();

    const gemmaHeading = await screen.findByText('Gemma 4 12B QAT UD Q4_K_XL - 128k context');
    const card = gemmaHeading.closest('.model-entry') as HTMLElement;
    const button = within(card).getByRole('button', { name: 'Request unload' });
    expect((button as HTMLButtonElement).disabled).toBe(false);
  });
});

describe('ModelsPage: the no-native-host refusal has its own distinct state', () => {
  it('clicking an enabled action, under jsdom (no window.chrome.webview, like `vite dev`), surfaces the honest refusal - not a fake success', async () => {
    requestMock.mockResolvedValue(withGemmaAvailable());
    renderPage();

    // Only gemma (model_statuses[0]) was made available above - every other
    // model still shares the "Request load" label but disabled, so this
    // scopes to gemma's own card rather than querying the label globally.
    const gemmaHeading = await screen.findByText('Gemma 4 12B QAT UD Q4_K_XL - 128k context');
    const card = gemmaHeading.closest('.model-entry') as HTMLElement;
    const button = within(card).getByRole('button', { name: 'Request load' });
    expect((button as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(button);

    const outcome = await within(card).findByText(/needs the native console host/);
    expect(outcome.closest('[role="alert"]')).toBeTruthy();
    expect(card.querySelector('.model-entry__outcome--no-native-host')).toBeTruthy();
    // Never a generic error class for this specific refusal.
    expect(card.querySelector('.model-entry__outcome--error')).toBeNull();
  });
});

describe('ModelsPage: advanced tier is nested behind its own toggle', () => {
  it('shows profile/checkpoints/runtime fields only after both "Show capacity details" and "Show advanced detail" are expanded', async () => {
    requestMock.mockResolvedValue(fixture);
    renderPage();

    const gemmaHeading = await screen.findByText('Gemma 4 12B QAT UD Q4_K_XL - 128k context');
    const card = gemmaHeading.closest('.model-entry') as HTMLElement;

    // Asserted through the accessibility tree rather than by text presence:
    // the panels stay mounted while collapsed so their aria-controls targets
    // always exist, so `queryByText` would find hidden content.
    expect(within(card).queryByRole('region', { name: /advanced detail/ })).toBeNull();

    fireEvent.click(within(card).getByRole('button', { name: 'Show capacity details' }));
    // Capacity details are now open; advanced is nested inside and still not.
    expect(within(card).queryByRole('region', { name: /advanced detail/ })).toBeNull();

    fireEvent.click(within(card).getByRole('button', { name: 'Show advanced detail' }));
    expect(within(card).getByRole('region', { name: /advanced detail/ })).toBeTruthy();
    expect(within(card).getByText('Weights')).toBeTruthy();
    expect(within(card).getByText('gemma-4-12B-it-qat-UD-Q4_K_XL')).toBeTruthy();
    expect(within(card).getByText('unsloth-runtime-candidate')).toBeTruthy();
    expect(within(card).getByText('7f15112cad13')).toBeTruthy();
  });
});
