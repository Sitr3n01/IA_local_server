import { describe, expect, it } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { Tooltip } from './Tooltip';

describe('Tooltip', () => {
  it('is reachable by keyboard: focusing the trigger shows it, Escape hides it, and focus never leaves the trigger', () => {
    render(
      <Tooltip content="Restarts the admission-control gate.">
        <button type="button">Restart gate</button>
      </Tooltip>,
    );

    const trigger = screen.getByRole('button', { name: 'Restart gate' });
    const tooltip = screen.getByRole('tooltip', { hidden: true });

    // Hidden until the trigger is focused - not merely visually hidden, but
    // not yet the thing an operator tabbing through the page is told about.
    expect(tooltip.className).not.toContain('ds-tooltip--visible');

    // Tab-equivalent: move focus onto the trigger with no mouse involved.
    // `.focus()` alone updates `document.activeElement` but jsdom does not
    // reliably dispatch the bubbling `focusin` event React's onFocus relies
    // on unless the event is fired explicitly - so both are needed here.
    trigger.focus();
    fireEvent.focus(trigger);
    expect(document.activeElement).toBe(trigger);
    expect(tooltip.className).toContain('ds-tooltip--visible');

    // The tooltip itself must never receive focus - Tab must be free to
    // leave the trigger for whatever follows it, i.e. no focus trap.
    expect(tooltip.hasAttribute('tabindex')).toBe(false);
    expect(document.activeElement).toBe(trigger);

    // Escape hides it without moving focus off the trigger.
    fireEvent.keyDown(trigger, { key: 'Escape' });
    expect(tooltip.className).not.toContain('ds-tooltip--visible');
    expect(document.activeElement).toBe(trigger);
  });

  it('also shows on hover and hides on mouse leave (pointer users get the same content)', () => {
    render(
      <Tooltip content="Pointer-accessible too">
        <button type="button">Trigger</button>
      </Tooltip>,
    );

    const trigger = screen.getByRole('button', { name: 'Trigger' });
    const tooltip = screen.getByRole('tooltip', { hidden: true });

    fireEvent.mouseEnter(trigger);
    expect(tooltip.className).toContain('ds-tooltip--visible');

    fireEvent.mouseLeave(trigger);
    expect(tooltip.className).not.toContain('ds-tooltip--visible');
  });

  it("associates the tooltip with the trigger via aria-describedby, referencing the tooltip's own id", () => {
    render(
      <Tooltip content="Described text">
        <button type="button">Trigger</button>
      </Tooltip>,
    );

    const trigger = screen.getByRole('button', { name: 'Trigger' });
    const tooltip = screen.getByRole('tooltip', { hidden: true });

    expect(trigger.getAttribute('aria-describedby')).toBe(tooltip.id);
  });
});
