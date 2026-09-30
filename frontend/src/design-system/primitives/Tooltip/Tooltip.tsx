import { cloneElement, isValidElement, useId, useState } from 'react';
import type { FocusEvent, KeyboardEvent, MouseEvent, ReactElement } from 'react';
import './Tooltip.css';

interface TriggerProps {
  onMouseEnter?: (event: MouseEvent<HTMLElement>) => void;
  onMouseLeave?: (event: MouseEvent<HTMLElement>) => void;
  onFocus?: (event: FocusEvent<HTMLElement>) => void;
  onBlur?: (event: FocusEvent<HTMLElement>) => void;
  onKeyDown?: (event: KeyboardEvent<HTMLElement>) => void;
  'aria-describedby'?: string;
}

export interface TooltipProps {
  /** Supplementary text only - never put content here a user needs to operate the trigger. */
  content: string;
  /** A single focusable element (Button, IconButton, or anything that accepts the listed handlers/aria-describedby). */
  children: ReactElement<TriggerProps>;
  placement?: 'top' | 'bottom';
}

/**
 * Shows supplementary text on hover *and* on keyboard focus - a tooltip
 * that only responds to `:hover` is invisible to keyboard-only operators,
 * which is why every handler below is duplicated across the mouse and
 * focus pairs rather than relying on `:hover` in CSS alone.
 *
 * Does not trap focus: the bubble itself is never in the tab sequence (no
 * `tabIndex`, no focusable content), so Tab always moves from the trigger
 * straight to whatever follows it in the DOM - the tooltip is purely
 * something the trigger *describes itself with* (`aria-describedby`), not a
 * stop along the way.
 *
 * The bubble stays mounted at all times and is hidden with
 * `visibility`/`opacity`, never `display: none` - the latter would remove
 * it from the accessibility tree, which would make `aria-describedby`
 * announce nothing at all even at the instant it's shown.
 *
 * Escape hides the tooltip without moving focus off the trigger, matching
 * the WAI-ARIA APG tooltip pattern.
 */
export function Tooltip({ content, children, placement = 'top' }: TooltipProps) {
  const [visible, setVisible] = useState(false);
  const generatedId = useId();
  const tooltipId = `tooltip-${generatedId}`;

  if (!isValidElement(children)) {
    return children;
  }

  const trigger = cloneElement(children, {
    'aria-describedby': tooltipId,
    onMouseEnter: (event: MouseEvent<HTMLElement>) => {
      setVisible(true);
      children.props.onMouseEnter?.(event);
    },
    onMouseLeave: (event: MouseEvent<HTMLElement>) => {
      setVisible(false);
      children.props.onMouseLeave?.(event);
    },
    onFocus: (event: FocusEvent<HTMLElement>) => {
      setVisible(true);
      children.props.onFocus?.(event);
    },
    onBlur: (event: FocusEvent<HTMLElement>) => {
      setVisible(false);
      children.props.onBlur?.(event);
    },
    onKeyDown: (event: KeyboardEvent<HTMLElement>) => {
      if (event.key === 'Escape' && visible) {
        setVisible(false);
      }
      children.props.onKeyDown?.(event);
    },
  });

  const classes = ['ds-tooltip', `ds-tooltip--${placement}`];
  if (visible) classes.push('ds-tooltip--visible');

  return (
    <span className="ds-tooltip-wrapper">
      {trigger}
      <span role="tooltip" id={tooltipId} className={classes.join(' ')}>
        {content}
      </span>
    </span>
  );
}
