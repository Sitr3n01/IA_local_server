# ADR 0011: Workstation memory profiles and live adapter telemetry

## Status

Proposed. Every model this ADR adds is a `candidate` in the `canary` deployment.
Nothing here promotes a model, changes `provider.public_model`, alters an
existing model's configuration, or moves any model to a different runtime.

## Context

The measured characterization in
`benchmarks/REPORT-qwen38-27b-gfx1201-20260821.md` established what Qwen3.8-27B
costs on the reference workstation and which weight split is worth shipping. It
did not answer the operator's actual question, which is whether the machine
remains usable while the server runs. Under the best profile measured, the
adapter sat at 15.9 of 16.3 GiB and the host at 27.6 of 31.1 GiB, leaving an IDE,
a browser and a game engine to compete for what was left.

Three separate defects turned out to be involved, and only the third is a tuning
question.

**The load log is not a memory budget.** An earlier revision of the benchmark
report claimed llama.cpp under-reports its own VRAM by about 2.7 GiB. That was
wrong. Sampling the adapter idle and again under load separates the terms: the
model's marginal cost was 12961 MiB against the 13012 MiB llama.cpp charged the
process, a 0.4% agreement. The missing 3.0 GiB is the desktop compositor and
browser, which hold it before any model loads. llama.cpp is accurate about
llama.cpp; it simply cannot see the rest of the card. The practical consequence
is that the budget available to a model on this workstation is roughly
16.3 − 3.0 = **13.3 GiB**, it moves with whatever is on screen, and no static
manifest can know it.

**Two measurement paths were silently returning nothing.**
`Measure-V2AgenticReuse.ps1` read `\Memory\Committed Bytes`, a localized
performance-counter path that does not resolve on a non-English Windows. The
helper caught the failure and returned `$null`, so every memory column in every
report produced on the pt-BR reference machine was empty, and empty was
indistinguishable from "no process was sampled". Separately,
`Test-V2AgenticHarness.ps1` could not pass under Windows PowerShell 5.1 at all:
5.1 wraps a native command's redirected stderr in a terminating error record, and
the harness deliberately runs a scenario that writes to stderr. CI runs `pwsh` 7,
where it passes, so the gate was green while being unrunnable on the machine it
describes.

**`resources.peak_commit_gib` was being filled with the wrong quantity.** The
edge compares `peak_commit_gib + 4 GiB` against the host's *available* commit
headroom, so the field has to be the model's own commit demand, exactly as
`peak_ram_gib` is its own resident footprint. The measurement script recorded
system-wide committed bytes instead — an absolute level, not a demand. On this
workstation that is roughly 40 GiB against 36 GiB of available headroom, so a
correctly-measured manifest would have been refused by its own admission gate.

## Decision

**Ship named context profiles instead of one maximal window.** llama-server
allocates the KV cache and recurrent state for the whole declared context at
load, not as a conversation grows into it, so a 128k declaration costs the
difference for the life of the process. Three profiles are declared —
`agentic-default` at 32768, `agentic-extended` at 65536, `agentic-huge` at
131072 — and the default is the smallest. 128k remains available and is marked
as a high-memory profile; it is not allocated permanently because the model
supports the window.

Switching profiles requires llama-swap to restart the model. No hot resize is
invented: llama.cpp cannot change `--ctx-size` on a running server, and a facade
that appeared to would be worse than an honest restart.

**Keep the 4-block CPU split as the default placement.** Full residency is worse
on every axis measured and is not offered as a profile. The 8-block split is
offered only for prompts reliably under 8k, where it is worth 3.6x on prefill.

**Reduce the default `ubatch` to 288.** Dedicated VRAM is roughly flat across the
band, but shared GPU memory — the paging signal — rises monotonically with
`ubatch`, and throughput does not. The value stays outside the 65..256 dead band
that `models.schema.json` refuses.

**Add a live adapter probe, and let it gate nothing.** `cia-edge` reads
adapter-level dedicated and shared memory through PDH and reports a verdict on
`/api/v1/status` and as metrics gauges. It is observability only. Refusing
requests or unloading a model on one instant of a noisy signal would trade a
silent slowdown for a silent outage, which is a worse failure and a decision the
operator is better placed to make. The probe is read on demand with a 2-second
cache; nothing samples on a timer.

The probe uses `PdhAddEnglishCounterW` rather than `PdhAddCounterW`, which is the
same locale defect described above fixed at the source rather than worked around.

**Classify pressure on two conditions, not one.** Occupancy alone cannot separate
a healthy configuration from a paging one: the fastest configuration measured sat
at 96.4%. Shared usage alone fires on any desktop. `pressured` therefore requires
occupancy ≥ 95% *and* shared ≥ 1024 MiB, a floor set between the healthy 514 MiB
case and the degraded 1253 MiB one. Both `internal/edge/gpumemory.go` and
`scripts/v2/Telemetry.ps1` carry the same table and are expected to agree.

## Consequences

The workstation profile is a candidate like every other model in the manifest and
inherits the same promotion discipline. `provider.public_model` still resolves to
`local-coding`; repointing it is a promotion decision governed by
`docs/MODEL_PROMOTION.md` and is not taken here.

`resources.peak_commit_gib` now means something different from what earlier
reports recorded. Reports produced before this change carry a system-wide level
under that name; they are not comparable to reports produced after it, and the
new `peak_system_commit_gib` field is where the old quantity now lives.

The pressure classifier is calibrated to one adapter on one machine. On different
hardware the shared-memory floor is a guess until it is measured, which is why
the thresholds sit next to the measurements that produced them and why
`Test-V2Telemetry.ps1` asserts the four measured cases rather than the
thresholds themselves.

The ≤15.0 GiB dedicated target stated by the operator is **not met** by the
recommended profile, and cannot be without giving up decode throughput that the
same operator ranked higher. That tension is recorded in the benchmark report
rather than resolved by quietly widening the split.

Nothing in this ADR touches the buun fork evaluation in ADR 0010, the checkpoint
fields in ADR 0009, or any non-AMD backend. The adapter probe compiles to an
"unavailable" stub off Windows and returns `unknown`, which is deliberately
distinct from `ok`: a host that cannot be observed has not been shown to be
healthy.
