import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { AppShell } from './AppShell';
import type { DestinationId } from './navigation';

afterEach(() => {
  window.localStorage.clear();
});

function Harness({
  initial = 'overview' as DestinationId,
  environment,
}: {
  initial?: DestinationId;
  environment?: string | undefined;
}) {
  const [destination, setDestination] = useState<DestinationId>(initial);
  return (
    <AppShell
      activeDestination={destination}
      onNavigate={setDestination}
      health={{ state: 'ready' }}
      environment={environment}
    >
      <p>Page content for {destination}</p>
    </AppShell>
  );
}

describe('AppShell navigation', () => {
  it('renders both destinations as real, focusable buttons reachable by keyboard', () => {
    render(<Harness />);
    const overview = screen.getByRole('button', { name: 'Overview' });
    const system = screen.getByRole('button', { name: 'System' });

    expect(overview.tagName).toBe('BUTTON');
    expect(system.tagName).toBe('BUTTON');

    system.focus();
    expect(document.activeElement).toBe(system);
  });

  it('marks only the active destination with aria-current="page"', () => {
    render(<Harness initial="overview" />);
    expect(screen.getByRole('button', { name: 'Overview' }).getAttribute('aria-current')).toBe('page');
    expect(screen.getByRole('button', { name: 'System' }).hasAttribute('aria-current')).toBe(false);
  });

  it('moves aria-current to System once it is navigated to, so the current destination is always programmatically announced', () => {
    render(<Harness initial="overview" />);
    fireEvent.click(screen.getByRole('button', { name: 'System' }));

    expect(screen.getByRole('button', { name: 'System' }).getAttribute('aria-current')).toBe('page');
    expect(screen.getByRole('button', { name: 'Overview' }).hasAttribute('aria-current')).toBe(false);
    expect(screen.getByText('Page content for system')).toBeTruthy();
  });

  it('adds Models (Sprint 4) as a real destination but still no Inference/Settings placeholder', () => {
    render(<Harness />);
    expect(screen.getByRole('button', { name: 'Models' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Inference/ })).toBeNull();
    expect(screen.queryByRole('button', { name: /Settings/ })).toBeNull();
  });

  it('keeps both nav items labelled for assistive technology while the rail is collapsed', () => {
    render(<Harness />);
    const toggle = screen.getByRole('button', { name: 'Collapse navigation' });
    fireEvent.click(toggle);

    expect(screen.getByRole('button', { name: 'Overview' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'System' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Expand navigation' })).toBeTruthy();
  });

  it('exposes the collapse control\'s current state via aria-expanded, not just its label text', () => {
    render(<Harness />);
    const collapseToggle = screen.getByRole('button', { name: 'Collapse navigation' });
    expect(collapseToggle.getAttribute('aria-expanded')).toBe('true');

    fireEvent.click(collapseToggle);
    expect(screen.getByRole('button', { name: 'Expand navigation' }).getAttribute('aria-expanded')).toBe('false');
  });

  it('renders the top bar health indicator with text, not colour alone', () => {
    render(<Harness />);
    expect(screen.getByText('Ready')).toBeTruthy();
  });

  it('names the deployment environment in the top bar, and does not repeat the destination', () => {
    // This replaces an assertion that the top bar names the active
    // destination. It did - and so did the page's own <h1> directly below it
    // and the rail's selected item to its left. One fact stated three times
    // in a single viewport, on the one strip that never scrolls away. The
    // environment is the fact that strip should carry instead: `canary` and
    // `final` look identical on every screen, and every model-lifecycle
    // button acts on whichever one is wired.
    render(<Harness initial="overview" environment="canary" />);
    expect(document.querySelector('.top-bar__environment-value')?.textContent).toBe('canary');
    expect(screen.queryByText('IA Local Console')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'System' }));
    // Still the environment, not the newly selected destination.
    expect(document.querySelector('.top-bar__environment-value')?.textContent).toBe('canary');
    expect(document.querySelector('.top-bar__product')).toBeNull();
  });

  it('renders no environment slot at all when the provider reports no deployment', () => {
    // `deployment` is `.optional()` in the schema - absent on a checkout that
    // has never been deployed. The strip must stay empty rather than invent a
    // label for an environment nobody has told it about.
    render(<Harness initial="overview" />);
    expect(document.querySelector('.top-bar__environment')).toBeNull();
  });
});

describe('AppShell health indicator states', () => {
  it('shows "Not ready" with an alert glyph when not ready', () => {
    render(
      <AppShell activeDestination="overview" onNavigate={vi.fn()} health={{ state: 'not-ready' }}>
        <p>content</p>
      </AppShell>,
    );
    expect(screen.getByText('Not ready')).toBeTruthy();
  });

  it('shows "Unreachable" when the health check itself errored', () => {
    render(
      <AppShell activeDestination="overview" onNavigate={vi.fn()} health={{ state: 'error' }}>
        <p>content</p>
      </AppShell>,
    );
    expect(screen.getByText('Unreachable')).toBeTruthy();
  });
});

describe('AppShell stale-data banner', () => {
  // The defect this pins, in two halves. The view models used to return
  // `error` on any poll failure, which unmounted the whole page - every
  // expanded disclosure and the operator's keyboard focus with it - several
  // times an hour on a machine under memory pressure, even though TanStack
  // Query still held the last good snapshot. They now keep rendering it.
  //
  // Which leaves the second half: a screen that still shows figures must not
  // let anyone read them as current. The top bar's "Unreachable" describes
  // the provider, not the numbers; this banner says the other half.
  it('says the figures on screen are the last received, not a current reading', () => {
    render(
      <AppShell activeDestination="overview" onNavigate={vi.fn()} health={{ state: 'error' }}>
        <p>page</p>
      </AppShell>,
    );
    expect(screen.getByText(/last figures received/)).toBeTruthy();
    expect(screen.getByText(/not a reading of the provider as it is at this moment/)).toBeTruthy();
  });

  it('carries no live-region role of its own - it mounts with its own message, which is never announced', () => {
    // The banner is visual only. A live region inserted into the DOM already
    // holding its text is frequently not announced at all, so giving this one
    // role="status" would have read correctly in the markup and been silent
    // in practice. Speech comes from the persistent region instead.
    const { container } = render(
      <AppShell activeDestination="overview" onNavigate={vi.fn()} health={{ state: 'error' }}>
        <p>page</p>
      </AppShell>,
    );
    const banner = container.querySelector('.app-shell__stale') as HTMLElement;
    expect(banner).toBeTruthy();
    expect(banner.getAttribute('role')).toBeNull();
    expect(banner.getAttribute('aria-live')).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('is absent in every healthy state, so it cannot become background noise', () => {
    for (const state of ['loading', 'ready', 'not-ready'] as const) {
      const { unmount } = render(
        <AppShell activeDestination="overview" onNavigate={vi.fn()} health={{ state }}>
          <p>page</p>
        </AppShell>,
      );
      expect(screen.queryByText(/last figures received/)).toBeNull();
      unmount();
    }
  });

  it('never hides the page content it is warning about', () => {
    render(
      <AppShell activeDestination="overview" onNavigate={vi.fn()} health={{ state: 'error' }}>
        <p>the last known figures</p>
      </AppShell>,
    );
    expect(screen.getByText('the last known figures')).toBeTruthy();
  });
});


describe('AppShell live region', () => {
  // The decision this pins: one persistent polite region, announcing only
  // semantic transitions - readiness, maintenance, stale and recovery - and
  // never a poll, a counter, or the queue. The status query polls every 5
  // seconds; announcing its values would speak ~720 times an hour, which is
  // indistinguishable from announcing nothing except that it also buries
  // whatever the operator was reading.
  function shell(announcement?: string) {
    return render(
      <AppShell
        activeDestination="overview"
        onNavigate={vi.fn()}
        health={{ state: 'ready' }}
        {...(announcement === undefined ? {} : { announcement })}
      >
        <p>page</p>
      </AppShell>,
    );
  }

  it('is present and empty when there is nothing to announce', () => {
    // Present, not absent: assistive technology announces mutations within an
    // existing region, so a region that appears together with its message is
    // frequently never spoken. Being empty is how it stays silent.
    shell();
    const region = screen.getByRole('status');
    expect(region).toBeTruthy();
    expect(region.textContent).toBe('');
  });

  it('is polite and atomic, never assertive - it must not interrupt', () => {
    shell();
    const region = screen.getByRole('status');
    expect(region.getAttribute('aria-live')).toBe('polite');
    expect(region.getAttribute('aria-atomic')).toBe('true');
  });

  it('keeps the same region element across renders, changing only its text', () => {
    const { rerender } = shell('');
    const before = screen.getByRole('status');
    rerender(
      <AppShell
        activeDestination="overview"
        onNavigate={vi.fn()}
        health={{ state: 'ready' }}
        announcement="IA Local is ready to serve."
      >
        <p>page</p>
      </AppShell>,
    );
    const after = screen.getByRole('status');
    expect(after).toBe(before);
    expect(after.textContent).toBe('IA Local is ready to serve.');
  });

  it('is visually hidden - every transition it speaks is already on screen', () => {
    shell('IA Local is ready to serve.');
    expect(screen.getByRole('status').className).toContain('ds-visually-hidden');
  });
});
