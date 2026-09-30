/**
 * Layer above the seven DS 0.1 primitives (`../primitives`): composed,
 * domain-shaped visual components that are still generic enough to be
 * design-system components rather than page/feature markup.
 *
 * `ResourceMeter` came first, for the Sprint 4 Models screen. `FieldList`
 * followed once the same label/value list had been written out three times.
 * Sprint 10 (visual foundation) added two more, both for the same reason
 * and both documented in their own files: `Notice`, because five screens
 * were each hand-rolling a status band out of a bordered `Surface`, and
 * `StatGroup`, because a caption over a figure kept being promoted into a
 * card.
 */
export { ResourceMeter } from './ResourceMeter/ResourceMeter';
export type { ResourceMeterProps } from './ResourceMeter/ResourceMeter';
export { FieldList } from './FieldList/FieldList';
export type { FieldListProps, FieldListItem } from './FieldList/FieldList';
export { Notice } from './Notice/Notice';
export type { NoticeProps, NoticeTone } from './Notice/Notice';
export { StatGroup } from './StatGroup/StatGroup';
export type { StatGroupProps, StatItem } from './StatGroup/StatGroup';
