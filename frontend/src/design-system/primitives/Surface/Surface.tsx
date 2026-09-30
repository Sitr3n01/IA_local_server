import type { ElementType, HTMLAttributes } from 'react';
import './Surface.css';

export type SurfaceLevel = 'canvas' | 'base' | 'subtle' | 'raised';

export interface SurfaceProps extends HTMLAttributes<HTMLElement> {
  /**
   * Which background token this panel sits on:
   *  - `canvas`: the page ground itself (rarely nested).
   *  - `base`: the default panel surface (most common).
   *  - `subtle`: a recessed fill for a lower-emphasis region within a panel.
   *  - `raised`: the highest elevation - overlays, popovers, tooltip
   *    bubbles sit here (see Tooltip.tsx).
   */
  level?: SurfaceLevel;
  /** Draws `--color-border-subtle` around the panel. Off by default. */
  bordered?: boolean;
  /** Applies `--radius-surface`. Off by default - not every Surface is a card. */
  rounded?: boolean;
  /** Polymorphic root element - e.g. `"section"` for a landmark region. */
  as?: ElementType;
}

/**
 * The base panel primitive every other visual primitive is built from (or
 * placed on top of). Purely presentational - it has no interactive states
 * of its own, so none of the hover/pressed/focus-visible matrix applies
 * here; that matrix is for the six *interactive* primitives.
 */
export function Surface({
  level = 'base',
  bordered = false,
  rounded = false,
  as: Component = 'div',
  className,
  children,
  ...rest
}: SurfaceProps) {
  const classes = ['ds-surface', `ds-surface--${level}`];
  if (bordered) classes.push('ds-surface--bordered');
  if (rounded) classes.push('ds-surface--rounded');
  if (className) classes.push(className);

  return (
    <Component className={classes.join(' ')} {...rest}>
      {children}
    </Component>
  );
}
