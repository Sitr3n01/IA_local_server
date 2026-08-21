# Qwen3.8 profile consolidation — delivery report

Date: 2026-08-21, America/Sao_Paulo.
Decision record: `docs/adr/0012-three-qwen38-profile-classes-and-the-output-contract.md`.
Evidence: `benchmarks/REPORT-qwen38-27b-q3-q2-kvq4-20260821.md` and the two
reports preceding it. No benchmark report, raw artifact, model-hash record or
incident report was modified by this work.

## 1. Profiles before and after

**Before** — five entries differing almost entirely by context window, which is
the axis that does not say what a profile is *for*:

| id | Weights | KV | ctx | State |
|---|---|---|---:|---|
| `qwen38-27b-ws-32k` | UD-IQ4_XS | `q8_0` | 32768 | candidate (default) |
| `qwen38-27b-ws-64k` | UD-IQ4_XS | `q8_0` | 65536 | candidate |
| `qwen38-27b-ws-128k` | UD-IQ4_XS | `q8_0` | 131072 | candidate |
| `qwen38-27b-ws-8k-prefill` | UD-IQ4_XS | `q8_0` | 8192 | candidate |
| `qwen38-27b-ws-32k-kv-q4` | UD-IQ4_XS | `q4_0` | 32768 | candidate (experimental) |

**After** — three classes, chosen by purpose:

| id | Display name | Weights | KV | ctx | out | `n_predict` | `reasoning_budget` | compact before |
|---|---|---|---|---:|---:|---:|---:|---:|
| `qwen38-27b-deep-32k` | Qwen3.8 Deep 32k | UD-IQ4_XS | `q8_0`/`q8_0` | 32768 | 8192 | 8192 | *(unset)* | 23552 |
| `qwen38-27b-agent-128k` | Qwen3.8 Agent 128k | UD-Q3_K_XL | `q4_0`/`q4_0` | 131072 | 8192 | 8192 | *(unset)* | 114688 |
| `qwen38-27b-huge-256k` | Qwen3.8 Huge 256k | UD-Q2_K_XL | `q4_0`/`q4_0` | 262144 | 32768 | 32768 | 24576 | 221184 |

All three: `-ub 288`, `-b 2048`, `-t 8`, `--parallel 1`, `-fa on`, `-ngl 99`,
`--no-context-shift`, 4-block CPU split `blk\.(6[0-3])\.ffn_.*=CPU`.

Operational surface for Qwen3.8 went from five profiles to three; the canary
deployment now exposes nine models in total.

## 2. Legacy profiles retired

`state: retired`, `deployments: []`. They stay in the manifest because the
benchmark reports reference them by id; they no longer reach any catalog.

| Previous profile | Action | Replacement | Why |
|---|---|---|---|
| `qwen38-27b-ws-32k` | retired → renamed | `qwen38-27b-deep-32k` | Byte-identical configuration, purposeful name |
| `qwen38-27b-ws-64k` | retired | `qwen38-27b-agent-128k` | No distinct role once Deep and Agent exist |
| `qwen38-27b-ws-128k` | retired | `qwen38-27b-agent-128k` | Agent beats it on every measured axis for this job |
| `qwen38-27b-ws-8k-prefill` | retired | — | Its only unique property was being the sole `ok`-pressure profile; Agent at 128k and Huge through 128k now reach `ok` |
| `qwen38-27b-ws-32k-kv-q4` | retired | `qwen38-27b-agent-128k` | The IQ4-vs-Q3 comparison at `q4_0` it existed to enable is complete |

No GGUF was deleted. `UD-IQ4_XS`, `UD-Q3_K_XL` and `UD-Q2_K_XL` are all in use;
`UD-Q4_K_M` remains on disk untouched and is not referenced by any profile.

## 3. Context and output policy

`compact_threshold_tokens = context_tokens − max_output_tokens − safety margin`,
declared per profile rather than derived from a global percentage.

| Profile | Context | Output reserve | Safety | Compact before | Codex percent |
|---|---:|---:|---:|---:|---:|
| Deep | 32768 | 8192 | 1024 | 23552 | 71 |
| Agent | 131072 | 8192 | 8192 | 114688 | 87 |
| Huge | 262144 | 32768 | 8192 | 221184 | 84 |

Models that declare no threshold keep the previous flat 85%, so no other model's
limits moved.

## 4. Q2 reasoning policy

The Huge profile splits its budget with flags verified present in
`llama-server --help` on b10549:

```
--n-predict 32768
--reasoning-budget 24576
--reasoning-budget-message "Thinking budget reached. Stop analysing and write the final answer now."
```

24576 thinking + roughly 8192 answer = 32768 total. Deep and Agent keep an 8192
ceiling with reasoning unrestricted — the configuration each was measured under.

Testing other budgets requires no code change: edit `max_output_tokens`,
`n_predict` and `reasoning_budget` in `config/models.yaml` and regenerate. 8192,
16384, 24576 and 32768 are all expressible declaratively.

