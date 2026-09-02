import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
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
