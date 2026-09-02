import { useId, useMemo, useState } from 'react';
import {
  Button,
  IconAlertCircle,
  IconCheck,
  IconChevronDown,
  IconInfo,
  Skeleton,
  Surface,
} from '../design-system/primitives';
import { FieldList } from '../design-system/components';
import { useActivityViewModel } from '../features/activity/useActivityViewModel';
import { useMaintenanceAction } from '../features/activity/useMaintenanceAction';
import { drainProvider, resumeProvider } from '../api/commands/maintenance';
import type { ActivityData, GateActivity, GatePressure } from '../features/activity/deriveActivity';
import { formatMib, formatUptime } from '../features/system-status/format';
import { assertNever } from '../lib/assertNever';
import './ActivityPage.css';
import { maintenanceLabel } from '../features/maintenanceState';

/**
 * Text + icon + colour for each `GatePressure` value - colour never carries
 * the meaning alone, per the design-system rule (see `ResourceMeter.tsx`).
 * Thresholds themselves live in `deriveActivity.ts`'s `classifyPressure` -
 * this is presentation only.
 */
const PRESSURE_INFO: Record<GatePressure, { label: string; description: string; icon: typeof IconCheck; className: string }> = {
  idle: {
    label: 'Idle',
    description: 'No requests are active or queued right now.',
    icon: IconCheck,
    className: 'activity-gate__pressure--idle',
  },
  working: {
    label: 'Working',
    description: 'Requests are flowing and the gate still has headroom.',
    icon: IconInfo,
    className: 'activity-gate__pressure--working',
  },
  saturated: {
    label: 'Saturated',
    description: 'The gate is at its limit right now - the next request will queue or be refused.',
    icon: IconAlertCircle,
    className: 'activity-gate__pressure--saturated',
  },
};

function PressureBadge({ pressure }: { pressure: GatePressure }) {
  const info = PRESSURE_INFO[pressure];
  const Icon = info.icon;
  return (
    <div className={`activity-gate__pressure ${info.className}`}>
      <Icon />
      <div>
        <span className="activity-gate__pressure-label">{info.label}</span>
        <p className="activity-gate__pressure-description">{info.description}</p>
      </div>
    </div>
  );
}

const BAR_VIEW_WIDTH = 300;
const BAR_HEIGHT = 16;
const BAR_Y = 4;

/**
 * One admission-budget bar (active slots, or queue slots). Uses the same
 * inline-SVG-with-numeric-attributes technique as `ResourceMeter` for the
 * proportional fill - never an inline `style` attribute, since the
 * production CSP (`style-src 'self'`, no `'unsafe-inline'`) silently drops
 * one. This is a proportion of a live snapshot (current count vs. its
 * configured ceiling), not a history - no axis, no time dimension.
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
 * Section 1, the signature widget: the admission gate. All seven raw
 * fields from `status.gate` render somewhere here, including
 * `wait_timeout_seconds`, which no earlier screen ever surfaced.
 */
function AdmissionGateSection({ gate }: { gate: GateActivity }) {
  return (
    <Surface as="section" level="base" bordered rounded className="activity-section" aria-labelledby="gate-heading">
      <h2 id="gate-heading" className="activity-section__title">
        Admission gate
      </h2>

      <PressureBadge pressure={gate.pressure} />

      <div className="activity-gate__bars">
        <GateSaturationBar label="Active" current={gate.active} max={gate.maxActive} ratio={gate.activeSaturation} />
        <GateSaturationBar label="Queued" current={gate.queued} max={gate.maxQueue} ratio={gate.queueSaturation} />
      </div>

      <FieldList
        fields={[
          { label: 'Wait timeout', value: `${gate.waitTimeoutSeconds}s`, mono: true },
          { label: 'Rejected (total)', value: gate.rejectedTotal, mono: true },
          { label: 'Timed out (total)', value: gate.timedOutTotal, mono: true },
        ]}
      />
    </Surface>
  );
}

/**
 * Known `maintenance.state` values (`cmd/cia-console` and `internal/edge`
 * only ever produce these three - `internal/edge/gate.go`'s
 * `maintenanceRunning`/`maintenanceDraining`/`maintenanceDrained`
 * constants). Same exact-match-with-fallback discipline as
 * `OverviewPage.tsx`'s own `MAINTENANCE_INFO` - an unrecognised state falls
 * back to showing the raw string rather than hiding it.
 */
