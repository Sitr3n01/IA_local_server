import { describe, expect, it } from 'vitest';
import { formatContextTokens, formatReclaimableGib } from './format';

describe('formatContextTokens', () => {
  it('adds thousands separators', () => {
    expect(formatContextTokens(131072)).toBe('131,072 tokens');
    expect(formatContextTokens(262144)).toBe('262,144 tokens');
  });
});

describe('formatReclaimableGib', () => {
  it('renders null (key absent) as null - meaning "show no row"', () => {
    expect(formatReclaimableGib(undefined)).toBeNull();
  });

  it('renders a present-but-null measurement as "not measured", never as 0', () => {
    expect(formatReclaimableGib(null)).toBe('not measured');
  });

  it('renders a measured value as a GiB figure', () => {
    expect(formatReclaimableGib(4.5)).toBe('4.5 GiB');
  });
});
