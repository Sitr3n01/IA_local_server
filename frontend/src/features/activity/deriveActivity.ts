import type { GpuMemory, RecentEvent, Status } from '../../api/schemas/status';

/**
 * `'idle' | 'working' | 'saturated'` - see `classifyPressure`'s doc comment
 * for exactly how each is derived.
 */
export type GatePressure = 'idle' | 'working' | 'saturated';

/**
 * Admission-gate snapshot, presentation-ready. Carries all seven raw
 * `status.gate` fields verbatim (see `GateSchema` in
 * `src/api/schemas/status.ts`) - including `wait_timeout_seconds`, which no
 * screen has ever rendered before this one - plus two derived saturation
 * ratios and a `pressure` classification the UI renders without
 * recomputing anything itself.
 */
export interface GateActivity {
  active: number;
  maxActive: number;
  queued: number;
  maxQueue: number;
  waitTimeoutSeconds: number;
  rejectedTotal: number;
  timedOutTotal: number;
  /** `active / maxActive`, clamped to [0, 1]. See `saturationRatio` for the `maxActive <= 0` case. */
  activeSaturation: number;
  /** `queued / maxQueue`, clamped to [0, 1]. Same zero-guard as `activeSaturation`. */
  queueSaturation: number;
  pressure: GatePressure;
}

export interface ActivityEvents {
  /**
   * The raw rolling window, verbatim from `status.recent_events`. This is a
   * log of *requests* the provider has handled, each with its own
   * timestamp - not a measurement of any resource (CPU/memory/VRAM) over
   * time. The status payload never reports the latter; see `ActivityData`'s
   * doc comment.
   */
  entries: RecentEvent[];
  /**
   * The newest few entries, newest first - what the Activity screen shows
   * without asking, above the full log.
   *
   * Sorted here rather than taken off either end of `entries`. cia-edge
   * appends in arrival order and shifts on overflow
   * (internal/edge/events.go's eventStore), so `entries.slice(-6).reverse()`
   * would give the same answer today - but nothing in the wire contract
   * promises that ordering, "newest first" is the ordering a log endpoint
   * naturally drifts toward, and a screen captioned "latest" that silently
   * shows the *oldest* six is a worse failure than a screen that shows
   * nothing. Same reasoning as `windowSpanMs`'s min/max below.
   *
   * Entries whose timestamp does not parse keep their payload order and go
   * last: an unreadable clock is not evidence of being recent.
   */
  recent: RecentEvent[];
  total: number;
  /** Count of `entries` whose `status` is >= 400. */
  errorCount: number;
  /** The largest `duration_ms` across `entries`. `0` when `entries` is empty - a real zero over an empty set, not "not measured". */
  slowestMs: number;
  /**
   * How much wall-clock time the window covers: the latest timestamp in
   * `entries` minus the earliest, in milliseconds. Computed as a min/max
   * over the whole window rather than an endpoint subtraction, so it does
   * not depend on `entries` being sorted - see `deriveEvents`. Never
   * negative.
   *
   * `null` when fewer than two events, or fewer than two *parseable*
   * timestamps, are available - a bad or foreign timestamp format degrades
   * to "unknown span", never to `NaN` reaching the page, a thrown
   * exception, or a fabricated zero.
   */
  windowSpanMs: number | null;
}

/**
 * Maintenance snapshot for Activity's own maintenance section, next to the
 * admission gate it acts on. `state`/`draining`/`drained` are the same
 * three fields Overview already derives in `deriveOverview.ts` - duplicated
 * here rather than imported, per the Sprint 7 brief, so Activity has no
 * cross-feature dependency on Overview's derivation.
 *
 * `active`/`queued`/`rejectedTotal` are new: `status.maintenance.active`
 * and `.queued` are rendered nowhere in the app before this section, even
 * though they are how an operator tells whether a drain has actually
 * finished (both reaching 0 is exactly `drained`'s own condition - see
 * `internal/edge/gate.go`'s `maintenance` method). `rejectedTotal` is a
 * *different* counter from `GateActivity.rejectedTotal` above, despite the
 * similar name: the gate's own `rejected_total` counts admissions refused
 * because the queue was full, while `maintenance.rejected_total` counts
 * only admissions refused specifically because the provider was draining
 * (`internal/edge/gate.go`'s `drainRejected`) - the two can move
 * independently and neither substitutes for the other.
 */
