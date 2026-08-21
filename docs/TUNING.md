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

## 1.2 Estimating the memory envelope before downloading anything

The envelope is computable from the model card and one measured anchor. Doing it
first costs minutes and can reject a configuration before a 15 GiB download.

**VRAM.** Three of the four terms are exact:

```
VRAM ≈ W_gpu  +  KV(ctx, type)  +  GDN_state  +  compute_buffers
```

`KV` is pure arithmetic from the architecture — for a 3:1 hybrid with 16
full-attention layers, 4 KV heads and `head_dim` 256:

| ctx | `f16` | `q8_0` | `q4_0` |
|---|---|---|---|
| 64k | 4.00 | 2.12 | 1.12 |
| 96k | 6.00 | 3.19 | 1.69 |
| 128k | 8.00 | 4.25 | 2.25 |
| 256k | 16.00 | 8.50 | 4.50 |

`GDN_state` is per-sequence and constant in context — roughly 0.1–0.3 GiB for 48
linear layers, small enough to treat as a rounding term but confirm from the load
log. `compute_buffers` runs ~0.5–1.2 GiB at `ubatch` 512.

Budget **~14.8 GiB usable**, not 15.92: the Windows desktop holds the difference.

**Commit follows VRAM, not RAM.** This is the non-obvious one. From the 9B
anchor: a model with *zero* CPU-resident weights still consumed 8.06 GiB of
commit against 6.66 GiB of dedicated VRAM — a fixed overhead of ~1.4 GiB above
VRAM. That is WDDM behaviour: Windows commits system memory to back video
allocations so they remain evictable. So:

```
commit_delta ≈ VRAM + W_cpu + cache_ram_mib + ~1.4 GiB
```

The practical consequence is severe and easy to miss: **a 27B costs ~16 GiB of
commit even with no offload and no prompt cache**, purely because it fills the
card. Eliminating offload solves throughput, not commit.

**RAM** splits into two figures that are often confused:

- **Must stay resident:** `W_cpu + cache_ram_mib + ~1 GiB` of process overhead.
  These are read every token or hold live state; if they page, throughput
  collapses. This is what `peak_ram_gib` should record.
- **Working set including the mapping:** llama.cpp mmaps the GGUF and the pages
  stay resident after load — the 9B's working set was 0.88x its file size with
  everything on the GPU. Those pages are clean and file-backed, so Windows can
  drop them under pressure without paging out. Alarming in Task Manager,
  mostly harmless.

**Worked example** — IQ4_XS, 96k, KV `q8_0`, 4.4 GiB offloaded:

| | GiB |
|---|---|
| VRAM: 11.30 weights + 3.19 KV + 0.2 GDN + 0.8 buffers | **15.49** |
| Commit: 15.49 VRAM + 4.4 CPU weights + 2.0 cache + 1.4 | **23.29** |
| RAM that must stay resident | **7.40** |

Two things fall out of this that are not obvious from the manifest:

1. **15.49 GiB of VRAM does not fit** in the ~14.8 GiB usable. This configuration
   needs ~5.1 GiB offloaded, not 4.4.
2. **128k with `q4_0` KV is cheaper than 96k with `q8_0`** (2.25 vs 3.19 GiB), so
   it needs *less* offload — ~4.15 GiB — and runs faster despite the longer
   context. On a VRAM-bound hybrid, KV precision buys context almost for free but
   costs throughput indirectly, through the offload it forces.

## 1.3 Choosing a quantization: the quality/speed frontier

The instinct on a VRAM-bound card is to drop a whole quantization level. Section
1.1's arithmetic says that is usually the wrong first move, because the frontier
is far steeper in *file size* than in *bit width*.

Modelled against the ~14.8 GiB usable budget, at 80% bandwidth utilization:

