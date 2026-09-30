# IA Local operator console (frontend)

React + TypeScript operator console, hosted inside a WebView2 control by the
native Go host process (`cia-console.exe`). Four screens - Overview, Models,
Activity, System - reading `GET /api/v1/status` and driving five privileged
operations through a native bridge.

**This file is the narrative**: how the console was built, sprint by sprint,
and why each decision went the way it did. For the reference - the layers,
what may import what, the token model, how to add a component or a feature,
what the tests prove and what they do not - see
[`docs/FRONTEND.md`](../docs/FRONTEND.md).

## Running it

```sh
npm ci
npm run typecheck
npm run lint
npm test
npm run test:monitor         # DOM regressions for the active browser monitor
npm run build
npm run verify:prod-bundle   # after build - asserts dist/ has no dev transport, no loopback URL, no external origin
npm run dev                  # vite dev server; needs cia-edge running on 127.0.0.1:18091
```

The console remains frozen under ADR 0019. Its existing Node dependencies also
run the active monitor's checks: `npm run lint:monitor` lints the browser scripts
and `npm run test:monitor` exercises the actual HTML/JavaScript with fake fetch
and time in jsdom. CI runs both. These tests make no request to the installed
provider and do not open or approve a native dialog.

## Directory layout

```
src/
├── app/            App.tsx (composition root), providers/ (TanStack Query
│                   client), shell/ (AppShell, NavRail, TopBar, nav data)
├── design-system/   tokens/ (foundation+semantic CSS), primitives/ (the 7
│                    DS 0.1 primitives + hand-authored icons), components/
│                    (ResourceMeter), styles/ (reset,
│                    motion/prefers-reduced-motion)
├── dev/             Gallery.tsx - dev-only primitive gallery, /__gallery
├── features/        system-status/ - reason/shortfall derivation, view-model
│                    hooks, and the live-region announcer (Sprints 3, 8);
│                    models/ - per-model derivation, action gating, and the
│                    action-invocation hook (Sprint 4); activity/ -
│                    admission-gate pressure, the request timeline, and the
│                    drain/resume control (Sprints 6, 7);
│                    maintenanceState.ts - the shared state label (Sprint 8)
├── pages/           OverviewPage (primary tier), ModelsPage (per-model
│                    three-tier disclosure), ActivityPage (gate + request
│                    timeline), SystemPage (machine identity)
├── api/
│   ├── schemas/     Zod schemas for backend payloads (status.ts)
│   ├── transport/   the only code allowed to touch the network/bridge
│   ├── queries/     read operations (transport -> Zod -> typed data)
│   ├── commands/    mutations - models.ts's load/unload/switch (Sprint 4)
│   │                and maintenance.ts's drain/resume (Sprint 7). Both go
│   │                straight to the native bridge - see "Models (Sprint 4)"
│   │                below. Callers import these domain modules directly
│   ├── errors.ts    NativeHostUnavailableError, shared by both layers
│   └── __fixtures__/ a captured real GET /api/v1/status response
└── lib/             small framework-agnostic helpers
```

## App shell and navigation (Sprint 3)

`src/app/App.tsx` is the composition root: it owns which of the four nav
destinations (`Overview`, `Models`, `Activity`, `System`) is selected and
renders `AppShell` around the matching page. `AppShell` (`src/app/shell/`) is a navigation rail plus a
top bar - purely presentational, driven entirely by props (`activeDestination`,
`onNavigate`, `health`) so it needs no `QueryClientProvider` in its own tests.
The rail's collapsed/expanded width lives in two component tokens
(`--app-shell-rail-width-collapsed` / `-expanded`, in `AppShell.css`) and its
choice persists to `localStorage` (wrapped in try/catch - see
`useRailCollapsed.ts`).

The rule is that a destination exists only when a real screen exists behind
it - never as a placeholder. Sprint 3 shipped exactly two under that rule
(`Overview`, `System`) because nothing real stood behind a third; Sprint 4
added `Models` and Sprint 6 added `Activity` when each acquired one. There is
still no `Inference` or `Settings` item, for the same reason.

`src/app/shell/navigation.ts`'s `NAV_DESTINATIONS` array is the rail's only
source of truth for what to render, so adding a destination is a data change
there plus a new page and a case in `App.tsx`'s exhaustive switch - never
surgery on the rail component. `DestinationId` is a closed union precisely so
the second half of that is compile-time enforced rather than a silent gap.

`src/features/system-status/` owns turning the raw `Status` payload into
what each screen renders: `reasonExplanations.ts` maps every one of the
eight literal `capacity.reason` values to a plain-language explanation via an
exact-string lookup (an unrecognised reason degrades to showing the raw
code, never a substring-matched guess), and `shortfall.ts` computes the
`required + reserve - available` arithmetic behind every `insufficient_*`
explanation.

## The transport boundary

`src/api/transport/Transport.ts` defines the interface every read/write goes
through. Two implementations exist:

- `bridgeTransport.ts` (production) - the live client for the native host.
  It posts a message over `window.chrome.webview`, tags it with a correlation
  id, holds the pending promise in a map, and settles it when
  `window.__ciaConsoleReply` fires with the matching id, or rejects on a
  timeout. It rejects with `NativeHostUnavailableError` only when
  `window.chrome.webview` is genuinely absent - i.e. when the page is being
  rendered somewhere other than inside `cia-console.exe`.

  This is the production privileged path: every model-lifecycle and
  maintenance mutation reaches the DACL-protected administrative pipe through
  it. Treat its reply envelope as a trust boundary, not as plumbing.
