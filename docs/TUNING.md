# Inference diagnosis and tuning

`BENCHMARKS.md` defines how to measure and what has to pass. This document is for
the other situation: something is already measured, the number is bad, and the
question is why. It is written against the RX 9070 XT / Ryzen 7 7700X target, and
every constant in it is either measured in this repository or cited.

## 1. Know the ceiling before calling anything slow

Autoregressive decode is memory-bandwidth bound: every token reads the whole
dense weight set once. That makes the upper bound arithmetic, not opinion.

```
t/s_ceiling = 1 / ( W_gpu / BW_gpu  +  W_cpu / BW_cpu )
```

| Constant | Value | Source |
|---|---|---|
| `BW_gpu` | 640 GB/s | RX 9070 XT, 256-bit GDDR6 @ 20 Gbps |
| `BW_cpu` | ~62 GB/s | 7700X is single-CCD; measured DDR5 reads land ~59–70 GB/s regardless of the 96 GB/s the DIMMs are rated for |
| Real utilization | ~80% of theoretical | Observed for dense 27B on RDNA4 in llama.cpp discussion #21043 |

**The cost of offload, for a 15.7 GiB weight set:**

| GPU / CPU split (GiB) | Theoretical | At 80% | Share of decode time spent on DDR5 |
|---|---|---|---|
| 15.7 / 0 | 38.0 t/s | 30.4 t/s | 0% |
| 14.7 / 1.0 | 23.8 t/s | 19.1 t/s | 41% |
| 13.7 / 2.0 | 17.4 t/s | 13.9 t/s | 60% |
| 12.7 / 3.0 | 13.6 t/s | 10.9 t/s | 71% |
| 11.3 / 4.4 | 10.5 t/s | **8.4 t/s** | 80% |
| 9.7 / 6.0 | 8.3 t/s | 6.7 t/s | 86% |

**The first gibibyte is the expensive one.** Moving 1 GiB of a 15.7 GiB model off
the GPU costs 37% of throughput, because DDR5 on this platform is roughly ten
times slower than GDDR6. By 4.4 GiB, four fifths of every token is spent waiting
on system RAM.

Compare against a quantization small enough to stay resident:

| Configuration | At 80%, no speculation |
|---|---|
| IQ4_XS, 4.4 GiB offloaded | 8.4 t/s |
| UD-Q3_K_XL (~12.0 GiB), fully resident | 39.7 t/s |

A fully resident Q3 is roughly **4.7x faster** than an offloaded Q4 on this
hardware, before speculative decoding. Section 1.1 shows why MTP does not simply
scale both sides by the same factor: the offloaded side reaches its own best
multiplier only at a much shallower draft depth, and at the shipped depth it can
be slower than not speculating at all. Whether the quality difference is worth
that is a judgment call, but it should be made against these numbers rather than
against an assumption that Q4 is "obviously better". Measure both with
`run-profile-quality-eval.py` before deciding.

## 1.1 What speculative decoding does, and does not, amortize

Speculative decoding verifies k drafted tokens in a single forward pass. That
pass reads the weight set **once** but performs **k times** the arithmetic. So
MTP amortizes memory reads and does nothing for compute:

```
time_per_token(k) ≈ max( bytes_read / BW / k , flops_per_token / compute )
```

The consequence is asymmetric:

- **On the GPU**, compute capacity is enormous relative to 640 GB/s. The
  bandwidth term dominates at every practical k, so amortization is close to
  linear until acceptance rate limits it. This is why RDNA4 reports ~2x at k=7.
- **On the CPU-resident portion**, `-ot "…=CPU"` runs that matmul on the CPU, not
  merely stores it there. Eight Zen 4 cores have a far tighter compute-to-
  bandwidth ratio, so the compute term binds after a small k. Past that point
  additional drafting costs linearly while accepted tokens grow sub-linearly, so
  **speculation becomes net-negative**.

MTP is still worth having when offloaded — it is the *draft depth* that has to
change, not the decision to speculate.

**Sensitivity to draft depth.** Modelled on the 11.3 / 4.4 GiB split with 80%
per-token acceptance, across a range of plausible CPU quantized-GEMM throughput
(the one constant here that this repository has never measured):

