import { describe, expect, it } from 'vitest';
import fixture from '../../api/__fixtures__/status.sample.json';
import { StatusSchema } from '../../api/schemas/status';
import { deriveActivity } from './deriveActivity';
import type { RecentEvent, Status } from '../../api/schemas/status';

function statusWith(overrides: Record<string, unknown>): Status {
  return StatusSchema.parse({ ...fixture, ...overrides });
}

function event(overrides: Partial<RecentEvent>): RecentEvent {
  return {
    time: '2026-08-27T13:00:00.0000000Z',
    request_id: 'r1',
    method: 'GET',
    path: '/api/v1/status',
    status: 200,
    duration_ms: 1,
    ...overrides,
  };
}

describe('deriveActivity: gate - all seven fields carried through', () => {
  it('carries every raw gate field verbatim, including wait_timeout_seconds', () => {
    const status = statusWith({
      gate: { active: 1, queued: 2, max_active: 4, max_queue: 8, wait_timeout_seconds: 45, rejected_total: 3, timed_out_total: 7 },
    });
    const activity = deriveActivity(status);

    expect(activity.gate.active).toBe(1);
    expect(activity.gate.queued).toBe(2);
    expect(activity.gate.maxActive).toBe(4);
    expect(activity.gate.maxQueue).toBe(8);
    expect(activity.gate.waitTimeoutSeconds).toBe(45);
    expect(activity.gate.rejectedTotal).toBe(3);
    expect(activity.gate.timedOutTotal).toBe(7);
  });
});

describe('deriveActivity: saturation zero-division guards', () => {
  it('does not produce NaN or Infinity when max_active is 0, and treats it as fully saturated', () => {
    const status = statusWith({ gate: { active: 0, queued: 0, max_active: 0, max_queue: 4, wait_timeout_seconds: 30, rejected_total: 0, timed_out_total: 0 } });
    const activity = deriveActivity(status);

    expect(Number.isFinite(activity.gate.activeSaturation)).toBe(true);
    expect(Number.isNaN(activity.gate.activeSaturation)).toBe(false);
    expect(activity.gate.activeSaturation).toBe(1);
    // A gate configured to admit nothing is reported saturated even with
    // active === 0 and queued === 0 - see classifyPressure's doc comment.
    expect(activity.gate.pressure).toBe('saturated');
  });

  it('does not produce NaN or Infinity when max_queue is 0', () => {
    const status = statusWith({ gate: { active: 0, queued: 0, max_active: 1, max_queue: 0, wait_timeout_seconds: 30, rejected_total: 0, timed_out_total: 0 } });
    const activity = deriveActivity(status);

    expect(Number.isFinite(activity.gate.queueSaturation)).toBe(true);
    expect(activity.gate.queueSaturation).toBe(1);
    expect(activity.gate.pressure).toBe('saturated');
  });

  it('computes a normal in-range ratio when max is positive', () => {
    const status = statusWith({ gate: { active: 1, queued: 2, max_active: 4, max_queue: 8, wait_timeout_seconds: 30, rejected_total: 0, timed_out_total: 0 } });
    const activity = deriveActivity(status);

    expect(activity.gate.activeSaturation).toBe(0.25);
    expect(activity.gate.queueSaturation).toBe(0.25);
  });

  it('clamps a saturation ratio to 1 even if active somehow exceeds max_active', () => {
    const status = statusWith({ gate: { active: 5, queued: 0, max_active: 2, max_queue: 4, wait_timeout_seconds: 30, rejected_total: 0, timed_out_total: 0 } });
    const activity = deriveActivity(status);

    expect(activity.gate.activeSaturation).toBe(1);
  });
});