- `httpTransport.dev.ts` (development only) - `fetch`es cia-edge through
  Vite's own dev-server proxy (`/__cia-edge`, configured in
  `vite.config.ts`) rather than the loopback address directly. This is the
  *only* file allowed to call `fetch` for a real request. The proxy exists
  because a direct browser fetch to `http://127.0.0.1:18091` is cross-origin
  from the page `vite dev` serves, and cia-edge deliberately sends no
  `Access-Control-Allow-Origin` header (see
  `internal/edge/server_test.go`'s assertion that the data plane
  "unexpectedly enabled CORS") - no CSP relaxation can work around that,
  since a browser enforces CORS independently of CSP. Vite's proxy forwards
  the request server-side (Node process to the Go process), so the
  browser-facing fetch stays same-origin and cia-edge's own response headers
  are untouched.

`src/api/transport/index.ts` selects between them via `import.meta.env.DEV`,
which Vite inlines as a literal `true`/`false` at build time. Behind a
dynamic `import()`, a statically-false condition lets Rollup eliminate the
whole branch - including the `import()` call - so `httpTransport.dev.ts`
never ends up in the production module graph. `npm run verify:prod-bundle`
(`scripts/verify-prod-bundle.mjs`) checks this actually happened, by scanning
the built `dist/` output for the loopback address and a string unique to the
dev transport's source, and failing loudly if either shows up.

Three ESLint rules (`eslint.config.js`) enforce the boundary at build time
rather than relying on discipline:

- `no-restricted-globals` bans the bare `fetch` global everywhere except
  `src/api/transport/`. A second exemption block turns it off for
  `vite.config.ts` and `scripts/**/*.mjs`, which are Node build tooling and
  never ship to the operator.
- `no-restricted-imports` bans importing `src/api/transport/*` from anywhere
  outside `src/api/` - only `api/queries` and `api/commands` may reach into
  it. Pages and features call those instead.
- `no-restricted-syntax` bans every HTML-injection sink by AST selector:
  the `dangerouslySetInnerHTML` JSX attribute, assignment to
  `innerHTML`/`outerHTML`, and `insertAdjacentHTML`. This is ADR 0018
  control 4. It is expressed as selectors rather than via
  `eslint-plugin-react`'s `no-danger` so it costs no dependency and also
  covers the plain-DOM sinks a React-only rule would miss.

## Schemas and nullability

`src/api/schemas/status.ts` mirrors the real, captured
`src/api/__fixtures__/status.sample.json`. Two things worth knowing before
touching it:

- Many numeric fields (capacity headroom, checkpoint step, GPU memory
  readings) are modeled as `nullable`, never defaulted to `0`. `null` means
  "not measured", which is a stricter and different condition than a
  measured zero.
- Every object schema ends in `.passthrough()`. The backend is an actively
  developed admission-control plane and will add fields without a matching
  console release; passthrough means an unrecognized key is carried through
  rather than stripping it or throwing.
- `capabilities` on a model is the *only* sanctioned way to feature-detect
  it. Never branch on a model ID substring anywhere in this app.

`src/api/schemas/status.test.ts` parses the captured fixture against the
schema and will fail loudly the day the two drift.

## Dependencies, and why each one is here

This project's culture is hostile to dependency creep. Runtime:

| Package | Why |
| --- | --- |
| `react`, `react-dom` | The UI runtime the console is built with. |
| `@tanstack/react-query` | Server-state cache/fetch-lifecycle for reads through the transport boundary (loading/error/refetch), instead of hand-rolling `useEffect` fetch logic per query. |
| `zod` | Runtime validation at the transport boundary - the one place raw, untrusted JSON becomes a typed value the rest of the app can trust. |
| `@fontsource-variable/roboto-flex` | The identity typeface, bundled locally as a variable woff2 (SIL OFL-1.1) so `font-src 'self'` holds and nothing is fetched at runtime. |

Dev toolchain - each earns its place by being required to satisfy one of
Sprint 1's explicit requirements (TypeScript strict, ESLint flat config +
custom boundary rule, Stylelint, Vitest + Testing Library, Vite build):

| Package | Why |
| --- | --- |
| `typescript` | Strict-mode type checking. Declared `^6.0.3`; `typescript-eslint`'s current release supports TypeScript `<6.1.0` as a peer, so the effective pin is `package-lock.json`, not the range - a `npm update` that crosses 6.1 would break lint before it broke the build. TypeScript 7 is a separate native rewrite the lint toolchain doesn't support yet. |
| `vite`, `@vitejs/plugin-react` | Build tool and its React (JSX + Fast Refresh) plugin. |
| `eslint`, `@eslint/js`, `typescript-eslint`, `globals` | Flat-config ESLint with type-aware TS rules; `globals` supplies browser/node global definitions the flat config format needs explicitly (no more `env: {...}`). |
| `eslint-plugin-react-hooks` | Enforces the Rules of Hooks - the one React-specific correctness rule worth a dependency on its own. |
| `stylelint` | CSS linting, required by Sprint 1. Configured with a small hand-written ruleset in `.stylelintrc.json` rather than pulling in `stylelint-config-standard`, since the design system doesn't exist yet and there isn't much CSS to lint. |
| `vitest` | Test runner, chosen for native Vite config/transform reuse over Jest. |
| `@testing-library/react`, `@testing-library/dom` | Component testing without reaching into implementation details; `@testing-library/dom` is a required peer of `@testing-library/react`. |
| `jsdom` | Browser-like DOM environment for Vitest's test runs. |
| `@types/node`, `@types/react`, `@types/react-dom` | Type definitions for Node (config/scripts), React, and ReactDOM. |

No UI component library, no CSS framework, no chart library, no state
management library beyond TanStack Query for server state - none of Sprint
1's requirements call for one, and React's built-in state is enough for a
single unstyled page.

## CSP and assets