| Build | Weights | ctx / KV | offloaded | base | ~MTP |
|---|---|---|---|---|---|
| IQ4_XS, 15.7 GiB | 15.7 | 96k / `q8_0` | 5.09 | 7.6 | ~17 |
| IQ4_XS, 15.7 GiB | 15.7 | 128k / `q4_0` | 4.15 | 8.8 | ~19 |
| IQ4_XS, 15.1 GB (14.1 GiB) | 14.1 | 128k / `q4_0` | 2.51 | 12.7 | ~28 |
| IQ4_XS, 14.7 GB (13.7 GiB) | 13.7 | 128k / `q4_0` | 2.14 | **14.2** | **~31** |
| IQ4_XS, 14.7 GB (13.7 GiB) | 13.7 | 96k / `q4_0` | 1.58 | 16.8 | ~37 |
| UD-Q3_K_XL, ~12.0 GiB | 12.0 | 128k / `q4_0` | 0.45 | 29.4 | ~65 |

**Two IQ4_XS builds one gibibyte apart differ by 60% in throughput.** Community
IQ4_XS builds of this model are reported between roughly 14.7 and 15.7 GB and
their quality differs by imatrix calibration, not only by size — so "IQ4_XS" is
not one thing.

**But size is not the selection criterion, and picking the most compact build is
a trap.** See the `blk.64` rule below: a build chosen purely for compactness will
usually have quantized the MTP block to Q4_K, which takes draft acceptance to
zero and costs the entire ~2x speculative speedup. Weigh the two together — a
slightly larger build that keeps `blk.64` at Q5_K or above beats a smaller one
that does not, by a wide margin.

**The authoritative reference is `Qwen/Qwen3.8-27B`** — Alibaba's own repository,
Apache-2.0. Architecture, context handling, and recommended sampling come from
there; every GGUF is a derived artifact whose publisher, revision, and per-tensor
choices have to be recorded and verified separately. Check the official card for
a first-party GGUF before reaching for a community rebuild.

**What Q3 actually costs.** For 27B-class Qwen models, community measurements put
`Q3_K_XL` at KL divergence above 0.1 with 85–90% top-token agreement against the
unquantized model. The rule of thumb those same measurements use is KLD below
0.05 for "indistinguishable from BF16" and above 0.08 for "quality drops". So
yes — Q3 is a real quality cost on this model class, not a free win, and it is
the level at which degradation stops being subtle.

Quantization method matters as much as bit width. A 3-bit build using imatrix
calibration with per-tensor overrides has been reported at 92.4% top-1 agreement
at 13.8 GB — materially better than the generic `Q3_K_XL` figures above, and
recommended by its author specifically for 16 GB cards. Bit width alone does not
predict quality; how the bits were allocated does.

That makes the sensible frontier:

1. **Require `blk.64` at Q5_K or above.** This filters the candidate list before
   any other consideration, because everything below it is worth ~2x.
2. **Use KV `q4_0`, not `q8_0`.** Per §1.2 this buys longer context *and* less
   offload simultaneously. It is the highest-leverage serving flag here.
3. **Among builds that pass (1), prefer the more compact and better calibrated
   one** — imatrix plus per-tensor overrides over a uniform quantization at the
   same nominal level.
4. Only then trade bit width, and only with a quality eval that justifies it.

Sizes on model cards are ambiguous between GB and GiB, and the difference is ~7%
— which at this point on the curve is worth several tokens per second. Take the
real byte count after download and recompute rather than trusting the card.

**Three failure modes that silently cost the entire MTP speedup:**

- **The MTP block must be quantized to Q5_K or higher.** This is the single most
  consequential per-tensor fact about this model. On Qwen3.8-27B the MTP head is
  `blk.64` — 15 tensors sitting after the 64 main blocks. Reported measurements
  on this model family are unambiguous: a build with `blk.64` at **Q4_K yields 0%
  draft acceptance** and speculation fails completely, while builds keeping those
  tensors in the Q5_K–Q8_0 range reach **73–74% acceptance**. The look-ahead
  projection has no error-correction path, so its precision is not negotiable the
  way the main stack's is.

  A nominal quantization label tells you nothing about this. Inspect the tensors:
  ```
  gguf-dump model.gguf | grep "blk\.64\."
  ```
  Every one of them should read Q5_K or better. This is what "per-tensor
  overrides" in a well-built quant buys you, and why a generic uniform Q4 build
  of this model cannot speculate.

