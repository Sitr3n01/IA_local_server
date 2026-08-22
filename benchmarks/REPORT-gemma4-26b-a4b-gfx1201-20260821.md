# Gemma 4 26B A4B on gfx1201 - MoE placement and first physical qualification

Date: 2026-08-21, America/Sao_Paulo.
Evidence labels follow `docs/BENCHMARKS.md`. Everything is **measured** on this
machine unless marked otherwise.

This is the single Gemma 4 26B report for the current campaign. It records
runtime readiness, GGUF discovery, local download verification, MoE placement
sweeps, 32k Deep/Agent throughput, the first 256k Huge pass, and the second
qualification pass on 2026-08-22, plus the quality-rescue pass that isolated
Gemma's reasoning/output behavior.

This is not a promotion report. No Gemma 26B model was added to
`config/models.yaml`. The repository inventory cleanup in the same worktree now
uses `provider.public_model=gemma4-12b-qat-ud-q4xl`; that is not a Gemma 26B
promotion and no Gemma 26B catalog entry was created.

## Scope

The campaign was narrowed to the first practical Gemma 4 26B A4B pass:

- Download and hash-verify only the selected QAT Q4, Q3, and Q2 artifacts.
- Use Gemma's generic MoE placement via `--n-cpu-moe N`, not Qwen tensor
  overrides.
- Hold the runtime constants stable: `-ngl 99`, `-fa on`, `-b 2048`,
  `-ub 288`, `-t 8`, `--parallel 1`, and no tensor override.
- Run Deep/Agent-style 32k load and throughput cells.
- Run Huge-style 256k load cells and short throughput cells.

Dropped scope is listed under "Not measured" rather than silently omitted.

## 1. Bill of materials

| Item | Value |
|---|---|
| GPU | AMD Radeon RX 9070 XT, gfx1201, 16304 MiB |
| CPU | AMD Ryzen 7 7700X, 8C/16T |
| RAM | ~31 GiB physical RAM |
| OS | Windows, pt-BR host |
| Runtime | llama.cpp `b10549`, ROCm 7.14 bundle |
| Runtime path | `C:\IA\runtimes\llama.cpp\b10549-rocm-7.14` |
| Device isolation | `HIP_VISIBLE_DEVICES=1`, `--device ROCm0`, `--split-mode none` |
| Source checkout | `C:\IA\IA_local_server` |

Held constant across every benchmark cell unless the cell explicitly sweeps it:
`-ngl 99`, `-fa on`, `-b 2048`, `-ub 288`, `-t 8`, `--parallel 1`,
`--cache-type-k q4_0`, `--cache-type-v q4_0`, and no tensor override.

## 2. GGUFs evaluated

| Role | Repository | Revision | File | Bytes | SHA-256 | Downloaded |
|---|---|---|---|---:|---|---|
| Deep/QAT candidate | `unsloth/gemma-4-26B-A4B-it-qat-GGUF` | `7b92b5b28818151e8669af2e45e88d6086f490dd` | `gemma-4-26B-A4B-it-qat-UD-Q4_K_XL.gguf` | 14249047104 | `a7c5bc715f5ff8e99a3e8901ce7d2b42b402c669bf24f7c5250747633d0f5891` | yes |
| Agent candidate | `unsloth/gemma-4-26B-A4B-it-GGUF` | `c099eb48e663fd284577b04978a94ffccb261841` | `gemma-4-26B-A4B-it-UD-Q3_K_XL.gguf` | 12907280096 | `90a918830420e6a36e01c0d219e563f1d0ca7f223dffd90ad4e02ef4f3253fde` | yes |
| Huge candidate | `unsloth/gemma-4-26B-A4B-it-GGUF` | `c099eb48e663fd284577b04978a94ffccb261841` | `gemma-4-26B-A4B-it-UD-Q2_K_XL.gguf` | 10546934240 | `2a1d26dfe6ea00a467940a5728316af6edb366bbdba950d65b85d232392fb658` | yes |
| Official QAT baseline | `google/gemma-4-26B-A4B-it-qat-q4_0-gguf` | `d1c082be9cf3c8a514acf63b8761f4b41935842e` | `gemma-4-26B_q4_0-it.gguf` | 14439363584 | `3eca3b8f6d7baf218a7dd6bba5fb59a56ee25fe2d567b6f5f589b4f697eca51d` | no |
| ggml Q4 baseline | `ggml-org/gemma-4-26B-A4B-it-GGUF` | `bb4531cda34d1ea09d9814959ed4d5833cf2a4c8` | `gemma-4-26B-A4B-it-Q4_0.gguf` | 14618145824 | `d208665ab1cd3a69f7a9a4bc59430e8448c8093d9b06334f566ac59d6d504a03` | no |
| Higher Deep candidate | `unsloth/gemma-4-26B-A4B-it-GGUF` | `c099eb48e663fd284577b04978a94ffccb261841` | `gemma-4-26B-A4B-it-UD-Q5_K_M.gguf` | 21150365408 | `769d386a69d43782321c1bad04d41d29a2e84b2c06e6a277cd99fd6265ec0e80` | no |

Local verified paths:

| File | Local path |
|---|---|
| `gemma-4-26B-A4B-it-qat-UD-Q4_K_XL.gguf` | `C:\IA\models\Gemma-4-26B-A4B-it-GGUF\gemma-4-26B-A4B-it-qat-UD-Q4_K_XL.gguf` |
| `gemma-4-26B-A4B-it-UD-Q3_K_XL.gguf` | `C:\IA\models\Gemma-4-26B-A4B-it-GGUF\gemma-4-26B-A4B-it-UD-Q3_K_XL.gguf` |
| `gemma-4-26B-A4B-it-UD-Q2_K_XL.gguf` | `C:\IA\models\Gemma-4-26B-A4B-it-GGUF\gemma-4-26B-A4B-it-UD-Q2_K_XL.gguf` |

## 3. GGUF structure and MoE gate

Every downloaded artifact reports the same model shape:

| Field | Value |
|---|---|
| `general.architecture` | `gemma4` |
| block count | 30 |
| native context | 262144 |
| tensor count | 658 |
| MTP head | none |

