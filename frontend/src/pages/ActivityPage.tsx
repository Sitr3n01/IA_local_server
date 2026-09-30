import { useId, useMemo, useState } from 'react';
import { Button, IconAlertCircle, IconCheck, IconChevronDown, IconInfo, Skeleton, Surface } from '../design-system/primitives';
import { FieldList, Notice, StatGroup } from '../design-system/components';
import { useActivityViewModel } from '../features/activity/useActivityViewModel';
import { useMaintenanceAction } from '../features/activity/useMaintenanceAction';
import { drainProvider, resumeProvider } from '../api/commands/maintenance';
import type { ActivityData, GateActivity, GatePressure } from '../features/activity/deriveActivity';
import { formatEventClock } from '../features/activity/format';
import { formatMib, formatUptime } from '../features/system-status/format';
import { assertNever } from '../lib/assertNever';
import './page.css';
import './ActivityPage.css';
import { maintenanceLabel } from '../features/maintenanceState';

/**
 * Text + icon + tone for each `GatePressure` value - colour never carries
 * the meaning alone, per the design-system rule (see `ResourceMeter.tsx`).
 * Thresholds themselves live in `deriveActivity.ts`'s `classifyPressure` -
 * this is presentation only.
 */
const PRESSURE_INFO: Record<GatePressure, { label: string; description: string; icon: typeof IconCheck; tone: string }> =
  {
    idle: {
      label: 'Idle',
      description: 'No requests are active or queued right now.',
      icon: IconCheck,
      tone: 'page-status--neutral',
    },
    working: {
      label: 'Working',
      description: 'Requests are flowing and the gate still has headroom.',
      icon: IconInfo,
      tone: 'page-status--positive',
    },
    saturated: {
      label: 'Saturated',
      description: 'The gate is at its limit right now - the next request will queue or be refused.',
      icon: IconAlertCircle,
      tone: 'page-status--attention',
    },
  };

/**
 * The gate's current pressure, as a line rather than as a filled badge on a
 * tinted panel. The state word is the largest thing in the section after
 * its heading, and the sentence under it says the same thing in full - so
 * neither the fill that used to be there nor the colour is doing any of the
 * work.
 */
function PressureLine({ pressure }: { pressure: GatePressure }) {
  const info = PRESSURE_INFO[pressure];
  const Icon = info.icon;
  return (
    <div className="activity-gate__pressure">
      <p className={`page-status ${info.tone} activity-gate__pressure-label`}>
        <Icon />
        {info.label}
      </p>
      <p className="page-section__lead">{info.description}</p>
    </div>
  );
}

const BAR_VIEW_WIDTH = 300;
const BAR_HEIGHT = 8;
const BAR_Y = 2;

/**
 * One admission-budget bar (active slots, or queue slots). Uses the same
 * inline-SVG-with-numeric-attributes technique as `ResourceMeter` for the
 * proportional fill - never an inline `style` attribute, since the
 * production CSP (`style-src 'self'`, no `'unsafe-inline'`) silently drops
 * one. This is a proportion of a live snapshot (current count vs. its
 * configured ceiling), not a history - no axis, no time dimension.
 *
 * Sprint 10 halved its height and moved the label to a sentence-case
 * caption. A 16px capsule reads as a component; an 8px rule reads as a
 * measurement, which is what it is, and lets the two bars stack as a
 * legible pair instead of as two widgets.
 */
function GateSaturationBar({
  label,
  current,
  max,
  ratio,
}: {
  label: string;
  current: number;
  max: number;
  ratio: number;
}) {
  const admitsNothing = max <= 0;
  const atCapacity = ratio >= 1;
  const width = Math.max(0, Math.min(BAR_VIEW_WIDTH, ratio * BAR_VIEW_WIDTH));

  const verdictText = admitsNothing ? 'Configured to admit none' : atCapacity ? 'At capacity' : 'Headroom available';
  const Icon = admitsNothing || atCapacity ? IconAlertCircle : IconCheck;

  return (
    <div className="activity-gate__bar">
      <div className="activity-gate__bar-header">
        <span className="activity-gate__bar-label">{label}</span>
        <span
          className={`activity-gate__bar-verdict${admitsNothing || atCapacity ? ' activity-gate__bar-verdict--attention' : ''}`}
        >
          <Icon />
          {current} / {max} &middot; {verdictText}
        </span>
      </div>
      <svg
        className="activity-gate__bar-svg"
        viewBox={`0 0 ${BAR_VIEW_WIDTH} ${BAR_HEIGHT + BAR_Y * 2}`}
        preserveAspectRatio="none"
        role="img"
        aria-label={`${label}: ${current} of ${max} in use - ${verdictText.toLowerCase()}`}
      >
        <rect
          x={0}
          y={BAR_Y}
          width={BAR_VIEW_WIDTH}
          height={BAR_HEIGHT}
          rx={BAR_HEIGHT / 2}
          className="activity-gate__bar-track"
        />
        <rect
          x={0}
          y={BAR_Y}
          width={width}
          height={BAR_HEIGHT}
          rx={BAR_HEIGHT / 2}
          className={`activity-gate__bar-fill${admitsNothing || atCapacity ? ' activity-gate__bar-fill--attention' : ''}`}
        />
      </svg>
    </div>
  );
}

