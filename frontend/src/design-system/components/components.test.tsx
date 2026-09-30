import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Notice } from './Notice/Notice';
import { StatGroup } from './StatGroup/StatGroup';

describe('Notice', () => {
  it('sets no ARIA role of its own, so the caller decides what the message is', () => {
    // The shell's stale-data banner deliberately passes none: a live region
    // inserted into the DOM already holding its text is frequently never
    // announced, so a role here would have read correctly in the markup and
    // been silent in practice (see AppShell.tsx).
    const { container } = render(<Notice tone="warning">Figures may be stale.</Notice>);
    const notice = container.firstElementChild as HTMLElement;

    expect(notice.getAttribute('role')).toBeNull();
    expect(notice.getAttribute('aria-live')).toBeNull();
  });

  it('passes a caller-supplied role straight through', () => {
    render(
      <Notice tone="danger" role="alert">
        IA Local is not reachable right now.
      </Notice>,
    );
    expect(screen.getByRole('alert').textContent).toContain('IA Local is not reachable right now.');
  });

  it('carries its tone in a class, never only in a colour the DOM cannot state', () => {
    const { container } = render(<Notice tone="danger">Something failed.</Notice>);
    expect((container.firstElementChild as HTMLElement).className).toContain('ds-notice--danger');
  });

  it('renders its glyph as decoration, so the sentence is the only thing announced', () => {
    // Every notice in this app states its meaning in words. The icon is a
    // second, non-colour channel for sighted readers and must not be read
    // out as a third copy of it.
    const { container } = render(<Notice tone="info">Draining stops readiness routing.</Notice>);
    expect(container.querySelector('svg')?.getAttribute('aria-hidden')).toBe('true');
  });

  it('renders an action beside the message when one is given, and nothing when none is', () => {
    const { container, rerender } = render(
      <Notice tone="danger" action={<button type="button">Retry</button>}>
        Unreachable.
      </Notice>,
    );
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy();

    rerender(<Notice tone="danger">Unreachable.</Notice>);
    expect(container.querySelector('.ds-notice__action')).toBeNull();
  });

  it('is polymorphic via `as`, for the cases where the notice is a region of the page', () => {
    render(
      <Notice tone="info" as="section" aria-label="Maintenance consequence">
        A drain does not survive a restart.
      </Notice>,
    );
    expect(screen.getByRole('region', { name: 'Maintenance consequence' }).tagName).toBe('SECTION');
  });
});

describe('StatGroup', () => {
  it('pairs each label with its figure through real dl/dt/dd semantics', () => {
    // Load-bearing, not decoration: assistive technology announces the
    // label/figure pairing from the description-list structure. A row of
    // divs would look identical and say nothing.
    render(
      <StatGroup
        items={[
          { label: 'Requests', value: 18 },
          { label: 'Errors', value: 5 },
        ]}
      />,
    );

    const errors = screen.getByText('Errors');
    expect(errors.tagName).toBe('DT');
    expect(errors.parentElement?.querySelector('dd')?.textContent).toBe('5');
  });

  it('renders a hint under a figure only when one is supplied', () => {
    const { container, rerender } = render(
      <StatGroup items={[{ label: 'Maintenance state', value: 'Running', hint: 'Serving normally.' }]} />,
    );
    expect(screen.getByText('Serving normally.')).toBeTruthy();

    rerender(<StatGroup items={[{ label: 'Maintenance state', value: 'Running' }]} />);
    expect(container.querySelector('.ds-stat-group__hint')).toBeNull();
  });

  it('marks a value as monospace only when asked, so prose and figures are not treated alike', () => {
    const { container } = render(
      <StatGroup
        items={[
          { label: 'Requests', value: 18, mono: true },
          { label: 'Span', value: 'over 3h 2m' },
        ]}
      />,
    );
    const values = Array.from(container.querySelectorAll('dd'));
    expect(values[0]?.className).toContain('ds-stat-group__value--mono');
    expect(values[1]?.className).not.toContain('ds-stat-group__value--mono');
  });
});