This differs from the Qwen3.8 campaign. Qwen uses a specific tensor override
for late FFN tensors, for example `-ot "blk\.(6[0-3])\.ffn_.*=CPU"`. Gemma is
handled with the runtime's MoE placement flags:

- `--n-cpu-moe N`: keep MoE weights of the first N layers on CPU.
- `--cpu-moe`: keep all MoE weights on CPU.

Local capability check:

| Binary | `--n-cpu-moe` | `--cpu-moe` | Consequence |
|---|---|---|---|
| `llama-server.exe` | present | present | server-side load tests may use either form |
| `llama-bench.exe` | present | absent | throughput all-CPU-MoE must use validated `--n-cpu-moe <all layers>` or remain NOT TESTED |

No Qwen tensor override was used in any Gemma cell.

## 4. Tensor census

`expert_ffn` dominates storage in every local artifact, which is why MoE
placement is the first lever.

| Artifact | attention bytes | expert FFN bytes | shared FFN bytes | embedding bytes | router bytes |
|---|---:|---:|---:|---:|---:|
| QAT UD Q4_K_XL | 624476160 | 12846382080 | 301086720 | 415236096 | 43591680 |
| UD Q3_K_XL | 1179566080 | 10312793088 | 568719360 | 784334848 | 43591680 |
| UD Q2_K_XL | 810287104 | 8730786816 | 436482816 | 507510784 | 43591680 |

Quant mix from local probes:

| Artifact | Quant mix |
|---|---|
| QAT UD Q4_K_XL | F32: 392, Q4_0: 266 |
| UD Q3_K_XL | F32: 392, Q8_0: 207, IQ4_NL: 29, IQ3_XXS: 29, IQ4_XS: 1 |
| UD Q2_K_XL | F32: 392, Q5_K: 117, Q6_K: 58, Q8_0: 31, IQ4_NL: 30, IQ2_XS: 29, IQ3_XXS: 1 |

## 5. Method

Load memory was measured with one `llama-server` start per cell. The script
waits for `/health`, samples adapter and process memory, then stops the server.
`marginal` is peak dedicated VRAM minus the idle baseline taken immediately
before the cell.

Throughput was measured with `llama-bench`, three repetitions per cell, while
sampling adapter memory. The tests used:

- `pp512`
- `pp8192`
- `pp32768`
- `tg128`

Important limitation for sections 8.1 and 8.2: `tg128` there is decode from the
benchmark's normal context state, not filled-context decode at 128k or 256k.
Filled-context decode is reported separately in sections 12 and 13.

## 6. Harness changes made for this campaign

Implemented and validated before the physical Gemma run:

- Generic manifest `moe_offload`.
- `moe_offload.cpu_layers` -> `--n-cpu-moe N`.
- `moe_offload.cpu_all=true` -> `--cpu-moe`, server-side only where supported.
- Semantic rejection for `cpu_all + cpu_layers`.
- Admission hardening for real MoE or tensor offload without measured
  `resources.peak_commit_gib`, `resources.peak_vram_gib`, or
  `resources.peak_ram_gib`.
- Shared llama.cpp argument builder for production config and benchmark runners.
- Separate capability gates for `llama-server` and `llama-bench`.
- Edge status summaries include MoE placement.

Validation already passed in this checkout:

| Gate | Result |
|---|---|
| `go test ./...` | PASS |
| `go vet ./...` | PASS |
| `Test-V2ConfigGeneration.ps1` | PASS |
| `Test-V2Manifest.ps1` | PASS |
| `Test-V2HarnessConfig.ps1` | PASS |
| `Test-V2Telemetry.ps1` | PASS |
| `Test-V2AgenticHarness.ps1` | PASS |
| `Test-V2McpInferenceIntegrations.ps1` | PASS |
| `Test-V2ProfileContracts.ps1` for Qwen Deep/Agent/Huge | PASS |

## 7. Memory envelope at load, measured

### 7.1 Deep and Agent candidates at 32k

Constants: `ctx=32768`, `KV=q4_0/q4_0`, `-ngl 99`, `-b 2048`, `-ub 288`,
`-t 8`, `--parallel 1`, no tensor override.

| Model | CPU MoE | load s | dedicated | shared | process WS | private | commit | marginal | state |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| QAT UD Q4_K_XL | 0 | 16.8 | 15614 | 2576 | 15.19 | 13.75 | 44.48 | 12235 | pressured |
| QAT UD Q4_K_XL | 2 | 8.5 | 15692 | 1809 | 13.63 | 12.95 | 43.80 | 12329 | pressured |
| QAT UD Q4_K_XL | 4 | 6.6 | 15642 | 1033 | 12.21 | 12.15 | 42.99 | 12278 | pressured |
| QAT UD Q4_K_XL | 6 | 6.6 | 15532 | 460 | 11.56 | 11.35 | 42.30 | 12169 | elevated |
| QAT UD Q4_K_XL | 8 | 6.5 | 14714 | 460 | 10.88 | 10.55 | 41.50 | 11351 | ok |
| QAT UD Q4_K_XL | 12 | 6.5 | 13078 | 460 | 9.55 | 8.96 | 39.89 | 9714 | ok |
| UD Q3_K_XL | 0 | 8.6 | 15612 | 1288 | 12.69 | 12.50 | 43.23 | 12249 | pressured |
| UD Q3_K_XL | 2 | 6.6 | 15437 | 949 | 11.79 | 11.87 | 42.71 | 12073 | ok |
| UD Q3_K_XL | 4 | 6.5 | 15282 | 460 | 11.27 | 11.24 | 42.05 | 11918 | ok |
| UD Q3_K_XL | 6 | 6.5 | 14686 | 460 | 10.75 | 10.61 | 41.56 | 11323 | ok |
| UD Q3_K_XL | 8 | 6.5 | 14042 | 460 | 10.22 | 9.98 | 40.94 | 10679 | ok |
| UD Q3_K_XL | 12 | 6.5 | 12754 | 460 | 9.16 | 8.72 | 39.68 | 9390 | ok |

Interpretation:

- QAT Q4 needs at least `--n-cpu-moe 8` to become a clean `ok` cell.
- Q3 becomes `ok` at `--n-cpu-moe 2`, but `2` is still close enough to the edge
  that throughput suffers later.
- `--n-cpu-moe 12` is a memory-saver mode for both QAT Q4 and Q3, not the
  default performance point.