/**
 * Section 1: the admission gate. All seven raw fields from `status.gate`
 * render somewhere here, including `wait_timeout_seconds`, which no earlier
 * screen ever surfaced.
 */
function AdmissionGateSection({ gate }: { gate: GateActivity }) {
  return (
    <section className="page-section" aria-labelledby="gate-heading">
      <h2 id="gate-heading" className="page-section__title">
        Admission gate
      </h2>

      <PressureLine pressure={gate.pressure} />

      <div className="activity-gate__bars">
        <GateSaturationBar label="Active" current={gate.active} max={gate.maxActive} ratio={gate.activeSaturation} />
        <GateSaturationBar label="Queued" current={gate.queued} max={gate.maxQueue} ratio={gate.queueSaturation} />
      </div>

      <FieldList
        className="activity-section__fields"
        fields={[
          { label: 'Wait timeout', value: `${gate.waitTimeoutSeconds}s`, mono: true },
          { label: 'Rejected (total)', value: gate.rejectedTotal, mono: true },
          { label: 'Timed out (total)', value: gate.timedOutTotal, mono: true },
        ]}
      />
    </section>
  );
}

/**
 * Activity's own description per maintenance state. The LABEL comes from
 * `maintenanceLabel` in `src/features/maintenanceState.ts`, shared with
 * Overview - Sprint 8 found this screen calling the `maintenance` state
 * "Drained" while Overview called the same state "Maintenance". The
 * descriptions stay page-local: this one points at the counters directly
 * below it, which only exist on this screen.
 *
 * Known values only (`internal/edge/gate.go`'s `maintenanceRunning`/
 * `maintenanceDraining`/`maintenanceDrained`), with the same exact-match
 * fallback as everywhere else - an unrecognised state shows the raw string
 * rather than being hidden.
 */
const MAINTENANCE_STATE_DESCRIPTIONS: Record<string, string> = {
  running: 'Serving normally - admitting new inference.',
  draining: 'Not admitting new inference. Waiting for the active and queued requests below to reach zero.',
  maintenance: 'Fully drained: no active or queued requests remain. Not serving.',
};

type MaintenanceActionKind = 'drain' | 'resume';

/**
 * Never offers the operation already in effect: once draining has started
 * (or finished, i.e. `drained`), only "resume" is offered; otherwise only
 * "drain" is. Both are idempotent server-side (`internal/edge/gate.go`'s
 * `drain`/`resume`), but offering the one already in effect would invite an
 * operator to wonder what a second click even does.
 */
function maintenanceActionKind(maintenance: ActivityData['maintenance']): MaintenanceActionKind {
  return maintenance.draining || maintenance.drained ? 'resume' : 'drain';
}

/**
 * Same "Request ___", never the bare verb, discipline as
 * `ModelsPage.tsx`'s `ACTION_LABEL`/`ACTION_CAPTION`, for the same reason:
 * ADR 0018 control 1 puts the real authorization in the native Win32 dialog
 * `cia-console.exe` shows outside this DOM (see
 * `cmd/cia-console/main_windows.go`'s `win32Approver.Approve`), so this
 * button must never read as the confirmation step itself.
 */
const MAINTENANCE_ACTION_LABEL: Record<MaintenanceActionKind, string> = {
  drain: 'Request drain',
  resume: 'Request resume',
};

const MAINTENANCE_ACTION_CAPTION: Record<MaintenanceActionKind, string> = {
  drain:
    'Asks the native console host to confirm before this provider stops admitting new inference. This button does not drain it by itself.',
  resume:
    'Asks the native console host to confirm before this provider resumes admitting new inference. This button does not resume it by itself.',
};