`index.html` ships a restrictive CSP
(`default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'none'`).
`connect-src 'none'` reflects reality: the production build has no network
access at all. `vite.config.ts` injects a development-only override of
`connect-src` (to allow the loopback status endpoint) purely at the Vite
plugin level, gated on Vite's `command === 'serve'` - `vite build` never
loads that plugin, so the production `index.html` ships the restrictive tag
unmodified. No remote assets anywhere: no CDN script, no runtime Google
Fonts, no external stylesheet - the font stacks are defined once in
`src/design-system/tokens/foundation.css` (`--font-family-sans` /
`--font-family-mono`) and applied globally by `src/design-system/styles/
reset.css`. `--font-family-sans` leads with Roboto Flex, which is **bundled**
(`@fontsource-variable/roboto-flex`, imported by `tokens/index.ts` and emitted
into `dist/assets` by Vite) - nothing is fetched at runtime, so `font-src
'self'` holds. `--font-family-mono` is a system stack by choice.
`verify-prod-bundle.mjs`'s external-origin scan covers this for the built
output too.

## Design system (Sprint 2 / DS 0.1)

`src/design-system/` implements the token architecture and the seven
primitives the design brief called for. Three token layers:

1. **Foundation** (`tokens/foundation.css`) - raw palette ramps, spacing,
   radii, type, motion. No meaning attached; the *only* file in the repo
   allowed a literal hex colour (enforced by the Stylelint override below).
2. **Semantic** (`tokens/semantic.css`) - the fixed `--color-*` / spacing /
   radius / motion / typography-role custom properties components actually
   reference. Resolves light vs. dark entirely in CSS, outside every
   component: `:root` is the light default; `@media (prefers-color-scheme:
   dark)` guarded by `:root:not([data-theme="light"])` follows the OS/browser
   preference; `:root[data-theme="dark"]` redefines the same tokens again so
   an explicit choice always wins, in both directions. No component branches
   on theme anywhere in this codebase.
3. **Component tokens** - defined next to the one component that needs one.
   `AppShell.css` declares the only two: `--app-shell-rail-width-collapsed`
   and `--app-shell-rail-width-expanded`, consumed by the rail's grid column.

A Stylelint override (`.stylelintrc.json`) bans literal hex colours and
`rgb()`/`rgba()`/`hsl()`/`hsla()` function calls in
`src/design-system/primitives/**`, `src/design-system/components/**`,
`src/features/**`, `src/pages/**`, and `src/app/**` - a component can only
reach a colour through a semantic custom property. `foundation.css` itself is explicitly
exempted in the same file.

### Palette

Neutrals are two separately designed tracks - a light-context one and a
dark-context one. Dark is *not* a mathematical inversion: dark grounds are
deliberately lifted off black, and neighbouring elevation steps are closer
together. Every text/border/status/accent colour was solved against the
*hardest-case* background in its own theme (the lightest of the four
dark-theme grounds, the darkest of the four light-theme grounds) until it
cleared its WCAG 2.2 target - not picked by eye. See
`src/design-system/tokens/tokens.test.ts` for the automated check.

**Sprint 10 re-solved both tracks.** They were a cool, blue-leaning ramp
(hue 222° at 15% saturation) with wide steps between the four grounds,
which is a large part of why the console read as a control panel assembled
out of boxes: every surface announced itself. Two things changed, and only
these two - the architecture of the layer is untouched.

1. The steps between the grounds were compressed hard. In light, the sidebar
   ground and the content ground are now 1.03:1 apart (they were 1.05:1, with
   a hairline drawn between them as well); in dark, 1.11:1 (was 1.16:1). A
   boundary at that distance is felt rather than seen, which is the point -
   the shell separates its regions by tone, alignment and whitespace now, not
   by a drawn grid.
2. The hue was quietened to near-neutral: a whisper of warmth in light (hue
   ~70° at ~2% saturation - paper, not blue paper), a whisper of cool in
   dark (hue ~225° at ~7%), because a warm dark ground reads as brown
   rather than as graphite.

Compressing the grounds moves the hardest case for every foreground, so the
text, border and neutral ramps were re-solved against the new ones and every
ratio was recomputed rather than assumed. The accent and status *hues* are
byte-identical to Sprint 2's and were re-verified, not re-picked - nothing in
this console changed meaning in the repaint. Measured minima after the
repaint: light text-primary 14.56:1, text-secondary 7.24:1, text-muted
4.65:1, border-default 3.05:1, every status colour ≥ 4.54:1; dark
text-primary 12.23:1, text-secondary 8.13:1, text-muted 4.95:1,
border-default 3.26:1, every status colour ≥ 5.11:1.

Sprint 10 also added three `--color-state-*` interaction tints outside the
closed seventeen-name colour vocabulary. They are not new colours: each is a
percentage of `--color-text-primary` over transparent, defined once in
`:root` and never repeated in the dark blocks, so they resolve per theme on
their own and always move *away* from whatever ground they composite over.
Before them, tonal hover reached for `--color-bg-surface-subtle`, which is
darker than its neighbours in *both* themes - so on dark, every hover read as
a pressed state.

### Typography

Roles (`--text-{display,headline,title,section,body,caption,label,code}-*`)
are defined as tokens; nothing in `src/features` or `src/pages` is meant to
invent its own font size.

`section` (16px semibold) and `caption` (13px medium) are Sprint 10
additions, and the reason they exist is the more interesting half. The scale
jumped 14 → 18px with nothing between, so every "section heading" in the
console reached for the 12px uppercase tracked `label` role instead: there
was no step that could carry a heading without shouting. The result was
ACTIVE MODEL, CAPABILITIES, MAINTENANCE, DEPLOYMENT & RELEASE - a
filing-cabinet label applied to ordinary phrases, every heading on every
screen at the same weight as every other, and the hierarchy it was supposed
to create flattened. Section headings are sentence case now, and `label`
survives reserved for what its treatment actually suits: short technical
abbreviations rendered in caps. `--tracking-tight` was added at the same time
and is used only by the two largest roles, where default tracking reads loose
and slightly clerical. `font-variant-numeric: tabular-nums` is set globally on `body`
(and on `code`/`pre`/etc.) because this console is full of GiB/queue-depth
figures that must not jitter in a column.

