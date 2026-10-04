# Documentation

Long-form documentation is written in English. Start with the
[project README](../README.md) for the overview and the results.

## Guides

| Document | Read it for |
|---|---|
| [Architecture](ARCHITECTURE.md) | Component contracts, state machine, failure behavior, architectural invariants |
| [Threat model](THREAT_MODEL.md) | Assets, trust boundaries, the threat/control/verification matrix, residual risks |
| [Runbook](RUNBOOK.md) | Installing, deploying, verifying and rolling back, step by step |
| [Model promotion](MODEL_PROMOTION.md) | What a model must prove to leave `candidate`, and how the gate enforces it |
| [Benchmarks](BENCHMARKS.md) | Measurement methodology and the evidence format |
| [Tuning](TUNING.md) | Bottleneck diagnosis, the memory-bandwidth ceiling, profile selection |
| [Monitor](MONITOR.md) | What the browser monitor shows, where each number comes from, and its controls |
| [Claude Desktop](CLAUDE_DESKTOP.md) | Claude Desktop as a third-party-inference client, and Claude Local beside the signed-in instance |

## Decisions

The [architecture decision records](adr/README.md) explain why the system is
built the way it is. The index there lists every record with its status.

## Reports

Dated records of validations, campaigns and reviews. Corrections are added as
errata or addenda rather than by rewriting what was measured.

| Report | Subject |
|---|---|
| [2026-07-20 canary validation](reports/2026-07-20-canary-validation.md) | First end-to-end validation of the v2 canary |
| [2026-07-21 canary completion](reports/2026-07-21-v2-canary-completion.md) | v2 canary completion |
| [2026-07-27 preservation checkpoint](reports/2026-07-27-local-ai-v2-preservation.md) | Source-state checkpoint of the v2 canary |
| [Qwen3.8 profile consolidation](reports/CONSOLIDATION-qwen38-profiles-20260821.md) | Consolidating the Qwen3.8 profiles (2026-08-21) |
| [MoE and Gemma readiness hardening](reports/HARDENING-moe-and-gemma-readiness-20260821.md) | Hardening before the MoE and Gemma campaigns (2026-08-21) |
| [Gemma 4 26B physical qualification](reports/GEMMA4-26B-PHYSICAL-QUALIFICATION-20260822.md) | Physical qualification of the Gemma 4 26B candidate (2026-08-22) |
| [Qualification campaign](reports/QUALIFICATION-CAMPAIGN-20260823.md) | Full quality and retention battery across the profiles (2026-08-23) |
| [Post-qualification hardening](reports/HARDENING-post-qualification-20260823.md) | Fixes that came out of the campaign (2026-08-23) |
| [Final roster](reports/FINAL-ROSTER-20260825.md) | The MoE head-to-head and the roster it produced (2026-08-25) |
| [2026-08-29 frontend sprint closure](reports/2026-08-29-frontend-sprint-closure.md) | Closure of the WebView console sprint (console since removed) |
| [2026-09-02 sprint 9 documentation](reports/2026-09-02-frontend-sprint9-documentation.md) | Documentation and stabilization of the console (since removed) |
| [Gemma 4 agentic qualification](reports/GEMMA4-AGENTIC-QUALIFICATION-20260929.md) | Gemma 4 12B agentic variant at 256k (2026-09-29) |
| [2026-10-01 technical audit fixes](reports/2026-10-01-senior-audit-fixes.md) | Defects found by the audit and how they were fixed |
| [2026-10-01 deployment readiness](reports/2026-10-01-deploy-readiness.md) | Readiness review: evidence, contracts and open gates |
| [2026-10-02 Claude instance choice](reports/2026-10-02-claude-instance-choice.md) | Native Claude and the local gateway side by side |

Raw evidence behind the reports lives in [`benchmarks/`](../benchmarks/). Its
inputs are the synthetic fixtures of the evaluation harness in
[`scripts/v2/eval`](../scripts/v2/eval/); no real user prompt is committed.

## Incidents

[2026-07-20 panel zstd credential exposure](../incident-reports/2026-07-20-panel-zstd-credential-exposure.md)
— the v1 failure that shaped the logging invariant, with its sanitized timeline.