describe('deriveActivity: pressure classification boundaries', () => {
  it('classifies idle when active and queued are both exactly 0 and capacity is positive', () => {
    const status = statusWith({ gate: { active: 0, queued: 0, max_active: 1, max_queue: 4, wait_timeout_seconds: 30, rejected_total: 0, timed_out_total: 0 } });
    expect(deriveActivity(status).gate.pressure).toBe('idle');
  });

  it('classifies working when active is below max_active and queued is below max_queue but not both zero', () => {
    const status = statusWith({ gate: { active: 1, queued: 0, max_active: 4, max_queue: 8, wait_timeout_seconds: 30, rejected_total: 0, timed_out_total: 0 } });
    expect(deriveActivity(status).gate.pressure).toBe('working');
  });

  it('classifies saturated at the exact boundary where active === max_active', () => {
    const status = statusWith({ gate: { active: 4, queued: 0, max_active: 4, max_queue: 8, wait_timeout_seconds: 30, rejected_total: 0, timed_out_total: 0 } });
    expect(deriveActivity(status).gate.pressure).toBe('saturated');
  });

  it('classifies working one unit below the active boundary (active === max_active - 1)', () => {
    const status = statusWith({ gate: { active: 3, queued: 0, max_active: 4, max_queue: 8, wait_timeout_seconds: 30, rejected_total: 0, timed_out_total: 0 } });
    expect(deriveActivity(status).gate.pressure).toBe('working');
  });

  it('classifies saturated at the exact boundary where queued === max_queue, even if active has headroom', () => {
    const status = statusWith({ gate: { active: 0, queued: 8, max_active: 4, max_queue: 8, wait_timeout_seconds: 30, rejected_total: 0, timed_out_total: 0 } });
    expect(deriveActivity(status).gate.pressure).toBe('saturated');
  });
});

describe('deriveActivity: events - error counting', () => {
  it('counts a status of exactly 400 as an error', () => {
    const status = statusWith({ recent_events: [event({ status: 400 })] });
    expect(deriveActivity(status).events.errorCount).toBe(1);
  });

  it('does not count a status of 399 as an error', () => {
    const status = statusWith({ recent_events: [event({ status: 399 })] });
    expect(deriveActivity(status).events.errorCount).toBe(0);
  });

  it('counts a mix of 2xx/4xx/5xx correctly and reports total/slowest alongside', () => {
    const status = statusWith({
      recent_events: [
        event({ request_id: 'a', status: 200, duration_ms: 5 }),
        event({ request_id: 'b', status: 503, duration_ms: 40 }),
        event({ request_id: 'c', status: 401, duration_ms: 2 }),
      ],
    });
    const { events } = deriveActivity(status);

    expect(events.total).toBe(3);
    expect(events.errorCount).toBe(2);
    expect(events.slowestMs).toBe(40);
  });

  it('reports slowestMs 0 for an empty event list, a real zero rather than "not measured"', () => {
    const status = statusWith({ recent_events: [] });
    expect(deriveActivity(status).events.slowestMs).toBe(0);
    expect(deriveActivity(status).events.total).toBe(0);
    expect(deriveActivity(status).events.errorCount).toBe(0);
  });
});

describe('deriveActivity: events - windowSpanMs', () => {
  it('is null when there are fewer than two events', () => {
    expect(deriveActivity(statusWith({ recent_events: [] })).events.windowSpanMs).toBeNull();
    expect(deriveActivity(statusWith({ recent_events: [event({})] })).events.windowSpanMs).toBeNull();
  });

  it('is the difference between the last and first timestamps when there are two or more events', () => {
    const status = statusWith({
      recent_events: [
        event({ request_id: 'a', time: '2026-08-27T13:00:00.000Z' }),
        event({ request_id: 'b', time: '2026-08-27T13:00:05.000Z' }),
      ],
    });
    expect(deriveActivity(status).events.windowSpanMs).toBe(5000);
  });

  it('does not crash and degrades to null when a timestamp is unparseable', () => {
    const status = statusWith({
      recent_events: [
        event({ request_id: 'a', time: 'not-a-real-timestamp' }),
        event({ request_id: 'b', time: '2026-08-27T13:00:05.000Z' }),
      ],
    });
    expect(() => deriveActivity(status)).not.toThrow();
    expect(deriveActivity(status).events.windowSpanMs).toBeNull();
  });

  it('carries the raw entries through verbatim as the request log', () => {
    const events = [event({ request_id: 'a' }), event({ request_id: 'b' })];
    const status = statusWith({ recent_events: events });
    expect(deriveActivity(status).events.entries.map((entry) => entry.request_id)).toEqual(['a', 'b']);
  });
});