### 7.2 Huge candidate at 256k

Constants: `ctx=262144`, `KV=q4_0/q4_0`, `-ngl 99`, `-b 2048`, `-ub 288`,
`-t 8`, `--parallel 1`, no tensor override.

| Model | CPU MoE | load s | dedicated | shared | process WS | private | commit | marginal | state |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| UD Q2_K_XL | 0 | 7.2 | 15275 | 1795 | 10.25 | 10.42 | 43.56 | 11906 | ok |
| UD Q2_K_XL | 2 | 6.5 | 14722 | 1917 | 9.80 | 9.88 | 43.13 | 11354 | ok |
| UD Q2_K_XL | 4 | 6.6 | 15500 | 586 | 9.35 | 9.34 | 42.55 | 12132 | elevated |
| UD Q2_K_XL | 6 | 6.5 | 14947 | 586 | 8.91 | 8.80 | 42.02 | 11579 | ok |
| UD Q2_K_XL | 8 | 6.6 | 14394 | 586 | 8.46 | 8.26 | 41.46 | 11025 | ok |
| UD Q2_K_XL | 12 | 4.5 | 13287 | 586 | 7.55 | 7.18 | 40.39 | 9919 | ok |

Interpretation:

- Huge Q2 can load a declared 256k context on this workstation.
- `--n-cpu-moe 0` is valid but uses high shared memory at load, around 1.8 GiB.
- `--n-cpu-moe 6` is the first practical low-shared Huge point.
- `--n-cpu-moe 12` is a memory-saver mode with significant throughput loss.

## 8. Throughput, measured

### 8.1 Deep and Agent 32k survivor cells

| Model | CPU MoE | pp512 | pp8192 | pp32768 | tg128 | peak dedicated | peak shared | verdict |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| QAT UD Q4_K_XL | 8 | 930.62 | 972.17 | 846.00 | 57.48 | 14820 | 489 | viable Deep/QAT comparison point |
| QAT UD Q4_K_XL | 12 | 671.60 | 732.51 | 655.89 | 47.63 | 13183 | 474 | memory saver; throughput loss too high |
| UD Q3_K_XL | 2 | 827.52 | 731.18 | 577.45 | 68.76 | 15587 | 963 | decode fast, but prefill hurt by pressure |
| UD Q3_K_XL | 6 | 1269.71 | 1259.30 | 1047.45 | 53.35 | 14791 | 481 | best Agent sweet spot |
| UD Q3_K_XL | 12 | 778.88 | 786.65 | 717.75 | 39.93 | 12859 | 473 | memory saver; throughput loss too high |

Deep/Agent conclusion:

- Best first-pass Agent candidate: `UD_Q3_K_XL`, `KV=q4_0/q4_0`,
  `--n-cpu-moe 6`.
- Best first-pass Deep/QAT comparison point: `QAT UD Q4_K_XL`,
  `KV=q4_0/q4_0`, `--n-cpu-moe 8`.
- Q3 at `--n-cpu-moe 2` has the fastest `tg128`, but its `pp32768` collapse
  makes it the wrong default for agentic use.

### 8.2 Huge-candidate short-context throughput

These throughput rows characterize the Huge candidate weights and placement
under short-context `llama-bench` operation. They do NOT represent decode with
256k tokens resident.

| Model | CPU MoE | pp512 | pp8192 | pp32768 | tg128 | peak dedicated | peak shared | verdict |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| UD Q2_K_XL | 0 | 2678.40 | 2343.35 | 1722.49 | 96.94 | 14409 | 470 | fastest short-throughput point |
| UD Q2_K_XL | 6 | 1257.82 | 1207.05 | 1041.39 | 57.31 | 12765 | 474 | practical Huge point |
| UD Q2_K_XL | 8 | 1051.97 | 1057.74 | 918.42 | 51.68 | 12211 | 474 | memory saver with clear loss |
| UD Q2_K_XL | 12 | 814.31 | 831.02 | 733.63 | 43.34 | 11105 | 482 | too slow for default Huge |

Huge conclusion:

- `--n-cpu-moe 0` is the raw speed winner.
- `--n-cpu-moe 6` is the first practical low-shared-memory candidate.
- The right default depends on filled-context decode: until that exists,
  `--n-cpu-moe 0` is a fast control and `--n-cpu-moe 6` is the safer candidate.

## 9. Deep QAT Q4 with KV q8_0/q8_0

MEASURED: Deep QAT Q4 at `ctx=32768`, `KV=q8_0/q8_0`,
`--n-cpu-moe 8` loads cleanly and remains operational. No `--n-cpu-moe 10` or
`12` rescue cell was run because the required fallback condition did not occur.

| Config | load s | dedicated | shared | marginal | process WS | private | commit | physical RAM | state |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| QAT Q4, KV q8/q8, CPU MoE 8 | 10.6 | 14971 | 467 | 11586 | 10.88 | 10.55 | 41.59 | 26.06 | ok |

| Test | t/s | stddev | peak dedicated | peak shared | wall s |
|---|---:|---:|---:|---:|---:|
| pp512 | 925.29 | 24.32 | 14486 | 454 | 9.5 |
| pp8192 | 965.31 | 8.62 | 14673 | 460 | 41.4 |
| pp32768 | 837.52 | 4.03 | 15072 | 474 | 163.7 |
| tg128 short | 59.11 | 2.70 | 14335 | 437 | 13.2 |

MEASURED verdict: `KV=q8_0/q8_0` is viable for Deep from a memory and throughput
standpoint. It does not solve the quality/reasoning failures in section 10.

## 10. Quality qualification before rescue

The second pass reused the Qwen-style harness categories where possible:
`chat,coding,tools,json`, temperature-default server generation, a hard output
ceiling of 8192 tokens for the controlled 32k quality runs, and exact tool
argument checking. Huge quality was run only on the filled-context placement
winner, `--n-cpu-moe 4`.

MEASURED:

