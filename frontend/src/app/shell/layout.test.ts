import { describe, it, expect } from 'vitest';
import { readCss } from '../../design-system/primitives/test-utils/readCss';

/**
 * Layout invariants of the app shell.
 *
 * Every assertion here exists because the property it guards was measured
 * broken in a real browser against a live provider, and none of the 249 tests
 * that existed at the time noticed. They are written as CSS-as-data rather
 * than as rendered assertions on purpose: jsdom computes no layout, so a
 * rendering test cannot tell a 413px column from a 960px one. The stylesheet
 * is the only place the fault is visible without a real engine.
 *
 * They are therefore necessary but not sufficient - they pin the specific
 * declaration that was missing, not the resulting geometry. The geometry
 * itself is checked by the native harness in cmd/cia-console.
 */

const appShell = readCss('app/shell/AppShell.css');
const navRail = readCss('app/shell/NavRail.css');

const PAGES = ['OverviewPage', 'ModelsPage', 'ActivityPage', 'SystemPage'] as const;

/** Strips comments so prose about a property is never mistaken for the property. */
function code(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, '');
}

/** The body of the first rule whose selector matches exactly. */
function ruleBody(css: string, selector: string): string {
  const source = code(css);
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = new RegExp(`(?:^|[},])\\s*${escaped}\\s*\\{([^}]*)\\}`, 'm').exec(source);
  return match?.[1] ?? '';
}

describe('app shell frame', () => {
  it('gives .app-shell a definite height, so the content pane can actually scroll', () => {
    const body = ruleBody(appShell, '.app-shell');
    // `min-height` alone lets the shell grow to its content. The content pane
    // then never overflows, `overflow-y: auto` never engages, and the document
    // scrolls instead - carrying the rail and the top bar, including the
    // readiness badge, off the top of the screen. Measured at scrollY=387:
    // top bar at y=-387.
    expect(body).toMatch(/(^|[\s;])height:\s*100vh/);
  });

  it('keeps the content pane the scroll container, not the document', () => {
    expect(ruleBody(appShell, '.app-shell')).toMatch(/overflow:\s*hidden/);
    expect(ruleBody(appShell, '.app-shell__content')).toMatch(/overflow-y:\s*auto/);
  });

  it('does not set a percentage height on the nav rail', () => {
    // `height: 100%` is a non-`auto` cross size, so the flex container skips
    // `align-items: stretch` for it; the percentage then resolves against
    // `.app-shell`'s own indefinite height and collapses to content height.
    // Measured: a 296px rail in an 821px viewport, canvas showing beneath.
    expect(ruleBody(navRail, '.nav-rail')).not.toMatch(/(^|[\s;])height:\s*100%/);
  });
});

describe('page content columns', () => {
  it.each(PAGES)('%s pairs margin-inline:auto with an explicit width', (page) => {
    const css = readCss(`pages/${page}.css`);
    const root = `.${page.replace('Page', '').toLowerCase()}-page`;
    const body = ruleBody(css, root);

    expect(body).toMatch(/margin-inline:\s*auto/);
    // Auto cross-axis margins suppress a flex item's stretch. Without an
    // explicit width the column shrink-wraps and never reaches its max-width:
    // the Overview measured 413px of an available 992px.
    expect(body).toMatch(/(^|[\s;])width:\s*100%/);
    expect(body).toMatch(/max-width:/);
  });
});

describe('selected navigation destination', () => {
  it('is not repainted by :hover', () => {
    // `.nav-rail__item:hover` is (0,2,0) and `.nav-rail__item--active` is
    // (0,1,0), so hover used to win: the one item whose state matters lost
    // its state exactly when the pointer was on it.
    const source = code(navRail);
    const activeRule = /\.nav-rail__item--active[^{]*\{/.exec(source);
    expect(activeRule).not.toBeNull();
    expect(activeRule![0]).toMatch(/\.nav-rail__item--active:hover/);
  });

  it('carries a non-colour channel, not hue alone', () => {
    // Measured: the 14% tint is 1.25:1 against the rail and accent-vs-muted
    // text is 1.55:1 apart in luminance - both below the 3:1 that WCAG 1.4.11
    // asks of a state indicator. The inset bar is a shape channel, legible
    // without colour vision.
    expect(ruleBody(navRail, '.nav-rail__item--active,\n.nav-rail__item--active:hover')).toMatch(
      /box-shadow:\s*inset/,
    );
  });
});

describe('design-system token layering', () => {
  // The semantic layer is the only one a component may reference. Nothing
  // enforced this - Stylelint bans hex and rgb() but says nothing about
  // reaching past semantic into foundation - and 29 such references had
  // accumulated across nine sprints.
  //
  // The four names still allowed below are not sloppiness: the semantic layer
  // has no alias for them at all, and semantic.css forbids additions, so a
  // component needing a pill radius or a 12px step has nowhere compliant to
  // go. They are listed explicitly so the gap stays visible instead of
  // dissolving back into general drift.
  const KNOWN_GAPS = ['--radius-xs', '--radius-md', '--radius-full', '--space-12', '--font-weight-medium'];

  const COMPONENT_STYLESHEETS = [
    'app/shell/AppShell.css',
    'app/shell/NavRail.css',
    'app/shell/TopBar.css',
    // Sprint 10's new stylesheets, listed the moment they were written
    // rather than after the next drift: `pages/page.css` carries the shared
    // page grammar, and Notice/StatGroup are the two components that
    // replaced the hand-rolled status bands and stat tiles.
    'pages/page.css',
    'design-system/components/Notice/Notice.css',
    'design-system/components/StatGroup/StatGroup.css',
    'pages/OverviewPage.css',
    'pages/ModelsPage.css',
    'pages/ActivityPage.css',
    'pages/SystemPage.css',
    'design-system/components/ResourceMeter/ResourceMeter.css',
    'design-system/primitives/Button/Button.css',
    'design-system/primitives/IconButton/IconButton.css',
    'design-system/primitives/Surface/Surface.css',
    'design-system/primitives/TextField/TextField.css',
    'design-system/primitives/Tooltip/Tooltip.css',
    'design-system/primitives/Skeleton/Skeleton.css',
    'design-system/primitives/Divider/Divider.css',
  ];

  it.each(COMPONENT_STYLESHEETS)('%s references no numeric foundation spacing token', (path) => {
    const found = code(readCss(path)).match(/var\(--space-\d+\)/g) ?? [];
    const unexpected = found.filter((token) => !KNOWN_GAPS.some((gap) => token.includes(gap)));
    expect(unexpected).toEqual([]);
  });
});
