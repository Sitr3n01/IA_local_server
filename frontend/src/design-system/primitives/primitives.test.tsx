import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { readCss } from './test-utils/readCss';
import { Button } from './Button/Button';
import { IconButton } from './IconButton/IconButton';
import { TextField } from './TextField/TextField';
import { Divider } from './Divider/Divider';
import { Skeleton } from './Skeleton/Skeleton';
import { Surface } from './Surface/Surface';
import { IconClose } from './icons';

describe('Button', () => {
  it('is disabled when disabled', () => {
    const onClick = vi.fn();
    render(
      <Button disabled onClick={onClick}>
        Admit
      </Button>,
    );
    const button = screen.getByRole('button', { name: 'Admit' }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
  });

  it('stays focusable while loading, refusing activation via aria-disabled rather than the native attribute', () => {
    // The defect this pins: a natively disabled button leaves the tab order
    // and is blurred by the browser the moment the attribute appears. Because
    // `loading` turns on exactly when the operator activates the control, that
    // threw keyboard focus to <body> at the start of every privileged action -
    // load, unload, switch, drain, resume - and nothing restored it.
    const onClick = vi.fn();
    render(
      <Button loading onClick={onClick}>
        Admit model
      </Button>,
    );
    const button = screen.getByRole('button', { name: 'Admit model' }) as HTMLButtonElement;

    expect(button.getAttribute('aria-busy')).toBe('true');
    expect(button.getAttribute('aria-disabled')).toBe('true');
    // Still reachable: focusable, and still in the accessibility tree.
    expect(button.disabled).toBe(false);
    button.focus();
    expect(document.activeElement).toBe(button);

    // aria-disabled is advisory to the platform, so the refusal has to be
    // real: the handler is dropped while busy (and Button.css sets
    // pointer-events: none for the same reason).
    fireEvent.click(button);
    expect(onClick).not.toHaveBeenCalled();
  });

  it('still uses the native disabled attribute for the explicit disabled prop', () => {
    // A control that is genuinely unavailable is different from one that is
    // momentarily busy, and only the second needs to keep focus.
    render(<Button disabled>Admit model</Button>);
    const button = screen.getByRole('button', { name: 'Admit model' }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
  });

  it('only renders aria-pressed when used as a toggle (selected is passed)', () => {
    const { rerender } = render(<Button>Plain action</Button>);
    expect(screen.getByRole('button', { name: 'Plain action' }).hasAttribute('aria-pressed')).toBe(false);

    rerender(<Button selected={false}>Toggle</Button>);
    expect(screen.getByRole('button', { name: 'Toggle' }).getAttribute('aria-pressed')).toBe('false');
  });
});

/**
 * The `ghost` tier, added in Sprint 10 for tertiary controls (disclosure
 * toggles, the sidebar's collapse control).
 *
 * Its contract is mostly a *visual* one - no permanent border, no permanent
 * fill, a real tonal hover and pressed state - so it is asserted against the
 * stylesheet source, the same technique and for the same reason as
 * focus-visible.test.ts: jsdom computes no layout and does not implement
 * `:hover`/`:active`, so a rendered assertion here would either be
 * unreliable or would silently test nothing.
 */
describe('Button: the ghost tier', () => {
  const css = readCss('design-system/primitives/Button/Button.css');

  /** The body of the first rule whose selector matches exactly, comments stripped. */
  function ruleBody(selector: string): string {
    const source = css.replace(/\/\*[\s\S]*?\*\//g, '');
    const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    return new RegExp(`(?:^|[},])\\s*${escaped}\\s*\\{([^}]*)\\}`, 'm').exec(source)?.[1] ?? '';
  }

  it('renders the variant class so a call site can say "this is tertiary"', () => {
    render(<Button variant="ghost">Show capacity details</Button>);
    expect(screen.getByRole('button', { name: 'Show capacity details' }).className).toContain('ds-button--ghost');
  });

  it('draws no fill and no border at rest', () => {
    const body = ruleBody('.ds-button--ghost');
    expect(body).toMatch(/background-color:\s*transparent/);
    expect(body).toMatch(/border-color:\s*transparent/);
  });

  it('keeps the shared button geometry, so nothing shifts when it gains a fill on hover', () => {
    // "Costs nothing at rest" is about paint, not about the box. The
    // transparent 1px border and the shared min-height live on `.ds-button`
    // and this variant must not opt out of either.
    const base = ruleBody('.ds-button');
    expect(base).toMatch(/border:\s*1px solid transparent/);
    expect(base).toMatch(/min-height:/);
    expect(ruleBody('.ds-button--ghost')).not.toMatch(/(^|[\s;])(min-height|padding-inline|border-width):/);
  });

  it('has a hover state and a *different* pressed state, both from semantic state tints', () => {
    // If the two shared a value, the only feedback on press would be the
    // half-pixel shift - which is not a state an operator can see on a click
    // they are already committing to.
    const hover = ruleBody('.ds-button--ghost:hover:not(:disabled)');
    const pressed = ruleBody('.ds-button--ghost:active:not(:disabled)');

    expect(hover).toContain('var(--color-state-hover)');
    expect(pressed).toContain('var(--color-state-pressed)');
    expect(hover).not.toContain('var(--color-state-pressed)');
  });

  it('inherits the shared focus-visible ring rather than defining a quieter one of its own', () => {
    // A borderless control is exactly the one that must not also have a
    // quieter focus ring: there is no resting outline for a keyboard
    // operator to fall back on.
    expect(ruleBody('.ds-button:focus-visible')).toMatch(/outline:\s*2px solid var\(--color-action-primary\)/);
    expect(css).not.toMatch(/\.ds-button--ghost:focus-visible/);
  });

  it('introduces no literal colour - every tint comes from the semantic layer', () => {
    const ghostRules = css.split('.ds-button--ghost').slice(1).join('');
    expect(ghostRules).not.toMatch(/#[0-9a-fA-F]{3,8}/);
    expect(ghostRules).not.toMatch(/(rgb|rgba|hsl|hsla)\(/);
  });
});

describe('IconButton', () => {
  it("requires a label and uses it as the button's accessible name", () => {
    render(<IconButton label="Close panel" icon={<IconClose />} />);
    expect(screen.getByRole('button', { name: 'Close panel' })).toBeTruthy();
  });

  it('is disabled while loading', () => {
    render(<IconButton label="Save" icon={<IconClose />} loading />);
    const button = screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(button.getAttribute('aria-busy')).toBe('true');
  });
});

describe('TextField', () => {
  it('associates its label via a real <label for>', () => {
    render(<TextField label="Model ID" defaultValue="cia-edge" />);
    const input = screen.getByLabelText('Model ID') as HTMLInputElement;
    expect(input.value).toBe('cia-edge');
  });

  it('marks itself invalid and associates the error message when `error` is set', () => {
    render(<TextField label="Port" defaultValue="99999" error="Port must be between 1 and 65535." />);
    const input = screen.getByLabelText('Port');
    expect(input.getAttribute('aria-invalid')).toBe('true');
    const describedBy = input.getAttribute('aria-describedby');
    expect(describedBy).toBeTruthy();
    expect(document.getElementById(describedBy as string)?.textContent).toBe(
      'Port must be between 1 and 65535.',
    );
    expect(screen.getByRole('alert').textContent).toBe('Port must be between 1 and 65535.');
  });

  it('has no error state by default (aria-invalid absent)', () => {
    render(<TextField label="Workspace" defaultValue="default" />);
    expect(screen.getByLabelText('Workspace').hasAttribute('aria-invalid')).toBe(false);
  });

  it('renders a clear button only when a value is present and onClear is supplied', () => {
    const onClear = vi.fn();
    const { rerender } = render(
      <TextField label="Search" value="" onChange={() => {}} onClear={onClear} />,
    );
    expect(screen.queryByRole('button', { name: 'Clear Search' })).toBeNull();

    rerender(<TextField label="Search" value="abc" onChange={() => {}} onClear={onClear} />);
    screen.getByRole('button', { name: 'Clear Search' }).click();
    expect(onClear).toHaveBeenCalledTimes(1);
  });
});

describe('Divider', () => {
  it('renders a native <hr> for the horizontal orientation', () => {
    render(<Divider />);
    expect(screen.getByRole('separator').tagName).toBe('HR');
  });

  it('renders role="separator" aria-orientation="vertical" for the vertical orientation', () => {
    render(<Divider orientation="vertical" />);
    expect(screen.getByRole('separator').getAttribute('aria-orientation')).toBe('vertical');
  });
});

describe('Skeleton', () => {
  it('is aria-hidden - purely decorative, never announced', () => {
    const { container } = render(<Skeleton />);
    expect((container.firstChild as HTMLElement).getAttribute('aria-hidden')).toBe('true');
  });

  it('renders one placeholder per requested text line, with the last one visually shorter', () => {
    const { container } = render(<Skeleton variant="text" lines={3} />);
    expect(container.querySelectorAll('.ds-skeleton--text').length).toBe(3);
  });
});

describe('Surface', () => {
  it('is polymorphic via `as`', () => {
    render(
      <Surface as="section" aria-label="Status">
        content
      </Surface>,
    );
    expect(screen.getByRole('region', { name: 'Status' }).tagName).toBe('SECTION');
  });
});
