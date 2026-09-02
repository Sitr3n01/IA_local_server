import { AppShell } from './shell/AppShell';
import { type DestinationId } from './shell/navigation';
import { useHashDestination } from './shell/useHashDestination';
import { useHealthSummary } from '../features/system-status/useHealthSummary';
import { useDeploymentIdentity } from '../features/system-status/useDeploymentIdentity';
import { useLiveAnnouncement } from '../features/system-status/useLiveAnnouncement';
import { OverviewPage } from '../pages/OverviewPage';
import { SystemPage } from '../pages/SystemPage';
import { ModelsPage } from '../pages/ModelsPage';
import { ActivityPage } from '../pages/ActivityPage';
import { assertNever } from '../lib/assertNever';

function renderDestination(destination: DestinationId) {
  switch (destination) {
    case 'overview':
      return <OverviewPage />;
    case 'models':
      return <ModelsPage />;
    case 'activity':
      return <ActivityPage />;
    case 'system':
      return <SystemPage />;
    default:
      return assertNever(destination, 'App.renderDestination');
  }
}

/**
 * The composition root: owns which destination is currently selected and
 * wires the top bar's health summary into `AppShell`. Deliberately in
 * `src/app/`, not `src/pages/` - it is shell wiring, not a screen of its
 * own, which is why it (like `AppShell`) is allowed to reach into
 * `src/features` directly for the health summary. `OverviewPage`,
 * `SystemPage`, `ModelsPage`, and `ActivityPage` each still fetch their own
 * richer view-model from `src/features` independently - see their own
 * files - sharing this hook's underlying query via TanStack Query's cache
 * rather than this component threading data down as props.
 */
export function App() {
  const [destination, setDestination] = useHashDestination();
  const health = useHealthSummary();
  const environment = useDeploymentIdentity();
  const announcement = useLiveAnnouncement();

  return (
    <AppShell
      activeDestination={destination}
      onNavigate={setDestination}
      health={health}
      environment={environment}
      announcement={announcement}
    >
      {renderDestination(destination)}
    </AppShell>
  );
}