describe('deriveActivity: gpuMemory carried through verbatim', () => {
  it('passes gpu_memory through unchanged, including a null field', () => {
    const status = statusWith({ gpu_memory: { state: 'unknown', dedicated_mib: null, shared_mib: 10, adapter: null } });
    const activity = deriveActivity(status);
    expect(activity.gpuMemory.dedicated_mib).toBeNull();
    expect(activity.gpuMemory.shared_mib).toBe(10);
  });
});

describe('deriveActivity: window span is independent of event ordering', () => {
  // cia-edge appends in arrival order today (internal/edge/events.go's
  // eventStore), so the orderings below are one window seen two ways.
  // Nothing in the wire contract promises that ordering, though, and
  // "newest first" is where a log view naturally drifts - under an endpoint
  // subtraction that silently renders a negative duration on screen. These
  // pin that it cannot.
  const early = event({ time: '2026-08-27T13:35:00.000Z', request_id: 'a' });
  const middle = event({ time: '2026-08-27T13:35:05.000Z', request_id: 'b' });
  const late = event({ time: '2026-08-27T13:35:12.000Z', request_id: 'c' });

  function spanFor(entries: RecentEvent[]): number | null {
    return deriveActivity(statusWith({ recent_events: entries })).events.windowSpanMs;
  }

  it('reports the same span for ascending and descending order', () => {
    expect(spanFor([early, middle, late])).toBe(12_000);
    expect(spanFor([late, middle, early])).toBe(12_000);
  });

  it('never reports a negative span, whatever the order', () => {
    for (const order of [[early, late], [late, early], [middle, early, late], [late, early, middle]]) {
      const span = spanFor(order);
      expect(span).not.toBeNull();
      expect(span as number).toBeGreaterThanOrEqual(0);
    }
  });

  it('loses only the unparseable sample, not the whole span', () => {
    expect(spanFor([early, event({ time: 'not-a-timestamp' }), late])).toBe(12_000);
  });

  it('is null when no timestamp at all can be parsed', () => {
    expect(spanFor([event({ time: 'x' }), event({ time: 'y' })])).toBeNull();
  });
});

describe('deriveActivity: maintenance (Sprint 7)', () => {
  it('carries state/draining/drained verbatim, matching the fixture default (running, not draining)', () => {
    const activity = deriveActivity(statusWith({}));

    expect(activity.maintenance.state).toBe('running');
    expect(activity.maintenance.draining).toBe(false);
    expect(activity.maintenance.drained).toBe(false);
  });

  it('carries the three previously-unrendered drain-progress counters (active, queued, rejectedTotal) verbatim', () => {
    const status = statusWith({
      maintenance: { state: 'draining', draining: true, drained: false, active: 3, queued: 5, rejected_total: 7 },
    });
    const activity = deriveActivity(status);

    expect(activity.maintenance.active).toBe(3);
    expect(activity.maintenance.queued).toBe(5);
    expect(activity.maintenance.rejectedTotal).toBe(7);
  });

  it('reports drained: true and state "maintenance" once active and queued both reach zero while draining', () => {
    const status = statusWith({
      maintenance: { state: 'maintenance', draining: true, drained: true, active: 0, queued: 0, rejected_total: 2 },
    });
    const activity = deriveActivity(status);

    expect(activity.maintenance.state).toBe('maintenance');
    expect(activity.maintenance.draining).toBe(true);
    expect(activity.maintenance.drained).toBe(true);
    expect(activity.maintenance.active).toBe(0);
    expect(activity.maintenance.queued).toBe(0);
  });

  it('keeps maintenance.rejectedTotal independent from the gate\'s own rejectedTotal - same field name, different counters', () => {
    const status = statusWith({
      gate: { active: 0, queued: 0, max_active: 4, max_queue: 8, wait_timeout_seconds: 30, rejected_total: 99, timed_out_total: 0 },
      maintenance: { state: 'draining', draining: true, drained: false, active: 0, queued: 1, rejected_total: 4 },
    });
    const activity = deriveActivity(status);

    expect(activity.gate.rejectedTotal).toBe(99);
    expect(activity.maintenance.rejectedTotal).toBe(4);
  });
});