/**
 * Activity's own description per maintenance state. The LABEL comes from
 * `maintenanceLabel` in `src/features/maintenanceState.ts`, shared with
 * Overview - Sprint 8 found this screen calling the `maintenance` state
 * "Drained" while Overview called the same state "Maintenance". The
 * descriptions stay page-local: this one points at the counters directly
 * below it, which only exist on this screen.
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
        <p
          className="activity-maintenance__action-outcome activity-maintenance__action-outcome--no-native-host"
          role="alert"
        >
          <IconAlertCircle />
          {outcome.message}
        </p>
      )}
      {outcome.status === 'error' && (
        <p className="activity-maintenance__action-outcome activity-maintenance__action-outcome--error" role="alert">
          <IconAlertCircle />
          The request could not be completed: {outcome.message}
        </p>
      )}
    </div>
  );
}

/**
 * Section 2, Sprint 7: maintenance drain/resume, placed beside the
 * admission gate it acts on (never on Overview, and never behind a new nav
 * destination - see the Sprint 7 brief's gate against a Settings-shaped
 * junk drawer). Shows the current state (`state`/`draining`/`drained`,
 * duplicated from `deriveOverview.ts` rather than imported - see
 * `deriveActivity.ts`'s `MaintenanceActivity` doc comment), the drain
 * progress counters no earlier screen has ever rendered (`active`/
 * `queued`/`rejectedTotal`), one action button offering only the operation
 * not already in effect, and the three operator-facing consequences of
 * draining verified against the Go source - stated here, next to the
 * control, not in a footnote.
 */
function MaintenanceSection({ maintenance }: { maintenance: ActivityData['maintenance'] }) {
  const description = MAINTENANCE_STATE_DESCRIPTIONS[maintenance.state];
  const isAbnormal = maintenance.draining || maintenance.drained;

  return (
    <Surface
      as="section"
      level="base"
      bordered
      rounded
      className="activity-section"
      aria-labelledby="maintenance-heading"
    >
      <h2 id="maintenance-heading" className="activity-section__title">
        Maintenance
      </h2>

      <div className="activity-maintenance__state">
        <span
          className={`activity-maintenance__state-badge${isAbnormal ? ' activity-maintenance__state-badge--attention' : ''}`}
        >
          {isAbnormal ? <IconAlertCircle /> : <IconCheck />}
          {maintenanceLabel(maintenance.state)}
        </span>
        <p className="activity-section__hint">
          {description ?? `Unrecognized maintenance state reported by the provider: ${maintenance.state}`}
        </p>
      </div>

      <FieldList
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
        <p className="activity-maintenance__consequence activity-maintenance__consequence--danger">
          <IconAlertCircle />
          <span>
            A drain does not survive a restart. Maintenance is process-local state: if the edge process restarts
            for any reason, it always comes back running, as if this drain had never happened - do not assume the
            provider is still offline just because a drain was requested earlier.
          </span>
        </p>
      </div>
    </Surface>
  );
}

/**
 * Section 3: GPU memory, moved verbatim (same fields, same null handling)
 * from `SystemPage.tsx`'s prior `GpuMemorySection` - see `deriveActivity.ts`
 * for why it lives here now. `null` still means "not measured", never 0.
 */
function GpuMemorySection({ gpuMemory }: { gpuMemory: ActivityData['gpuMemory'] }) {
  return (
    <Surface as="section" level="base" bordered rounded className="activity-section" aria-labelledby="gpu-memory-heading">
      <h2 id="gpu-memory-heading" className="activity-section__title">
        GPU memory
      </h2>
      <p className="activity-section__hint">
        A single reading taken just now - the provider does not report GPU memory over time, so no trend or
        history is shown here.
      </p>
      <FieldList
        fields={[
          { label: 'State', value: gpuMemory.state },
          { label: 'Dedicated', value: formatMib(gpuMemory.dedicated_mib), mono: true },
          { label: 'Shared', value: formatMib(gpuMemory.shared_mib), mono: true },
          { label: 'Adapter', value: gpuMemory.adapter ?? 'not reported', mono: true },
          ...(gpuMemory.message ? [{ label: 'Note', value: gpuMemory.message }] : []),
        ]}
      />
    </Surface>
  );
}