| CPU GEMM | best `draft_n_max` | gain vs no MTP | gain at `draft_n_max: 7` |
|---|---|---|---|
| 200 GFLOP/s | 1 | 1.80x | **0.77x — slower than not speculating** |
| 300 GFLOP/s | 2 | 2.04x | 1.13x |
| 500 GFLOP/s | 3 | 2.69x | 1.82x |
| 800 GFLOP/s | 5 | 3.25x | 2.76x |

Two things follow, and both are actionable without knowing the true constant:

1. **The optimum for an offloaded split is `draft_n_max` 2–3, not the 7 that is
   optimal for a fully resident model.** The value is wrong in a direction that
   costs real throughput, and at the pessimistic end a depth of 7 is worse than
   leaving MTP off entirely.
2. **The optimum moves with the split.** Change the `-ot` pattern and the best
   draft depth changes with it. They cannot be tuned independently.

Treat the table as a shape, not a prediction. It assumes GPU and CPU phases
serialize and that CPU GEMM efficiency is constant in k; in practice larger
batches reuse cache better, which makes the pessimistic rows somewhat too
pessimistic. Measure with the `qwen38-27b-iq4xs-kv-q8-96k` / `-nomtp` pair and
sweep `spec_draft_n_max` over {0, 2, 3, 5, 7}, recording acceptance rate next to
throughput.

**Threads stop being free.** While everything sits in VRAM the CPU thread count
barely matters, and `bench-llama.ps1` defaults to `-Threads 16`. Once the CPU
holds part of the model and the compute term binds, that default is a tuning
variable: the 7700X is 8C/16T, and SMT contention often makes 8 threads beat 16
on quantized GEMM. Sweep it alongside the draft depth.

**Do not let the draft head get offloaded.** The MTP head runs on every
speculation step, so pushing it to the CPU multiplies its cost by k and can erase
the entire gain. The shipped `-ot` pattern is scoped to `blk\.N\.ffn_.*` and
cannot match it, but a broader regex written by hand can. Check the load log
after any change to the pattern.

**Use the ceiling as a diagnostic.** If measured decode is close to the table,
the configuration is working and only a different configuration will help. If it
is far below, something in section 2 is wrong and no amount of re-quantizing will
fix it.

## 2. Bottleneck decision tree

### The model will not start

Read `reason` from `GET /api/v1/status` and use the table in `RUNBOOK.md`
"Health interpretation". Admission refusals are deliberate and each names its
own remedy. `resource_measurement_required_for_host_memory` in particular never
resolves on its own — it means an offloading model has no measured profile.

### Decode is far below the ceiling

Check in this order; each step is cheap and rules out the next.

1. **Is the Gated DeltaNet kernel running on the GPU?** This is the one failure
   that makes everything else pointless. `GGML_OP_GATED_DELTA_NET` merged with
   CPU and CUDA backends only; Vulkan falls back to CPU and on gfx1151 the HIP
   path measures at CPU speed. Run the `qwen38-27b-iq4xs-sanity` profile — no
   offload, short context, no MTP — and compare against the 9B baselines in
   `benchmarks/`. A hybrid 27B landing near ~12 t/s with *nothing* offloaded is
   the signature of this failure, not of a memory problem.

2. **Are the weights where you think they are?** The load log is authoritative;
   the manifest is only intent.
   ```
   llm_load_tensors: ROCm0 buffer size = ...
   llm_load_tensors:   CPU buffer size = ...
   ```
   A `-ot` regex that matches nothing fails silently and leaves everything on the
   GPU until it OOMs; one that matches too much quietly halves your throughput.
   Compare the CPU figure against the split you intended, and check that no
   full-attention layer (`blk.3, 7, 11, … 63` on a 3:1 hybrid) landed on the CPU.

3. **Are the weights being paged?** Free physical RAM below the process working
   set turns DDR5 reads into SSD reads, which is roughly another 10x. The edge
   now refuses this outright with `insufficient_physical_memory`, but only for a
   model that recorded `peak_ram_gib`. Sustained disk activity during decode,
   with no file being written, means paging.

4. **Is `ubatch` inside the dead band?** 65–256 collapses throughput up to 40x on
   hybrid models. `models.schema.json` and `bench-llama.ps1` both refuse the
   band, so this only bites when running `llama-server` by hand.

