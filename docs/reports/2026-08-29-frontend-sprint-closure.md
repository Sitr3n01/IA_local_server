# Frontend sprint closure - 2026-08-29

This report closes the frontend evidence currently present in
`C:\IA\IA_local_server`. Sprint 5 and Sprint 8 are closed by operator decision
during this pass, and this pass also closes the native visual validation gap
left by the earlier E2E harness.

**Corrected 2026-09-02.** This report originally numbered that visual-validation
work "Sprint 9" and declared Sprints 1 through 9 closed. That was a mislabel:
Sprint 9 in this program is Documentation and Stabilization. The visual work is
real and is retained below under Sprint 8, whose audit gap it closed. Sprint 9's
own evidence is `2026-09-02-frontend-sprint9-documentation.md`.

## Status by sprint

| Sprint | Status | Evidence |
| --- | --- | --- |
| 1 | Closed | React/TypeScript console foundation, transport boundary, fixture-backed status schema, query path, strict build gates. |
| 2 | Closed | DS 0.1 tokens and primitives, CSP-safe local assets, primitive/token tests. |
| 3 | Closed | App shell, rail/top bar, Overview and System pages, real-destination-only navigation rule. |
| 4 | Closed | Models page, per-model derivation, capability/capacity display, bridge-only load/unload/switch actions. |
| 5 | Closed | ADR 0018 records the architecture decision: inference transport requires a credentialless renderer and real cancellation; no inference UI is exposed by this console. |
| 6 | Closed | Activity page owns gate pressure, GPU snapshot, and request timeline without duplicating System. |
| 7 | Closed | Maintenance drain/resume controls are placed beside the gate and route through native confirmation. |
| 8 | Closed | Hardening unified maintenance labels and centralized spoken updates in a persistent live region. |
| 9 | (see correction above) | Not closed by this pass. The visual-validation work listed here as Sprint 9 belongs to Sprint 8's audit gap. |

## Validation commands

Run from `C:\IA\IA_local_server\frontend`:

```powershell
npm ci
npm run typecheck
npm run lint
npm test
npm run build
npm run verify:prod-bundle
```

Run from `C:\IA\IA_local_server`:

```powershell
C:\IA\toolchains\go1.26.5\go\bin\go.exe test ./cmd/cia-console/... -count=1 -v
$env:CIA_CONSOLE_E2E='1'; & 'C:\IA\toolchains\go1.26.5\go\bin\go.exe' test ./cmd/cia-console/... -run TestConsoleE2E -count=1 -v
```

## Validation result

Result on 2026-08-29: all commands above passed. The native E2E harness was
run with `-count=1` so Go did not replay cached output.

- `npm ci`: 329 packages installed, 0 vulnerabilities.
- `npm test`: 27 test files passed, 249 tests passed.
- `npm run build`: Vite production build completed; Rollup's known
  `INEFFECTIVE_DYNAMIC_IMPORT` notice for `bridgeTransport` appeared.
- `npm run verify:prod-bundle`: passed; no loopback/dev/gallery markers,
  external origins, or CSP drift found in `dist/`.
- `go test ./cmd/cia-console/... -count=1 -v`: passed; `TestConsoleE2E` skipped
  as expected without opt-in.
- `CIA_CONSOLE_E2E=1 ... TestConsoleE2E`: passed. Observations from that run:
  `horizontalOverflowPx=0`, five visible primary-nav buttons, one main heading,
  four Overview sections, zero undersized interactive controls, and a 1264x821
  captured bitmap with 336 quantized color buckets. These are what one run
  measured, not what the harness requires; every assertion behind them is an
  inequality (overflow <= 1px, nav buttons >= 5, sections >= 4, colour buckets
  >= 10, dominant-colour ratio <= 98.5%).

  **Corrected 2026-09-02.** This entry originally read "passed against
  `cia-edge` version `claude-model-swap-20260827`, `ready=false`, six reported
  models". That was wrong and overstated the evidence. The harness never
  contacts cia-edge: it reads
  `frontend/src/api/__fixtures__/status.sample.json`, serves those bytes from
  an in-process `httptest` server, and points the console at it via
  `CIA_CONTROL_URL`. The version string and the model count came from the
  fixture. No part of this pass exercised a live provider.

The WebView2 E2E pass proves the native host serves the built frontend, completes
navigation, mounts React, round-trips status through the bridge, preserves the
full status payload shape needed by the frontend schema, contains navigation and
new-window attempts, composites a visible controller, opens no listening TCP
socket, renders the default layout without horizontal overflow, and captures a
nonblank/nonmonochrome bitmap. It is still not a pixel-perfect or
golden-screenshot visual-design certification.

## Honest gaps

- The console does not provide prompt composition, streaming inference, or a
  cancel button. Per ADR 0018, that is the Sprint 5 closure state for this
  credentialless console, not a half-built screen.
- This pass does not promote or install `cia-console` into
  `C:\IA\local-ai-v2`.

**Gaps added 2026-09-02**, found by the Sprint 9 documentation audit. They were
true on 2026-08-29 and this list should have named them:

- `frontend/` and `cmd/cia-console/` are untracked in git, as are ADR 0018 and
  this report. Nothing here is covered by review, branch protection, or the
  repository's secret scanning.
- The `frontend` CI job is itself uncommitted. Committing
  `.github/workflows/ci.yml` without the frontend would produce a job that
  fails on `npm ci` in a directory that does not exist.
- CI never runs the E2E harness. It is opt-in behind `CIA_CONSOLE_E2E` because
  it needs an interactive desktop session, so the defect class it exists to
  catch is one no CI job here will ever see.
- Nothing runs under `-race`. The race job is ubuntu-only and the harness is
  `//go:build windows`; separately, the module does not currently build for
  linux at all (`cmd/cia-tray/app.go` references a Windows-only symbol), so
  that job is red for an unrelated reason.
- `bridge_status_arrives_intact` compares the keys that arrived against what
  the frontend schema requires, but the fixture and the schema derive from the
  same captured response. It proves the transport does not narrow the payload;
  it cannot detect the real cia-edge drifting from the schema.
- The pinned toolchain path in the command block above,
  `C:\IA	oolchains\go1.26.5\goin\go.exe`, reports `go1.26.6`: `go.mod`
  declares `go 1.26.6` and `GOTOOLCHAIN=auto` silently substitutes it. The
  commands work; the version in the path is not the version that runs.