function maintenanceCommandFor(kind: MaintenanceActionKind): () => Promise<void> {
  switch (kind) {
    case 'drain':
      return drainProvider;
    case 'resume':
      return resumeProvider;
    default:
      return assertNever(kind, 'ActivityPage.maintenanceCommandFor');
  }
}

function MaintenanceActionButton({ maintenance }: { maintenance: ActivityData['maintenance'] }) {
  const kind = maintenanceActionKind(maintenance);
  // Re-created only when the offered kind flips between drain/resume - not
  // on every render - same memoization discipline as `ModelsPage.tsx`'s
  // `ModelActionButton`.
  const command = useMemo(() => maintenanceCommandFor(kind), [kind]);
  const [outcome, run] = useMaintenanceAction(command);
  const captionId = useId();
  const isPending = outcome.status === 'pending';

  return (
    <div className="activity-maintenance__action">
      <Button
        variant="primary"
        size="sm"
        disabled={isPending}
        loading={isPending}
        aria-describedby={captionId}
        onClick={() => void run()}
      >
        {MAINTENANCE_ACTION_LABEL[kind]}
      </Button>
      <p id={captionId} className="activity-maintenance__action-caption">
        {MAINTENANCE_ACTION_CAPTION[kind]}
      </p>
      {outcome.status === 'no-native-host' && (
        <Notice
          tone="warning"
          role="alert"
          className="activity-maintenance__action-outcome activity-maintenance__action-outcome--no-native-host"
        >
          {outcome.message}
        </Notice>
      )}
      {outcome.status === 'error' && (
        <Notice
          tone="danger"
          role="alert"
          className="activity-maintenance__action-outcome activity-maintenance__action-outcome--error"
        >
          The request could not be completed: {outcome.message}
        </Notice>
      )}
    </div>
  );
}

/**
 * Section 2 (Sprint 7): maintenance drain/resume, placed beside the
 * admission gate it acts on. Shows the current state, the drain progress
 * counters no earlier screen has ever rendered, one action button offering
 * only the operation not already in effect, and the three operator-facing
 * consequences of draining, verified against the Go source - stated here,
 * next to the control, not in a footnote.
 *
 * Two of those three consequences are plain rows now rather than filled
 * panels. The third keeps a band, because it is the one an operator can be
 * actively misled by: a drain does not survive a restart, and someone who
 * believes otherwise will act on a provider they think is offline.
 */
function MaintenanceSection({ maintenance }: { maintenance: ActivityData['maintenance'] }) {
  const description = MAINTENANCE_STATE_DESCRIPTIONS[maintenance.state];
  const isAbnormal = maintenance.draining || maintenance.drained;

  return (
    <section className="page-section page-section--ruled" aria-labelledby="maintenance-heading">
      <h2 id="maintenance-heading" className="page-section__title">
        Maintenance
      </h2>

      <div className="activity-maintenance__state">
        <p
          className={`page-status ${isAbnormal ? 'page-status--attention' : 'page-status--positive'} activity-maintenance__state-label`}
        >
          {isAbnormal ? <IconAlertCircle /> : <IconCheck />}
          {maintenanceLabel(maintenance.state)}
        </p>
        <p className="page-section__lead">
          {description ?? `Unrecognized maintenance state reported by the provider: ${maintenance.state}`}
        </p>
      </div>

      <FieldList
        className="activity-section__fields"
        fields={[
          { label: 'Drained', value: maintenance.drained ? 'Yes' : 'No' },
          { label: 'Active requests', value: maintenance.active, mono: true },
          { label: 'Queued requests', value: maintenance.queued, mono: true },
          { label: 'Rejected while draining (total)', value: maintenance.rejectedTotal, mono: true },
        ]}
      />

      <MaintenanceActionButton maintenance={maintenance} />

      <div className="activity-maintenance__consequences">
        <p className="activity-maintenance__consequence">
          <IconInfo />
          <span>
            Draining stops readiness routing: <code>/readyz</code> reports not-ready (with a Retry-After header)
            while draining, so anything that gates traffic on readiness stops sending it here.
          </span>
        </p>
        <p className="activity-maintenance__consequence">
          <IconInfo />
          <span>
            Draining does not unblock model control: load, unload, and switch stay refused (409) for the whole
            time a drain is in progress. They only become available again once the provider is fully drained -
            active and queued both at 0, above.
          </span>
        </p>
        <Notice tone="danger" className="activity-maintenance__warning">
          <span>
            A drain does not survive a restart. Maintenance is process-local state: if the edge process restarts
            for any reason, it always comes back running, as if this drain had never happened - do not assume the
            provider is still offline just because a drain was requested earlier.
          </span>
        </Notice>
      </div>
    </section>
  );
}

