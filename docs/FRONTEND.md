# Operator console frontend

Reference for `frontend/` — the React and TypeScript console rendered inside a
WebView2 control by `cia-console.exe` (ADR 0018). `frontend/README.md` is the
narrative of how it was built, sprint by sprint. This is the reference: what
the layers are, what may call what, what is enforced, and what is only
convention.

Every claim here was checked against the code. Where a property is a
convention with no enforcement behind it, this document says so rather than
implying a gate exists.

## What this console is, and is not

It is a **read-mostly operator surface**. It renders `GET /api/v1/status` and
exposes five state-changing operations: load, unload and switch a model, and
drain and resume the admission gate.

It is not a client. There is no prompt composition, no streaming, no cancel
button, and no inference UI of any kind — see "Inference" under the ADR
decisions below. There is no Settings screen either, and that is structural
rather than pending: the bridge's `Operation` type carries a `kind` and an
optional `model_id` and nothing else, so there is no field in the protocol
that could carry a value to write.

## Layers, and the direction of dependency

```
pages/  ->  features/  ->  api/queries/   ->  api/transport/  ->  host
                       ->  api/commands/   ->  api/transport/  ->  host
design-system/  <-  pages/, app/          (leaf; depends on nothing above it)
```

| Directory | Owns |
| --- | --- |
| `app/` | `App.tsx` composition root, the TanStack Query provider, and the shell (`AppShell`, `NavRail`, `TopBar`, `navigation.ts`). |
| `pages/` | The four screens: Overview, Models, Activity, System. Layout and markup only. |
| `features/` | Derivation and view-model hooks. See `src/features/README.md`. |
| `api/schemas/` | Zod schemas for backend payloads. |
| `api/queries/` | Reads: transport → Zod → typed data. |
| `api/commands/` | Mutations. |
| `api/transport/` | The only code permitted to touch the bridge or the network. |
| `design-system/` | Tokens, primitives, components, global styles. Imports nothing from `features/`, `pages/` or `api/`. |
| `lib/` | Framework-agnostic helpers (`assertNever`). |
| `dev/` | `Gallery.tsx`, the dev-only primitive gallery at `/__gallery`. Excluded from the production bundle and asserted absent by `verify:prod-bundle`. |

**Enforced by ESLint** (`eslint.config.js`), so these fail the build:

- The bare `fetch` global is banned outside `src/api/transport/`.
- Importing `src/api/transport/*` is banned outside `src/api/`.
- `dangerouslySetInnerHTML`, assignment to `innerHTML`/`outerHTML`, and
  `insertAdjacentHTML` are banned everywhere, by AST selector.

**Convention only** — no rule enforces these, and they hold today by review:

- A page takes its data from exactly one view-model hook, never from a query.
- Derivation lives in a `derive*.ts`, not in a component.
- `design-system/` stays a leaf.

## The API boundary

`Transport` is one method:

```ts
interface Transport { request<_T>(op: string, params?: unknown): Promise<unknown>; }
```

It returns `unknown` on purpose. The type parameter is a call-site
documentation hint, not a guarantee — nothing in the transport validates
shape. **Validation is the schema layer's job**: every query parses the raw
result through a Zod schema before anything treats it as trustworthy. Do not
add an `as T` cast and call it done.

Two implementations, selected at build time by `import.meta.env.DEV` behind a
dynamic `import()` so Rollup drops the unused one entirely:

- **`bridgeTransport`** (production) — posts over `window.chrome.webview`,
  tags the message with a correlation id, holds the pending promise in a map,
  and settles it when `window.__ciaConsoleReply` fires with the matching id or
  a 35-second timeout expires. Throws `NativeHostUnavailableError` when
  `window.chrome.webview` is absent, i.e. when the page is not inside
  `cia-console.exe`.
- **`httpTransport.dev`** (development) — `fetch`es cia-edge through Vite's
  dev-server proxy (`/__cia-edge`). A direct browser fetch to
  `127.0.0.1:18091` would be cross-origin and cia-edge deliberately sends no
  CORS header; a browser enforces CORS independently of CSP, so no policy
  relaxation could work around it. The proxy forwards server-side instead.

The host accepts six operation kinds and no others, matched by whole-string
lookup in a closed allowlist — never by prefix or substring, so an unknown
kind is refused rather than defaulting into a bucket:

| Kind | Class | `model_id` |
| --- | --- | --- |
| `status` | read | forbidden |
| `load`, `unload`, `switch` | mutation | required, ≤256 bytes |
| `drain`, `resume` | mutation | forbidden |

Reads never reach the admin client. Mutations always pass `Approver.Approve`
first. Status is polled every 5 seconds; failed queries retry once.

**Schemas are permissive on purpose.** Every object schema ends in
`.passthrough()`, and numeric fields that may be unmeasured are `nullable`
rather than defaulted to `0` — `null` means "not measured", which is a
different and stricter condition than a measured zero. A backend that adds a
field must not break the console.

One consequence is load-bearing and was learned the hard way: **a transport
carries bytes, it never re-models them.** Routing the console's status read
through a narrower Go struct on the host side silently dropped a third of
cia-edge's snapshot; the page's schema rejected what arrived and rendered an
error while every host-side signal reported success. The host now reads
`StatusRaw` and forwards the bytes. Any Go struct in the middle of this path
is a field the console can lose.

## The security boundary

The renderer is treated as hostile input, not as part of the host.

- **No credential ever reaches the renderer.** The console holds none at all:
  it never links `internal/credential`, never invokes `cia-credential.exe`,
  and supplies `TokenProvider` as a stub that always returns an error, so the
  deprecated HTTP mutation path fails closed rather than reaching Credential
  Manager. A compromised page inherits the host's authority to *ask*, never
  the host's credential.
- **Every mutation stops at a native confirmation.** A Win32 `MB_OKCANCEL`
  dialog owned by the host window names the operation and its target and
  defaults focus to Cancel. It runs on the host's thread, driven by a syscall
  the DOM has no handle to, so the page can neither forge, suppress, nor
  answer it.
- **No socket.** The bundle is served from disk through a WebView2
  virtual-host mapping at `cia-console.invalid` with `DENY_CORS`. There is no
  HTTP server in the package.
- **CSP:** `default-src 'none'; script-src 'self'; style-src 'self'; img-src
  'self' data:; font-src 'self'; connect-src 'none'`. Hand-written in
  `index.html`; `vite.config.ts` relaxes `connect-src` *and* `style-src` for
  `vite dev` only, and `verify:prod-bundle` asserts the built page carries the
  strict form with no `'unsafe-inline'`, no `'unsafe-eval'`, and no loopback
  address.
- **Navigation containment.** `connect-src 'none'` closes fetch, XHR and
  WebSocket. It does not close window opening — CSP governs what a document
  fetches, embeds and executes, not whether the browser may open a window, and
  a `window.open` target URL carries whatever the caller puts in it. Two host
  guards fail closed on anything that is not exactly
  `https://cia-console.invalid`: `NavigationStarting` cancels, and
  `NewWindowRequested` marks the request handled and opens nothing.
- **Ambient environment.** `WEBVIEW2_*` overrides are neutralized by the
  binding's `init()` before any WebView2 environment exists, and the host
  re-asserts this at startup and calls `log.Fatalf("refusing to start")` if it
  does not hold — including a check that the pure-Go loader is in use, which
  is only true when the `native_webview2loader` build tag is unset.

A note on `style-src 'self'`: it **silently drops inline `style` attributes**.
Anything proportional — a meter's width, a bar's length — must be expressed as
inline SVG numeric attributes, not as an inline style. There is no error when
this is got wrong; the geometry simply does not apply.

## The token model

Three layers, and a component may only reach the middle one.

1. **Foundation** (`tokens/foundation.css`) — raw palette ramps, spacing,
   radii, type steps, motion durations. No meaning attached. The only file
   permitted a literal hex colour.
2. **Semantic** (`tokens/semantic.css`) — the fixed `--color-*`, spacing,
   radius, motion and typography-role properties components actually
   reference. This layer resolves light and dark **entirely in CSS**: `:root`
   is the light default; `@media (prefers-color-scheme: dark)` guarded by
   `:root:not([data-theme="light"])` follows the OS; `:root[data-theme="dark"]`
   redefines the same tokens again so an explicit choice wins in both
   directions. No component branches on theme anywhere in this codebase.
3. **Component tokens** — declared next to the one component that needs them.
   `AppShell.css` holds all four: the sidebar's collapsed and expanded widths,
   and (added by the Sprint 10 visual foundation) the shared content column's
   max width and inline inset, which the header, the stale-data banner and all
   four page roots resolve through so they share one left edge.