export interface MaintenanceActivity {
  state: string;
  draining: boolean;
  drained: boolean;
  active: number;
  queued: number;
  rejectedTotal: number;
}

export interface ActivityData {
  gate: GateActivity;
  /**
   * Carried through verbatim from `status.gpu_memory` (moved here from
   * `deriveSystem.ts` in Sprint 6 - see that file's doc comment). A
   * snapshot reading, never a history: this payload has no VRAM-over-time
   * series, so nothing downstream of this field may imply a trend (no
   * sparkline, no chart) - only the current state.
   */
  gpuMemory: GpuMemory;
  events: ActivityEvents;
  /** Sprint 7 addition: see `MaintenanceActivity`'s doc comment. */
  maintenance: MaintenanceActivity;
}

/**
 * `numerator / denominator`, clamped to [0, 1], with an explicit rule for
 * `denominator <= 0`: a gate configured with a max of 0 admits nothing at
 * all, ever, regardless of the numerator - so it is treated as *already
 * fully saturated* (`1`) rather than computed as `0/0 = NaN` or
 * `x/0 = Infinity`. A zero-capacity gate is not "0% busy"; it is
 * permanently at its own ceiling.
 */
function saturationRatio(numerator: number, denominator: number): number {
  if (denominator <= 0) return 1;
  return Math.min(1, Math.max(0, numerator / denominator));
}

/**
 * Classifies the gate's current pressure directly from its own admission
 * semantics, not from an invented percentage cutoff:
 *
 *  - `saturated`: the gate is at (or, defensively, beyond) a hard limit
 *    right now - either every active slot is in use
 *    (`activeSaturation >= 1`, so the *next* request cannot start
 *    immediately and must queue) or the queue itself is full
 *    (`queueSaturation >= 1`, so the *next* request is refused/times out
 *    outright). This is the one state actually worth an operator's
 *    attention, because it changes what happens to the next request.
 *  - `idle`: nothing is happening - zero active and zero queued.
 *  - `working`: anything in between - some load, but headroom remains.
 *
 * A gate whose `maxActive` or `maxQueue` is 0 is classified `saturated`
 * even at `active === 0 && queued === 0` (see `saturationRatio` above): a
 * gate structurally configured to admit nothing is not "calm", it is
 * inert-by-configuration, which is exactly the kind of thing this screen
 * exists to surface rather than hide behind an "idle" label.
 *
 * Deliberately no "busy but not yet saturated" middle threshold
 * (e.g. 70%/90% full): the payload only actually supports one meaningful
 * boundary - full vs. not full - so that is the only one this function
 * invents.
 */
function classifyPressure(
  activeSaturation: number,
  queueSaturation: number,
  active: number,
  queued: number,
): GatePressure {
  if (activeSaturation >= 1 || queueSaturation >= 1) return 'saturated';
  if (active === 0 && queued === 0) return 'idle';
  return 'working';
}

function deriveMaintenance(status: Status): MaintenanceActivity {
  const { maintenance } = status;
  return {
    state: maintenance.state,
    draining: maintenance.draining,
    drained: maintenance.drained,
    active: maintenance.active,
    queued: maintenance.queued,
    rejectedTotal: maintenance.rejected_total,
  };
}

function deriveGate(status: Status): GateActivity {
  const { gate } = status;
  const activeSaturation = saturationRatio(gate.active, gate.max_active);
  const queueSaturation = saturationRatio(gate.queued, gate.max_queue);

  return {
    active: gate.active,
    maxActive: gate.max_active,
    queued: gate.queued,
    maxQueue: gate.max_queue,
    waitTimeoutSeconds: gate.wait_timeout_seconds,
    rejectedTotal: gate.rejected_total,
    timedOutTotal: gate.timed_out_total,
    activeSaturation,
    queueSaturation,
    pressure: classifyPressure(activeSaturation, queueSaturation, gate.active, gate.queued),
  };
}

/** `Date.parse`, but an unparseable string collapses to `null` instead of poisoning downstream subtraction with `NaN`. */
function parseTimeMs(time: string): number | null {
  const ms = Date.parse(time);
  return Number.isNaN(ms) ? null : ms;
}