/**
 * Section 3: GPU memory, moved verbatim (same fields, same null handling)
 * from `SystemPage.tsx`'s prior `GpuMemorySection` - see `deriveActivity.ts`
 * for why it lives here now. `null` still means "not measured", never 0.
 */
function GpuMemorySection({ gpuMemory }: { gpuMemory: ActivityData['gpuMemory'] }) {
  return (
    <section className="page-section page-section--ruled" aria-labelledby="gpu-memory-heading">
      <h2 id="gpu-memory-heading" className="page-section__title">
        GPU memory
      </h2>
      <p className="page-section__lead">
        A single reading taken just now - the provider does not report GPU memory over time, so no trend or
        history is shown here.
      </p>
      <FieldList
        className="activity-section__fields"
        fields={[
          { label: 'State', value: gpuMemory.state },
          { label: 'Dedicated', value: formatMib(gpuMemory.dedicated_mib), mono: true },
          { label: 'Shared', value: formatMib(gpuMemory.shared_mib), mono: true },
          { label: 'Adapter', value: gpuMemory.adapter ?? 'not reported', mono: true },
          ...(gpuMemory.message ? [{ label: 'Note', value: gpuMemory.message }] : []),
        ]}
      />
    </section>
  );
}

/**
 * The always-visible tail of the window: the last few requests, newest
 * first, as one line each.
 *
 * This is the screen's one genuinely temporal element, and it is why
 * Activity no longer reads as Models with different words in it. The
 * figures above summarise; this shows the provider's pulse - when it last
 * did anything, how fast, and whether it failed. Ordering comes from
 * `deriveActivity.ts`'s `recent`, which sorts rather than trusting the
 * payload's arrival order.
 *
 * A status of 400 or more is tinted, but the code itself is the
 * information - `503` says what happened whether or not the tint is
 * perceivable.
 */
function LatestRequests({ recent }: { recent: ActivityData['events']['recent'] }) {
  return (
    <div className="activity-latest">
      {/* A real heading, and the list names itself by it. The label is the
          only thing that says these six lines are the newest ones rather
          than an arbitrary sample, so it has to be in the outline, not just
          on the screen. */}
      <h3 id="activity-latest-heading" className="page-subsection__title">
        Latest requests
      </h3>
      <ol className="activity-latest__list" aria-labelledby="activity-latest-heading">
        {recent.map((entry) => (
          <li key={entry.request_id} className="activity-latest__row">
            <span className="activity-latest__time">{formatEventClock(entry.time)}</span>
            <span className="activity-latest__route">
              {entry.method} {entry.path}
            </span>
            <span
              className={`activity-latest__status${entry.status >= 400 ? ' activity-latest__status--error' : ''}`}
            >
              {entry.status}
            </span>
            <span className="activity-latest__duration">{entry.duration_ms} ms</span>
          </li>
        ))}
      </ol>
    </div>
  );
}

/**
 * Section 4: the request timeline. "Default calm + details on demand" -
 * the four-figure summary and the latest few requests are always visible;
 * the full per-request table is behind a disclosure toggle, mirroring
 * `ModelsPage.tsx`'s "Show capacity details" pattern (same
 * `aria-expanded`/`aria-controls` wiring, same rotating-chevron
 * affordance, now on a `ghost` button).
 */