Neutrals are two separately designed tracks — dark is not a mathematical
inversion of light. Sprint 10 re-solved both. The grounds were a cool,
blue-leaning ramp (hue 222°, 15% saturation) with wide steps; they are now
near-neutral (a whisper of warmth in light, a whisper of cool in dark) with
the steps between them compressed hard — about 1.03:1 between the sidebar and
the canvas in light, 1.11:1 in dark. That compression is what let the shell
drop its structural borders: the boundary is carried by tone, alignment and
whitespace instead of by a drawn line. Every foreground was re-verified
against the new hardest-case ground in its own theme (in light, the darkest
of the four; in dark, the lightest) rather than assumed to still clear.

Sprint 10 also added, outside the closed seventeen-name colour vocabulary,
three `--color-state-*` interaction tints. They are not new colours: each is a
percentage of `--color-text-primary` over transparent, so they resolve per
theme automatically and always move *away* from whatever ground they composite
over. Before them, tonal hover reached for `--color-bg-surface-subtle`, which
is darker than its neighbours in both themes — so in dark mode every hover
read as a pressed state.

Two type roles were added at the same time, `--text-section-*` (16px semibold)
and `--text-caption-*` (13px medium), to close the 14→18px gap that was
pushing every section heading in the console into the 12px uppercase tracked
`label` role. `label` survives, now reserved for what its treatment suits:
short technical abbreviations rendered in caps.

Stylelint bans literal hex and `rgb()`/`rgba()`/`hsl()`/`hsla()` across
`design-system/primitives`, `design-system/components`, `features`, `pages`
and `app`. `foundation.css` is explicitly exempt.

## Component maturity

| Layer | Members | State |
| --- | --- | --- |
| Primitives | `Surface`, `Button`, `IconButton`, `TextField`, `Divider`, `Tooltip`, `Skeleton` | Stable. The DS 0.1 set; treat their props as a contract. |
| Icons | 11 hand-authored inline SVGs, 24×24, 1.5px stroke, `currentColor` | Stable. Add one here rather than inlining SVG in a page. |
| Components | `ResourceMeter`, `FieldList`, `Notice`, `StatGroup` | Mixed. `ResourceMeter` still has one consumer (Models). `FieldList` has three (Models, Activity, System). `Notice` and `StatGroup` are Sprint 10 extractions with six and three call sites respectively — see below. |

`TextField` is **used by nothing in the four shipped pages.** It exists because
DS 0.1 called for it and the gallery exercises it. Treat it as unproven under
real use.

That debt is now paid. `FieldList` was extracted from the three byte-identical
copies in `ActivityPage`/`ModelsPage`/`SystemPage`; Sprint 10 extracted two
more from the same signal:

- **`Notice`** — the flat status band. Five screens were each hand-rolling one
  out of a bordered `Surface` with a status-coloured border: the stale-data
  banner, the gate-busy banner, four page error states, and the two
  action-outcome refusals. It deliberately sets **no ARIA role of its own** —
  whether a given notice is an `alert`, a `status`, or silent chrome depends
  on the situation, and only the caller knows. `AppShell`'s stale banner
  passes none on purpose.
- **`StatGroup`** — a handful of short label/figure pairs side by side. This is
  the pattern that kept becoming cards: Overview rendered its gate occupancy
  and maintenance state as two bordered panels in a tile grid, Activity
  rendered its four timeline figures as a bespoke `<dl>`, and both were a
  caption over a number. It is a sibling to `FieldList`, not a merge of it —
  `FieldList` is a vertical list of many facts about one thing, read by
  scanning down the label column; this is a horizontal band of a few headline
  figures, read across.

## Adding a component

1. Decide the layer. A **primitive** is generic and domain-free. A
   **component** composes primitives but is still reusable (`ResourceMeter`).
   If it knows about models, gates or capacity, it is page or feature markup —
   leave it there.
2. Create `design-system/<layer>/<Name>/<Name>.tsx` plus `<Name>.css`.
3. Reach colour only through semantic tokens. Stylelint will fail the build on
   a hex literal.
4. If it is interactive, give it a `:focus-visible` rule with real geometry —
   an outline or ring, not only a colour change. `focus-visible.test.ts`
   asserts this for every interactive primitive and will fail without it.
