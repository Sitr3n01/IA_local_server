import { IconButton, IconChevronLeft } from '../../design-system/primitives';
import { NAV_DESTINATIONS, type DestinationId } from './navigation';
import './NavRail.css';

export interface NavRailProps {
  activeDestination: DestinationId;
  onNavigate: (id: DestinationId) => void;
  collapsed: boolean;
  onToggleCollapsed: () => void;
}

/**
 * The navigation rail. Two states, `collapsed`/expanded, both driven by
 * `AppShell` (persisted via `useRailCollapsed`). Collapsed narrows the rail
 * to icon-only width, but every item's accessible name still comes from its
 * label text - the label `<span>` stays in the DOM and is only ever
 * visually hidden (see `.nav-rail__label--hidden` in NavRail.css), never
 * removed, so a screen reader announces the same name in both states. The
 * active destination is exposed via `aria-current="page"`, and every item
 * is a native `<button>` - reachable by Tab, activated by Enter/Space with
 * no extra keyboard wiring needed.
 *
 * The collapse control lives in its own `.nav-rail__footer` region, set off
 * from the nav list by a hairline top border (see NavRail.css) rather than
 * sitting in undifferentiated whitespace below the last item. Its
 * accessible name states the action it performs ("Expand"/"Collapse
 * navigation"), and `aria-expanded` carries the rail's current state
 * (`true` while expanded) so assistive tech gets both without the label
 * itself having to spell out "(currently collapsed)".
 */
export function NavRail({ activeDestination, onNavigate, collapsed, onToggleCollapsed }: NavRailProps) {
  return (
    <nav className={`nav-rail${collapsed ? ' nav-rail--collapsed' : ''}`} aria-label="Primary">
      <div className="nav-rail__brand" aria-hidden="true">
        {collapsed ? 'IA' : 'IA Local'}
      </div>

      <ul className="nav-rail__list">
        {NAV_DESTINATIONS.map((destination) => {
          const Icon = destination.icon;
          const isActive = destination.id === activeDestination;
          return (
            <li key={destination.id}>
              <button
                type="button"
                className={`nav-rail__item${isActive ? ' nav-rail__item--active' : ''}`}
                aria-current={isActive ? 'page' : undefined}
                onClick={() => onNavigate(destination.id)}
              >
                <Icon className="nav-rail__icon" />
                <span className={`nav-rail__label${collapsed ? ' ds-visually-hidden' : ''}`}>
                  {destination.label}
                </span>
              </button>
            </li>
          );
        })}
      </ul>

      <div className="nav-rail__footer">
        <IconButton
          label={collapsed ? 'Expand navigation' : 'Collapse navigation'}
          aria-expanded={!collapsed}
          icon={
            <IconChevronLeft
              className={`nav-rail__collapse-icon${collapsed ? ' nav-rail__collapse-icon--collapsed' : ''}`}
            />
          }
          variant="secondary"
          size="sm"
          onClick={onToggleCollapsed}
        />
      </div>
    </nav>
  );
}