**Font**: Roboto Flex is the identity face, shipped as a variable woff2 by
`@fontsource-variable/roboto-flex` (SIL OFL-1.1). `tokens/index.ts` imports
the package's stylesheet, Vite emits the font into `dist/assets`, and
`--font-family-sans` leads with `'Roboto Flex Variable'` followed by a real
system fallback stack for first paint. Nothing is fetched at runtime, so
`font-src 'self'` holds and the external-origin scan still passes. Monospace
stays a system stack on purpose: it carries hashes, paths and GiB figures,
where the OS's own mono face is already well hinted at small sizes and costs
no bytes.

### Icons

This project's dependency culture leans against an icon package for the
handful of glyphs the console actually uses.
`src/design-system/primitives/icons/icons.tsx` is a hand-authored inline SVG
set of eleven: `IconClose`, `IconCheck`, `IconAlertCircle`, `IconInfo` and
`IconSpinner` for the primitives, and `IconGrid`, `IconServer`, `IconCpu`,
`IconActivity`, `IconChevronLeft` and `IconChevronDown` added for the nav rail
and the disclosure controls. All are 24x24 viewbox, 1.5px stroke, round
caps/joins, `currentColor` throughout so an icon always inherits its
container's semantic text colour. Authored from scratch for this project - no
third-party licence applies.

### Primitives

