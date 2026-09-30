import { forwardRef } from 'react';
import type { ButtonHTMLAttributes, ReactNode } from 'react';
import { IconSpinner } from '../icons';
import './Button.css';

/**
 * `ghost` is the tertiary tier, added in Sprint 10: no permanent border and
 * no permanent fill, so it costs the layout nothing at rest, and a tonal
 * hover/pressed state so it is unmistakably a control once reached. It is
 * for actions that were being given a box they had not earned - disclosure
 * toggles, the rail's collapse control, small secondary affordances beside
 * a heavier action.
 *
 * It is NOT for anything consequential. Every model-lifecycle and
 * maintenance operation in this console stays `primary`, and a recovery
 * action ("Retry") stays `secondary`: a control that changes the provider's
 * state must look like one before it is hovered.
 */
export type ButtonVariant = 'primary' | 'secondary' | 'ghost';
export type ButtonSize = 'md' | 'sm';

export interface ButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'type'> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  /**
   * Shows a spinner in place of the leading icon, sets `aria-busy`, and
   * blocks interaction (via `aria-disabled` + a pointer-events guard) while
   * keeping the button's own dimensions stable - the label stays put so
   * nothing reflows around it.
   */
  loading?: boolean;
  /**
   * Toggle-button use only - omit entirely for a plain action button.
   * Passing `true`/`false` renders `aria-pressed` and a tonal "on" fill;
   * leaving it `undefined` renders neither, so an ordinary button never
   * gets a stray `aria-pressed="false"` announced as if it were a toggle.
   */
  selected?: boolean;
  leadingIcon?: ReactNode;
  trailingIcon?: ReactNode;
  /** Native `button[type]`, defaulted to `"button"` so it never accidentally submits a form. */
  type?: 'button' | 'submit' | 'reset';
  children?: ReactNode;
}

/**
 * States implemented: default, hover, pressed (`:active`), focus-visible
 * (a real offset ring, not just a colour swap - see Button.css), disabled,
 * loading, and selected (toggle-button use). Error doesn't apply to Button
 * per se - a button doesn't have a validity state of its own; that's
 * TextField's job.
 */
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    variant = 'primary',
    size = 'md',
    loading = false,
    selected,
    disabled = false,
    leadingIcon,
    trailingIcon,
    type = 'button',
    className,
    children,
    ...rest
  },
  ref,
) {
  // `loading` and `disabled` are deliberately NOT collapsed into one native
  // `disabled` attribute, which is what this component used to do.
  //
  // A natively disabled button is removed from the tab order and is blurred
  // by the browser the instant the attribute appears. Since `loading` turns
  // on at the moment the operator activates the button, that meant every
  // privileged action - load, unload, switch, drain, resume - threw the
  // keyboard operator's focus to `<body>` exactly when the operation they
  // just started needed watching. Nothing in this app restores focus
  // afterwards, so it simply stayed lost.
  //
  // So an in-flight button stays focusable and announces its state through
  // `aria-disabled` instead, which assistive technology reports as
  // unavailable without removing it from the page. `disabled` remains native
  // for the explicit `disabled` prop, where the control is genuinely
  // unavailable rather than momentarily busy.
  //
  // `aria-disabled` is advisory to the platform, so activation must be
  // refused here as well: the click handler is dropped while busy, and
  // `.ds-button[aria-disabled='true']` sets `pointer-events: none` in
  // Button.css. This is the behaviour this component's own `loading`
  // docblock has always described - it just did not implement it.
  const isBusy = loading;
  const classes = ['ds-button', `ds-button--${variant}`, `ds-button--${size}`];
  if (selected === true) classes.push('ds-button--selected');
  if (loading) classes.push('ds-button--loading');
  if (className) classes.push(className);

  return (
    <button
      ref={ref}
      type={type}
      className={classes.join(' ')}
      disabled={disabled}
      aria-disabled={isBusy || undefined}
      aria-busy={loading || undefined}
      aria-pressed={selected}
      {...rest}
      onClick={isBusy ? undefined : rest.onClick}
    >
      {loading ? (
        <IconSpinner className="ds-button__icon" />
      ) : (
        leadingIcon && <span className="ds-button__icon">{leadingIcon}</span>
      )}
      <span className="ds-button__label">{children}</span>
      {!loading && trailingIcon && <span className="ds-button__icon">{trailingIcon}</span>}
    </button>
  );
});