Long reasoning on Huge is not a failure. Reaching 32768 without a useful answer
is an operational result to record, never silently raised to 64k and never
retried indefinitely.

## 5. Codex fix

Both `integrations/codex/cia-local.config.toml` and `cia-local-canary.config.toml`
had, as profile-global keys:

```
model_context_window = 131072
model_auto_compact_token_limit = 110000
```

They governed whichever model the profile selected: a 32k profile inherited a
128k window and a 110k compaction threshold; a 256k profile was silently capped
at 128k. **Both keys are deleted**, with a comment in their place explaining why.
`codex-model-catalog.json` — generated from the manifest — is now the sole source
of the context contract, carrying `context_window`, `max_context_window` and a
per-model `effective_context_window_percent`.

**Codex's actual precedence could not be demonstrated on this machine.** The
Codex CLI is not installed here; Codex is the desktop application, so there is no
binary to dump a resolved configuration from. The keys were therefore removed
rather than reordered or relied upon: removing them cannot produce a
disagreement with the catalog, leaving them could. This is labelled `INFERRED`
in the benchmark report and is not presented as a demonstrated behaviour.

## 6. OpenCode changes

`opencode.local-provider.jsonc` and `opencode.canary-provider.jsonc` already
declared `limit.context` and `limit.output` per model, which is the correct
approach and was preserved. They were regenerated so the three new profiles
appear with the right limits:

| Model | context | output |
|---|---:|---:|
| `qwen38-27b-deep-32k` | 32768 | 8192 |
| `qwen38-27b-agent-128k` | 131072 | 8192 |
| `qwen38-27b-huge-256k` | 262144 | 32768 |

No existing model's limits were reduced.

## 7. Claude Code

No explicit Claude Code integration exists in this repository, and none was
created — building a proxy or conversation framework was out of scope. The
contract is ready for one: the three profiles are selectable by model id through
the same OpenAI-compatible data plane, and `/api/v1/status` now reports each
profile's weights, cache types, output ceiling, reasoning budget and compaction
threshold, so a future integration can choose between deep/agent/huge without
any change to the core.

## 8. Files changed

**Manifest and schema**

| File | Change |
|---|---|
| `config/models.schema.json` | Four optional fields: `n_predict`, `reasoning_budget`, `reasoning_budget_message`, `compact_threshold_tokens`. `additionalProperties: false` unchanged. |
| `config/models.yaml` | Three profiles added with measured serving resources; five retired. |

**Generation and edge**

| File | Change |
|---|---|
| `scripts/v2/Common.ps1` | Emits the three new flags between `--context-shift` and `--jinja`, per ADR 0009 §3. |
| `scripts/v2/New-V2ClientCatalogs.ps1` | `Get-V2EffectiveContextPercent` derives Codex's compaction percentage from `compact_threshold_tokens`; falls back to 85. |
| `internal/edge/manifest.go` | Parses the new fields; `weightsName` reduces a GGUF path to a filename. |
| `internal/edge/config.go` | `ProfileSummary` type; `DefaultHeaderTimeout` 1800s → 3600s with the derivation recorded. |
| `internal/edge/server.go` | `profile` block on `/api/v1/status`. |
| `internal/edge/manifest_test.go` | Canary allowlist updated; `TestLoadModelsReportsProfileSummary` added. |

**Integrations**

| File | Change |
|---|---|
| `integrations/codex/cia-local.config.toml` | Two global context keys removed. |
| `integrations/codex/cia-local-canary.config.toml` | Two global context keys removed. |
| `integrations/codex/codex-model-catalog.json` | Regenerated. |
| `integrations/opencode/*.jsonc` | Regenerated. |

**Documentation**

| File | Change |
|---|---|
| `docs/adr/0012-…md` | New decision record. |
| `docs/RUNBOOK.md` | §13 rewritten as "Choosing a Qwen3.8 profile". |
| `docs/TUNING.md` | §1.8 added. |
| `README.md` | "Perfis Qwen3.8" section. |
| `model-test-matrix.json` | `qwen38-27b-q3-placement-sweep` recorded as planned. |

**New tooling**

`scripts/v2/Test-V2ProfileContracts.ps1` (output/reasoning contract),
`scripts/v2/Invoke-V2ProfileQualification.ps1`,
`scripts/v2/Invoke-V2ThroughputSweep.ps1`,
`scripts/v2/Invoke-V2KvContextMatrix.ps1`, and `scripts/v2/eval/` (the graded
coding suite, the long-context fixture generator, the GGUF range probe and the
campaign summarizer).

## 9. Tests executed

