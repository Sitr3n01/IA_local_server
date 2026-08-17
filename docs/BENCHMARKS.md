# Benchmark and acceptance protocol

Benchmark reports must contain no prompts, responses, headers, cookies, or credentials. Store only scenario IDs, timestamps, hashes, token counts, status, timing, queue state, memory measurements, and pass/fail assertions.

## Environments

Measure the same immutable model/runtime pair through:

1. Direct `llama-server` on a temporary loopback port.
2. Canary llama-swap without edge, using the router credential.
3. Full canary edge path.
4. Codex and OpenCode end-to-end sessions.

Do not duplicate real harness prompts to a shadow service. Synthetic fixtures only may be replayed.

## Metrics

- Cold load time and first-token latency.
- Warm time to first token, p50/p95/p99.
- Prompt and generated tokens per second from native counters.
- End-to-end duration and edge overhead.
- HTTP/SSE status, cancellation propagation, and retry result.
- Peak dedicated VRAM, process working set, system RAM, and committed bytes.
- Queue depth, wait time, rejection count, process starts/stops, and orphan count.

Never derive throughput by dividing an already reported tokens/second metric.

## Contract matrix

| Scenario | Required result |
|---|---|
| `GET /v1/models` idle | 200, one declared model, no process start |
| Identity/gzip/zstd requests | Same semantic response |
| Unsupported encoding | 415 |
| Wire/decoded/ratio limit | 413 without upstream request |
| Unknown route/model | Deterministic local 404 |
| Responses streaming | Native events forwarded incrementally |
| Chat streaming | Valid incremental chunks |
| Function tool | Valid name/JSON arguments and continuation |
| Client cancellation | Upstream request terminates promptly |
| Five waiters plus active | Bounded queue; overflow 429 + `Retry-After` |
| Router unavailable | Edge readiness false and request 503 |
| Idle 900 seconds | Model unloads exactly once |

Stateful Responses features that llama.cpp does not implement must return `400 unsupported_feature`; fabricated IDs or reconstructed history are failures.

## Quality suite

Use versioned, non-secret fixtures representing:

- Repository navigation and change planning.
- A focused bug fix with tests.
- Multi-file refactor without unrelated edits.
- Tool choice and valid tool arguments.
- Strict JSON/structured output.
- Long-context retrieval near the declared limit.
- Refusal to invent successful tool execution.

Score correctness, compile/test result, tool validity, instruction adherence, unsupported-feature honesty, and absence of reasoning/template artifacts. Publish fixture definitions and aggregate results, not private code or generated content.

## Hybrid Gated DeltaNet models

Qwen3.8-class models interleave 48 Gated DeltaNet layers with 16 full-attention layers. Two consequences change how they are measured.

**Evidence labels.** Every figure in this repository's docs and reports carries one of these, and they must not be mixed:

| Label | Meaning |
|---|---|
| `measured` | Produced by a run on this hardware, with the artifact hashes recorded |
| `modelled` | Derived arithmetically from measured constants; a prediction, not an observation |
| `upstream-reported` | Taken from an issue, discussion, or model card elsewhere |
| `unverified on gfx1201` | Reported on other hardware and not reproduced here |

The bandwidth ceilings and MTP sensitivity tables in `TUNING.md` are **modelled**. The Gated DeltaNet kernel behaviour, the `blk.64` acceptance figures, and the checkpoint defects are **upstream-reported** and **unverified on gfx1201**. Nothing about Qwen3.8-27B is `measured` here yet.

**Gate zero — is the kernel accelerated at all.** `GGML_OP_GATED_DELTA_NET` landed in llama.cpp with CPU and CUDA backends only. Vulkan falls back to CPU, and on RDNA 3.5 (gfx1151) the HIP path measures at CPU speed. Whether gfx1201 escapes that is unverified here. Run the `qwen38-27b-iq4xs-sanity` profile first: no offload, short context, no speculative decoding, so the numbers isolate kernel throughput.

Decode alone is not sufficient evidence. Prefill runs a different code path, and a hybrid model can decode acceptably while prompt processing is severely degraded — which for an agentic workload with a large standing context is the more expensive failure. Record `tg128`, `pp512`, `pp8192`, and cold TTFT; add `pp32768` only once the model is known to fit, since it is meaningless on a configuration that cannot load.

