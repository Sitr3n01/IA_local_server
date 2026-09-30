import './Divider.css';

export interface DividerProps {
  orientation?: 'horizontal' | 'vertical';
  className?: string;
}

/**
 * A hairline separator. Not interactive - no hover/pressed/focus states
 * apply, same reasoning as Surface.
 *
 * Horizontal renders a real `<hr>` (correct native semantics for free).
 * `<hr>` has no vertical equivalent, so vertical renders a `<div
 * role="separator" aria-orientation="vertical">` instead - the ARIA
 * separator role is exactly what `<hr>` maps to anyway.
 *
 * Uses `--color-border-subtle` deliberately: a divider's absence loses no
 * content (WCAG 1.4.11 doesn't apply to purely decorative dividers), so it
 * is intentionally lower-contrast than the load-bearing `border-default`/
 * `border-strong` tokens used on interactive primitives.
 */
export function Divider({ orientation = 'horizontal', className }: DividerProps) {
  const classes = ['ds-divider', `ds-divider--${orientation}`];
  if (className) classes.push(className);

  if (orientation === 'vertical') {
    return <div className={classes.join(' ')} role="separator" aria-orientation="vertical" />;
  }

  return <hr className={classes.join(' ')} />;
}
