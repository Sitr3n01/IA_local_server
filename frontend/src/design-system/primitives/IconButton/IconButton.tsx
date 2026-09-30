import { forwardRef } from 'react';
import type { ButtonHTMLAttributes, ReactNode } from 'react';
import { IconSpinner } from '../icons';
import './IconButton.css';

/**
 * `ghost` is the tertiary tier, matching `Button`'s own (Sprint 10). It
 * differs from `secondary` in pressed/selected weight rather than at rest -
 * this primitive has never drawn a resting border - and exists so an
 * icon-only tertiary control can *say* it is tertiary at the call site
 * instead of the reader having to know that `secondary` happens to render
 * borderless here.
 */
export type IconButtonVariant = 'primary' | 'secondary' | 'ghost';
export type IconButtonSize = 'md' | 'sm';

export interface IconButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'type'> {
  /** Required: this button has no visible text, so its accessible name comes from here. */
  label: string;
  icon: ReactNode;
  variant?: IconButtonVariant;
  size?: IconButtonSize;
  loading?: boolean;
  /** Toggle-button use only - see Button's `selected` doc for why this is left undefined by default. */
  selected?: boolean;
  type?: 'button' | 'submit' | 'reset';
}

/**
 * Icon-only action button. Square footprint (not `radius-full` - that's
 * reserved for chip/badge/avatar/segmented-control per the brief), min 40px
 * (2.5rem) hit target at the default size, comfortably above the 24px CSS
 * WCAG 2.5.8 minimum and still true at 200% zoom since it's set in rem.
 *
 * States implemented: default, hover, pressed, focus-visible, disabled,
 * loading, selected (toggle use, e.g. a mute/unmute control).
 */
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  {
    label,
    icon,
    variant = 'secondary',
    size = 'md',
    loading = false,
    selected,
    disabled = false,
    type = 'button',
    className,
    ...rest
  },
  ref,
) {
  const isDisabled = disabled || loading;
  const classes = ['ds-icon-button', `ds-icon-button--${variant}`, `ds-icon-button--${size}`];
  if (selected === true) classes.push('ds-icon-button--selected');
  if (className) classes.push(className);

  return (
    <button
      ref={ref}
      type={type}
      className={classes.join(' ')}
      disabled={isDisabled}
      aria-label={label}
      aria-busy={loading || undefined}
      aria-pressed={selected}
      title={label}
      {...rest}
    >
      <span className="ds-icon-button__icon">{loading ? <IconSpinner /> : icon}</span>
    </button>
  );
});