5. Export it from the layer's `index.ts`.
6. Add it to `dev/Gallery.tsx` so it is visible in isolation.
7. Write tests: behaviour in `primitives.test.tsx` (or a dedicated file), plus
   any CSS-level invariant via `test-utils/readCss.ts`.

## Adding a feature

1. Create `features/<name>/`.
2. Put derivation in `derive<Name>.ts` as pure functions over the parsed
   `Status`. Test these directly; this is where most of the value is.
3. Add `use<Name>ViewModel.ts`. It calls a query and returns a discriminated
   union. **Branch on `data`, never on `isError`** — TanStack Query keeps the
   last good snapshot while a later poll fails, so branching on `isError`
   first blanks a working screen on one bad poll:

   ```ts
   if (data === undefined) {
     if (isError) return { state: 'error', ... };
     return { state: 'loading' };
   }
   ```
4. For a mutation, add the command to `api/commands/` — importing
   `bridgeTransport` directly, so it can never run over the dev HTTP transport,
   which has no native confirmation behind it — and a `use<Name>Action` hook
   for the idle → pending → settled lifecycle.
5. Add the page and, if it is a destination, an entry in
   `app/shell/navigation.ts`. `DestinationId` is a closed union, so `App.tsx`'s
   exhaustive switch will fail to compile until the page is wired.

**A destination exists only when a real screen exists behind it.** Never add a
placeholder nav item. This is why there is no Inference or Settings entry.

## Testing

```sh
npm ci && npm run typecheck && npm run lint && npm test && npm run build && npm run verify:prod-bundle
```

27 test files, run by Vitest under jsdom. The layers:

| Layer | Examples | What it buys |
| --- | --- | --- |
| Pure derivation | `deriveModels`, `deriveActivity`, `shortfall`, `format` | Most of the coverage. Cheap and total. |
| CSS-as-data | `tokens.test.ts`, `focus-visible.test.ts`, `motion.test.ts` | Reads the stylesheet as text and asserts structural invariants a runtime test cannot see. |
| Component | `primitives.test.tsx`, `Tooltip`, `ResourceMeter` | Behaviour through the accessibility tree. |
| Page | four `*Page.test.tsx` | Loading, error and populated states end to end. |
| Hook | `useModelAction`, `useMaintenanceAction`, `useLiveAnnouncement`, `useRailCollapsed` | Lifecycles and the announcement contract. |

TypeScript runs with `noUncheckedIndexedAccess` and
`exactOptionalPropertyTypes` on top of `strict`. Both change how you write
code, not just what compiles.

**Prove a new assertion has teeth by breaking the thing it guards and watching
it fail.** This is not ceremony: three consecutive attempts to do exactly that
on the native harness reported PASS from Go's test cache without launching
anything, and the assertion looked fine while testing nothing.

## Visual testing, and what it does not prove

The native harness is `cmd/cia-console/e2e_windows_test.go`. It is **opt-in**
and CI never runs it — it needs an interactive desktop session, because
WebView2 will not compose onto a headless or service session, and a built
`frontend/dist`:

```sh
cd frontend && npm ci && npm run build && cd ..
CIA_CONSOLE_E2E=1 go test ./cmd/cia-console/... -run TestConsoleE2E -count=1 -v
```

`-count=1` is not optional. `go test` replays a cached pass — log output and
all — whenever it believes the inputs are unchanged, and it cannot see this
harness's real inputs.

A pass proves: the window opened, navigated to the virtual host, mounted
React, round-tripped status through the bridge **without narrowing the
payload**, rendered the default layout with no horizontal overflow, composited
a visible controller, refused a navigation and a `window.open` to a foreign
origin, captured a non-blank and non-monochrome bitmap of the visible content,
and opened no listening socket.

It does **not** prove:

- That the console looks right. There is no golden screenshot, no OCR, no
  pixel comparison. The bitmap check distinguishes "something rendered" from
  "blank or one flat colour" and nothing finer.
- That the console works against a real provider. The harness serves
  `frontend/src/api/__fixtures__/status.sample.json` from an in-process
  `httptest` server via `CIA_CONTROL_URL`. It never contacts cia-edge.
- That the schema still matches cia-edge. The fixture and the Zod schema are
  derived from the *same* captured response, so this check cannot detect
  backend drift. It detects the transport narrowing the payload, which is the
  defect it was built for and a real one.
