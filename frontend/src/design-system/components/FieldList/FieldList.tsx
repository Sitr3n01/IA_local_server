import type { ReactNode } from 'react';
import './FieldList.css';

export interface FieldListItem {
  /** The row's label. Also the React key, so labels must be unique per list. */
  label: string;
  value: ReactNode;
  /**
   * Render the value in the monospace face with tabular figures. For values
   * that are read as data rather than prose - hashes, paths, counts, byte
   * figures - so digits line up down the column and a hash can be compared
   * character by character.
   */
  mono?: boolean;
}

export interface FieldListProps {
  fields: FieldListItem[];
  className?: string;
}

/**
 * A label/value list: the console's one way of presenting a set of read-only
 * facts about a thing.
 *
 * Extracted after being written three separate times - in ActivityPage,
 * ModelsPage and SystemPage - as byte-identical components differing only in
 * their class prefix, with byte-identical stylesheets behind them. Three
 * copies of a pattern is the signal that a component wants to exist; the
 * failure mode of leaving them is that a fix to the column alignment or the
 * mono treatment lands in one screen and silently not the other two.
 *
 * It is a design-system *component* rather than a primitive because it
 * composes rather than being atomic, and it belongs here rather than in a
 * feature because it knows nothing about models, gates or capacity - it takes
 * labels and values and lays them out.
 *
 * The `<dl>`/`<dt>`/`<dd>` structure is load-bearing, not decoration: this is
 * a description list semantically, and assistive technology announces the
 * label/value pairing from it. The rows use `display: contents` so the grid's
 * two columns align across every row of the list rather than each row
 * establishing its own independent track.
 */
export function FieldList({ fields, className }: FieldListProps) {
  return (
    <dl className={className ? `ds-field-list ${className}` : 'ds-field-list'}>
      {fields.map((field) => (
        <div className="ds-field-list__row" key={field.label}>
          <dt className="ds-field-list__label">{field.label}</dt>
          <dd className={`ds-field-list__value${field.mono ? ' ds-field-list__value--mono' : ''}`}>{field.value}</dd>
        </div>
      ))}
    </dl>
  );
}
