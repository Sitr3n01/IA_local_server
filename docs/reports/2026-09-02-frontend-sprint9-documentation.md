# Sprint 9 — Documentation and Stabilization — 2026-09-02

Sprint 9's rule is that **no document may promise behaviour that does not exist
in code**. The method was to audit each documentation surface against the code
it describes, verify each finding by reading the code directly, and correct
what was wrong.

## What the audit found

A fan-out of twelve auditors was run over the documentation surfaces. It was
cut short twice by session limits: of 22 agents, 8 completed and most verifiers
died. **Four surfaces produced 90 raw findings and only 3 were independently
verified by another agent.** Every correction below was therefore checked by
hand against the code before it was made, and the findings that were not
checked have not been acted on.

That failure is worth recording for its own sake. The first run's
post-processing classified "no verdict returned" as *refuted* and reported "0
survivors of 52" — which reads as *the auditors found nothing real* when the
truth was *nothing was checked*. A dead verifier must never be able to look
like a clean bill of health. The script was corrected to report unverified
findings as unverified.

## Corrections made

**`docs/THREAT_MODEL.md` — P0.** The mitigation for stored/browser XSS was
still recorded as **"No web UI in v2"**. There has been a web UI since the
console shipped. ADR 0018 recorded this exact edit as a consequence of its own
decision — *"the row is not edited in place; the old control is gone and
pretending otherwise would hide the change"* — and the edit was never made. The
row is now gone, replaced by four rows naming the real risks and the controls
that actually bound them, plus a new trust boundary (page to host), the served
bundle as an asset, and three residual risks. Every control named was verified
in code first.

**`frontend/README.md` — P0.** It described `bridgeTransport` as *"a typed stub
today: `cia-console.exe` doesn't exist yet"*. That is the production privileged
path — every model-lifecycle and maintenance mutation reaches the
DACL-protected pipe through it — and it is a full correlated request/reply
client with a 35-second timeout. A reader told it is inert would not review it.
Also corrected there: the font stacks (Roboto Flex is bundled and shipped, not
absent), the icon inventory (eleven, not five), the Stylelint glob list, the
component-token claim, the TypeScript pin, the dependency table, the directory
layout, the third ESLint rule, and the Sprint 1 framing on a nine-sprint tree.

**`frontend/src/features/README.md`.** Rewritten. It claimed *"Nothing outside
this directory reaches into `capacity.reason`, `model_statuses`, or
`maintenance.state` directly"* — two pages read `maintenance.state` directly
today. It also still called maintenance control a future module, and omitted
the `activity/` module entirely. The rewrite separates what ESLint enforces
from what is convention, which the old text conflated.

**`docs/reports/2026-08-29-frontend-sprint-closure.md`.** Two corrections,
amended in place rather than rewritten. It claimed the E2E *"passed against
`cia-edge` version `claude-model-swap-20260827`, `ready=false`, six reported
models"*. The harness never contacts cia-edge — it replays
`status.sample.json` from an in-process `httptest` server. And it numbered the
visual-validation work "Sprint 9"; that work closed Sprint 8's audit gap. Six
gaps its "Honest gaps" section should have named were added.

**`NOTICE`.** Listed no npm dependency, though React, React DOM, TanStack
Query, Zod and Roboto Flex all ship inside the console bundle — and omitted
`go-webview2`, a direct Go dependency. Every licence was read from the package
itself rather than recalled.

**`frontend/src/design-system/tokens/foundation.css`.** A half-deleted sentence
left the typography comment contradicting itself mid-paragraph.

## New

**`docs/FRONTEND.md`** — the frontend reference the sprint asked for, covering
all twelve required topics: architecture and layering, the API boundary, the
security boundary, the token model, design-system rules, component maturity,
how to add a component, how to add a feature, testing, visual testing,
accessibility, and the ADR decisions that constrain the frontend. It states
throughout which properties are enforced and which are convention.

Three things it records that were not written down anywhere before: that
`style-src 'self'` silently drops inline `style` attributes, so proportional
geometry must be inline SVG numeric attributes; that contrast ratios live in
CSS comments with no test recomputing them; and that ADR 0018 control 4 is only
partly implemented.

## ADR 0018 controls, as built

| Control | State |
| --- | --- |
| 1 — native confirmation for every mutation | Implemented |
| 2 — host sanitizes its own environment | Implemented, fails closed at startup |
| 3 — runtime pinned to the Fixed Version distribution | **Not implemented** |
| 4 — no HTML injection path, proven by the build | Partly |
| 5 — npm tree covered by CI | Partly |

Control 3 is the significant one. The ADR rejects the Evergreen runtime as
*"this project's first unpinned, auto-updating third-party dependency"* — and
the console runs on Evergreen. There is no `BrowserExecutableFolder`, no fixed
-version package, and nothing in the release inventory. The console currently
ships exactly what its own ADR rejected. This is now recorded in
`THREAT_MODEL.md` as a residual risk instead of being implied to be solved.

