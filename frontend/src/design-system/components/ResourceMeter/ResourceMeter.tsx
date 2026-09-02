import { useId } from 'react';
import type { ShortfallResult } from '../../../features/system-status/shortfall';
import { formatGibOrUnmeasured } from '../../../features/system-status/shortfall';
import { IconAlertCircle, IconCheck, IconInfo } from '../../primitives';
import './ResourceMeter.css';

export interface ResourceMeterProps {
  /** The full arithmetic for one admission budget - reused verbatim from `computeShortfall` (see `src/features/system-status/shortfall.ts`), never re-derived here. */
  result: ShortfallResult;
  className?: string;
}

const VIEW_WIDTH = 300;
const BAR_HEIGHT = 16;
const BAR_Y = 4;

function widthFor(value: number, scale: number): number {
  if (scale <= 0) return 0;
  return Math.max(0, Math.min(VIEW_WIDTH, (value / scale) * VIEW_WIDTH));
}

/**
 * The Models screen's signature component: a model's requirement plotted
 * against available headroom and the safety reserve, so a shortfall reads
 * as a quantity (a bar overrunning a marker) rather than only as a sentence.
 *
 * Deliberately never uses an inline `style` attribute for the proportional
 * geometry - the production CSP is `style-src 'self'` with no
 * `'unsafe-inline'`, which silently drops a `style=""` attribute in the
 * shipped console (see `Skeleton.tsx`'s doc comment for the same trap).
 * Instead this renders as inline SVG and sets plain numeric SVG attributes
 * (`x`/`width` on `<rect>`), which are ordinary presentation attributes, not
 * a `style=` sink - unaffected by `style-src` and provably CSP-safe.
 *
 * Readable without colour: every verdict pairs an icon with text ("Fits" /
 * "Short by"), and the shortfall region is a diagonal hatch pattern, not
 * just a danger-coloured fill - a colour-blind or greyscale operator still
 * sees the shape of the problem.
 *
 * Degrades honestly when `result.computable` is `false` (any one of
 * available/required/reserve is `null`, i.e. "not measured"): renders a
 * dashed, non-proportional track and an explicit "Not measured" label
 * instead of a bar that would otherwise silently imply a measured zero.
 * Whichever of the three figures *are* present still render as GiB values
 * beneath the track - only the ones that are `null` show "not measured".
 */
