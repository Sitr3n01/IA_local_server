import './Skeleton.css';

export type SkeletonVariant = 'text' | 'circle' | 'block';
export type SkeletonSize = 'sm' | 'md' | 'lg';

export interface SkeletonProps {
  variant?: SkeletonVariant;
  /** Diameter scale for `variant="circle"` (avatar-sized). Ignored otherwise. */
  size?: SkeletonSize;
  /** Stacked line count for `variant="text"`. The last line renders shorter, mimicking ragged text. Ignored otherwise. */
  lines?: number;
  className?: string;
}

/**
 * A loading placeholder. Deliberately has no `width`/`height` props: the
 * production CSP is `style-src 'self'` with no `'unsafe-inline'`, so an
 * inline `style` attribute (which an arbitrary-dimension prop would need)
 * is silently dropped by the browser in the shipped console - it would work
 * in `vite dev` and tests, then silently fail to size anything once built.
 * Sizing instead comes from a fixed scale (`size`) or from CSS doing what
 * it already does well: `block` fills whatever box its container's layout
 * gives it (flex/grid sizing on the *parent*), and `text` fills the
 * container's inline width one line at a time.
 *
 * Purely decorative and `aria-hidden` - it never announces itself to
 * assistive tech. The component that shows a Skeleton while data loads owns
 * its own `aria-busy` / accessible loading announcement (e.g. a live region
 * saying "Loading model list"); Skeleton itself has no content to describe.
 *
 * Not interactive, so the hover/pressed/focus-visible matrix doesn't apply
 * (same reasoning as Surface and Divider). The shimmer is a plain CSS
 * `background-position` sweep, collapsed to a static subtle fill under
 * `prefers-reduced-motion` by the central rule in
 * src/design-system/styles/motion.css.
 */
export function Skeleton({ variant = 'text', size = 'md', lines = 1, className }: SkeletonProps) {
  const classes = ['ds-skeleton', `ds-skeleton--${variant}`];
  if (variant === 'circle') classes.push(`ds-skeleton--${size}`);
  if (className) classes.push(className);

  if (variant === 'text' && lines > 1) {
    return (
      <div className="ds-skeleton-lines" aria-hidden="true">
        {Array.from({ length: lines }, (_, index) => (
          <div key={index} className={classes.join(' ')} />
        ))}
      </div>
    );
  }

  return <div className={classes.join(' ')} aria-hidden="true" />;
}