Control 4's gap is narrower and partly unimplementable as written: the ADR says
the build asserts no `innerHTML` sink in the shipped bundle, but React's own
vendored code contains such sinks, so that grep would fail on every build. The
ban is enforced on our source instead, which is the meaningful half. The ADR
also says the CSP is *"emitted by the build, never hand-written into a template
where it can drift"* — it is hand-written in `index.html`; what the build does
is *assert* it, which achieves the intent by a different route.

## Validation

Run from `frontend/`, all uncached:

```bash
npm run typecheck && npm run lint && npm test && npm run build && npm run verify:prod-bundle
```

- `npm run typecheck`: clean.
- `npm run lint`: clean (ESLint + Stylelint).
- `npm test`: **27 files, 249 tests, all passed.**
- `npm run build`: succeeded; Rollup's known `INEFFECTIVE_DYNAMIC_IMPORT`
  notice for `bridgeTransport` appeared, as expected — the command modules
  import it statically on purpose.
- `npm run verify:prod-bundle`: OK; no dev transport, gallery marker, loopback
  address, external origin, or CSP drift.

Run from the repository root:

```bash
gofmt -l cmd/cia-console/ && go vet ./cmd/cia-console/ && go test ./cmd/cia-console/... -count=1
```

- `gofmt`: clean. `go vet`: clean. `go test -count=1`: ok.

Note on the toolchain: `C:\IA\toolchains\go1.26.5\go\bin\go.exe` reports
`go1.26.6`. `go.mod` declares `go 1.26.6` and `GOTOOLCHAIN=auto` substitutes
it. The commands work; the version in the path is not the version that runs.

The native WebView2 E2E was not re-run in this pass. Nothing in it changed, and
this pass changed documentation, one CSS comment, and no console behaviour.

## Stabilization: what is still unstable

This is the half of Sprint 9 that is **not** closed, and it is not closeable by
writing.

1. **`frontend/` and `cmd/cia-console/` are untracked in git**, along with ADR
   0018, ADR 0017, `docs/CLAUDE_DESKTOP.md`, `internal/claudedesktop/`,
   `internal/modeloverlay/` and both sprint reports. Nine sprints of work exist
   only in one working tree on one workstation, covered by no review, no branch
   protection, and no secret scanning.

2. **The `frontend` CI job is itself uncommitted.** `.github/workflows/ci.yml`
   is modified in the working tree; `HEAD` has no frontend job. Committing the
   workflow without the frontend produces a job that fails on `npm ci` in a
   directory that does not exist. The two must land in the same commit.

3. **An 8.3 MB Linux ELF binary sits at the repository root** as `cia-console`,
   left by a `go build`. Two independent holes let it through: `.gitignore`
   covers `*.exe` but not extensionless output, and CI's "Reject tracked
   binary/model artifacts" gate matches by extension
   (`gguf|safetensors|pt|pth|exe|dll|zip|7z|tar|gz`) and would not see it
   either. `git add -A` would commit it today.

4. **The module does not build for linux.** `cmd/cia-tray/app.go:158`
   references `claudedesktop.NewGatewayVerifier`, which is defined only in a
   `//go:build windows` file, so `go test -race ./...` fails at the build stage
   and the race job is red. This belongs to the in-flight ADR 0017 work, not to
   this program — but while it is red, the console's navigation-policy tests,
   which were deliberately written portable so they would get race coverage,
   are not exercised there.

5. **No human has looked at the console.** No pixel has been inspected. The
   bitmap assertion distinguishes "something rendered" from "blank" and nothing
   finer.

Items 1 and 2 are a single commit, and it is the user's call to make. Items 3
and 4 are one-line fixes in files another author currently holds uncommitted,
so they were not touched.

## Deliberately not done

`docs/ARCHITECTURE.md` still has no `cia-console` entry, and its "Operator
panel" section still describes only `cia-tray`. ADR 0018 already recorded that
amendment as deferred, for a reason that still holds: the file carries another
author's uncommitted ADR 0017 work, and two authors editing one file at once is
how a contradiction gets committed. `docs/RUNBOOK.md` and
`docs/MODEL_PROMOTION.md` are modified in the working tree for the same reason
and were left alone. The console has no operator procedure in the RUNBOOK; that
is owed once ADR 0017's work lands.

Of the 90 audit findings, 76 were never independently verified. They are not
acted on here. The four surfaces that produced them — the threat model, the
frontend README's first half, the features README, and the closure report — are
the four this pass corrected, so the material findings were reached by hand;
what remains unswept is `ARCHITECTURE.md`, the root READMEs, `SECURITY.md`,
`CONTRIBUTING.md`, `CHANGELOG.md`, the RUNBOOK, ADR 0018's own text, the
console README, and the frontend README's second half.
