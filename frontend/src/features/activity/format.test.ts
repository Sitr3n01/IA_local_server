import { describe, expect, it } from 'vitest';
import { formatEventClock } from './format';

describe('formatEventClock', () => {
  it('renders a fixed 24-hour hh:mm:ss clock', () => {
    // Asserted as a shape, not as an exact string: the value is the
    // machine's local time, and CI, this developer's box and an operator's
    // workstation are not in the same time zone. The format is the thing
    // this function is responsible for; the offset is the platform's.
    expect(formatEventClock('2026-08-27T13:53:59.4612286Z')).toMatch(/^\d{2}:\d{2}:\d{2}$/);
  });

  it('never renders a 12-hour clock, whatever the host locale prefers', () => {
    const formatted = formatEventClock('2026-08-27T21:05:00Z');
    expect(formatted).not.toMatch(/[ap]m/i);
  });

  it('returns an unparseable timestamp verbatim rather than "Invalid Date"', () => {
    // Whatever the provider actually sent is more useful to whoever has to
    // debug it than a formatter's opinion of it - and an empty cell would
    // hide that a bad value ever arrived.
    expect(formatEventClock('not-a-timestamp')).toBe('not-a-timestamp');
    expect(formatEventClock('')).toBe('');
  });
});