| Role | Config | Chat | Coding | Tools | JSON | Constraints | No-answer | Dangerous errors | Verdict |
|---|---|---:|---:|---:|---:|---:|---:|---:|---|
| Deep | QAT Q4, KV q8/q8, ctx32k, CPU MoE 8 | 4/4 | 3/10 | 6/7 | pass | fail | 1 | 2+ | NOT READY |
| Agent | Q3, KV q4/q4, ctx32k, CPU MoE 6 | 4/4 | 4/10 | 6/7 | pass | fail | 0 | 2+ | NOT READY |
| Huge | Q2, KV q4/q4, ctx32k, CPU MoE 4 | 4/4 | 3/10 | 6/7 | pass | fail | 3 | 2+ | NOT READY |

Coding detail:

| Task | Deep | Agent | Huge |
|---|---|---|---|
| `py_bugfix` | exact | exact | exact |
| `py_impl` | missing/length | exact | missing/length |
| `go_bugfix` | incorrect | incorrect | missing/length |
| `go_impl` | incorrect | incorrect | incorrect |
| `ts_impl` | exact | exact | exact |
| `ts_refactor` | exact | exact | exact |
| `cs_bugfix` | incorrect | incorrect | missing/length |
| `unity_impl` | incorrect | incorrect | incorrect |
| `constraint_frozen_file` | constraint violation | constraint violation | constraint violation |
| `no_invented_api` | hallucinated/invented API | hallucinated/invented API | hallucinated/invented API |

INFERRED: the harness did not expose a separate `confident_incorrect` counter,
so `dangerous_errors` is recorded conservatively as at least the constraint and
invented-API failures, plus any incorrect coding failures. Unknown/no-answer is
not counted as hallucination.

MEASURED: QAT Q4 does not beat Q3 in this suite. Agent Q3 is slightly better
than Deep QAT Q4 by pass count, 4/10 vs 3/10, and passed `py_impl` where Deep
returned no answer. Neither profile is acceptable for canary because both fail
constraints, invented-API, Go, C#, Unity C#, and exact nested tool arguments.

## 10.1 Gemma quality rescue / reasoning and protocol tuning

MEASURED: a controlled rescue campaign was run on 2026-08-22 after aligning the
repository baseline with the current intentional model inventory. The physical
placements were not changed:

- Deep: QAT UD Q4_K_XL, KV q8/q8, ctx32k, `--n-cpu-moe 8`.
- Agent: UD Q3_K_XL, KV q4/q4, ctx128k, `--n-cpu-moe 6`.
- Huge: UD Q2_K_XL, KV q4/q4, ctx256k, `--n-cpu-moe 4`.

The harness was extended to preserve raw request payloads, raw wire responses,
parsed harness responses, `reasoning_content`, final content, finish reasons,
output-token counts, and tool arguments before and after JSON parsing. Artifacts
are under `benchmarks/campaign-gemma4-26b/quality-rescue/`.

### Diagnostic matrix

All Agent diagnostic cells used fixed `temperature=0`, `seed=20260822`, the
same coding subset (`py_impl`, `go_bugfix`, `cs_bugfix`, `unity_impl`,
`constraint_frozen_file`, `no_invented_api`), the same two tool tasks
(`tool_pick_tests`, `tool_pick_search_many_nested`), and one structured JSON
case.

| Config | Output cap | Reasoning budget | System policy | Coding diag | Tools | No-answer | Dangerous errors |
|---|---:|---:|---|---:|---:|---:|---:|
| Agent A | 8192 | unrestricted | current | 5/6 | 1/2 | 1 | 1 tool-arg |
| Agent B | 16384 | unrestricted | current | partial: `py_impl` no-answer | NOT COMPLETED | 1 | NOT TESTED |
| Agent C | 16384 | 8192 | current | 6/6 | 1/2 | 0 | 1 tool-arg |
| Agent D | 16384 | 4096 | current | 6/6 | 1/2 | 0 | 1 tool-arg |
| Agent E | 16384 | 2048 | current | 6/6 | 1/2 | 0 | 1 tool-arg |
| Agent F | 16384 | 0 | current | 6/6 | 1/2 | 0 | 1 tool-arg |
| Agent G | 4096 | 0 | strict tool policy | NOT RUN | 1/2 | 0 | 1 tool-arg |
| Agent H | 4096 | 0 | strict tool policy + schema descriptions | NOT RUN | 1/2 | 0 | 1 tool-arg |
| Agent I | 4096 | 0 | agentic system + strict tool policy + schema descriptions | NOT RUN | 1/2 | 0 | 1 tool-arg |
| Agent J | 4096 | 0 | current, temperature 0.2 | NOT RUN | 6/7 | 0 | 1 tool-arg |

MEASURED: increasing the output cap from 8192 to 16384 without a reasoning
budget did not fix `py_impl`; the model again spent the entire answer in
`reasoning_content` and returned no final code. `reasoning_budget=8192` fixed
that no-answer, and smaller budgets preserved the diagnostic pass rate.
`reasoning_budget=0` was the best diagnostic point: 6/6 coding, no reasoning
tokens, no no-answer, and far lower wall time.

MEASURED: the `search_files` failure is not a parser failure. The raw model
tool call already changed the literal exclude glob before the harness saw it:

| Run | Model-emitted arguments | Verifier result |
|---|---|---|
| Agent full rb0 | `exclude=["vendor/*"]` | wanted `exclude=["vendor/**"]` |
| Deep full rb0 | `exclude=["vendor"]` | wanted `exclude=["vendor/**"]` |
| Huge full rb0 | `exclude=["vendor"]` | wanted `exclude=["vendor/**"]` |

Generic strict-tool policy, schema descriptions for literal fields, an
agentic-style system prompt, and a minimal sampling A/B did not resolve that
tool-argument failure. The function name selection remained correct.

### Full rescue results

| Full config | Coding 10 | Tools 7 | JSON | Chat | Dangerous errors | Verdict |
|---|---:|---:|---|---|---:|---|
| Deep QAT Q4, KV q8/q8, ctx32k, CPU MoE 8, rb0 | 10/10 | 6/7 | PASS | 4/4 | 1 tool-arg | NOT READY |
| Agent Q3, KV q4/q4, ctx128k, CPU MoE 6, rb0 | 10/10 | 6/7 | PASS | 4/4 | 1 tool-arg | NOT READY |
| Huge Q2, KV q4/q4, ctx256k, CPU MoE 4, rb0 | 10/10 | 6/7 | PASS | 4/4 | 1 tool-arg | NOT READY |