`Surface`, `Button`, `IconButton`, `TextField`, `Divider`, `Tooltip`,
`Skeleton` - exactly these seven, each in its own directory under
`src/design-system/primitives/`. Every *interactive* one (Button,
IconButton, TextField, and Tooltip's trigger) implements default, hover,
pressed, focus-visible, disabled, and loading, plus `selected` (Button/
IconButton toggle use) or `error` (TextField) where that state actually
applies. `Surface`, `Divider`, and `Skeleton` are non-interactive
containers/separators/placeholders, so that state matrix doesn't apply to
them - see the doc comment at the top of each for why.

Notably, `Skeleton` takes no `width`/`height` prop: the production CSP
(`style-src 'self'`, no `'unsafe-inline'`) silently drops inline `style`
attributes in the shipped console, so an arbitrary-dimension prop would work
in `vite dev`/tests and then silently fail to size anything once built.
Sizing instead comes from a fixed scale (`size` for the circle variant) or
from the surrounding layout (`block` fills its container; `text` fills the
container's inline width).

### Dev-only primitive gallery

`src/dev/Gallery.tsx`, reachable at `/__gallery` under `vite dev` (see
`src/main.tsx`), renders every primitive across its explicit prop-driven
states (disabled/loading/selected/error) with a theme toggle (system/light/
dark) at the top; hover/pressed/focus-visible are real pseudo-classes best
exercised by actually moving the mouse or tabbing through that live page.
It is excluded from the production bundle by the same
`import.meta.env.DEV` + dynamic-`import()` technique
`src/api/transport/index.ts` already uses to drop `httpTransport.dev.ts` -
`npm run verify:prod-bundle` was extended to scan `dist/` for a marker
string unique to `Gallery.tsx`'s source, the same way it already does for
the dev transport.

## Models (Sprint 4)

The third nav destination. `src/features/models/deriveModels.ts` turns the
raw `Status` payload into one `ModelCardData` per `model_statuses[]` entry;
`src/pages/ModelsPage.tsx` renders each as a card with three disclosure
tiers, per the sprint brief:

- **Primary (always visible):** display name, whether it is the loaded
  model ("Loaded"/"Not loaded"), whether it can be admitted right now
  ("Available now"/"Not available now" - read straight from
  `model_statuses[].available`, never re-derived from `capacity` fields),
  and its context window (`context_tokens`, formatted with thousands
  separators).
- **Secondary ("Show capacity details"):** the full capacity verdict
  (`explainReason` from `src/features/system-status/reasonExplanations.ts`,
  reused, not re-derived), three `ResourceMeter`s - one each for committed
  memory, physical memory, and dedicated GPU memory, each built from
  `computeShortfall` (`src/features/system-status/shortfall.ts`, likewise
  reused), `reclaimable_commit_gib`/`reclaimable_physical_gib` when present,
  and the model's reported capability flags.
- **Advanced ("Show advanced detail", nested inside Secondary):** `profile`
  (weights, KV cache types, output/predict budgets, reasoning budget,
  compact threshold, MoE offload), `checkpoints`, and runtime identity
  (id, engine, variant, backend, `artifact_sha256_prefix`,
  checkpoint-capable).

### Two backend contract gaps this screen deliberately does not paper over

**No model lifecycle state is on the wire.** `edge.Model.State`
(candidate/qualified/enabled/retired) is tagged `json:"-"` on the Go side
and never reaches `GET /api/v1/status`. The `state` field visible inside
`runtime` (`RuntimeFields`'s "State (runtime, not model lifecycle)" row) is
the *runtime's* state, not the model's - this screen labels it as such and
renders no model-lifecycle indicator anywhere, rather than inventing a
placeholder or silently reusing the runtime's state as if it meant the
same thing.

**`runtime.commit` does not exist on this backend version.** An earlier
draft of this sprint's brief listed `commit` as one of `runtime`'s fields
alongside `id`/`state`/`engine`/`variant`/`backend`/
`artifact_sha256_prefix`/`checkpoint_capable`. Neither the current
`RuntimeInfoSchema` (`src/api/schemas/status.ts`) nor any `runtime` object
in the captured fixture (`src/api/__fixtures__/status.sample.json`) actually
carries one - only the top-level `deployment.commit` does (already
surfaced on the System page), and that is a machine-wide release commit,
not a per-runtime one. Rather than invent a field the payload doesn't send,
`RuntimeFields` renders only the fields `RuntimeInfoSchema` actually
declares. If the backend starts sending a per-runtime commit, add it to
`RuntimeInfoSchema` (it's `.passthrough()`, so it already survives parsing
today, just untyped) and to `RuntimeFields`.

### Capability flags

`ModelCapabilitiesSchema` now declares all six flags the Sprint 4 brief
names - `responses`, `chat_completions`, `streaming`, `function_calling`,
`structured_output`, `reasoning` - each optional, matching the existing
convention that an unsupported capability is omitted from the payload
entirely rather than sent as `false`. Only the three original flags are
exercised by the captured fixture; the other three are declared anyway so
the console renders them the instant a backend starts sending them.
`ModelsPage.tsx`'s `CapabilitiesRow` renders a chip only for a flag that is
*exactly* `true` - never inferred from a model id, and never upgraded from
absence.

### ResourceMeter

`src/design-system/components/ResourceMeter/` - the screen's signature
component, one layer above the seven DS 0.1 primitives (hence
`design-system/components/`, not `design-system/primitives/`: it is a
composed, domain-shaped visual, not a generic atom). It takes a
`ShortfallResult` (from `shortfall.ts`) directly rather than raw numbers, so
it can never compute a different verdict than the sentence shown beside it.

Renders as inline SVG rather than a `<div>` sized with inline `style`: the
production CSP (`style-src 'self'`, no `'unsafe-inline'`) silently drops a
`style=""` attribute in the shipped console (the same trap `Skeleton.tsx`'s
doc comment already calls out), but plain numeric SVG attributes
(`x`/`width` on `<rect>`) are ordinary presentation attributes, not a
`style=` sink, and are unaffected by `style-src`. Every verdict pairs an
icon with a word ("Fits"/"Short by"/"Not measured"), and the shortfall
region is a diagonal SVG hatch pattern, not only a danger-coloured fill, so
the meter reads without colour. When any one of available/required/reserve
is `null` ("not measured"), the meter renders a dashed, non-proportional
track and an explicit "Not measured" label instead of a bar that would
otherwise silently look like a measured zero - whichever of the three
figures *are* present still render as GiB values underneath.

### Actions: load, unload, switch

`src/api/commands/models.ts` declares `loadModel`/`unloadModel`/
`switchModel`. These three call `bridgeTransport` directly, in every
environment, without using the dev/prod-switched `getTransport()` selector.
That is deliberate:
ADR 0018 control 1 puts the real authorization for every load/unload/switch
in a native Win32 dialog owned by `cia-console.exe`, entirely outside this
DOM, precisely so a compromised page can never approve its own mutation.
That control only exists on the native bridge path - cia-edge's dev-only
HTTP surface was never meant to carry a mutation - so a model-lifecycle
command must reach the native bridge even under `vite dev`, rather than
falling back to the loopback shortcut just because it's more convenient
under `npm run dev`.

The consequence is the honest refusal the brief asks for: under `vite dev`
there is no `window.chrome.webview`, so `bridgeTransport` rejects, and
`requestModelOperation` turns that specific rejection into a typed
`NativeHostUnavailableError`. `src/features/models/useModelAction.ts`
`instanceof`-checks for it and renders a distinct `no-native-host` UI state
(its own CSS class, `.model-card__action-outcome--no-native-host`, so it's
never confused with a generic `error` state in tests or in the DOM) rather
than a generic failure message. Nothing here fakes success, hides the
buttons, or stubs an approval - the op names and `{ model_id }` wire shape
are provisional, exactly like Sprint 1's stubs, since no backend mutation
endpoint exists yet to call.

**Wording**, chosen so the button never reads as the confirmation step
itself: every button says "Request load" / "Request unload" / "Request
switch" (never bare "Load"/"Unload"/"Switch"), and a caption underneath
- wired to the button via `aria-describedby` - spells it out: *"Asks the
native console host to confirm before this model is loaded. This button
does not load it by itself."* (and the unload/switch equivalents).

When the console's own gating has already disabled the button, that
caption switches to a short, generic pointer instead - *"Refused while the
admission gate is busy right now - see the banner above"* or *"Not offered
right now because this model cannot be admitted - see 'Show capacity
details' below for why"* (`ModelsPage.tsx`'s `actionCaption`, keyed off
`model.action.disabledCause`) - never the full gate-busy or
capacity-shortfall sentence verbatim. That distinction is deliberate, not
an oversight: `model.action.disabledReason` *does* hold the full sentence
(and is exactly what a test asserts against), but rendering it on this
always-visible caption would put "the capacity verdict in full" on the
primary tier, which the Sprint 4 brief reserves for the secondary tier
behind "Show capacity details". The full sentence is one click away there
either way.

`npm run build` prints one informational Rollup notice as a result of this:
`[INEFFECTIVE_DYNAMIC_IMPORT] src/api/transport/bridgeTransport.ts is
dynamically imported by src/api/transport/index.ts but also statically
imported by src/api/commands/maintenance.ts, src/api/commands/models.ts`
(Sprint 7's `maintenance.ts` joined the notice for the identical reason
`models.ts` was already there). This is expected and benign, not a
regression - `bridgeTransport` is the production transport either way, so
it always ships in the bundle; the only effect is that Rollup can no longer
split it into its own lazily-loaded chunk. `verify-prod-bundle.mjs`'s
checks (no loopback address, no dev-transport marker, the restrictive CSP)
are unaffected, since none of them depend on `bridgeTransport` being in a
separate chunk.

**Gating**, derived from the payload rather than guessed, in
`deriveModels.ts`'s `deriveAction`:

- Every action is disabled whenever `gate.active > 0 || gate.queued > 0`,
  with the shared reason `"The admission gate is busy right now (N active,
  N queued) - every load, unload, and switch is refused while inference is
  in flight."`
- The currently active model (`model_statuses[].active === true`) only ever
  offers `unload`.
- A non-active model offers `load` only when *no* model is currently
  active; the instant another model is active, it offers `switch` instead
  - this console never renders a `load` button that the provider would
    refuse for "another model is already loaded", per the brief.
- Beyond the gate, `load`/`switch` are also disabled when
  `model_statuses[].available` is `false`, with the model's own reused
  `explainReason(...).sentence` as the stated reason - the same sentence
  the secondary tier shows in full, so the two can never drift apart.

## Activity (Sprint 6)

The fourth destination, and a split rather than an addition. Through Sprint 5
the System page answered two different questions at once - *what machine is
this* (deployment, runtimes, upstream, uptime) and *what is it doing right
now* (gate counters, GPU memory, the request feed). Sprint 6's principle is
"default calm, details on demand", which is unreachable while both live on
one screen, so the second set moved here. It moved; it is not duplicated -
`status.gate`, `status.gpu_memory` and `status.recent_events` are read in
`src/features/activity/deriveActivity.ts` and nowhere else.

**The admission gate finally has a home.** All seven `gate` fields render
here, including `wait_timeout_seconds`, which no screen had ever shown before
this one - it was in the schema and on the wire, and simply never reached a
human. `deriveActivity` also derives two saturation ratios and a `pressure`
classification so the page renders no arithmetic of its own.

The pressure thresholds are deliberately not a percentage tier. There is no
"busy at 70%" band, because the payload supports exactly one meaningful
boundary: full or not full. `saturated` means the gate is at a hard limit
*right now* - every active slot in use (the next request must queue) or the
queue itself full (the next request is refused). That is the only state that
changes what happens to the next request, which is the only thing an operator
can act on.

Two edge cases are decided rather than left to arithmetic. A `max_active` or
`max_queue` of `0` yields saturation `1`, not `0/0`: a gate configured to
admit nothing is not "0% busy", it is permanently at its own ceiling, and it
reports `saturated` even at zero active - inert-by-configuration is precisely
the kind of thing this screen exists to surface. And `windowSpanMs` is a
min/max across the whole event window, not last-minus-first: cia-edge appends
in arrival order today (`internal/edge/events.go`), but nothing in the wire
contract promises that ordering, and "newest first" is where a log view
naturally drifts - under an endpoint subtraction that silently renders a
negative duration. It needs *two* parseable timestamps, not one; with a
single sample the subtraction would yield `0`, which reads as "every request
happened at the same instant" rather than "not enough information".

**There is no resource history, and the screen says so.** The status payload
is one snapshot: no VRAM-over-time, no CPU-over-time, no trend of any kind.
`recent_events` is a rolling window of *requests* - each with its own
timestamp, method, path, status and duration - which supports a request
timeline and nothing else. Nothing here is plotted as a series, and where a
reader would reasonably expect a trend the page states that the provider
reports a snapshot only. A sparkline of one point is a lie with a nice shape.

The timeline itself is a calm four-figure summary by default (how many
requests, how many errors, the slowest, over what span), with the full
per-request table behind the same disclosure pattern Sprint 4 uses for
capacity details. The raw six-column table is available on demand and is not
what the screen opens with - a dump is the opposite of calm, and this brief's
gate is that telemetry must not dominate the normal experience. Overview is
unchanged and remains the default destination.

The saturation bars use the same inline-SVG-with-numeric-attributes technique
as `ResourceMeter` (see "CSP and assets"): the production policy is
`style-src 'self'` with no `'unsafe-inline'`, so a `style=""` attribute is
silently dropped in the shipped console and proportional geometry has to be
expressed as ordinary SVG presentation attributes. Every bar pairs its colour
with an icon and a verdict in words, so the state survives greyscale and
colour blindness.

### Maintenance (Sprint 7)

Sprint 7's brief pairs "configuração clara" with "operações avançadas
seguras", and only the second half turned out to exist. **There is no Settings
screen and no Settings destination**, because the console cannot write
configuration at all: the wire between the page and the host is
`cmd/cia-console/bridge.go`'s `Operation`, which carries a `kind` and a
`model_id` and no field for a *value*. Every configuration figure the status
payload reports - `gate.max_active`, `gate.wait_timeout_seconds`, the three
`reserve_*_gib` budgets, each model's profile, everything in
`config/models.yaml` - is display-only from here. A screen labelled Settings
would be a read-only list of things nobody can change, which is precisely the
"junk drawer" the sprint's own gate forbids. See ADR 0018 for why the narrow
wire is a design property rather than a gap.

What did exist was drain and resume: wired end to end in Go, allowlisted in
the bridge (`KindDrain`/`KindResume` are already in `mutationKinds` and
`modelIDForbidden`), and with zero frontend code. They live on Activity,
beside the admission gate they act on.

`src/api/commands/maintenance.ts` reaches `bridgeTransport` **directly**,
never through `getTransport()` - the same rule `models.ts` follows, and for a
reason worth restating: under `vite dev` the transport selector returns the
HTTP transport, and a drain routed through it would run with no native
confirmation at all. Going straight to the bridge makes these two operations
impossible outside `cia-console.exe`. They also send **no** `model_id`;
`modelIDForbidden` refuses either verb outright if one is present, and
`maintenance.test.ts` asserts the absence against the actual wire envelope
rather than trusting the source.

The confirmation is the host's native Win32 dialog and nothing else. No modal,
no in-page confirm step, no "type DRAIN to continue" - ADR 0018 control 1
exists because a compromised page can forge an in-DOM dialog and cannot forge
an OS one.

`maintenance.active`, `maintenance.queued` and `maintenance.rejected_total`
render here for the first time. They are how an operator knows whether a drain
has actually *finished*: draining stops new admissions immediately but lets
everything already active or queued run to completion, so the drain is only
done when the first two reach zero.

Three consequences are stated next to the control, in the operator's words,
because the brief demands explicit consequence and each was verified in the Go
source:

1. Draining stops readiness routing - `/readyz` reports not-ready with a
   `Retry-After`, so anything gating traffic on readiness stops sending it here.
2. Draining does **not** unblock model control. `internal/edge/control.go`
   says so outright: `beginControl` still requires an idle gate, so load,
   unload and switch stay refused with 409 for the whole time a drain is in
   progress, and only become available once it has finished.
3. **A drain does not survive a restart.** `internal/edge/gate.go`'s `resume`
   doc comment: maintenance is process-local state, and a restarted edge
   always comes back running. This one carries the danger token, because it is
   the one that can actively mislead - an operator can believe the provider is
   offline while it is serving.

## Visual foundation (Sprint 10)

Sprints 1-9 built a console that worked. It looked like a control panel: four
screens made of bordered, rounded `Surface` panels, section headings set in
12px uppercase, a navigation rail fenced off by a vertical hairline, and a
header whose first statement was `ENVIRONMENT canary`. Every screen was the
same template with different words in it.

This sprint changed the visual language and, deliberately, nothing else. No
schema, no command, no transport, no gate, no disclosure, no ARIA contract and
no rendered datum was altered. What follows is what moved, and why.

### The rule that drove it

**A bordered card is the exception, not the default.** Before writing a
container, the question is "without this border, would the operator stop
understanding this grouping?" - and the answer was no almost everywhere.
Whitespace, alignment, typography and a tonal ground group content perfectly
well at this density. What survives as a bounded region is the loaded model on
the Models screen (a tonal ground, no border) and `Notice` (a band with one
edge rule, no box).

Counting the four page stylesheets: fourteen `Surface bordered rounded` call
sites are gone, along with three badge-pill rules, one capability-chip
capsule, and four page-local error-card rules. Five hairlines remain across
the whole app - between model entries, between runtime blocks, above a ruled
section, above the advanced disclosure, and under a table header row - plus
the borders on the request-log table itself.

### The shell

`NavRail` stopped drawing `border-right`. That single line was the console's
strongest visual statement and it was saying the same thing the tonal step
already said. Removing it only works because the grounds were compressed at
the same time (see Palette above): sidebar and canvas are ~1.03:1 apart in
light, 1.11:1 in dark - felt, not pointed at. The width did not change; 272px
was never the problem. What changed is that the nav list now takes the free
vertical space, so the emptiness below the fourth destination is deliberate,
and is the room a contextual area would occupy later.

The selected destination moved out of accent blue. It used to be a blue tint,
blue text and a 3px blue inset bar - the loudest element in the shell, for the
one thing the operator already knows. It now carries four channels, none of
them accent: a neutral tonal pill, the foreground stepping secondary to
primary, the weight stepping medium to semibold, and a neutral inset marker on
the leading edge. That marker is the non-colour channel Sprint 3 added after
measuring the old tint at 1.25:1 against the rail; Sprint 10 kept the channel
and changed its colour. Accent blue in this console now means "an action you
can take", not "where you are".

`TopBar` was a status strip and is now the shell's statement of context. The
active model's display name is the largest type in the application - larger
than any page's own `<h1>`, deliberately - with its weights and context window
on a quiet second line beneath. That inversion is the point: the destination
you are on is already named twice (the sidebar's selected item, the page
title), while the model you are acting on was previously named only inside one
screen. Nothing was dropped to make room. Readiness is still there, now a
glyph and a word with no capsule drawn around it; the environment is still
there, still monospace because it is an identifier an operator matches against
a deployment, one step quieter beside the readiness indicator. The header
paints no ground and draws no bottom border - it sits on the same canvas as
the page below it.

`src/features/system-status/activeModel.ts` is the one derivation behind that.
`deriveOverview` used to inline its own copy of the id lookup; both call this
now, so there is one answer to "which model is loaded" rather than two to keep
in step. The lookup is an exact id match, because two models in the fixture
differ only by a `-256k` suffix and a loose match would confidently attribute
one model's context window to the other.

One shell change that is easy to miss: `--app-shell-content-max-width` and
`--app-shell-content-padding-inline`. Every page used to set its own measure
while the header set none, so - measured in the browser - the header's first
character began 61px to the left of the page title directly beneath it, on
every screen and at every window size. Two blocks of text that do not share an
edge read as two documents. The column is the shell's property now, and the
header, the stale-data banner and all four page roots resolve through it.

### `ghost`, the tertiary tier

`Button` and `IconButton` gained a `ghost` variant: no border and no fill at
rest, a tonal hover, a *different* tonal pressed state, the shared
focus-visible ring, and the shared geometry so nothing shifts when it gains a
fill. Before it, a disclosure toggle had to be `secondary` - so the least
important control on a screen was also the only outlined box on it.

Where it is used: the three disclosure toggles (`Show capacity details`,
`Show advanced detail`, `Show request details`) and the sidebar's collapse
control. Where it is deliberately *not* used: every model-lifecycle button
(`Request load`/`unload`/`switch`), the maintenance `Request drain`/`resume`,
and `Retry`. A control that changes the provider's state must look like one
before it is hovered.

The blanket `opacity: 0.5` on disabled buttons went at the same time. Measured
in dark theme it put the primary button's label at about 1.9:1 against its own
fill - "Request switch" was, in practice, unreadable on the five refused
models the Models screen shows at rest. It was never a WCAG failure (1.4.3
exempts inactive controls) and it was the wrong outcome anyway: an operator
reading a screen full of refused actions needs to know *which* action is being
refused. Each variant states its own disabled treatment now, all landing on
`--color-text-muted`, which clears 4.5:1 on every ground in both themes.

### Two new design-system components

Both were extractions, not inventions - the same signal that produced
`FieldList`.

- **`Notice`** (`design-system/components/Notice/`) - the flat status band, on
  a tonal ground with a 3px tone-coloured rule down the leading edge and the
  sentence itself in ordinary body colour. Its call sites: the stale-data
  banner, the gate-busy banner, four page error states, the two action-outcome
  refusals, and the one drain consequence that can actively mislead. It sets
  **no ARIA role of its own** - whether a notice is an `alert`, a `status`, or
  silent chrome depends on the situation, and only the caller knows.
  `AppShell`'s stale banner passes none on purpose, for the reason its own doc
  comment gives.
- **`StatGroup`** (`design-system/components/StatGroup/`) - a handful of short
  label/figure pairs side by side. This is the pattern that kept becoming
  cards. It is a sibling to `FieldList`, not a merge: `FieldList` is a vertical
  list of many facts about one thing, read by scanning down the label column;
  this is a horizontal band of a few headline figures, read across. One
  component doing both would be a `direction` prop hiding two components.

### Four pages, four compositions

`src/pages/page.css` holds the shared grammar - the type roles, the vertical
rhythm, the one opt-in hairline - so each page's own stylesheet spends itself
entirely on what makes that page different. Before it, the four screens
achieved their "personality" by re-declaring the same heading and section
rules under four class prefixes, which is not four personalities; it is one
template with four names.

| Screen | Personality | The structural device that makes it so |
| --- | --- | --- |
| Overview | Editorial, calm | The only screen using the `display` role: a 32px readiness verdict on open canvas, prose at a 64ch measure beneath it, and exactly one hairline on the whole page. Four bordered panels became zero. |
| Models | Model-centric | The loaded model is hoisted to the top of the list, labelled "Active model", set on a tonal ground with room around it, and given the largest name on the page. Five channels - position, label, ground, spacing, type size - none of them a colour, so it survives a greyscale print. Six cards became a list of entries separated by hairlines; three badge pills per entry became one line of words. |
| Activity | Temporal, linear | Four sections read top to bottom as one column. The gate bars halved in height, from a 16px capsule that read as a component to an 8px rule that reads as a measurement. A new always-visible "Latest requests" list - newest first, one line each - is what makes this screen a flow of events rather than an inventory. |
| System | Dense, technical | The one screen that runs *tighter* than the shared rhythm (`--space-xl` between sections, not `--space-2xl`). Density is the point: an operator here is comparing a commit hash against a deployment. Four bordered sections became three flat ones, and the fourth existed only to hold a single uptime figure, which is a row now. |

That "Latest requests" list needed the only derivation change in this sprint:
`deriveActivity`'s `events.recent`. It sorts by parsed timestamp, newest
first, with a stable tie-break on payload order and unparseable timestamps
last. `entries.slice(-6).reverse()` would give the same answer against today's
cia-edge, which appends in arrival order - but nothing in the wire contract
promises that, "newest first" is the ordering a log endpoint naturally drifts
toward, and a list captioned "latest" that silently shows the oldest six is a
worse failure than one that shows nothing. Same reasoning as `windowSpanMs`'s
existing min/max. Times in that list are the machine's local clock, and the
screen says so, because the full log one disclosure below renders the
provider's own UTC timestamps verbatim.

### What this sprint deliberately did not do

- **No Chat.** No composer, no message list, no conversation history, no
  placeholder destination. The rule that a navigation item exists only when a
  real screen stands behind it is intact - there are still exactly four.
- **No AI accent.** `--color-accent-ai` is untouched and still unused by any
  component. No purple, no gradients, no glow. The structure has to work in
  neutrals first; the identity layer comes later.
- **No glassmorphism.** No backdrop blur, no translucent panels, no large
  shadows. Elevation appears only where something genuinely floats, which in
  this console is still only `Tooltip`.
- **No font change.** Roboto Flex stays. The visual reference for this pass
  uses a different face; adopting it would have traded away the one piece of
  identity this console already owns for a resemblance nobody asked for. What
  changed is how the face is *used*.

## Sprint closure status

This checkout represents Sprints 1 through 10.

One correction to an earlier version of this table. The 2026-08-29 pass that
added settled-layout-geometry and bitmap assertions to the native harness was
recorded here as "Sprint 9". It is not: that work closed the visual-audit gap
flagged during Sprint 8, and it is listed under Sprint 8 below where it
belongs. Sprint 9 is Documentation and Stabilization, and its evidence is the
2026-09-02 pass.

| Sprint | Status | Closure evidence |
| --- | --- | --- |
| 1 | Closed | Transport boundary, status schema, fixture-backed query path, strict TypeScript/lint/test/build gates. |
| 2 | Closed | DS 0.1 token layers, primitives, CSP-safe assets, primitive/token tests. |
| 3 | Closed | App shell, real navigation destinations only, Overview/System split, health summary. |
| 4 | Closed | Models page, capability/capacity derivation, native-bridge model commands, action gating tests. |
| 5 | Closed | ADR 0018 closes inference transport as an architectural decision: credentialless renderer and real cancellation are contract requirements; inference UI remains outside this console surface until a new transport decision exists. |
| 6 | Closed | Activity page owns gate, GPU snapshot, and request timeline; System no longer duplicates live activity. |
| 7 | Closed | Drain/resume maintenance controls live beside the admission gate and use native confirmation through the bridge. |
| 8 | Closed | Hardening pass unifies maintenance-state labels and moves spoken status updates into one persistent live region. Also closed the visual-audit gap it exposed: the native E2E now checks settled layout geometry and a Win32 bitmap summary of the visible WebView2 content, catching blank/monochrome regressions without a golden screenshot. |
| 10 | Closed with debt | Visual foundation: card-based composition replaced by a flat, typography-led one; the sidebar structural border removed in favour of a compressed tonal step; the active model promoted to the shell header; `ghost` added as the tertiary control tier; `Notice` and `StatGroup` extracted; the four screens given genuinely different compositions. Typecheck, lint, 315 tests, production build and `verify:prod-bundle` all green. Debt recorded, not resolved: no golden-screenshot regression test exists, so the light/dark visual result of this sprint is verified by inspection in a real browser and by CSS-source assertions, not by CI. |
| 9 | Closed with debt | Documentation audited against code and corrected; `docs/FRONTEND.md` written as the frontend reference; `THREAT_MODEL.md` amended for the console per ADR 0018's own recorded consequence; NOTICE completed. Debt recorded, not resolved: the source is still untracked and ADR 0018 control 3 is still unimplemented. |

The closure evidence is intentionally not a production promotion. The WebView2
E2E harness proves load, mount, bridge integrity, navigation containment,
controller visibility, absence of listening sockets, stable default-layout
geometry, and a nonblank captured bitmap. It still does not certify exact visual
design or compare against a golden screenshot.