5. **Is MTP on, and is it accepting?** Speculative decoding is the largest single
   lever on RDNA4 for this architecture. A low acceptance rate means the draft
   head is producing tokens the model rejects, and the speculation is pure
   overhead — sweep `spec_draft_n_max` over {3, 5, 7} and keep the acceptance
   rate in the report next to the throughput. If acceptance is high but the
   speedup is small — or negative — that is section 1.1, not a bug: on an
   offloaded split the draft depth that is optimal for a resident model is past
   the point where speculation starts costing more than it returns. Try depth 2
   before concluding MTP does not help. Confirm the draft head itself did not
   land on the CPU.

### Prefill is slow but decode is fine

These are separate paths and separate fixes. Prefill is compute-bound and scales
with context; decode is bandwidth-bound and does not.

- A cold ~90k-token prompt is *expected* to take minutes. That is what the prompt
  cache exists to avoid — see section 3.
- If the *second* identical prompt is also slow, checkpoints are not being
  restored. Confirm `--cache-ram`, `--ctx-checkpoints`, and
  `--checkpoint-every-n-tokens` are actually in the generated `cmd:`, and that
  the harness prefix did not change between turns.
- `--cache-reuse` will not help here and is not a fix to reach for: it cannot
  work on a model with recurrent state.

### Quality regressed without a configuration change

- **MTP is the first suspect.** llama.cpp has open reports of speculative state
  being retained between requests, producing non-deterministic output that drifts
  over a long session. Every MTP profile has a `-nomtp` control for exactly this;
  run it before blaming the quantization.
- **Context shift on a recurrent model.** `context_shift: false` is mandatory for
  hybrids. The schema enforces it alongside `spec_decoding`, but a hand-run
  server has no such guard.
- **Template drift.** `--jinja` with the wrong chat template produces plausible
  text and broken tool calls. Validate with the forced-tool-call case in
  `run-profile-stress-eval.py`, not by reading responses.

## 3. Levers, ordered by expected effect

Work down this list. The ordering reflects the bandwidth arithmetic in section 1,
not preference.

| Lever | Typical effect | Cost |
|---|---|---|
| Eliminate offload — pick a quant that fits entirely in VRAM | up to ~4.7x, and it raises the ceiling MTP then multiplies | quantization quality |
| MTP speculative decoding (`spec_decoding`) | ~2x fully resident at depth 7; ~1.8–3x offloaded but only at depth 2–3, see 1.1 | stability; needs a `-nomtp` control |
| Prompt cache / context checkpoints | prefill only, but can be orders of magnitude | host RAM, charged to commit in full |
| Reduce KV cache (`q8_0` → `q4_0`, or less context) | frees VRAM → *less offload* → compounds with lever 1 | long-context recall |
| `ubatch` sweep {288, 512, 1024, 2048} | single-digit %, occasionally large on hybrids | none |
| `ROCBLAS_USE_HIPBLASLT` A/B | unmeasured on gfx1201 | none |

Note how levers 1 and 4 interact: KV quantization is not really a context
decision on a VRAM-constrained model, it is an *offload* decision. Halving KV at
128k frees 2 GiB of VRAM, and per section 1 the first 2 GiB brought back from
system RAM is worth more than anything below it on this list.

## 4. Traps

- **Reading throughput from the wrong place.** Never derive tokens/second by
  dividing a figure that is already a rate. Use the native `prompt eval time` /
  `eval time` counters.
- **Sweeping two variables at once.** The interactions here are not additive —
  `ubatch` behaves differently with MTP on, and offload changes which end is the
  bottleneck. One variable per run, recorded.
- **Trusting a benchmark taken on a busy machine.** `peak_commit_gib` is a delta
  against the idle baseline, and on the target machine that baseline was 31.82 of
  42.30 GiB. Record `idle_commit_gib` with every result or the numbers are not
  comparable across sessions. See `RUNBOOK.md` §11.0.
- **Assuming a bigger model at a lower quant beats a smaller one.** Usually true
  when both are resident; frequently false when the bigger one has to offload,
  because the penalty is bandwidth, not parameters.