export function ResourceMeter({ result, className }: ResourceMeterProps) {
  const classes = ['ds-resource-meter'];
  if (className) classes.push(className);
  const heading = `${result.label} headroom`;
  const patternId = `ds-resource-meter-hatch-${useId()}`;

  if (!result.computable) {
    return (
      <div className={classes.join(' ')}>
        <div className="ds-resource-meter__header">
          <span className="ds-resource-meter__title">{heading}</span>
          <span className="ds-resource-meter__verdict ds-resource-meter__verdict--unmeasured">
            <IconInfo />
            Not measured
          </span>
        </div>
        <div
          className="ds-resource-meter__track ds-resource-meter__track--unmeasured"
          role="img"
          aria-label={`${heading}: not fully measured, so no proportional bar can be drawn`}
        >
          <span className="ds-resource-meter__unmeasured-label">Not measured</span>
        </div>
        <div className="ds-resource-meter__figures">
          <div className="ds-resource-meter__figure">
            <span className="ds-resource-meter__figure-label">Available</span>
            <span className="ds-resource-meter__figure-value">{formatGibOrUnmeasured(result.availableGib)}</span>
          </div>
          <div className="ds-resource-meter__figure">
            <span className="ds-resource-meter__figure-label">Required</span>
            <span className="ds-resource-meter__figure-value">{formatGibOrUnmeasured(result.requiredGib)}</span>
          </div>
          <div className="ds-resource-meter__figure">
            <span className="ds-resource-meter__figure-label">Reserve</span>
            <span className="ds-resource-meter__figure-value">{formatGibOrUnmeasured(result.reserveGib)}</span>
          </div>
        </div>
      </div>
    );
  }

  // Safe: `computable` guarantees every operand below is a measured number.
  const available = result.availableGib as number;
  const required = result.requiredGib as number;
  const reserve = result.reserveGib as number;
  const totalNeeded = result.totalNeededGib as number;
  const shortfall = result.shortfallGib as number;
  const fits = shortfall <= 0;

  const scale = Math.max(available, totalNeeded, 1);
  const requiredWidth = widthFor(required, scale);
  const reserveWidth = widthFor(reserve, scale);
  const availableX = widthFor(available, scale);
  const overflowWidth = fits ? 0 : widthFor(shortfall, scale);

  const verdictText =
    shortfall === 0
      ? 'Fits exactly, no spare headroom'
      : fits
        ? `Fits, ${formatGibOrUnmeasured(-shortfall)} to spare`
        : `Short by ${formatGibOrUnmeasured(shortfall)}`;

  return (
    <div className={classes.join(' ')}>
      <div className="ds-resource-meter__header">
        <span className="ds-resource-meter__title">{heading}</span>
        <span
          className={`ds-resource-meter__verdict ${fits ? 'ds-resource-meter__verdict--fits' : 'ds-resource-meter__verdict--short'}`}
        >
          {fits ? <IconCheck /> : <IconAlertCircle />}
          {verdictText}
        </span>
      </div>

      <svg
        className="ds-resource-meter__svg"
        viewBox={`0 0 ${VIEW_WIDTH} ${BAR_HEIGHT + BAR_Y * 2}`}
        preserveAspectRatio="none"
        role="img"
        aria-label={`${heading}: ${formatGibOrUnmeasured(available)} available, ${formatGibOrUnmeasured(required)} required plus ${formatGibOrUnmeasured(reserve)} reserve - ${verdictText}`}
      >
        <defs>
          <pattern id={patternId} width="6" height="6" patternTransform="rotate(45)" patternUnits="userSpaceOnUse">
            <rect width="6" height="6" className="ds-resource-meter__hatch-bg" />
            <line x1="0" y1="0" x2="0" y2="6" className="ds-resource-meter__hatch-line" />
          </pattern>
        </defs>

        <rect
          x={0}
          y={BAR_Y}
          width={VIEW_WIDTH}
          height={BAR_HEIGHT}
          rx={BAR_HEIGHT / 2}
          className="ds-resource-meter__track-bg"
        />
        <rect x={0} y={BAR_Y} width={requiredWidth} height={BAR_HEIGHT} className="ds-resource-meter__required" />
        <rect
          x={requiredWidth}
          y={BAR_Y}
          width={reserveWidth}
          height={BAR_HEIGHT}
          className="ds-resource-meter__reserve"
        />
        {!fits && (
          <rect
            x={availableX}
            y={BAR_Y}
            width={overflowWidth}
            height={BAR_HEIGHT}
            fill={`url(#${patternId})`}
            className="ds-resource-meter__overflow"
          />
        )}
        <rect
          x={Math.max(0, availableX - 1)}
          y={0}
          width={2}
          height={BAR_HEIGHT + BAR_Y * 2}
          className="ds-resource-meter__available-marker"
        />
      </svg>

      <div className="ds-resource-meter__figures">
        <div className="ds-resource-meter__figure">
          <span className="ds-resource-meter__figure-label">Available</span>
          <span className="ds-resource-meter__figure-value">{formatGibOrUnmeasured(available)}</span>
        </div>
        <div className="ds-resource-meter__figure">
          <span className="ds-resource-meter__figure-label">Required</span>
          <span className="ds-resource-meter__figure-value">{formatGibOrUnmeasured(required)}</span>
        </div>
        <div className="ds-resource-meter__figure">
          <span className="ds-resource-meter__figure-label">Reserve</span>
          <span className="ds-resource-meter__figure-value">{formatGibOrUnmeasured(reserve)}</span>
        </div>
        <div className="ds-resource-meter__figure">
          <span className="ds-resource-meter__figure-label">Total needed</span>
          <span className="ds-resource-meter__figure-value">{formatGibOrUnmeasured(totalNeeded)}</span>
        </div>
      </div>
    </div>
  );
}