function RequestTimelineSection({ events }: { events: ActivityData['events'] }) {
  const [detailsOpen, setDetailsOpen] = useState(false);
  const detailsId = useId();

  const spanText =
    events.windowSpanMs === null
      ? 'not enough events to measure a span'
      : `over ${formatUptime(Math.round(events.windowSpanMs / 1000))}`;

  return (
    <section className="page-section page-section--ruled" aria-labelledby="timeline-heading">
      <h2 id="timeline-heading" className="page-section__title">
        Request timeline
      </h2>
      <p className="page-section__lead">
        A rolling window of the most recent requests this provider has handled - metadata only (time, request
        id, method, sanitized path, status, duration). This is a log of requests, not a resource trend: the
        provider reports a snapshot for CPU/memory/GPU, never a history. Times in the list below are this
        machine&rsquo;s local clock; the full log keeps the provider&rsquo;s own UTC timestamps.
      </p>

      {events.total === 0 ? (
        <p className="page-empty">No requests recorded yet.</p>
      ) : (
        <>
          <StatGroup
            items={[
              { label: 'Requests', value: events.total, mono: true },
              { label: 'Errors', value: events.errorCount, mono: true },
              { label: 'Slowest', value: `${events.slowestMs} ms`, mono: true },
              { label: 'Span', value: spanText },
            ]}
          />

          <LatestRequests recent={events.recent} />

          <Button
            variant="ghost"
            size="sm"
            className="activity-timeline__disclosure"
            aria-expanded={detailsOpen}
            aria-controls={detailsId}
            onClick={() => setDetailsOpen((value) => !value)}
            trailingIcon={
              <IconChevronDown
                className={`page-disclosure-icon${detailsOpen ? ' page-disclosure-icon--open' : ''}`}
              />
            }
          >
            {detailsOpen ? 'Hide request details' : 'Show request details'}
          </Button>

          {/* Stays mounted while collapsed, hidden by attribute rather than
              unmounted: the toggle's `aria-controls` must resolve to a real
              element in BOTH states. Conditionally rendering this left the
              control pointing at an id that did not exist whenever it was
              closed, which a screen reader follows to nothing. See `[hidden]`
              in design-system/styles/reset.css for why the attribute alone is
              not sufficient against component `display` rules. */}
          <div
            id={detailsId}
            className="activity-events__scroll"
            role="region"
            aria-label="Full request log"
            hidden={!detailsOpen}
          >
            <table className="activity-events__table">
              <thead>
                <tr>
                  <th scope="col">Time</th>
                  <th scope="col">Request ID</th>
                  <th scope="col">Method</th>
                  <th scope="col">Path</th>
                  <th scope="col">Status</th>
                  <th scope="col">Duration (ms)</th>
                </tr>
              </thead>
              <tbody>
                {events.entries.map((event) => (
                  <tr key={event.request_id}>
                    <td className="activity-events__cell">{event.time}</td>
                    <td className="activity-events__cell">{event.request_id}</td>
                    <td className="activity-events__cell">{event.method}</td>
                    <td className="activity-events__cell">{event.path}</td>
                    <td className="activity-events__cell">{event.status}</td>
                    <td className="activity-events__cell">{event.duration_ms}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </section>
  );
}

function ActivitySkeleton() {
  return (
    <div className="activity-page__body app-shell__sparse-state" aria-busy="true">
      <span className="ds-visually-hidden" role="status">
        Loading activity…
      </span>
      <div className="activity-skeleton">
        {[0, 1, 2].map((key) => (
          <div key={key} className="activity-skeleton__section">
            <Skeleton variant="text" className="activity-skeleton__heading" />
            <Skeleton variant="text" lines={2} />
          </div>
        ))}
      </div>
    </div>
  );
}

/**
 * Screen 4: "what is the provider doing right now" - see
 * `useActivityViewModel`/`deriveActivity.ts` for the derivation this
 * renders. Four sections, read top to bottom as one vertical flow separated
 * by hairlines rather than as four panels: the admission gate, maintenance
 * drain/resume (Sprint 7, placed right beside the gate it acts on), GPU
 * memory (moved here from System), and the request timeline - summary, the
 * latest few requests, and the full table on demand.
 *
 * There is no resource history anywhere on this screen, by design: the
 * status payload is a single snapshot, and `recent_events` is a rolling
 * window of requests, not of any resource measurement. Nothing here draws
 * a chart, a sparkline, or any other implied trend - see the Sprint 6
 * brief's explicit gate against that. The two gate bars are proportions of
 * a live count against its configured ceiling, with no time axis.
 */
export function ActivityPage() {
  const vm = useActivityViewModel();

  return (
    <Surface as="main" level="canvas" className="activity-page" aria-labelledby="activity-heading">
      <div className="activity-page__header">
        <h1 id="activity-heading" className="page-heading">
          Activity
        </h1>
        <p className="page-lead">
          What this provider is doing at this moment: what it will admit, whether it is in maintenance, and the
          requests it has just handled.
        </p>
      </div>

      {vm.state === 'loading' && <ActivitySkeleton />}

      {vm.state === 'error' && (
        <div className="activity-page__body app-shell__sparse-state">
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
        <div className="activity-page__body page-flow">
          <AdmissionGateSection gate={vm.data.gate} />
          <MaintenanceSection maintenance={vm.data.maintenance} />
          <GpuMemorySection gpuMemory={vm.data.gpuMemory} />
          <RequestTimelineSection events={vm.data.events} />
        </div>
      )}
    </Surface>
  );
}
