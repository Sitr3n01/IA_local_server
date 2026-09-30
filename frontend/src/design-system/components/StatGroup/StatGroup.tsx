import type { ReactNode } from 'react';
import './StatGroup.css';

export interface StatItem {
  /** The quiet name above the figure. Also the React key, so labels must be unique per group. */
  label: string;
  value: ReactNode;
  /** Render the value in the monospace face with tabular figures - counts, ratios, byte figures. */
  mono?: boolean;
  /** One short sentence under the figure, for a state that needs a word of explanation. */
  hint?: ReactNode;
}

export interface StatGroupProps {
  items: StatItem[];
  className?: string;
}

/**
 * A handful of short label/figure pairs, side by side.
 *
 * This is the pattern that used to become cards. Overview rendered its
 * gate occupancy and its maintenance state as two bordered panels in a
 * grid; Activity rendered its four timeline figures as a bespoke `<dl>`;
 * both were a caption over a number and nothing else. A figure that short
 * does not need a box drawn around it - proximity and alignment group it
 * perfectly well, and four boxes in a row is precisely the "dashboard of
 * tiles" read Sprint 10 set out to remove.
 *
 * Sibling to `FieldList` and deliberately not merged with it: `FieldList`
 * is a vertical two-column list of many facts about one thing, read by
 * scanning down the label column. This is a horizontal band of a few
 * headline figures, read across. They want opposite layouts, and one
 * component doing both would be a `direction` prop hiding two components.
 *
 * `<dl>`/`<dt>`/`<dd>` is load-bearing rather than decorative, same as
 * `FieldList`: assistive technology announces the label/figure pairing from
 * it. The `display: contents` trick FieldList uses is deliberately *not*
 * used here - each item is its own column, so each row wrapper is a real
 * box.
 */
export function StatGroup({ items, className }: StatGroupProps) {
  return (
    <dl className={className ? `ds-stat-group ${className}` : 'ds-stat-group'}>
      {items.map((item) => (
        <div className="ds-stat-group__item" key={item.label}>
          <dt className="ds-stat-group__label">{item.label}</dt>
          <dd className={`ds-stat-group__value${item.mono ? ' ds-stat-group__value--mono' : ''}`}>{item.value}</dd>
          {item.hint !== undefined && <p className="ds-stat-group__hint">{item.hint}</p>}
        </div>
      ))}
    </dl>
  );
}
