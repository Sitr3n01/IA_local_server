# Gemma 4 26B physical qualification - delivery report

Date: 2026-08-22, America/Sao_Paulo.
Evidence: `benchmarks/REPORT-gemma4-26b-a4b-gfx1201-20260821.md`.
Raw artifacts: `benchmarks/campaign-gemma4-26b/`.

This report summarizes the completed Gemma 4 26B A4B physical campaign on the
gfx1201 workstation. It is a delivery report, not a promotion report. No Gemma
26B manifest entries were created, no Codex/OpenCode catalogs were regenerated,
and this campaign did not alter `provider.public_model`.

## 1. Scope completed

The campaign started from the already downloaded and hash-verified Gemma 4 26B
A4B artifacts:

| Role | Weights | Context target | KV | MoE candidates |
|---|---|---:|---|---|
| Deep | `gemma-4-26B-A4B-it-qat-UD-Q4_K_XL.gguf` | 32768 | `q8_0/q8_0` tested this round | `--n-cpu-moe 8` |
| Agent | `gemma-4-26B-A4B-it-UD-Q3_K_XL.gguf` | 131072 | `q4_0/q4_0` | `--n-cpu-moe 6` |
| Huge | `gemma-4-26B-A4B-it-UD-Q2_K_XL.gguf` | 262144 | `q4_0/q4_0` | `--n-cpu-moe 0`, `4`, `6` |

The following work was completed:

- Corrected the Huge short-throughput nomenclature so the report no longer
  implies `tg128` was measured with 256k resident tokens.
- Measured Deep QAT Q4 with `KV=q8_0/q8_0`.
- Added chat-template smoke coverage to the qualification harness.
- Expanded tool-calling tests to include similar function names and nested
  arguments.
- Ran chat, tool, structured JSON and coding quality gates for the three Gemma
  profile classes.
- Measured Agent 128k load and `tg128` with about 115k tokens resident.
- Measured Huge 256k filled-context decode at about 230k tokens resident for
  `--n-cpu-moe 0`, `4` and `6`.
- Compared Gemma against the existing Qwen reference results without rerunning
  Qwen.

Explicitly not done:

- No Q5_K_M download.
- No full rediscovery.
- No full placement sweep rerun.
- No Gemma 26B canary manifest entries.
- No `qualified`, `enabled` or `final` Gemma status.
- No `provider.public_model` change.

## 2. Harness and script changes

| File | Change |
|---|---|
| `scripts/v2/Invoke-V2ThroughputSweep.ps1` | Added `-ContextTokens`; records `context_tokens`; now uses the shared `New-V2LlamaBenchArguments` path instead of ad hoc bench flags. |
| `scripts/v2/eval/coding_tasks.py` | Added `read_files`, `read_file_range`, `search_file`, `search_files` and nested-argument tool tasks. |
| `scripts/v2/eval/qualify.py` | Added `chat` suite covering system/user, multi-turn, streaming and tool-result continuation; added nested argument comparison. |
| `benchmarks/REPORT-gemma4-26b-a4b-gfx1201-20260821.md` | Consolidated all Gemma results, decisions, raw artifact paths and validation status. |

These changes support measurement and reporting. They do not publish a Gemma
26B model profile.

## 3. Deep results

Deep candidate:

| Field | Value |
|---|---|
| Weights | QAT UD Q4_K_XL |
| KV | `q8_0/q8_0` |
| Context | 32768 |
| MoE | `--n-cpu-moe 8` |

Load and short throughput:

| Metric | Result |
|---|---:|
| Load time | 10.6s |
| Dedicated VRAM at load | 14971 MiB |
| Shared VRAM at load | 467 MiB |
| Process WS at load | 10.88 GiB |
| Commit at load | 41.59 GiB |
| Pressure | `ok` |
| `pp32768` | 837.52 t/s |
| short `tg128` | 59.11 t/s |

Quality:

| Suite | Result |
|---|---|
| Chat smoke | 4/4 |
| Coding | 3/10 |
| Tools | 6/7 |
| Structured JSON | pass |

Decision: Deep QAT Q4 with KV Q8 is memory/performance viable, but not usable
as a canary because the quality gate failed. It did not beat Q3 on quality.

Status: **NOT READY**.

