# ADR 0009: Partial weight offload and host-RAM context cache for hybrid models

## Status

Proposed.

## Context

Every model in `config/models.yaml` today is 4B–12B and declares `gpu_layers: 99`,
because every one of them fits entirely in the RX 9070 XT's 16304 MiB. The
generator in `New-V2Config.ps1` encodes that assumption: it emits one fixed
`llama-server` flag list, always including `--context-shift`, with no way to
express weight placement, speculative decoding, or cache sizing.

Qwen3.8-27B breaks the assumption on two axes at once.

**Memory shape.** It is a dense 27.78B model with *hybrid* attention: 64 blocks,
of which 48 are Gated DeltaNet (linear attention, constant recurrent state, no KV
cache) and 16 are full attention (GQA 24 Q / 4 KV, `head_dim` 256, at
`blk.3, 7, 11, … 63`). KV therefore costs 32.768 elements per token, not the
~131.072 a uniform-attention 27B would cost:

| ctx | `f16` | `q8_0` | `q4_0` |
|---|---|---|---|
| 128k | 8.00 GiB | 4.25 GiB | 2.25 GiB |
| 256k | 16.0 GiB | 8.50 GiB | 4.50 GiB |

Long context is cheap on this architecture; the weights are what does not fit.
At IQ4_XS (15.7 GiB) with KV `q8_0` at 128k, roughly 4–6 GiB of weights must live
in system RAM. That is the first model in this project for which `gpu_layers: 99`
is not merely suboptimal but impossible.

**Cache shape.** llama.cpp's `--cache-reuse` reuses a KV *prefix*. It does not
work for models carrying a recurrent state, because the DeltaNet state cannot be
rewound to an arbitrary earlier position. The same property makes
`--context-shift` unusable: shifting rewrites absolute KV positions with no
corresponding operation on the recurrent state. The mechanism that does work is
whole-state context checkpoints held in host RAM (`--cache-ram`,
`--ctx-checkpoints`, `--checkpoint-every-n-tokens`), which is also where the
operator's ~10 GB RAM budget is best spent: for an agentic loop, restoring a
checkpoint replaces re-prefilling ~100k tokens.

Meanwhile `internal/edge/capacity.go` gates admission on Windows commit headroom
alone. It never consults VRAM, even though `resources.peak_vram_gib` already
exists in the manifest and schema, and it cannot see a host-RAM prompt cache at
all. Because every `resources.peak_*` is currently `null`, all six models are
admitted through the `canary_resource_measurement_pending` escape hatch. For a
9B that is a survivable shortcut; for a 27B holding several GiB of weights plus a
multi-GiB prompt cache in RAM, it is an out-of-memory stall waiting to happen.

## Decision

**1. Grow the manifest with typed optional fields, not a generic escape hatch.**
`config/models.schema.json` gains `context_shift`, `kv_unified`, `cache_ram_mib`,
`ctx_checkpoints`, `checkpoint_every_n_tokens`, `cache_idle_slots`,
`spec_decoding`, and `tensor_overrides`. All are optional and
`additionalProperties: false` stays in force, so the manifest remains an
exhaustive contract rather than a pass-through to a command line.

**Rejected alternative:** a generic `extra_args: [string]` field. It is one line
of generator code instead of a schema change, but it moves the boundary of what
the manifest guarantees from "these settings" to "whatever string an operator
typed", which defeats the point of ADR 0003 and of `cia-manifest` running in CI.

**2. Offload by tensor pattern, never by reducing `gpu_layers`.**
Lowering `--n-gpu-layers` evicts whole layers from the tail of the stack, and on
a 3:1 hybrid that tail contains full-attention layers whose KV cache would then
live in system RAM — re-read over PCIe on every token. `tensor_overrides` emits
`-ot "<pattern>=CPU"` and is used to move FFN tensors only, keeping all 16
attention layers and the entire KV cache resident on the device.