- **The quant may not contain the MTP head at all.** Conversion tooling has been
  reported to drop tensors it does not recognize as part of a vanilla
  transformer block, and the community publishes explicitly `-MTP-GGUF` builds
  because of it. Verify before benchmarking:
  ```
  gguf-dump model.gguf | grep -iE "nextn|mtp"
  ```
  Expect the `…nextn_predict_layers` metadata key and `blk.N.nextn.*` tensors
  (`eh_proj`, `enorm`). A build without them will run fine and simply never
  speculate.
- **Acceptance collapses at particular `--ctx-size` values.** llama.cpp issue
  #23658 documents draft acceptance falling to near zero at specific context
  sizes on a ~2048-token period, with a 256-token difference separating 1.91x
  from 1.12x, and 0% acceptance at some values. It is unfixed and independent of
  quantization. Never assume a context size is fine: record the acceptance rate
  at the exact `context_tokens` you ship, and if it is poor, try ±256 and ±2048
  before concluding MTP does not work.

## 1.4 Context checkpoints do not currently work on this architecture

Upstream status, **unverified here**, and it invalidates the obvious use of the
host-RAM budget:

- llama.cpp **#24055** — context checkpoints are created and then immediately
  invalidated on hybrid/recurrent models, with the server logging *"forcing full
  prompt re-processing due to lack of cache data (likely due to SWA or
  hybrid/recurrent memory)"*. `--checkpoint-min-step` has no effect on such
  models; `--cache-ram` allocates but nothing persists.
- llama.cpp **#22384** — root cause: the checkpoint search tests
  `cur.pos_min < pos_min_thold`, but on a recurrent model `pos_min` always equals
  the full sequence length, so the test can never pass. A fix exists in a fork
  and is **not merged**.

Reported consequence: a 15K-token conversation reprocesses everything per turn,
seconds instead of milliseconds, which the reporter describes as making agentic
workflows unusable.

**What this means for configuration.** Until a build demonstrably restores
checkpoints on this architecture, `cache_ram_mib` buys nothing on Qwen3.8 and
should be left unset. That is not merely neutral: the gate charges it to commit
in full, and commit is the binding constraint on this machine (§1.2), so an
inert cache actively costs admission headroom.

The multi-turn scenario in `BENCHMARKS.md` is the acceptance test for this. Run
it against any candidate runtime before setting `cache_ram_mib`; if the second
turn reprocesses the whole context, the feature is still broken in that build
regardless of what the flags accept.

## 1.5 The buun-llama-cpp runtime, and what it does not change

`spiritbuun/buun-llama-cpp` carries a correction for §1.4. ADR 0010 adopts a
pinned commit of it as a **separate, experimental runtime**; the upstream build
stays exactly as it is and remains the baseline and the fallback. Four things
about it change how you tune against it.

**Its defaults are not upstream's.** The fork ships `cache_ram_mib = 8192`,
`cache_idle_slots = true`, and dynamic variable-bitrate KV on both cache sides.
Omitting a manifest field therefore does not mean "behave like upstream" — it
enables a fork behaviour, and the result gets attributed to whatever you were
actually testing. `Assert-V2ManifestSemantics` refuses a model on a fork runtime
that leaves `context_shift`, `kv_unified`, `cache_ram_mib`, `cache_idle_slots`,
`ctx_checkpoints` or `checkpoint_min_step` undeclared, and an explicit
`--cache-type-k q4_0` is what turns the variable-bitrate cache off.

