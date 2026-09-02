import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { computeShortfall } from '../../../features/system-status/shortfall';
import { ResourceMeter } from './ResourceMeter';

describe('ResourceMeter: not measured', () => {
  it('never renders a bar implying zero when an operand is null - shows "Not measured" instead', () => {
    const result = computeShortfall({
      label: 'physical memory',
      availableGib: null,
      requiredGib: 13.7,
      reserveGib: 2,
    });
    render(<ResourceMeter result={result} />);

    expect(screen.getAllByText('Not measured').length).toBeGreaterThan(0);
    // The figures that ARE known still render as numbers, not "not measured".
    expect(screen.getByText('13.7 GiB')).toBeTruthy();
    expect(screen.getByText('2 GiB')).toBeTruthy();
    // The unmeasured operand shows the words "not measured", never "0 GiB".
    expect(screen.queryByText('0 GiB')).toBeNull();
  });

  it('is fully unmeasured when every operand is null', () => {
    const result = computeShortfall({ label: 'committed memory', availableGib: null, requiredGib: null, reserveGib: null });
    render(<ResourceMeter result={result} />);

    const notMeasured = screen.getAllByText('not measured');
    expect(notMeasured.length).toBe(3); // available, required, reserve
  });
});

describe('ResourceMeter: computable, fits', () => {
  it('renders a "Fits" verdict with an icon-paired word, not colour alone', () => {
    const result = computeShortfall({ label: 'dedicated GPU memory', availableGib: 15.92, requiredGib: 10.8, reserveGib: 3 });
    render(<ResourceMeter result={result} />);

    expect(screen.getByText(/Fits/)).toBeTruthy();
    expect(screen.getByText('15.92 GiB')).toBeTruthy();
    expect(screen.getByText('10.8 GiB')).toBeTruthy();
  });
});

describe('ResourceMeter: computable, short', () => {
  it('renders a "Short by" verdict with the exact shortfall figure', () => {
    // Sprint 3 brief scenario: headroom 8.63, required 13.7, reserve 2 -> short by 7.07.
    const result = computeShortfall({ label: 'physical memory', availableGib: 8.63, requiredGib: 13.7, reserveGib: 2 });
    render(<ResourceMeter result={result} />);

    expect(screen.getByText('Short by 7.07 GiB')).toBeTruthy();
  });
});

describe('ResourceMeter: multiple instances', () => {
  it('gives each instance its own SVG hatch pattern id (no collisions across cards)', () => {
    const short = computeShortfall({ label: 'physical memory', availableGib: 8.63, requiredGib: 13.7, reserveGib: 2 });
    const { container } = render(
      <>
        <ResourceMeter result={short} />
        <ResourceMeter result={short} />
      </>,
    );

    const patternIds = Array.from(container.querySelectorAll('pattern')).map((el) => el.id);
    expect(patternIds.length).toBe(2);
    expect(new Set(patternIds).size).toBe(2);
  });
});