## 4. Agent results

Agent candidate:

| Field | Value |
|---|---|
| Weights | UD Q3_K_XL |
| KV | `q4_0/q4_0` |
| Context | 131072 |
| MoE | `--n-cpu-moe 6` |

128k load:

| Metric | Result |
|---|---:|
| Load time | 6.6s |
| Dedicated VRAM | 14903 MiB |
| Shared VRAM | 1261 MiB |
| Process WS | 10.81 GiB |
| Commit | 42.22 GiB |
| Pressure | `ok` |

Filled-context decode:

| Depth | `tg128` | Wall time | Peak dedicated | Peak shared | Process WS |
|---:|---:|---:|---:|---:|---:|
| 115000 | 34.39 t/s | 201.4s | 15732 MiB | 550 MiB | 13.03 GiB |

Quality:

| Suite | Result |
|---|---|
| Chat smoke | 4/4 |
| Coding | 4/10 |
| Tools | 6/7 |
| Structured JSON | pass |

Decision: Agent Q3 remains the best measured Gemma Agent performance candidate,
but it is not canary-ready. It loses 35.5% decode from short context to about
115k occupancy, which is still operationally good; quality and tool exactness
are the blockers.

Status: **NOT READY**.

## 5. Huge results

Huge candidate:

| Field | Value |
|---|---|
| Weights | UD Q2_K_XL |
| KV | `q4_0/q4_0` |
| Context | 262144 |
| Filled-depth target | about 230k |

Filled-context placement result:

| CPU MoE | `tg128 @230k` | Wall time | Peak dedicated | Peak shared | Verdict |
|---:|---:|---:|---:|---:|---|
| 0 | 2.77 t/s | 1108.1s | 15525 MiB | 1674 MiB | decode collapse |
| 4 | 30.32 t/s | 535.0s | 15442 MiB | 606 MiB | winner |
| 6 | 26.28 t/s | 568.4s | 15135 MiB | 606 MiB | stable but slower |

Quality on the winner, `--n-cpu-moe 4`:

| Suite | Result |
|---|---|
| Chat smoke | 4/4 |
| Coding | 3/10 |
| Tools | 6/7 |
| Structured JSON | pass |

Decision: Huge should use `--n-cpu-moe 4` if Gemma Q2 is revisited. The
short-context winner, `--n-cpu-moe 0`, does not survive context occupancy. Q2 is
not agentically acceptable in this run because quality fails and multiple
generations hit length/no-answer.

Status: **NOT READY**.

## 6. Quality findings

Gemma passed chat smoke and pure JSON, but failed basic coding quality and exact
tool reliability.

| Role | Coding | Tools | JSON | Main blockers |
|---|---:|---:|---:|---|
| Deep | 3/10 | 6/7 | pass | no-answer, Go/C#/Unity failures, constraint violation, invented API |
| Agent | 4/10 | 6/7 | pass | Go/C#/Unity failures, constraint violation, invented API |
| Huge | 3/10 | 6/7 | pass | no-answer, Go/C#/Unity failures, constraint violation, invented API |

The common tool failure was exact nested argument adherence on `search_files`.
All three selected the correct function, but changed the requested glob or
exclude pattern. This is a real canary blocker for strict tool contracts.

Unknown/no-answer was not counted as hallucination. Constraint violation and
invented API failures are dangerous errors.

## 7. Reasoning behavior

No explicit `reasoning_budget` was used. The run found a Gemma reasoning/output
control problem:

| Role | Length/no-answer examples | Largest observed reasoning chars |
|---|---|---:|
| Deep | `py_impl` | 20488 |
| Agent | long failed `cs_bugfix` | 26792 |
| Huge | `py_impl`, `go_bugfix`, `cs_bugfix` | 28550 |

Do not copy Qwen Huge's `reasoning_budget=24576` to Gemma. Gemma needs its own
budget experiment if this family is revisited.

## 8. Qwen comparison

Qwen was not rerun. The comparison uses existing project evidence.

| Role | Quality winner | Performance winner | Recommendation |
|---|---|---|---|
| Deep | Qwen | Gemma short `tg128` | keep Qwen Deep |
| Agent | Qwen | Gemma filled `tg128 @115k` | keep Qwen Agent for daily use |
| Huge | Qwen | Gemma filled `tg128 @230k` | keep Qwen Huge; Gemma Huge remains research-only |

