import type { ReactNode } from 'react';
import { Notice } from '../../design-system/components';
import { NavRail } from './NavRail';
import { TopBar } from './TopBar';
import { useRailCollapsed } from './useRailCollapsed';
import { type DestinationId } from './navigation';
import type { HealthSummary } from '../../features/system-status/useHealthSummary';
import type { ActiveModelState } from '../../features/system-status/useActiveModel';
import './AppShell.css';

export interface AppShellProps {
  activeDestination: DestinationId;
  onNavigate: (id: DestinationId) => void;
  health: HealthSummary;
  /** Deployment environment for the header; `undefined` when the provider reports none. */
  environment?: string | undefined;
  /** Which model is loaded, for the header. Omitted in tests that don't exercise it. */
  activeModel?: ActiveModelState | undefined;
  /**
   * What to speak into the persistent live region, or the empty string for
   * silence. Computed by `useLiveAnnouncement` and passed in as a prop so
   * this component keeps its no-query rule (see the doc comment below).
   */
  announcement?: string;
  children: ReactNode;
}

/**
 * Shown whenever the status query is failing while a screen is still
 * rendering figures.
 *
 * The pages deliberately keep displaying the last snapshot they received
 * rather than blanking on a transient poll failure (see any of the four view
 * models for why), which leaves one honest question unanswered: is what I am
 * looking at current? The header already switches to "Unreachable" on this
 * same condition, but "unreachable" describes the provider, not the numbers -
 * an operator could reasonably read a live-looking screen as live. This says
 * the other half out loud.
 *
 * It lives here, once, rather than in each page: the condition is identical
 * for all four, and four copies would be four things to keep in step.
 *
 * It is purely visual and carries NO live-region role. It mounts and unmounts
 * with the condition it describes, and a live region inserted into the DOM
 * already holding its message is frequently never announced - the markup
 * would have read correctly and been silent in practice. Speech comes from
 * the always-present region below instead. `Notice` sets no role of its own,
 * precisely so this caller can decline one.
 */
function StaleDataBanner() {
  return (
    <div className="app-shell__column">
      <Notice tone="warning" className="app-shell__stale">
        <span>
          Showing the last figures received. IA Local is not answering right now, so nothing on this screen is
          updating - it is not a reading of the provider as it is at this moment.
        </span>
      </Notice>
    </div>
  );
}

/**
 * Root layout: the workspace sidebar plus a header around whatever page is
 * currently active. Purely presentational and data-agnostic beyond the props
 * it is handed - it never calls `useStatusQuery` or any other query itself
 * (see `src/app/App.tsx`, which owns that and passes the derived summaries
 * down), so it stays trivially testable with plain props and no
 * `QueryClientProvider`.
 *
 * The sidebar's collapsed/expanded choice is the one piece of state this
 * component owns directly - it's UI chrome, not application data, and
 * `useRailCollapsed` is what persists it to `localStorage`.
 */
export function AppShell({
  activeDestination,
  onNavigate,
  health,
  environment,
  activeModel,
  announcement = '',
  children,
}: AppShellProps) {
  const [collapsed, setCollapsed] = useRailCollapsed();

  return (
    <div className="app-shell">
      <NavRail
        activeDestination={activeDestination}
        onNavigate={onNavigate}
        collapsed={collapsed}
        onToggleCollapsed={() => setCollapsed(!collapsed)}
      />
      <div className="app-shell__main">
        <TopBar health={health} environment={environment} activeModel={activeModel} />
        {health.state === 'error' && <StaleDataBanner />}

        {/* The app's one spoken channel, mounted for the whole session and
            never conditionally rendered. Assistive technology announces
            mutations *within* a live region that already exists; a region
            inserted along with its message is frequently not announced at
            all, which is why this is always here and only its text changes.
            `useLiveAnnouncement` emits on semantic transitions only -
            readiness, maintenance, and whether the figures are still live -
            never on a poll, a counter, or the queue. Visually hidden: every
            one of these transitions is already visible on screen. */}
        <div className="ds-visually-hidden" role="status" aria-live="polite" aria-atomic="true">
          {announcement}
        </div>

        <div className="app-shell__content">{children}</div>
      </div>
    </div>
  );
}
