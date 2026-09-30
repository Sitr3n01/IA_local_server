import { Button, IconAlertCircle, IconCheck, Skeleton, Surface } from '../design-system/primitives';
import { Notice, StatGroup } from '../design-system/components';
import { useOverviewViewModel } from '../features/system-status/useOverviewViewModel';
import type { OverviewData } from '../features/system-status/deriveOverview';
import './page.css';
import './OverviewPage.css';
import { maintenanceLabel } from '../features/maintenanceState';

/**
 * Overview's own one-line description per maintenance state. The LABEL is
 * not here: it comes from `maintenanceLabel` in
 * `src/features/maintenanceState.ts`, shared with the Activity screen, after
 * Sprint 8 found the same provider state being named "Maintenance" here and
 * "Drained" there. The descriptions stay page-local on purpose - this one is
 * a health summary, Activity's sits beside the drain counters, and one
 * sentence cannot serve both without reading as a non-sequitur somewhere.
 *
 * Exact-match lookup, same discipline as the `reason` lookup in
 * `src/features/system-status/reasonExplanations.ts` - an unrecognised state
 * falls back to showing the raw string.
 */
const MAINTENANCE_DESCRIPTIONS: Record<string, string> = {
  running: 'Serving normally.',
  draining:
    'Finishing in-flight requests and not accepting new ones. Every control on this console now targets a provider that is shutting its workload down, not one accepting fresh work.',
  maintenance: 'Not serving. The provider has been deliberately taken offline for maintenance.',
};

/**
 * The screen's headline: can this provider serve right now.
 *
 * This is the whole reason Overview exists, and until Sprint 10 it was a
 * bordered panel carrying an 18px heading - the same visual weight as the
 * three supporting figures beside it, so nothing on the screen claimed to
 * be the answer. It is now a display-sized statement on the open canvas
 * with no container at all, and the two sentences under it are ordinary
 * prose at a readable measure.
 *
 * The verdict is never carried by colour: the glyph is a shape (a check or
 * an alert ring), the sentence says it in words, and only the glyph takes a
 * status colour. The failure explanation stays in full-strength foreground
 * rather than being tinted red, because it is the thing to be *read*, not a
 * thing to be alarmed by.
 */
function ReadinessStatement({ data, noModels }: { data: OverviewData; noModels: boolean }) {
  const { ready, readiness, availableModelCount, totalModelCount } = data;

  return (
    <section className="overview-readiness" aria-labelledby="overview-readiness-heading">
      <h2
        id="overview-readiness-heading"
        className={`overview-readiness__statement${ready ? '' : ' overview-readiness__statement--not-ready'}`}
      >
        {ready ? (
          <IconCheck className="overview-readiness__icon" />
        ) : (
          <IconAlertCircle className="overview-readiness__icon" />
        )}
        {ready ? 'Ready to serve' : 'Not ready to serve'}
      </h2>

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
    </section>
  );
}

/**
 * The second tier: what is loaded, and the two operational figures that
 * decide whether anything can be done about it.
 *
 * One hairline above it is the only structural line on this screen. Inside,
 * nothing is boxed: the model's name is simply the largest type in the
 * block, and the gate and maintenance figures are a `StatGroup` - captions
 * over values, grouped by proximity. They used to be two more bordered
 * panels in a three-column grid, which is how a two-fact summary became a
 * dashboard.
 */
function OverviewDetail({ data }: { data: OverviewData }) {
  const { activeModelLabel, activeModelDetail, gate, maintenance } = data;
  const maintenanceDescription = MAINTENANCE_DESCRIPTIONS[maintenance.state];

  return (
    <div className="overview-detail page-flow page-section--ruled">
      <section className="page-section" aria-labelledby="overview-model-heading">
        <h2 id="overview-model-heading" className="page-section__title">
          Active model
        </h2>
        <div className="overview-detail__model">
          <p
            className={`overview-detail__model-name${activeModelLabel === null ? ' overview-detail__model-name--none' : ''}`}
          >
            {activeModelLabel ?? 'None loaded'}
          </p>
          {activeModelDetail !== null && <p className="overview-detail__model-detail">{activeModelDetail}</p>}
        </div>
      </section>

      {/* Its own labelled region rather than more content under "Active
          model": these two figures are about the provider's willingness to
          take work, not about the model, and one heading standing over both
          would have named the section after only half of what it contains. */}
      <section className="page-section" aria-labelledby="overview-admission-heading">
        <h2 id="overview-admission-heading" className="page-section__title">
          Admission and maintenance
        </h2>
        <StatGroup
          items={[
            {
              label: 'Admission gate',
              value: `${gate.active} / ${gate.maxActive} active · ${gate.queued} / ${gate.maxQueue} queued`,
              mono: true,
            },
            {
              label: 'Maintenance state',
              value: maintenanceLabel(maintenance.state),
              hint:
                maintenanceDescription ??
                `Unrecognized maintenance state reported by the provider: ${maintenance.state}`,
            },
          ]}
        />
      </section>
    </div>
  );
}

function OverviewSkeleton() {
  return (
    <div className="overview-page__body app-shell__sparse-state" aria-busy="true">
      <span className="ds-visually-hidden" role="status">
        Loading system overview…
      </span>
      {/* Shaped like what is coming: one display-sized statement, a line of
          prose under it, then the detail block. Four identical stacked
          rectangles would promise four panels that no longer exist. */}
      <div className="overview-skeleton">
        <Skeleton variant="text" className="overview-skeleton__statement" />
        <Skeleton variant="text" lines={2} />
        <Skeleton variant="text" className="overview-skeleton__detail" />
      </div>
    </div>
  );
}

/**
 * Screen 1: the home of the console. Answers, at a glance, "can the provider
 * serve right now, and if not, why" - see `useOverviewViewModel` for the
 * data derivation this renders.
 *
 * Sprint 10 rebuilt it as an editorial page rather than a summary
 * dashboard: one large verdict, the prose that explains it, and a quiet
 * detail block behind a single hairline. Every one of the four bordered
 * panels it used to be made of is gone, and no fact went with them.
 */
export function OverviewPage() {
  const vm = useOverviewViewModel();

  return (
    <Surface as="main" level="canvas" className="overview-page" aria-labelledby="overview-heading">
      <h1 id="overview-heading" className="page-heading">
        Overview
      </h1>

      {vm.state === 'loading' && <OverviewSkeleton />}

      {vm.state === 'error' && (
        <div className="overview-page__body app-shell__sparse-state">
          <Notice
            tone="danger"
            role="alert"
            action={
              <Button variant="secondary" onClick={vm.retry}>
                Retry
              </Button>
            }
          >
            {vm.message}
          </Notice>
        </div>
      )}

      {(vm.state === 'empty' || vm.state === 'populated') && (
        <div className="overview-page__body page-flow">
          <ReadinessStatement data={vm.data} noModels={vm.state === 'empty'} />
          <OverviewDetail data={vm.data} />
        </div>
      )}
    </Surface>
  );
}