**3. Make the emitted flag order load-bearing and test it.**
Every optional flag is emitted between `--context-shift` and `--jinja`. A model
declaring none of the new fields produces a byte-identical command line to the
one generated before the fields existed, so regenerating a qualified deployment
is a genuine no-op. `Test-V2ConfigGeneration.ps1` asserts this against the real
manifest in CI, and `New-V2LlamaServerCommand` was moved into `Common.ps1`
specifically so it could be asserted without running the publication transaction.

**4. Teach admission control about VRAM and host memory.**
`capacity.go` gains four behaviours: the required commit now includes
`cache_ram_mib`; a model whose measured `peak_vram_gib` plus a 1 GiB reserve
exceeds the runtime's declared `device.vram_mib` is refused with
`insufficient_vram_budget`; a model whose measured `peak_ram_gib` plus a 2 GiB
reserve exceeds free physical RAM is refused with
`insufficient_physical_memory`; and the `canary_resource_measurement_pending`
escape hatch no longer covers a model that declares `tensor_overrides` or a
non-zero `cache_ram_mib`, which now fail closed with
`resource_measurement_required_for_host_memory`.

A static device budget is sound here only because `provider.max_loaded_models` is
pinned to `1` — exactly one model is ever resident, so there is no allocation to
race against and no live VRAM probe is required.

Commit and physical RAM are separate axes because they answer different
questions: commit bounds what may be *reserved*, physical bounds what may stay
*resident*. For a model with weights deliberately living in system RAM the
second is what decides throughput — passing the commit test while the weights
page out degrades silently to SSD speed, which is worse than a 503. The probe
costs nothing new: `GlobalMemoryStatusEx` was already being called and
`AvailablePhysical` was already in the struct, simply discarded. The physical
reserve is 2 GiB rather than the commit reserve's 4 GiB because the two axes
overlap — every touched allocation is charged to both — so reusing 4 GiB would
refuse configurations that fit.

**Measurement discipline is part of the contract.** Because `--cache-ram` is a
ceiling that fills over a session rather than an allocation made at load, the
gate adds `cache_ram_mib` on top of the measured peak instead of expecting it to
appear inside it. That only works if `peak_commit_gib` is captured with a cold
prompt cache; otherwise the same gibibytes are reserved twice. Likewise
`peak_commit_gib` is a delta and is meaningless without the idle baseline it was
measured against. Both rules are stated in `docs/MODEL_PROMOTION.md` and
`docs/BENCHMARKS.md`, and `docs/RUNBOOK.md` §11.0 makes reclaiming that baseline
the first step of onboarding — on the machine this was designed for, 31.82 GiB
of the 42.30 GiB commit limit was already spoken for with no model loaded, which
constrains a 27B more than any quantization choice does.

**5. Refuse the `ubatch` dead band declaratively.**
Micro-batch sizes in `[65, 256]` collapse throughput on hybrid Gated DeltaNet
models by up to 40×. The schema refuses the band outright and `bench-llama.ps1`
refuses to sweep into it, so the failure mode cannot be reached by tuning and
then be misread as a property of the model.

## Consequences

- The `gpu_layers: 99` invariant is gone. Any future model may declare a split,
  but only with a measured `resources.peak_vram_gib` behind it — the gate makes
  measurement a precondition for offload rather than a follow-up task.
- Two things previously always true of the generated `cmd:` no longer are:
  `--context-shift` is conditional, and `--parallel` now reflects
  `model.parallel` instead of a hardcoded `1`. The schema still pins
  `parallel` to `1`, so today's output is unchanged.
- `healthCheckTimeout` moves from 180s to 600s and Codex
  `stream_idle_timeout_ms` from 300s to 900s. Both were sized for models that
  load and prefill entirely in VRAM; neither bound is reachable for a partially
  offloaded 27B, and a spurious health-check failure would look like a crash.
- Admission can now refuse a model the router would happily start. That is the
  intent: a 503 from the edge is recoverable, an out-of-memory Windows session is
  not.
- This ADR does not promote Qwen3.8-27B, change `provider.public_model`, or add
  the model to the manifest. It makes such an entry expressible and safe to
  admit; whether it is worth admitting depends on the Gated DeltaNet kernel
  measurement described in `docs/BENCHMARKS.md`, which is not settled on gfx1201.
