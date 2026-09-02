/**
 * Hand-authored inline icon set.
 *
 * The brief asked for one icon language, bundled locally, with its licence
 * stated in the README. No icon package could be installed without a fresh
 * network download this environment isn't authorized to perform mid-task
 * (see frontend/README.md, "Icons" section) - so instead of an icon font or
 * CDN, this is a small set of hand-authored inline SVGs covering only the
 * icons the seven primitives actually need. Authored from scratch for this
 * project; no third-party licence applies.
 *
 * Shared visual language across every icon in this file:
 *   - 24x24 viewBox, 1.5px stroke, round line caps/joins, no fills except
 *     where a glyph is inherently a filled shape (none currently are).
 *   - `currentColor` throughout, so an icon inherits whatever text/icon
 *     colour its containing primitive sets via semantic tokens - icons
 *     never carry their own colour.
 *   - `aria-hidden="true"` by default (the primitive that places an icon is
 *     responsible for the accessible name - e.g. IconButton's `label`
 *     prop), but every component accepts a `title` to opt into being an
 *     accessible graphic on its own when used standalone.
 */
import type { SVGProps } from 'react';
import './icons.css';

export interface IconProps extends SVGProps<SVGSVGElement> {
  /** Optional accessible name. Omit to keep the icon purely decorative. */
  title?: string;
}

function iconAriaProps(title: string | undefined) {
  return title ? ({ role: 'img', 'aria-label': title } as const) : ({ 'aria-hidden': true } as const);
}

const BASE_PROPS = {
  viewBox: '0 0 24 24',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.5,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
};

export function IconClose({ title, ...rest }: IconProps) {
  return (
    <svg {...BASE_PROPS} {...iconAriaProps(title)} {...rest}>
      <path d="M6 6l12 12M18 6L6 18" />
    </svg>
  );
}

export function IconCheck({ title, ...rest }: IconProps) {
  return (
    <svg {...BASE_PROPS} {...iconAriaProps(title)} {...rest}>
      <path d="M5 12.5l4.5 4.5L19 7" />
    </svg>
  );
}

export function IconAlertCircle({ title, ...rest }: IconProps) {
  return (
    <svg {...BASE_PROPS} {...iconAriaProps(title)} {...rest}>
      <circle cx="12" cy="12" r="8.25" />
      <path d="M12 8.25v4.5" />
      <path d="M12 15.75h.01" />
    </svg>
  );
}

export function IconInfo({ title, ...rest }: IconProps) {
  return (
    <svg {...BASE_PROPS} {...iconAriaProps(title)} {...rest}>
      <circle cx="12" cy="12" r="8.25" />
      <path d="M12 11v5" />
      <path d="M12 8.25h.01" />
    </svg>
  );
}

/**
 * Sprint 3 addition: the Overview nav destination's icon. A plain 2x2 grid -
 * "everything at a glance" - kept in the same 24x24/1.5px/round-cap language
 * as the rest of this set.
 */
export function IconGrid({ title, ...rest }: IconProps) {
  return (
    <svg {...BASE_PROPS} {...iconAriaProps(title)} {...rest}>
      <rect x="4" y="4" width="7" height="7" rx="1.25" />
      <rect x="13" y="4" width="7" height="7" rx="1.25" />
      <rect x="4" y="13" width="7" height="7" rx="1.25" />
      <rect x="13" y="13" width="7" height="7" rx="1.25" />
    </svg>
  );
}

/**
 * Sprint 3 addition: the System nav destination's icon. Stacked
 * rectangles - a small server/runtime-stack glyph, distinct enough from
 * IconGrid at a glance in the collapsed 72px rail.
 */
export function IconServer({ title, ...rest }: IconProps) {
  return (
    <svg {...BASE_PROPS} {...iconAriaProps(title)} {...rest}>
      <rect x="4" y="4" width="16" height="6" rx="1.5" />
      <rect x="4" y="14" width="16" height="6" rx="1.5" />
      <path d="M7.5 7h.01" />
      <path d="M7.5 17h.01" />
    </svg>
  );
}

/**
 * Sprint 3 addition: rail collapse/expand toggle. A single left-pointing
 * chevron; the "expand" direction is produced by rotating this same glyph
 * 180deg in CSS rather than authoring a second mirrored icon.
 */
export function IconChevronLeft({ title, ...rest }: IconProps) {
  return (
    <svg {...BASE_PROPS} {...iconAriaProps(title)} {...rest}>
      <path d="M15 5.25L8.25 12L15 18.75" />
    </svg>
  );
}

/**
 * Sprint 4 addition: the Models nav destination's icon. A small chip/die
 * glyph (a square body with corner leads) - reads as "hardware/compute" at
 * a glance, distinct from IconGrid (Overview) and IconServer (System) in
 * the collapsed 72px rail.
 */
export function IconCpu({ title, ...rest }: IconProps) {
  return (
    <svg {...BASE_PROPS} {...iconAriaProps(title)} {...rest}>
      <rect x="7" y="7" width="10" height="10" rx="1.5" />
      <path d="M9.5 7V4M14.5 7V4M9.5 20v-3M14.5 20v-3M7 9.5H4M7 14.5H4M20 9.5h-3M20 14.5h-3" />
    </svg>
  );
}

/**
 * Sprint 4 addition: expand/collapse toggle for a disclosure section (the
 * Models screen's secondary/advanced detail tiers). A single down-pointing
 * chevron; the "collapse" direction is produced by rotating this same glyph
 * 180deg in CSS, matching IconChevronLeft's existing precedent rather than
 * authoring a second mirrored icon.
 */
export function IconChevronDown({ title, ...rest }: IconProps) {
  return (
    <svg {...BASE_PROPS} {...iconAriaProps(title)} {...rest}>
      <path d="M5.25 8.25L12 15L18.75 8.25" />
    </svg>
  );
}

/**
 * Sprint 6 addition: the Activity nav destination's icon. A pulse/ECG
 * line - reads as "live activity" at a glance, distinct from IconGrid
 * (Overview), IconServer (System), and IconCpu (Models) in the collapsed
 * 72px rail.
 */
export function IconActivity({ title, ...rest }: IconProps) {
  return (
    <svg {...BASE_PROPS} {...iconAriaProps(title)} {...rest}>
      <path d="M3 12h3.5l2-7 4.5 14 2-7H20" />
    </svg>
  );
}

/**
 * Loading spinner. A plain rotating ring rather than a dashed/segmented
 * one, kept as a single SVG (not per-frame CSS shapes) so it composes with
 * the rest of the icon set. The rotation itself is a CSS animation defined
 * once, in this directory's own icons.css (`@keyframes ds-spin`), not in
 * each consuming component's stylesheet - collapsed to a static ring under
 * `prefers-reduced-motion` by the central rule in
 * src/design-system/styles/motion.css, nothing here has to special-case it.
 */
export function IconSpinner({ title, className, ...rest }: IconProps) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      className={className ? `ds-spin ${className}` : 'ds-spin'}
      {...iconAriaProps(title)}
      {...rest}
    >
      <circle cx="12" cy="12" r="9" stroke="currentColor" strokeWidth="2.5" opacity="0.25" />
      <path
        d="M21 12a9 9 0 0 0-9-9"
        stroke="currentColor"
        strokeWidth="2.5"
        strokeLinecap="round"
      />
    </svg>
  );
}