/**
 * How many entries the always-visible "latest requests" list carries.
 *
 * Six, because the point of that list is a sense of *recency and rhythm* -
 * is this provider busy, is it erroring, when did it last do anything - and
 * that is legible in a handful of lines. The full window stays one
 * disclosure away, complete and untruncated.
 */
const RECENT_EVENT_LIMIT = 6;

/**
 * Newest first, with a stable tie-break on payload order so two events
 * recorded in the same millisecond never swap places between renders.
 * Unparseable timestamps sort last, also in payload order.
 */
function sortNewestFirst(entries: RecentEvent[]): RecentEvent[] {
  return entries
    .map((entry, index) => ({ entry, index, ms: parseTimeMs(entry.time) }))
    .sort((a, b) => {
      if (a.ms === null || b.ms === null) {
        if (a.ms === b.ms) return a.index - b.index;
        return a.ms === null ? 1 : -1;
      }
      if (a.ms !== b.ms) return b.ms - a.ms;
      return a.index - b.index;
    })
    .map((item) => item.entry);
}

function deriveEvents(entries: RecentEvent[]): ActivityEvents {
  const total = entries.length;
  const errorCount = entries.filter((entry) => entry.status >= 400).length;
  const slowestMs = entries.reduce((max, entry) => Math.max(max, entry.duration_ms), 0);

  // Earliest and latest across the whole window, not first-minus-last.
  //
  // cia-edge does append in arrival order and shift on overflow
  // (internal/edge/events.go's eventStore), so the endpoints would give the
  // same answer today. But nothing in the wire contract promises that
  // ordering - GateSchema's sibling `RecentEventSchema` says nothing about
  // it - and "newest first" is the ordering a log view naturally drifts
  // toward. Under that ordering an endpoint subtraction silently yields a
  // negative span, which would render as a negative duration rather than
  // fail. Taking the min and max is order-independent, so this cannot be
  // broken by a backend change that is otherwise entirely reasonable.
  //
  // It also degrades better on bad input: a single unparseable timestamp
  // anywhere costs only that one sample, where parsing just the endpoints
  // loses the whole span if either happens to be the bad one.
  let windowSpanMs: number | null = null;
  if (entries.length >= 2) {
    let earliest: number | null = null;
    let latest: number | null = null;
    let parsed = 0;
    for (const entry of entries) {
      const ms = parseTimeMs(entry.time);
      if (ms === null) continue;
      parsed += 1;
      if (earliest === null || ms < earliest) earliest = ms;
      if (latest === null || ms > latest) latest = ms;
    }
    // Two parseable samples are the minimum for a span to mean anything.
    // With one, `earliest` and `latest` are the same sample and the
    // subtraction yields 0 - which on screen reads as "every request
    // happened at the same instant" rather than "not enough information".
    // Those are different claims and only one of them is true.
    windowSpanMs = parsed >= 2 && earliest !== null && latest !== null ? latest - earliest : null;
  }

  return { entries, recent: sortNewestFirst(entries).slice(0, RECENT_EVENT_LIMIT), total, errorCount, slowestMs, windowSpanMs };
}

/**
 * Derives Activity's presentation-ready state from a raw, Zod-validated
 * `Status` payload. Sprint 6: "what is the provider doing right now" - the
 * admission gate (all seven fields), GPU memory (moved verbatim from
 * `deriveSystem.ts`), and the recent-events feed reframed as a request
 * timeline. Sprint 7 added `maintenance`: drain/resume live beside the
 * admission gate they act on - see `MaintenanceActivity`'s doc comment.
 *
 * There is deliberately no resource-history figure anywhere in this
 * module's output. `status` is one snapshot with no VRAM-over-time or
 * CPU-over-time series, and `recent_events` is a rolling window of
 * *requests* (each with its own timestamp/method/path/status/duration) -
 * that supports a request timeline, not a resource trend. Nothing this
 * function returns should ever be plotted as if it were a measurement over
 * time beyond what `events.entries` genuinely is: a log of discrete past
 * requests.
 */
export function deriveActivity(status: Status): ActivityData {
  return {
    gate: deriveGate(status),
    gpuMemory: status.gpu_memory,
    events: deriveEvents(status.recent_events),
    maintenance: deriveMaintenance(status),
  };
}