function TimelineSummary({ events }: { events: ActivityData['events'] }) {
  const spanText =
    events.windowSpanMs === null
      ? 'not enough events to measure a span'
      : `over ${formatUptime(Math.round(events.windowSpanMs / 1000))}`;

  return (
    <dl className="activity-timeline-summary">
      <div className="activity-timeline-summary__item">
        <dt>Requests</dt>
        <dd>{events.total}</dd>
      </div>
      <div className="activity-timeline-summary__item">
        <dt>Errors</dt>
        <dd>{events.errorCount}</dd>
      </div>
      <div className="activity-timeline-summary__item">
        <dt>Slowest</dt>
        <dd>{events.slowestMs} ms</dd>
      </div>
      <div className="activity-timeline-summary__item">
        <dt>Span</dt>
        <dd>{spanText}</dd>
      </div>
    </dl>
  );
}

/**
 * Section 4: the request timeline. "Default calm + details on demand" -
 * the four-number summary above is always visible; the full per-request
 * table is behind a disclosure toggle, mirroring `ModelsPage.tsx`'s
 * "Show capacity details" pattern (same `aria-expanded`/`aria-controls`
 * wiring, same rotating-chevron affordance).
 */
function RequestTimelineSection({ events }: { events: ActivityData['events'] }) {
  const [detailsOpen, setDetailsOpen] = useState(false);
  const detailsId = useId();

  return (
    <Surface as="section" level="base" bordered rounded className="activity-section" aria-labelledby="timeline-heading">
      <h2 id="timeline-heading" className="activity-section__title">
        Request timeline
      </h2>
      <p className="activity-section__hint">
        A rolling window of the most recent requests this provider has handled - metadata only (time, request
        id, method, sanitized path, status, duration). This is a log of requests, not a resource trend: the
        provider reports a snapshot for CPU/memory/GPU, never a history.
      </p>

      {events.total === 0 ? (
        <p className="activity-section__empty">No requests recorded yet.</p>
      ) : (
        <>
          <TimelineSummary events={events} />

          <Button
            variant="secondary"
            size="sm"
            aria-expanded={detailsOpen}
            aria-controls={detailsId}
            onClick={() => setDetailsOpen((value) => !value)}
            trailingIcon={
              <IconChevronDown
                className={`activity-disclosure-icon${detailsOpen ? ' activity-disclosure-icon--open' : ''}`}
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
    </Surface>
  );
}

function ActivitySkeleton() {
  return (
    <div className="activity-page__body app-shell__sparse-state" aria-busy="true">
      <span className="ds-visually-hidden" role="status">
        Loading activity…
      </span>
      {[0, 1, 2].map((key) => (
        <Surface key={key} level="base" bordered rounded className="activity-section">
          <Skeleton variant="text" lines={3} />
        </Surface>
      ))}
    </div>
  );
}

function ActivityError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <Surface level="subtle" bordered rounded className="activity-section activity-error" role="alert">
      <p className="activity-error__message">{message}</p>
      <Button variant="secondary" onClick={onRetry}>
        Retry
      </Button>
    </Surface>
  );
}

/**
 * Screen 4: "what is the provider doing right now" - see
 * `useActivityViewModel`/`deriveActivity.ts` for the derivation this
 * renders. Four sections: the admission gate (this screen's signature
 * widget), maintenance drain/resume (Sprint 7, placed right beside the gate
 * it acts on), GPU memory (moved here from System), and a request timeline
 * (default calm summary, full table on demand).
 *
 * There is no resource history anywhere on this screen, by design: the
 * status payload is a single snapshot, and `recent_events` is a rolling
 * window of requests, not of any resource measurement. Nothing here draws
 * a chart, a sparkline, or any other implied trend - see the Sprint 6
 * brief's explicit gate against that.
 */
export function ActivityPage() {
  const vm = useActivityViewModel();

  return (
    <Surface as="main" level="canvas" className="activity-page" aria-labelledby="activity-heading">
      <h1 id="activity-heading" className="activity-page__title">
        Activity
      </h1>

      {vm.state === 'loading' && <ActivitySkeleton />}

      {vm.state === 'error' && (
        <div className="activity-page__body app-shell__sparse-state">
          <ActivityError message={vm.message} onRetry={vm.retry} />
        </div>
      )}

      {(vm.state === 'empty' || vm.state === 'populated') && (
        <div className="activity-page__body">
          <AdmissionGateSection gate={vm.data.gate} />
          <MaintenanceSection maintenance={vm.data.maintenance} />
          <GpuMemorySection gpuMemory={vm.data.gpuMemory} />
          <RequestTimelineSection events={vm.data.events} />
        </div>
      )}
    </Surface>
  );
}