Judge against evidence rather than an invented threshold. The comparison points, in order of preference: the 9B and 12B baselines already in `benchmarks/` scaled by parameter count and bandwidth; the same GGUF on a second qualified backend; and the §1 bandwidth ceiling in `TUNING.md` for the split under test. A decode figure near the ceiling with prefill an order of magnitude below the existing baselines is the signature of a prefill-path problem, not a memory one. Abort the expensive profiles when *either* axis is far below its comparison point.

**Sweeps.** Record each independently, one variable at a time:

- `ubatch_size` across {288, 512, 1024, 2048}. Never sweep inside 65..256: that band collapses throughput by up to 40x on these models. Both `models.schema.json` and `bench-llama.ps1` refuse it, so a value inside the band in a report means the report is wrong.
- `spec_draft_n_max` across {0, 2, 3, 5, 7}, recording draft acceptance rate alongside tokens per second. Confirm first that the GGUF's MTP block (`blk.64` on Qwen3.8-27B) is quantized to Q5_K or higher — `gguf-dump | grep 'blk\.64\.'`. At Q4_K the measured acceptance is 0% and speculation fails completely, so a sweep over draft depth on such a build measures nothing. Confirm too that the head is present at all (`grep -i nextn`); a build that lost it during conversion serves normally and never speculates. Acceptance also collapses at particular `--ctx-size` values on a ~2048-token period (llama.cpp #23658), so record it at the exact shipped context rather than at a convenient round number. Speculative decoding is the single largest lever on RDNA4 for this architecture; it is also the least stable, so every MTP profile needs a `-nomtp` control run for quality comparison. On a partially offloaded split the optimum is shallow (2-3) and a depth of 7 can be slower than no speculation at all, so sweep the low end and do not assume monotonicity. The optimum moves with the `-ot` pattern; re-sweep after changing the split.
- `--threads` {8, 16} whenever part of the model is CPU-resident. With everything in VRAM the thread count is nearly irrelevant and the scripts default to 16; once CPU compute binds, SMT contention on this 8C/16T part often makes 8 the faster choice.
- `ROCBLAS_USE_HIPBLASLT` across {0, 1} via `bench-llama.ps1 -RocBlasUseHipBlasLt`. The pinned runtimes disable it; that choice has never been measured on gfx1201.
- Weight split: vary `-NGpuLayers` or the `-ot` pattern and record `llm_load_tensors: CPU buffer size` from the load log against the resulting decode rate. This is the curve that decides whether a quant fits the hardware.

**Agentic context restoration.** Two identical prompts are the minimum test, not a sufficient one: real harness traffic grows a large context by small increments interleaved with tool calls, and that is the pattern checkpoints must survive. Drive a synthetic multi-turn scenario against a live server:

```
60k base context
  → +2k user increment → response
  → tool call → +2k tool result
  → +2k increment → tool call → +2k tool result
  → continue for at least six turns
```

Record, per turn, how many prompt tokens the server actually processed against how many are new. A healthy run processes roughly the increment; a broken one reprocesses the whole context every turn. **This is a regression gate, not an observation:** fail the run when processed tokens approach total context on any turn after the first.

Note the upstream defects in `TUNING.md` §1.4 before interpreting a failure — context checkpoints are reported broken on hybrid/recurrent models, so a failing result may be the runtime rather than the configuration. Use synthetic fixtures only; never store real prompts.

`scripts/v2/Measure-V2AgenticReuse.ps1` runs this scenario and emits the verdict
`incremental_reuse_pass` or `incremental_reuse_fail` with the supporting numbers.
It reads `timings.cache_n` and `timings.prompt_n` from the server directly, and
its own fixture is a seeded synthetic filler so the two sides of an A/B see byte-
identical input. `scripts/v2/Test-V2AgenticHarness.ps1` checks the harness itself
against scripted servers in both states, because a gate that cannot fail is not a
gate.

**Metrics for agentic long context.** Decode throughput alone answers the wrong
question. Report these separately, per turn:

| Metric | Source |
|---|---|
| Processed prompt tokens | `timings.prompt_n` |
| New prompt tokens | Context growth since the previous turn |
| Reuse ratio | `cache_n / (cache_n + prompt_n)` |
| Prompt throughput | `timings.prompt_per_second` |
| TTFT / prefill latency | `timings.prompt_ms` |
| Decode throughput | `timings.predicted_per_second` |
| Effective turn latency | Wall clock for the request |
| MTP acceptance | `draft_n_accepted / draft_n` |
| Peak VRAM / RAM / commit | Sampled per turn |