- Anything under `-race`. The race job is ubuntu-only and the harness is
  `//go:build windows`.

## Accessibility

What is **enforced by a test**:

- Every interactive primitive defines a `:focus-visible` rule with real
  geometry, and its ring colour comes from a semantic token.
- Non-interactive primitives (`Surface`, `Divider`, `Skeleton`) define none, on
  purpose.
- `prefers-reduced-motion: reduce` is honoured by one global rule with
  `!important`, imported once from `main.tsx` rather than per component.
- Exactly one live region exists, and what it is allowed to say.

What is **expected of new code** but has no test behind it: label every
control, keep the accessible name and the visible label in agreement, and
prefer a real element over a `div` with a `role`.

**The live-region rule.** There is one `role="status"` region, polite and
atomic, mounted for the life of the session in `AppShell`. It is never
unmounted — assistive technology announces mutations *inside* a region that
already exists, and a region inserted together with its message is frequently
not announced at all. Only its text changes.

It announces semantic transitions only: readiness, maintenance, and entering
or leaving the unreachable state. It never announces polls, counters, or queue
depth. At a 5-second poll that would be roughly 720 utterances an hour,
indistinguishable from silence except that it buries whatever the operator was
reading. `useLiveAnnouncement` starts its refs `undefined` so the first poll
never speaks.

**Contrast is not tested.** The ratios recorded in `semantic.css`'s comments
were solved by hand at design time. `tokens.test.ts` asserts cascade structure
— that all three blocks exist, that every token is redefined in each, that
light and dark differ, that OS-dark and explicit-dark agree — and computes no
contrast ratio anywhere. A token edit can break contrast silently.

Nothing here has been exercised with real assistive technology.

## ADR decisions that constrain this frontend

ADR 0018 governs. Its five controls are conditions of the decision, not
implementation detail:

| Control | State |
| --- | --- |
| 1 — every mutation requires a native confirmation outside the DOM | Implemented; `Approver` in `bridge.go`, Win32 dialog in `main_windows.go`. |
| 2 — the host sanitizes its own environment before creating WebView2 | Implemented and fails closed at startup. |
| 3 — the runtime is pinned to the Fixed Version distribution | **Not implemented.** The console runs on the machine's auto-updating Evergreen runtime — the one thing the ADR explicitly rejected. See "Known gaps". |
| 4 — no HTML injection path, and the build proves it | Partly. The lint bans are real and the CSP is asserted against the built page. The ADR also says the CSP is "emitted by the build, never hand-written into a template" — it *is* hand-written in `index.html` — and that the build asserts no `innerHTML` sink in the bundle, which it does not. React's own vendored code contains such sinks, so a bundle-level grep would fail on every build; the ban is enforced on *our* source instead. |
| 5 — the npm dependency tree is covered by CI | Partly. CycloneDX SBOM and `npm audit --audit-level=high` run in CI. The WebView2 user-data folder and Crashpad dump retention policy the ADR also requires does not exist. |

**Inference is out of scope by decision, not by omission.** Sprint 5 resolved
that an inference transport must satisfy two named invariants — a
*credentialless renderer* (it is the renderer, not the console, that may never
hold a credential; the host may) and *mandatory cancellation in the contract*
(request/response plus real abort). No transport satisfying both has been
built, so no inference UI is exposed. Half a privileged path in the tree is
the failure mode this program has spent its whole length avoiding.

## Known gaps

- **The WebView2 runtime is unpinned.** ADR 0018 control 3 is decided and
  unimplemented. It is the only component executing operator-plane code that
  the project's "production runs only from bytes we hashed" rule does not
  cover.
- **`frontend/` and `cmd/cia-console/` are untracked in git.** Nine sprints of
  work exist only in a working tree. The CI `frontend` job is itself
  uncommitted, so committing `.github/workflows/ci.yml` without the frontend
  would produce a job that fails on `npm ci` in a directory that does not
  exist. These two must land together.
- **`CIA_CONSOLE_FRONTEND_DIR` is read unvalidated**, so a serving-user process
  can substitute the console's content. This grants no new authority — that
  account can open the pipe directly — but it can misrepresent state and
  solicit approval for an operation the operator did not intend.
- **`FieldList` is triplicated** across three pages.
- **No visual design audit has been performed by a human.** No pixel has been
  inspected by anyone.
