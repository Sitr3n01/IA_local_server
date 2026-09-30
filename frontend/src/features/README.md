# features/

A feature module turns the raw `GET /api/v1/status` payload into
presentation-ready state, and owns the hooks a page renders. Pure derivation
lives in `derive*.ts`/`format.ts` and is tested directly; the hooks are thin.

## The modules

`system-status/` (Sprint 3) owns the `reason` -> plain-language lookup
(`reasonExplanations.ts`), the shortfall arithmetic for `insufficient_*`
budgets (`shortfall.ts`), the byte/duration formatters (`format.ts`), error
description (`describeError.ts`), and four hooks: `useOverviewViewModel`,
`useSystemViewModel`, `useHealthSummary`, and `useLiveAnnouncement`.

`models/` (Sprint 4) owns per-model derivation for `ModelsPage`.
`deriveModels.ts` turns `model_statuses[]` into one `ModelCardData` each -
reusing `system-status`'s `reasonExplanations`/`shortfall` for the capacity
verdict rather than re-deriving it - and computes load/unload/switch gating
from `gate`/`active_model`/`available`. `useModelsViewModel` is the page's one
data entry point; `useModelAction` drives one button's
idle -> pending -> (idle | no-native-host | error) lifecycle.

`activity/` (Sprint 6) owns admission-gate pressure, the GPU snapshot and the
request timeline for `ActivityPage` (`deriveActivity.ts`,
`useActivityViewModel`), plus the drain/resume control's lifecycle
(`useMaintenanceAction`, Sprint 7).

`maintenanceState.ts` (Sprint 8) sits at the root of `features/`, in no module,
because it is the one thing three unrelated screens must agree on: the label
for a `maintenance.state`. It is imported by `OverviewPage`, `ActivityPage`,
and `useLiveAnnouncement`. Only the *label* is shared - each screen keeps its
own description on purpose; the module's own doc comment says why.

## What is enforced, and what is only convention

Two ESLint rules in `eslint.config.js` are real gates:

- `no-restricted-globals` bans the bare `fetch` outside `src/api/transport/`.
- `no-restricted-imports` bans importing `src/api/transport/*` from outside
  `src/api/`. Features and pages cannot reach the transport layer.

Everything below this line is convention. Nothing enforces it, so it is worth
stating precisely rather than implying a rule exists:

- A page's data comes from exactly one view-model hook. No page calls
  `useStatusQuery` or any other query directly, and none does today.
- Pages may additionally import: a feature's action hook, a feature's pure
  formatters and types, and command functions from `src/api/commands`.
  `ModelsPage` and `ActivityPage` both do the last one - they import the
  command and hand it to the action hook, which is where the lifecycle lives.
- Derivation belongs in a `derive*.ts`, not in a component.

## Two conventions the code follows that are easy to get wrong

**View-model hooks branch on `data`, never on `isError`.** All four hooks do
this, each carrying the same justification: TanStack Query keeps the last good
snapshot while a later poll is failing, so `isError` can be true with perfectly
good data in hand. Branching on it first would blank a working screen on one
failed poll. The shape is always:

```ts
if (data === undefined) {
  if (isError) return { state: 'error', ... };
  return { state: 'loading' };
}
```

**Command modules reach `bridgeTransport` directly.** `commands/models.ts` and
`commands/maintenance.ts` both import it rather than calling `getTransport()`.
This is deliberate and load-bearing: a mutation must never be able to run over
the dev HTTP transport, which has no native confirmation behind it (ADR 0018
control 1). They are still inside `src/api/`, so the import ban does not apply.

## Known drift

The raw payload is read outside `system-status/` in several places, and no
rule prevents it: `models/deriveModels.ts` reads `model_statuses`, `gate` and
`active_model`; `activity/deriveActivity.ts` reads the `maintenance` block; and
`OverviewPage`/`ActivityPage` both read `maintenance.state` directly to select
a description. An earlier version of this file claimed nothing outside
`system-status/` reached into those fields. That was true when it was written
and is not true now, which is why it is recorded here instead of deleted.