MEASURED: after bounding reasoning to zero, QAT Q4 no longer demonstrates a
quality advantage over Q3 or Q2 in this suite. All three profiles tie at 10/10
coding, 6/7 tools, JSON PASS, chat PASS, and the same exact nested-argument
failure. Q2 is therefore no longer disqualified by short-context coding quality,
but it still fails the canary tool gate.

## 10.2 Reasoning calibration

MEASURED: a follow-up reasoning calibration was run on 2026-08-22. No physical
configuration changed. The only primary variable was `--reasoning-budget`.
Discovery was run on Agent Q3 with `ctx=131072`, KV q4/q4, `--n-cpu-moe 6`,
`temperature=0`, `seed=20260822`, `--n-predict 16384`, and harness
`max_tokens=16384`.

The hard suite is intentionally separate from the historic 10-task coding suite.
It includes two multi-file Go bugfixes, one non-trivial algorithm task, C#/Unity
style bugfixing, TypeScript refactor constraints, no-invented-API,
frozen-file/constraint, and one missing-information honesty case. A separate
literal-fidelity mini-suite checks path/glob/regex/identifier preservation.

### Agent reasoning curve

| Budget | Hard suite | Tools | Literal tools | JSON | Reasoning chars | Output tokens | Hard wall s | Delta wall vs rb0 | Dangerous errors |
|---:|---:|---:|---:|---|---:|---:|---:|---:|---:|
| 0 | 5/8 | 0/1 | 2/4 | PASS | 0 | 1985 | 61.8 | 0.0 | 4 |
| 256 | 5/8 | 0/1 | 2/4 | PASS | 8903 | 35202 | 711.0 | +649.2 | 4 |
| 512 | 5/8 | 0/1 | 2/4 | PASS | 15295 | 20885 | 427.5 | +365.7 | 4 |
| 1024 | 5/8 | 0/1 | 2/4 | PASS | 26240 | 8677 | 180.2 | +118.4 | 4 |
| 2048 | 4/8 | 0/1 | 2/4 | PASS | 46217 | 16097 | 326.2 | +264.4 | 5 |
| 4096 | 5/8 | 0/1 | 2/4 | PASS | 74676 | 22565 | 452.3 | +390.5 | 4 |

MEASURED: no positive budget improved total hard-suite pass rate over rb0.
Budgets 256 and 512 recovered `hard_go_retry_multifile`, but both introduced a
`go_impl` regression; 256 also hit the 16384-token ceiling on `ts_refactor`.
Budgets 1024 and 4096 returned to 5/8 but with much higher reasoning cost.
Budget 2048 dropped to 4/8.

MEASURED: positive reasoning did not improve literal tool fidelity. Every Agent
budget failed the strict `search_files` task and the same two literal-fidelity
checks:

- `literal_cs_glob`: emitted `case_sensitive=false` instead of `true`.
- `literal_regex`: emitted `exclude=["vendor"]` or `["vendor/*"]` instead of
  `["vendor/**"]`.

### Profile rb0 check

Because no positive budget beat rb0 on Agent, no positive budget was replicated
to Deep or Huge. The selected rb0 policy was checked on the other profiles only
as a hard-suite baseline:

| Profile | Selected reasoning | Why | Quality delta vs rb0 | Latency delta vs rb0 |
|---|---|---|---|---|
| Agent Q3 | `--reasoning-budget 0` | best quality/cost point; full suite passes with 8192 ceiling | baseline | baseline |
| Deep QAT Q4 | `--reasoning-budget 0` | hard rb0 is 6/8, one task better than Agent; no positive Agent winner to replicate | +1 hard task vs Agent rb0 | NOT COMPARED |
| Huge Q2 | `--reasoning-budget 0` | hard rb0 is 6/8 and literal tools 3/4; no positive Agent winner to replicate | +1 hard task and +1 literal vs Agent rb0 | NOT COMPARED |

Production-contract confirmation:

| Profile | Output ceiling tested | Coding | Tools | Chat | JSON | Verdict |
|---|---:|---:|---:|---:|---:|---|
| Agent Q3 rb0 | 8192 | 10/10 | 6/7 | 4/4 | PASS | output reserve adequate; tool gate still fails |

Decision:

- rb0 remains best.
- No tested positive budget from 256 to 4096 buys net quality.
- There is no smallest useful positive budget in this evidence.
- Positive reasoning increases hard-suite wall time by +118.4s to +649.2s
  without improving pass rate.
- Positive reasoning does not improve literal tool fidelity.
- Reasoning should be disabled by default for Gemma Agent.
- Output ceiling 8192 is sufficient for the measured rb0 full Agent suite.

POLICY A
REASONING OFF BY DEFAULT

## 11. Tool calling, structured output, and chat template

MEASURED before and after the rescue pass:

| Role | system/user | multi-turn | streaming | tool result continuation | exact tool selection | similar names | nested args | structured JSON |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Deep | pass | pass | pass | pass | pass | pass | fail | pass |
| Agent | pass | pass | pass | pass | pass | pass | fail | pass |
| Huge | pass | pass | pass | pass | pass | pass | fail | pass |

All three selected the correct function for the ambiguous `search_files` task,
but failed exact nested argument matching:

- Deep used `include=["**/*.go"]` instead of `["*.go"]`.
- Agent used `exclude=["vendor/*"]` instead of `["vendor/**"]`.
- Huge used `exclude=["vendor"]` instead of `["vendor/**"]`.

MEASURED after raw capture: the emitted arguments and the parsed arguments
match. The nested-argument failure originates in the model output, not in the
llama.cpp parser or the local harness.

MEASURED: JSON-only structured output passed for all three profiles.

MEASURED: the local GGUF metadata for Agent Q3 reports:

| Metadata | Value |
|---|---|
| `tokenizer.ggml.model` | `gemma4` |
| `tokenizer.ggml.bos_token_id` | 2 |
| `tokenizer.ggml.eos_token_id` | 106 |
| `tokenizer.ggml.padding_token_id` | 0 |
| `tokenizer.ggml.add_bos_token` | true |
| `tokenizer.chat_template` | present, Gemma-style Jinja template |

The template maps assistant turns to `model`, supports system/developer,
`user`, `tool_call`, `tool_response`, and a thought channel. The template
snapshot is preserved in
`benchmarks/campaign-gemma4-26b/quality-rescue/metadata-agent-q3-template.json`.