| Suite | Result |
|---|---|
| `gofmt -l .` | clean |
| `go vet ./...` | clean |
| `go test ./...` | all packages pass |
| PowerShell parse, `scripts/v2` + `integrations` | 0 errors |
| `Test-V2Manifest.ps1` | valid — 3 runtimes, 14 models, 9 canary, 26 semantic policy tests |
| `Test-V2ConfigGeneration.ps1` | valid — 9 generation tests, **6 byte-stable models** (ADR 0009 §3 invariant intact) |
| `Test-V2HarnessConfig.ps1` | ok |
| `Test-V2Telemetry.ps1` | 17 checks |
| `Test-V2AgenticHarness.ps1` | pass |
| `scripts/v2/eval/test_verifiers.py` | 20/20 graders discriminate |

## 10. Smoke and contract results

`Test-V2WorkstationSmoke.ps1`, each at its own declared context, driven through
the same command builder the deployment uses:

| Profile | Verdict | Checks | Load | Marginal VRAM | WS | Private | Shared | Pressure |
|---|---|---:|---:|---:|---:|---:|---:|---|
| Deep 32k | `smoke_pass` | 13/13 | 12.7s | 11.77 GiB | 14.31 | 14.32 | 2229 | pressured |
| Agent 128k | `smoke_pass` | 13/13 | 12.6s | 11.39 GiB | 13.27 | 13.38 | 3545 | **ok** |
| Huge 256k | `smoke_pass` | 13/13 | 10.6s | 12.01 GiB | 11.66 | 15.30 | 3204 | pressured |

Each covers startup, model load, health, model list, non-streaming chat,
native timings, streaming, tool call with a valid function name and parseable
JSON arguments, shutdown, no surviving process and port release.

`Test-V2ProfileContracts.ps1` — served window, output ceiling, oversize-prompt
refusal, cancellation, slot release, orphan check:

| Profile | Verdict | Notes |
|---|---|---|
| Deep 32k | `contract_pass` **8/8** | 12288-token request clamped to 572; oversize prompt refused |
| Agent 128k | `contract_pass` **8/8** | 12288-token request clamped to 1223; oversize prompt refused |
| Huge 256k | `contract_pass` **9/9** | Served window 262144; 36864-token request bounded by the 32768 ceiling; **one generation produced 17310 tokens with `finish_reason: stop`** |

The Huge row is the one that could not be established by reading a config file.
The profile exists because Q2_K_XL was measured returning nothing on three of ten
coding tasks, having spent an entire 8192-token allowance inside
`reasoning_content`. A single generation of **17310 tokens, ending on `stop`
rather than on `length`**, is direct evidence that the old ceiling is gone and
that the 24576-token reasoning budget leaves the model room to finish rather than
cutting it off. 32768 was not consumed, which is the point: the budget is a
ceiling to work under, not a target to reach.

The manifest's `resources` blocks were written from these serving runs, not from
load-time footprints — the difference is 3–5 GiB and admission control gates on
the larger figure.

## 11. Remaining qualification gaps

Recorded rather than papered over. None of these was executed in this work, and
the consolidation deliberately did not run the long 128k/256k campaign.

**Blocking full qualification of Agent and Huge**

- **Long-context retention** at 32k/64k/128k/192k/240k occupancy for both
  profiles. The fixture generator, grader and runner exist and are validated
  offline; `benchmarks/campaign-256k/resume-campaign.ps1` runs the ramp in about
  5 hours unattended.
- **Decode with the context filled** beyond 32k. Q3_K_XL at 32k occupancy is
  measured at 18.33 t/s against 23.12 empty; 128k and 256k are unmeasured, and
  they decide whether either profile is FAST, BALANCED or SLOW under the
  thresholds in `docs/BENCHMARKS.md`.
- **Agentic endurance** — a 20–50 turn tool loop — and the 72-hour soak.

**Known-open, non-blocking**

- **Q3 placement sweep.** The 4-block split was inherited from the IQ4_XS
  characterization and never re-derived for a 12.24 GiB weight set. Recorded as
  `qwen38-27b-q3-placement-sweep` in `model-test-matrix.json`.
- **Per-profile edge timeouts.** `DefaultHeaderTimeout` is a single global knob,
  now 3600s. A genuinely hung upstream takes an hour to surface on a
  non-streaming request. Per-model timeouts are the correct fix.
- **Codex precedence** is `INFERRED`, not demonstrated — no CLI on this machine.
- **KV `K q8_0 / V q4_0` quality** is unmeasured; only its memory cost is known,
  and it is worse than `q4_0/q4_0` at 256k on this adapter.
- **Speculative decoding.** Every shipped GGUF passes the `blk.64` gate at
  Q6_K/Q8_0, so the `spec_draft_n_max` sweep is now meaningful. It has not run.

**Promotion state.** Deep inherits the IQ4_XS/`q8_0` qualification whose
configuration it reproduces exactly. Agent is the recommended daily profile with
long-context retention pending. Huge is an experimental huge-context profile
whose 256k memory envelope is measured and whose retention and filled-context
decode are not. All three are `candidate` in `canary`; none is registered as
qualified in an aspect that has not been tested, and `provider.public_model`
still resolves to `local-coding`.
