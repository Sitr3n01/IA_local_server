import type { ComponentType } from 'react';
import { IconActivity, IconCpu, IconGrid, IconServer } from '../../design-system/primitives';
import type { IconProps } from '../../design-system/primitives';

/**
 * Four destinations exist as of Sprint 6 - Overview, System, Models, and
 * Activity. The Sprint 3 brief kept this list to exactly two ("Do not add
 * Models, Inference, Settings, or any placeholder") specifically because no
 * real destination existed behind a Models item yet; Sprint 4 added one,
 * and Sprint 6 adds a fourth (Activity), per that same rule ("A navigation
 * item exists only when a real destination exists behind it"). `DestinationId`
 * is a closed union rather than a plain `string` on purpose: every place
 * that renders a page for a destination (see `src/app/App.tsx`) switches
 * over it exhaustively, so adding a destination is a compile-time-enforced
 * two-step change (extend this union, extend that switch) rather than a
 * silent gap - this file is living proof: adding Models, and later Activity,
 * each touched only this array and one line in `App.tsx`, not `NavRail.tsx`
 * or `AppShell.tsx`.
 *
 * What *is* meant to be "data, not surgery" is the rail itself: `NavRail`
 * never hard-codes "Overview", "System", "Models", or "Activity" - it only
 * ever maps over `NAV_DESTINATIONS` below.
 */
export type DestinationId = 'overview' | 'system' | 'models' | 'activity';

export interface NavDestination {
  id: DestinationId;
  label: string;
  icon: ComponentType<IconProps>;
}

export const NAV_DESTINATIONS: readonly NavDestination[] = [
  { id: 'overview', label: 'Overview', icon: IconGrid },
  { id: 'models', label: 'Models', icon: IconCpu },
  { id: 'activity', label: 'Activity', icon: IconActivity },
  { id: 'system', label: 'System', icon: IconServer },
];