Gemma is impressive on filled-context throughput once placement is chosen, but
Qwen remains the practical family for Codex, Claude Code, OpenCode and Unity
work because the quality/tool gates are much stronger.

## 9. Canary decision

Gate outcome:

| Gate | Deep | Agent | Huge |
|---|---|---|---|
| Local artifact and hash | pass | pass | pass |
| Target context load | pass | pass | pass |
| Filled-context performance | not required | pass | pass |
| Chat template smoke | pass | pass | pass |
| Structured JSON | pass | pass | pass |
| Tool calling | fail | fail | fail |
| Basic quality | fail | fail | fail |
| Dangerous failures absent | fail | fail | fail |
| Manifest entry allowed | no | no | no |

No Gemma 26B profile qualifies for `candidate/canary`. None is `qualified`.

Final state:

| Profile | State |
|---|---|
| `gemma4-26b-deep-32k` | NOT READY |
| `gemma4-26b-agent-128k` | NOT READY |
| `gemma4-26b-huge-256k` | NOT READY |

## 10. Validation after report update

| Gate | Result |
|---|---|
| `python -m py_compile scripts/v2/eval/qualify.py scripts/v2/eval/coding_tasks.py` | PASS |
| `python scripts/v2/eval/test_verifiers.py` | PASS |
| PowerShell parser check for touched/requested V2 scripts | PASS |
| `go fmt ./...` | PASS |
| `gofmt -l .` via toolchain path | PASS, no output |
| `go vet ./...` | PASS |
| `go test ./...` | FAIL |
| `Test-V2ConfigGeneration.ps1` | PASS |
| `Test-V2Telemetry.ps1` | PASS |
| `Test-V2AgenticHarness.ps1` | PASS |
| `Test-V2McpInferenceIntegrations.ps1` | PASS |
| `Test-V2ProfileContracts.ps1 -ModelId qwen38-27b-deep-32k` | PASS, 8/8 |
| `Test-V2ProfileContracts.ps1 -ModelId qwen38-27b-agent-128k` | PASS, 8/8 |
| `Test-V2ProfileContracts.ps1 -ModelId qwen38-27b-huge-256k` | PASS, 8/8 |
| `Test-V2Manifest.ps1` | FAIL |
| `Test-V2HarnessConfig.ps1` | FAIL |

The failing gates are tied to the current dirty manifest/catalog state:

- `go test ./...` reports `provider.public_model =
  "gemma4-12b-qat-ud-q4xl"` and a canary allowlist count mismatch.
- `Test-V2Manifest.ps1` reports that semantic policy accepted a manifest
  expected to fail with `retired runtime`.
- `Test-V2HarnessConfig.ps1` reports that the Codex model catalog count does
  not match the canary manifest.

No corrective edit was made here because this Gemma 26B campaign explicitly did
not change `provider.public_model`.

## 11. Raw artifacts

| Artifact group | Path |
|---|---|
| Consolidated benchmark report | `benchmarks/REPORT-gemma4-26b-a4b-gfx1201-20260821.md` |
| First campaign summaries | `benchmarks/campaign-gemma4-26b/summary-*.json` |
| Deep q8/q8 load and throughput | `benchmarks/campaign-gemma4-26b/deep-q8/` |
| Quality profiles and raw outputs | `benchmarks/campaign-gemma4-26b/quality/` |
| Agent 128k filled-context | `benchmarks/campaign-gemma4-26b/filled-agent-128k/` |
| Huge 256k filled-context | `benchmarks/campaign-gemma4-26b/filled-huge-256k/` |

## 12. Recommendation

Do not run Gemma 4 26B behind Codex, Claude Code or OpenCode yet. The best
practical configuration discovered is:

- Deep memory/perf: QAT Q4, KV q8/q8, `--n-cpu-moe 8`.
- Agent performance: Q3, KV q4/q4, `--n-cpu-moe 6`.
- Huge filled-context performance: Q2, KV q4/q4, `--n-cpu-moe 4`.

Those are research results, not deployment profiles. The next Gemma work should
be a reasoning/output-control experiment and a strict tool/quality rerun, not a
new throughput sweep.