**Context checkpoints are not the prompt cache.** They are different mechanisms
and the first qualification uses only the first. `cache_ram_mib: 0` and
`cache_idle_slots: false` are pinned, so what is being measured is whole-state
reuse inside a live session — no host RAM budget, no cross-process persistence,
no `/slots`. If you set `cache_ram_mib` on a fork profile you are running a
different experiment, and §1.2's commit arithmetic applies to it in full.

**Checkpoint count and spacing are a sweep, not a setting.** Start from
`ctx_checkpoints` in {32, 64, 128} and `checkpoint_min_step` in {256, 512, 1024,
2048}, and stop as soon as the results eliminate a region — the grid is not worth
running blind. Pick the **smallest** count that gives consistent reuse without a
relevant re-prefill. More checkpoints cost commit, and commit is what binds here;
fitting in RAM is not a reason to allocate.

**Speed is not the metric.** Judge on processed prompt tokens against new prompt
tokens per turn, taken from the server's `cache_n` and `prompt_n` counters via
`Measure-V2AgenticReuse.ps1`. A runtime that decodes faster while re-prefilling
200k tokens a turn has failed. Everything in §1 and §3 still applies to decode,
but it applies *after* this question is settled.

`/slots` save and restore is out of scope and the runtime is not marked failed
for lacking it. It is disk persistence between processes; the problem here is
reuse within an active session.

**Returning to upstream** is a manifest edit and nothing else: set the model's
`runtime` back to the upstream entry and regenerate. No code path knows the fork
by name.

## 1.6 The adapter is shared, and the load log is not a memory budget

`llm_load_tensors` and the `llama_context` buffer lines are accurate. Measured
against the adapter on the reference workstation, llama.cpp's charge to its own
process (13012 MiB) matches the model's marginal dedicated cost (12961 MiB) to
within 0.4%. Nothing is under-reported.

What those lines cannot see is the rest of the card. Sampled with no model
loaded, this workstation's desktop compositor and browser hold **2967-3126 MiB
of dedicated VRAM**. A 12.7 GiB model on a 16.3 GiB adapter therefore sits at
97-98% occupancy, not at the ~80% the load log implies.

| Quantity, 4-block split, `ub` 288 | Value |
|---|---:|
| Adapter dedicated, idle | 2967 MiB |
| Adapter dedicated, peak under load | 15928 MiB |
| Marginal — the model's own cost | 12961 MiB |
| llama.cpp's charge to the process | 13012 MiB |

**The budget a model actually gets on this machine is about 16.3 − 3.0 =
13.3 GiB, and it moves with whatever is on screen.** Past the adapter's limit
the AMD driver does not fail an allocation; it pages the excess over PCIe.
Prompt processing loses a factor of three, decode barely moves, and no error is
raised anywhere.

Two consequences for measurement:

- **Sample idle and peak, and report both.** Their difference is the model's
  cost; the peak alone is what the budget applies to. A report carrying only one
  cannot distinguish a large model from a busy desktop.
  `scripts/v2/Measure-V2MemoryProfile.ps1` and
  `scripts/v2/Measure-V2ContextFootprint.ps1` do this.
- **Watch shared GPU memory, not just dedicated.** Dedicated saturates and then
  stops moving; shared is what keeps climbing. It is the only visible signal that
  paging has started.

`resources.peak_vram_gib` holds the *marginal* figure, and `vramReserveGiB` in
`internal/edge/capacity.go` is what accounts for everything else on the card. It
was 1.0 GiB, which was an estimate and too small by a factor of three; it is now
3.0 GiB, measured. `cia-edge` also reports a live verdict on `/api/v1/status`
and as `cia_edge_gpu_*` metrics, because a static manifest cannot know what the
desktop is holding today.

### The `ubatch` result on this hardware

The compute buffer is charged to the adapter, so `ubatch` moves memory pressure
directly. Measured on the 4-block split, KV `q8_0`, three repetitions:

| `ubatch` | pp512 | pp8192 | tg128 | dedicated | shared | verdict |
|---:|---:|---:|---:|---:|---:|---|
| 256 | 997.92 | 281.09 | 23.45 | 15886 | 845 | elevated |
| 288 | 956.82 | 269.00 | 23.66 | 15938 | 882 | elevated |
| 384 | 292.13 | 283.11 | 23.31 | 15824 | 1185 | pressured |
| 512 | 299.72 | 289.36 | 23.68 | 15885 | 1330 | pressured |

Dedicated VRAM is flat. Shared memory rises monotonically with `ubatch`, and
short-prompt prefill collapses by a factor of three once it does. Decode is
unaffected across the whole band.

The advantage does not extend to long prompts. Measured separately at three
repetitions each:

| `ubatch` | pp16384 | pp32768 | dedicated | shared |
|---:|---:|---:|---:|---:|
| 512 | 268.36 | 239.98 | 16057 | 2142 |
| 288 | 249.90 | 222.04 | 15932 | 1906 |

Past roughly 16k the KV cache rather than the compute buffer is what overflows,
so `ubatch` stops being the lever and 288 costs a consistent ~7%. Context choice
is the lever there; see 1.7.

**288 is the default**: it triples short-prompt prefill, costs at most 7.5%
anywhere else, leaves decode unchanged, lowers shared memory at every prompt
length, and stays outside the dead band the schema refuses.

> **A documented claim this contradicts.** `model-test-matrix.json` records that
> `ubatch` in 65..256 collapses throughput by up to 40x on hybrid models, and
> both `models.schema.json` and `bench-llama.ps1` refuse the band. Measured here,
> 256 did not collapse — it was the fastest configuration at pp512 and level at
> pp8192. One measurement on one machine does not overturn the note, and the
> schema still refuses the band, so 256 cannot be shipped. It is recorded because
> a future reader comparing these numbers to that note deserves to know they
> disagree. `Measure-V2MemoryProfile.ps1 -AllowUBatchDeadBand` is how the band
> gets characterized without producing a promotable report.

## 1.7 Context is allocated at load, not as it fills

llama-server sizes the KV cache and the recurrent state for the full declared
`--ctx-size` when it starts. Declaring 128k on a session that will reach 20k
costs the difference for the life of the process. Changing it requires a restart;
llama.cpp has no hot resize and this repository does not pretend otherwise.

On a Gated DeltaNet hybrid only 16 of 64 layers hold a KV cache, so the dense
arithmetic overstates the cost roughly fourfold. Measured at load, 4-block split,
`ub` 288:

| Context | KV | Marginal VRAM | Shared GPU | Process WS |
|---:|---|---:|---:|---:|
| 32768 | `q8_0` | 12754 MiB | 1608 MiB | 12.75 GiB |
| 32768 | `q4_0` | 12753 MiB | **1095 MiB** | 12.73 GiB |
| 65536 | `q8_0` | 12902 MiB | 2713 MiB | 12.75 GiB |
| 131072 | `q8_0` | 12956 MiB | **5171 MiB** | **16.63 GiB** |
| 131072 | `q4_0` | 12590 MiB | 3507 MiB | 12.78 GiB |

Read the columns together. Marginal *dedicated* VRAM barely moves — 200 MiB
across a fourfold context increase — which is the hybrid working as advertised.
But dedicated is already full, so the cache lands in **shared** memory instead,
and that column more than triples. A 128k window is not cheap on this hardware;
it is a 3.5 GiB KV cache reached over PCIe, plus 3.9 GiB of extra host RAM.

Hence three named profiles rather than one maximal window, with 32768 as the
default. 128k stays available and is marked `high-memory`. Nothing allocates it
because the model supports it.

`q4_0` halves the shared-memory cost at every context and leaves dedicated VRAM
unchanged. It is a genuine lever and it is **not** the default: no quality
evaluation has been run on it, and `docs/MODEL_PROMOTION.md` does not allow a
cache-precision change on a memory argument alone. It ships as an experimental
profile.

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
  `--checkpoint-min-step` are actually in the generated `cmd:`, and that
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
