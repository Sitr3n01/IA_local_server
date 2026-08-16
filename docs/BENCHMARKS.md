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

**Gate zero — is the kernel accelerated at all.** `GGML_OP_GATED_DELTA_NET` landed in llama.cpp with CPU and CUDA backends only. Vulkan falls back to CPU, and on RDNA 3.5 (gfx1151) the HIP path measures at CPU speed. Whether gfx1201 escapes that is unverified here. Run the `qwen38-27b-iq4xs-sanity` profile first: no offload, short context, no speculative decoding, so the number isolates kernel throughput. If `tg128` lands near the CPU-fallback range rather than in the tens of tokens per second, stop — no amount of quantization or cache tuning recovers a kernel that is not running on the GPU, and the remaining profiles are not worth the hours.

**Sweeps.** Record each independently, one variable at a time:

- `ubatch_size` across {288, 512, 1024, 2048}. Never sweep inside 65..256: that band collapses throughput by up to 40x on these models. Both `models.schema.json` and `bench-llama.ps1` refuse it, so a value inside the band in a report means the report is wrong.
- `spec_draft_n_max` across {0, 2, 3, 5, 7}, recording draft acceptance rate alongside tokens per second. Speculative decoding is the single largest lever on RDNA4 for this architecture; it is also the least stable, so every MTP profile needs a `-nomtp` control run for quality comparison. On a partially offloaded split the optimum is shallow (2-3) and a depth of 7 can be slower than no speculation at all, so sweep the low end and do not assume monotonicity. The optimum moves with the `-ot` pattern; re-sweep after changing the split.
- `--threads` {8, 16} whenever part of the model is CPU-resident. With everything in VRAM the thread count is nearly irrelevant and the scripts default to 16; once CPU compute binds, SMT contention on this 8C/16T part often makes 8 the faster choice.
- `ROCBLAS_USE_HIPBLASLT` across {0, 1} via `bench-llama.ps1 -RocBlasUseHipBlasLt`. The pinned runtimes disable it; that choice has never been measured on gfx1201.
- Weight split: vary `-NGpuLayers` or the `-ot` pattern and record `llm_load_tensors: CPU buffer size` from the load log against the resulting decode rate. This is the curve that decides whether a quant fits the hardware.

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