## 12. Agent 128k filled-context

MEASURED: Agent Q3 at `ctx=131072`, `KV=q4_0/q4_0`, `--n-cpu-moe 6` loads
successfully and decodes at `tg128` with about 115k prompt-depth occupancy.

Load-only:

| load s | dedicated | shared | marginal | process WS | private | commit | physical RAM | state |
|---:|---:|---:|---:|---:|---:|---:|---:|---|
| 6.6 | 14903 | 1261 | 11507 | 10.81 | 10.67 | 42.22 | 26.02 | ok |

Filled-context decode:

| Test | depth | ctx | tg128 | stddev | wall s | peak dedicated | peak shared | process WS | physical RAM |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| Agent Q3 filled | 115000 | 131072 | 34.39 | 0.48 | 201.4 | 15732 | 550 | 13.03 | 28.05 |

MEASURED: Agent loses decode from 53.35 t/s short-context to 34.39 t/s at
~115k occupancy, a 35.5% reduction. By the operational labels in the campaign,
34.39 t/s is "muito bom para Agent".

NOT TESTED: cold TTFT and effective prompt throughput were not separately
emitted by this `llama-bench -d` wrapper. The measured wall time includes the
prefill and decode work, but the artifact does not split them into TTFT/prefill
throughput fields.

## 13. Huge 256k filled-context

MEASURED: Huge Q2 at `ctx=262144`, `KV=q4_0/q4_0` was tested at about 230k
prompt-depth occupancy for the three final placements requested.

| CPU MoE | depth | tg128 | wall s | peak dedicated | peak shared | process WS | physical RAM | Verdict |
|---:|---:|---:|---:|---:|---:|---:|---:|---|
| 0 | 230000 | 2.77 | 1108.1 | 15525 | 1674 | 11.60 | 27.58 | decode collapse |
| 4 | 230000 | 30.32 | 535.0 | 15442 | 606 | 11.56 | 27.03 | winner |
| 6 | 230000 | 26.28 | 568.4 | 15135 | 606 | 11.55 | 26.41 | stable but slower |

MEASURED: the short-context winner `--n-cpu-moe 0` does not survive filled
context. It falls from 96.94 t/s short to 2.77 t/s at ~230k occupancy and uses
1.67 GiB shared memory. The best filled-context placement is `--n-cpu-moe 4`:
it is 15.4% faster than `--n-cpu-moe 6` at the same shared-memory peak.

NOT TESTED: cold TTFT and effective prompt throughput were not separately
emitted by this `llama-bench -d` wrapper.

## 14. Reasoning behavior

MEASURED before rescue: no explicit `reasoning_budget` was set for any Gemma
profile.

MEASURED: reasoning/runaway is a blocking behavior. Several failed tasks reached
the 8192-token output ceiling without an answer:

| Role | Length/no-answer examples | Largest observed reasoning chars |
|---|---|---:|
| Deep | `py_impl` | 20488 |
| Agent | none at length, but long failed `cs_bugfix` | 26792 |
| Huge | `py_impl`, `go_bugfix`, `cs_bugfix` | 28550 |

INFERRED: Gemma shows a reasoning/no-answer problem of the same operational
class as the Qwen Huge issue, but the fix is different. Do not copy Qwen Huge's
`reasoning_budget=24576` into Gemma.

MEASURED during rescue: `llama-server b10549` supports
`--reasoning-budget N`, with `-1` unrestricted and `0` meaning immediate
end-of-thinking. On Agent Q3:

| Output cap | Reasoning budget | Result |
|---:|---:|---|
| 8192 | unrestricted | `py_impl` no-answer, 8192 output tokens, 17147 reasoning chars |
| 16384 | unrestricted | `py_impl` no-answer again, 16384 output tokens, 33795 reasoning chars |
| 16384 | 8192 | diagnostic coding 6/6 |
| 16384 | 4096 | diagnostic coding 6/6 |
| 16384 | 2048 | diagnostic coding 6/6 |
| 16384 | 0 | diagnostic coding 6/6, full coding 10/10 |

MEASURED verdict: the dominant quality failure was not quantization. It was the
Gemma chat/reasoning contract spending the answer budget in
`reasoning_content`. For these coding fixtures, `reasoning_budget=0` improves
reliability and time-to-solution rather than degrading it. A positive
reasoning-budget message was not tested because budget 0 already answered the
diagnostic question more cleanly.

## 15. Retention and workstation usability

NOT TESTED: retention probes were intentionally not run after all three profiles
failed basic quality/tool gates. The deterministic corpus remains unmeasured for
Gemma:

- exact needle
- frozen constraint
- superseded API
- rejected approach
- similar entities
- recency conflict
- impossible/unknown probe

NOT TESTED: workstation usability with Unity/IDE/browser load was not run after
the same quality gate failure. The memory numbers above are benchmark-session
peaks, not a representative workstation multitasking peak.

## 16. Qwen comparison

Qwen reference is the existing campaign result; Qwen was not rerun.

| Role | Qwen config | Gemma config | Quality winner | Perf winner | Memory winner | Recommendation |
|---|---|---|---|---|---|---|
| Deep | Qwen UD-IQ4_XS, KV q8/q8, ctx32k | Gemma QAT Q4, KV q8/q8, ctx32k, CPU MoE 8, rb0 | mixed | Gemma short tg | Gemma shared | Gemma rescued for coding, Qwen remains safer on tools |
| Agent | Qwen UD-Q3_K_XL, KV q4/q4, ctx128k | Gemma Q3, KV q4/q4, ctx128k, CPU MoE 6, rb0 | mixed | Gemma filled tg @115k | Gemma filled shared | Gemma is performance winner, but not canary until tools 7/7 |
| Huge | Qwen UD-Q2_K_XL, KV q4/q4, ctx256k | Gemma Q2, KV q4/q4, ctx256k, CPU MoE 4, rb0 | mixed | Gemma filled tg @230k | Gemma filled shared | Gemma Huge is promising, still blocked by exact tool args |

Reference points:

- Qwen Q3 quality: 9/10 coding, 4/4 tools, JSON pass.
- Qwen Q2 quality: 7/10 coding, 4/4 tools, JSON pass, no incorrect code.
- Gemma best quality before rescue: Agent Q3 at 4/10 coding, 6/7 tools, JSON pass.
- Gemma best quality after rescue: all three profiles at 10/10 coding, 6/7
  tools, JSON pass, chat 4/4.
- Qwen Q3 filled decode at 32k occupancy: 18.33 t/s.
- Gemma Agent Q3 filled decode at ~115k occupancy: 34.39 t/s.
- Gemma Huge Q2 filled decode at ~230k occupancy: 30.32 t/s with CPU MoE 4.

MEASURED/INFERRED: Gemma is the performance winner in these new filled-context
cells and is now competitive on coding after `reasoning_budget=0`. Qwen remains
the safer agentic winner on exact tool reliability until Gemma reaches 7/7 tools
under the same strict contract.

## 17. Final performance table

| Role | Quant | KV | ctx | CPU MoE | short tg | filled tg | pp32768 | shared peak | RAM | verdict |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|---|
| Deep | QAT UD Q4_K_XL | q8/q8 | 32768 | 8 | 59.11 | NOT TESTED | 837.52 | 474 physical / 461 rescue | 29.08 rescue | memory/perf viable; coding rescued; tools 6/7 |
| Agent | UD Q3_K_XL | q4/q4 | 131072 | 6 | 53.35 | 34.39 @115k | 1047.45 | 550 filled / 516 rescue | 28.05 filled | perf viable; coding rescued; tools 6/7 |
| Huge | UD Q2_K_XL | q4/q4 | 262144 | 4 | NOT TESTED for n4 | 30.32 @230k | NOT TESTED for n4 | 606 filled / 588 rescue | 27.03 filled | best Huge placement; coding rescued; tools 6/7 |

## 18. Canary decision

MEASURED:

- Artifacts are local and hash-verified.
- Target context load passes for Agent and Huge; Deep q8/q8 32k load passes.
- Chat template smoke passes.
- Structured JSON passes.
- Coding quality is rescued to 10/10 on all three profiles with
  `reasoning_budget=0`.
- Tool calling still does not pass exact nested-argument reliability.
- The remaining dangerous failure is an exact tool-argument contract violation
  on `search_files`.

Decision:

| Profile | Candidate entry | Catalog regen | `provider.public_model` | Status |
|---|---|---|---|---|
| `gemma4-26b-deep-32k` | not created | not run | no Gemma 26B public change | NOT READY |
| `gemma4-26b-agent-128k` | not created | not run | no Gemma 26B public change | NOT READY |
| `gemma4-26b-huge-256k` | not created | not run | no Gemma 26B public change | NOT READY |

No Gemma 26B profile should be added as `candidate/canary` from this evidence
because the strict tool gate remains 6/7. No Gemma 26B profile is `qualified`
or `final`.

## 19. Post-update validation

MEASURED after the quality-rescue and reasoning-calibration updates:

| Gate | Result |
|---|---|
| `python -m py_compile scripts/v2/eval/qualify.py scripts/v2/eval/coding_tasks.py` | PASS |
| `python scripts/v2/eval/test_verifiers.py` | PASS |
| PowerShell parser check for touched V2 scripts | PASS |
| `gofmt -l .` excluding preserved benchmark `work-*` artifacts | PASS |
| `go vet ./...` via `C:\IA\toolchains\go1.26.5\go\bin\go.exe` | PASS |
| `Test-V2Manifest.ps1` | PASS |
| `Test-V2ConfigGeneration.ps1` | PASS |
| `Test-V2HarnessConfig.ps1` | PASS |
| `Test-V2Telemetry.ps1` | PASS |
| `Test-V2AgenticHarness.ps1` | PASS |
| `Test-V2McpInferenceIntegrations.ps1` | PASS |
| `Test-V2ProfileContracts.ps1 -ModelId qwen38-27b-deep-32k` | PASS, 8/8 |
| `Test-V2ProfileContracts.ps1 -ModelId qwen38-27b-agent-128k` | PASS, 8/8 |
| `Test-V2ProfileContracts.ps1 -ModelId qwen38-27b-huge-256k` | PASS, 8/8 |
| `go test ./...` via `C:\IA\toolchains\go1.26.5\go\bin\go.exe` | PASS |

MEASURED: the red gates from the previous report were caused by the intentional
model inventory cleanup, not by Gemma 26B. The current desired inventory has 10
manifest models, 5 canary models, and 5 retired models. The removed historical
entries (`local-coding`, `local-fast`, `qwen35-9b-q4km`,
`qwen35-9b-ud-q4xl`) are not restored, and their missing GGUF files are not
required by unit/schema/config-generation tests. Catalogs and tests were aligned
to the current inventory.

MEASURED: raw model outputs under
`benchmarks/campaign-gemma4-26b/quality-rescue/work-*` and
`benchmarks/campaign-gemma4-26b/reasoning-calibration/work-*` are preserved as
evidence and are not reformatted even when `gofmt -l .` reports generated Go
files there.

## 20. Raw artifacts

Primary report artifacts:

| Artifact | Path |
|---|---|
| Discovery | `benchmarks/campaign-gemma4-26b/artifact-discovery.json` |
| QAT Q4 local probe | `benchmarks/campaign-gemma4-26b/probe-local-gemma4-26b-qat-ud-q4kxl.json` |
| Q3 local probe | `benchmarks/campaign-gemma4-26b/probe-local-gemma4-26b-ud-q3kxl.json` |
| Q2 local probe | `benchmarks/campaign-gemma4-26b/probe-local-gemma4-26b-ud-q2kxl.json` |
| QAT/Q3 32k footprint summary | `benchmarks/campaign-gemma4-26b/summary-footprint-ctx32768-kvq4_0.json` |
| QAT/Q3 32k throughput summary | `benchmarks/campaign-gemma4-26b/summary-throughput-ctx32768-kvq4_0.json` |
| Q2 Huge 256k footprint summary | `benchmarks/campaign-gemma4-26b/summary-footprint-huge-ud-q2kxl-ctx262144-kvq4_0.json` |
| Q2 Huge short-throughput summary | `benchmarks/campaign-gemma4-26b/summary-throughput-huge-ud-q2kxl-kvq4_0.json` |
| Deep q8/q8 load | `benchmarks/campaign-gemma4-26b/deep-q8/footprint-deep-qat-q4kxl-ctx32768-kvq8_0-ncpumoe8.json` |
| Deep q8/q8 throughput | `benchmarks/campaign-gemma4-26b/deep-q8/throughput-deep-qat-q4kxl-kvq8-ncpumoe8.json` |
| Quality profiles | `benchmarks/campaign-gemma4-26b/quality/` |
| Quality rescue profiles | `benchmarks/campaign-gemma4-26b/quality-rescue/` |
| Reasoning calibration profiles | `benchmarks/campaign-gemma4-26b/reasoning-calibration/` |
| Agent Q3 template snapshot | `benchmarks/campaign-gemma4-26b/quality-rescue/metadata-agent-q3-template.json` |
| Agent 128k filled | `benchmarks/campaign-gemma4-26b/filled-agent-128k/` |
| Huge 256k filled | `benchmarks/campaign-gemma4-26b/filled-huge-256k/` |

