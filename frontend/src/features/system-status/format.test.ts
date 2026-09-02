import { describe, expect, it } from 'vitest';
import { formatMib, formatUptime } from './format';

describe('formatUptime', () => {
  it('formats the captured fixture\'s uptime_seconds (9620) as hours/minutes/seconds', () => {
    expect(formatUptime(9620)).toBe('2h 40m 20s');
  });

  it('omits days, hours, and minutes when all are zero - just seconds', () => {
    expect(formatUptime(45)).toBe('45s');
  });

  it('includes minutes once uptime crosses 60s, without days/hours', () => {
    expect(formatUptime(125)).toBe('2m 5s');
  });

  it('includes days once uptime crosses 24h', () => {
    expect(formatUptime(90_061)).toBe('1d 1h 1m 1s');
  });
});

describe('formatMib', () => {
  it('renders "not measured" for null, never 0', () => {
    expect(formatMib(null)).toBe('not measured');
  });

  it('renders one decimal place of MiB', () => {
    expect(formatMib(3588.28125)).toBe('3588.3 MiB');
  });
});