Alongside them record `agentic_turn_efficiency = new_prompt_tokens /
processed_prompt_tokens`. Ideal is ≈1.0 — 2k new against 2.1k processed is 0.95;
2k new against 182k processed is 0.011. Treat it as evidence to read next to the
raw counts and the server log, not as a threshold to tune against.

## Runtime A/B: upstream against buun-llama-cpp

The fork adopted in ADR 0010 is qualified against upstream, not against
expectations. Run the identical fixture on both and diff the reports with
`scripts/v2/Compare-V2Runtimes.ps1`, which refuses two reports whose fixture
hash, seed, base context, increment, turn count, output cap or threshold differ.

Everything except the runtime is held: model, GGUF, quantization, context, KV
types, batch, ubatch, tensor split, MTP settings, sampling, template, prompt,
tool sequence and hardware. Note that the fork's own defaults differ from
upstream's — see `TUNING.md` §1.5 — so the profile pins them rather than omitting
them; an omitted field is a changed variable here, not a neutral one.

Compare: full re-prefill occurrences, processed against new prompt tokens, TTFT,
prompt tokens/s, decode tokens/s, MTP acceptance, peak VRAM, RAM and commit, and
stability across turns. "It felt faster" is not a result.

Qualification is progressive, and each gate is a stop:

| Gate | Context | What it establishes |
|---|---|---|
| A | Short | ROCm and gfx1201 active, no severe Gated DeltaNet CPU fallback, MTP initializes, prefill and decode sane |
| B | ~60k | Incremental reuse across at least six turns with tool calls — the decisive test |
| C | 128k then 192k | Reuse still holds, memory stable, no full re-prefill, no runtime fallback, MTP healthy |
| D | ~256k | Reuse holds near the limit across several turns of incremental growth |

Do not start at 256k. If the Gated DeltaNet kernel falls back at gate A, nothing
below it is worth measuring.

**Cache restoration.** A prompt-cache measurement is meaningless as a single run. Issue the same ~90k-token prompt twice against a live server and record `prompt_tps` for both. The first is prefill; the second must be a checkpoint restore, and the ratio between them is the metric. If they are within an order of magnitude, checkpoints are not being restored and `--cache-ram` is only consuming commit. Note that `--cache-reuse` cannot produce this result on a recurrent model, and that any change to the system prompt invalidates every checkpoint — the harness prefix must be byte-stable across turns for the measurement to mean anything.

When a measurement comes back bad, `TUNING.md` has the bottleneck decision tree and the bandwidth ceiling to compare it against.

## Performance gates

- Edge overhead p95 below 50 ms for non-load time and below 5% throughput regression versus direct serving.
- Edge and router ready within 30 seconds of interactive logon.
- First cold response completes within 60 seconds.
- No unplanned runtime exit, orphan, duplicate model load, or silent model swap.
- Admission reserves remain at least 1 GiB VRAM, 4 GiB commit, and 2 GiB physical RAM beyond the measured profile peak.

## Soak

Run for 72 continuous hours with at least 500 mixed Responses/Chat requests and 20 complete load/unload cycles. Include concurrent Codex/OpenCode use, cancellation, queue overflow, router restart, edge restart, and an interactive-logon restart.

Acceptance is at least 99% success after excluding deliberate 4xx/429 policy tests, with zero unrecovered crash, external request, credential finding, or model duplication.

## Report shape

```json
{
  "schema_version": 1,
  "manifest_sha256": "...",
  "model_sha256": "...",
  "runtime_sha256": "...",
  "started_utc": "...",
  "scenario": "responses_streaming",
  "requests": 100,
  "success": 100,
  "p95_ttft_ms": 0,
  "p95_edge_overhead_ms": 0,
  "peak_vram_gib": 0,
  "peak_commit_gib": 0,
  "peak_ram_gib": 0,
  "idle_commit_gib": 0,
  "external_connections": 0,
  "credential_findings": 0
}
```

`peak_commit_gib` is a **delta**, not an absolute, so `idle_commit_gib` must accompany it or the number is not reproducible across machine states. Capture it with a **cold prompt cache** — the first request after process start — because admission adds `cache_ram_mib` separately as its full ceiling; a warm-cache measurement charges the same gibibytes twice. See `MODEL_PROMOTION.md`.

Reports are evidence, not configuration. Promotion values must be reviewed into the manifest deliberately.
