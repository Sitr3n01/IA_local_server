import { Button, IconAlertCircle, IconCheck, Skeleton, Surface } from '../design-system/primitives';
import { useOverviewViewModel } from '../features/system-status/useOverviewViewModel';
import type { OverviewData } from '../features/system-status/deriveOverview';
import './OverviewPage.css';
import { maintenanceLabel } from '../features/maintenanceState';

/**
 * Known `maintenance.state` values from the Sprint 3 brief, each with an
 * operator-facing label and a description of what the state means for the
 * controls on this screen. Exact-match lookup, same discipline as the
 * `reason` lookup in `src/features/system-status/reasonExplanations.ts` -
 * an unrecognised state falls back to showing the raw string.
 */
/**
 * Overview's own one-line description per maintenance state. The LABEL is
 * not here: it comes from `maintenanceLabel` in
 * `src/features/maintenanceState.ts`, shared with the Activity screen, after
 * Sprint 8 found the same provider state being named "Maintenance" here and
 * "Drained" there. The descriptions stay page-local on purpose - this one is
 * a health summary, Activity's sits beside the drain counters, and one
 * sentence cannot serve both without reading as a non-sequitur somewhere.
 */
const MAINTENANCE_DESCRIPTIONS: Record<string, string> = {
  running: 'Serving normally.',
  draining:
    'Finishing in-flight requests and not accepting new ones. Every control on this console now targets a provider that is shutting its workload down, not one accepting fresh work.',
  maintenance: 'Not serving. The provider has been deliberately taken offline for maintenance.',
};

function ReadinessSection({ data, noModels }: { data: OverviewData; noModels: boolean }) {
  const { ready, readiness, availableModelCount, totalModelCount } = data;

  return (
    <Surface
      level={ready ? 'base' : 'subtle'}
      bordered
      rounded
      className={`overview-section overview-readiness${ready ? '' : ' overview-readiness--not-ready'}`}
    >
      <div className="overview-readiness__header">
        {ready ? (
          <IconCheck className="overview-readiness__icon overview-readiness__icon--ready" />
        ) : (
          <IconAlertCircle className="overview-readiness__icon overview-readiness__icon--not-ready" />
        )}
        <h2 className="overview-readiness__title">{ready ? 'Ready to serve' : 'Not ready to serve'}</h2>
      </div>

      {!ready && (
        <p className="overview-readiness__explanation">
          {readiness.recognized ? readiness.sentence : `Reason reported by the provider: ${readiness.code}`}
        </p>
      )}

      <p className="overview-readiness__models">
        {noModels
          ? 'No models are deployed on this provider yet.'
          : `${availableModelCount} of ${totalModelCount} configured models are available right now.`}
      </p>
    </Surface>
  );
}

function ActiveModelSection({ activeModelLabel }: { activeModelLabel: string | null }) {
  return (
    <Surface level="base" bordered rounded className="overview-section">
      <h2 className="overview-section__title">Active model</h2>
      <p className="overview-section__value">{activeModelLabel ?? 'None loaded'}</p>
    </Surface>
  );
}

function GateSection({ gate }: { gate: OverviewData['gate'] }) {
  return (
    <Surface level="base" bordered rounded className="overview-section">
      <h2 className="overview-section__title">Admission gate</h2>
      <p className="overview-section__value overview-section__value--mono">
        {gate.active} / {gate.maxActive} active · {gate.queued} / {gate.maxQueue} queued
      </p>
    </Surface>
  );
}

function MaintenanceSection({ maintenance }: { maintenance: OverviewData['maintenance'] }) {
  const description = MAINTENANCE_DESCRIPTIONS[maintenance.state];
  const isAbnormal = maintenance.state !== 'running';

  return (
    <Surface
      level={isAbnormal ? 'subtle' : 'base'}
      bordered
      rounded
      className={`overview-section${isAbnormal ? ' overview-section--maintenance-abnormal' : ''}`}
    >
      <h2 className="overview-section__title">Maintenance state</h2>
      <p className="overview-section__value">{maintenanceLabel(maintenance.state)}</p>
      <p className="overview-section__hint">
        {description ?? `Unrecognized maintenance state reported by the provider: ${maintenance.state}`}
      </p>
    </Surface>
  );
}

function OverviewSkeleton() {
  return (
    <div className="overview-page__body app-shell__sparse-state" aria-busy="true">
      <span className="ds-visually-hidden" role="status">
        Loading system overview…
      </span>
      {[0, 1, 2, 3].map((key) => (
        <Surface key={key} level="base" bordered rounded className="overview-section">
          <Skeleton variant="text" lines={2} />
        </Surface>
      ))}
    </div>
  );
}

function OverviewError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <Surface level="subtle" bordered rounded className="overview-section overview-error" role="alert">
      <p className="overview-error__message">{message}</p>
      <Button variant="secondary" onClick={onRetry}>
        Retry
      </Button>
    </Surface>
  );
}

/**
 * Screen 1: the primary tier. Answers, at a glance, "can the provider serve
 * right now, and if not, why" - see `useOverviewViewModel` for the data
 * derivation this renders. Kept calm and narrow (~800-960px of readable
 * content) rather than filled with cards, per the Sprint 3 brief.
 */
export function OverviewPage() {
  const vm = useOverviewViewModel();

  return (
    <Surface as="main" level="canvas" className="overview-page" aria-labelledby="overview-heading">
      <h1 id="overview-heading" className="overview-page__title">
        Overview
      </h1>

      {vm.state === 'loading' && <OverviewSkeleton />}

      {vm.state === 'error' && (
        <div className="overview-page__body app-shell__sparse-state">
          <OverviewError message={vm.message} onRetry={vm.retry} />
        </div>
      )}

      {(vm.state === 'empty' || vm.state === 'populated') && (
        <div className="overview-page__body overview-page__body--summary">
          <ReadinessSection data={vm.data} noModels={vm.state === 'empty'} />
          <ActiveModelSection activeModelLabel={vm.data.activeModelLabel} />
          <GateSection gate={vm.data.gate} />
          <MaintenanceSection maintenance={vm.data.maintenance} />
        </div>
      )}
    </Surface>
  );
}