## 21. Final answers

**A. QAT Q4 + KV Q8 is viable as Gemma Deep?** MEASURED yes for memory,
throughput, and coding when served with `reasoning_budget=0`; MEASURED no for
canary because tools remain 6/7.

**B. QAT Q4 really beats Q3 in quality?** MEASURED no. Before rescue, Q3 was
4/10 and QAT Q4 was 3/10. After rescue, both are 10/10 coding and 6/7 tools.

**C. Q3 `--n-cpu-moe 6` remains best Agent at 128k?** MEASURED yes for the
tested Agent candidate's memory/performance and rescued coding quality;
MEASURED no for canary readiness until tools reach 7/7.

**D. How much does Q3 lose at ~120k occupancy?** MEASURED: 53.35 short tg to
34.39 at ~115k, a 35.5% decode reduction.

**E. Huge Q2 should use `--n-cpu-moe 0`, `4`, or `6`?** MEASURED: `4`.

**F. What does Huge deliver at ~220-240k occupancy?** MEASURED: 30.32 t/s at
~230k with `--n-cpu-moe 4`.

**G. Q2 keeps acceptable agentic quality?** MEASURED partially. Q2 reaches
10/10 coding with `reasoning_budget=0`, but still fails exact nested tool args.

**H. Same reasoning runaway/no-answer problem as Qwen?** MEASURED same
operational class, but Gemma's best observed coding behavior uses
`reasoning_budget=0`, not Qwen Huge's large positive budget.

**I. Explicit `reasoning_budget` necessary?** MEASURED yes for reliable Gemma
coding in this harness. The best observed setting is `reasoning_budget=0`, and
the 2026-08-22 calibration found no positive budget from 256 to 4096 that bought
net quality.

**J. Gemma Agent is really better than Qwen Agent for daily use?** MEASURED no
for canary/daily use yet. It is faster and coding-rescued, but still fails the
strict tool contract.

**K. Preferred family by role?** INFERRED from current evidence: Gemma is the
performance winner and now a serious research candidate for every role, but
Qwen remains the safer daily agentic family until Gemma resolves the exact
tool-argument failure.

**L. Reasoning policy?** MEASURED: POLICY A, reasoning off by default. Positive
reasoning remains experimental, not default.

## 22. Quality rescue conclusion

ROOT CAUSE

| Area | Result |
|---|---|
| template | MEASURED: GGUF carries a Gemma 4 Jinja template with tools and thought channel; chat smoke passes 4/4. |
| reasoning | MEASURED: unrestricted reasoning consumes output and causes no-answer; `reasoning_budget=0` fixes coding. |
| output cap | MEASURED: 16k cap alone does not fix no-answer. |
| tool protocol | MEASURED: parser preserves emitted args; model mutates `vendor/**` to `vendor/*` or `vendor`. |
| sampling | MEASURED: temperature 0.2 does not fix tools. |
| calibration | MEASURED: budgets 256/512/1024/2048/4096 do not beat rb0 on Agent hard suite. |
| quantization/artifact | INFERRED: not dominant for the full coding suite; QAT Q4, Q3, and Q2 tie after reasoning control. |
| remaining uncertainty | Tool-argument reliability and long-context retention after rb0 remain open. |

BEST GEMMA AGENT CONFIG

| Field | Value |
|---|---|
| weights | `gemma-4-26B-A4B-it-UD-Q3_K_XL.gguf` |
| KV | q4_0/q4_0 |
| ctx | 131072 |
| MoE | `--n-cpu-moe 6` |
| output | `--n-predict 8192`, harness `max_tokens=8192` confirmed for Agent full suite |
| reasoning | `--reasoning-budget 0`; POLICY A |
| system/tool policy | current policy; strict/schema variants did not improve tools |
| coding | 10/10 |
| tools | 6/7 |
| dangerous errors | 1 exact tool-argument contract failure |
| status | NOT READY |

BEST GEMMA DEEP CONFIG

| Field | Value |
|---|---|
| weights | `gemma-4-26B-A4B-it-qat-UD-Q4_K_XL.gguf` |
| KV | q8_0/q8_0 |
| ctx | 32768 |
| MoE | `--n-cpu-moe 8` |
| output | tested with `--n-predict 16384`, harness `max_tokens=16384`; final profile value NOT SELECTED |
| reasoning | `--reasoning-budget 0`; POLICY A |
| system/tool policy | current policy; strict/schema variants not selected |
| coding | 10/10 |
| tools | 6/7 |
| dangerous errors | 1 exact tool-argument contract failure |
| status | NOT READY |

BEST GEMMA HUGE CONFIG

| Field | Value |
|---|---|
| weights | `gemma-4-26B-A4B-it-UD-Q2_K_XL.gguf` |
| KV | q4_0/q4_0 |
| ctx | 262144 |
| MoE | `--n-cpu-moe 4` |
| output | tested with `--n-predict 16384`, harness `max_tokens=16384`; final profile value NOT SELECTED |
| reasoning | `--reasoning-budget 0`; POLICY A |
| system/tool policy | current policy; strict/schema variants not selected |
| coding | 10/10 |
| tools | 6/7 |
| dangerous errors | 1 exact tool-argument contract failure |
| status | NOT READY |

GEMMA IMPROVED — STILL BELOW CANARY
