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
 * The workspace sidebar.
 *
 * Sprint 10 changed what this is, not what it contains. It held - and still
 * holds - exactly the four destinations that have a real screen behind
 * them, and nothing was invented to fill it out. What changed is that it
 * stopped presenting itself as a strip of four buttons fenced off from the
 * app by a vertical rule, and started presenting itself as one side of a
 * single workspace:
 *
 *  - the rule is gone. Sidebar and canvas are told apart by a ~1.03:1 tonal
 *    step and by alignment (see NavRail.css and the ground compression in
 *    foundation.css). A boundary you can feel and not point at is the
 *    intended result.
 *  - the nav list takes the free vertical space, so the emptiness below the
 *    fourth destination is deliberate rather than leftover. It is also the
 *    room a contextual area would occupy later, which is the honest reason
 *    to leave it empty now instead of filling it.
 *  - the selected destination is stated in neutrals rather than in accent
 *    blue. See NavRail.css for the four channels that carry it and why none
 *    of them is allowed to be the only one.
 *
 * Accessibility is unchanged. Collapsed narrows the sidebar to icon-only
 * width, but every item's accessible name still comes from its label text -
 * the label `<span>` stays in the DOM and is only ever visually hidden,
 * never removed, so a screen reader announces the same name in both states.
 * The active destination is exposed via `aria-current="page"`, and every
 * item is a native `<button>` - reachable by Tab, activated by Enter/Space
 * with no extra keyboard wiring.
 *
 * The collapse control is a `ghost` IconButton now: it is the least
 * important control in the shell and used to be the only one down here
 * with a hairline drawn above it to justify its position. Bottom alignment
 * and the whitespace above it place it perfectly well.
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
          variant="ghost"
          size="sm"
          onClick={onToggleCollapsed}
        />
      </div>
    </nav>
  );
}
