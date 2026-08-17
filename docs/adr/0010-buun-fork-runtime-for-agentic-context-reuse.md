# ADR 0010: A pinned buun-llama-cpp runtime for Qwen3.8 agentic context reuse

## Status

Proposed. The runtime is a `candidate`; nothing in this ADR promotes it, changes
`provider.public_model`, or moves any existing model off the runtime it uses.

## Context

ADR 0009 added the typed fields for context checkpoints and then recorded, in its
own addendum, that the feature they configure does not work. llama.cpp issues
**#24055** and **#22384** report that checkpoints are created and immediately
invalidated on hybrid and recurrent models: the checkpoint search tests
`cur.pos_min < pos_min_thold`, and on a model carrying recurrent state `pos_min`
follows the sequence frontier, so the test that decides whether a checkpoint can
be used is asking a question the architecture cannot answer. The server logs
*"forcing full prompt re-processing due to lack of cache data"* and re-prefills.

For Qwen3.8-27B this is not a performance detail, it is the whole workload. The
model interleaves 48 Gated DeltaNet layers with 16 full-attention ones, which
makes long context cheap in KV terms — 4.50 GiB at 256k with `q4_0` — and makes
an agentic loop the obvious use. But an agentic loop is a large standing context
grown by small increments interleaved with tool calls, and if every increment
re-prefills the accumulated context, a 200k-token session spends minutes per turn
regardless of how fast it decodes. `--cache-reuse` cannot help: it reuses a KV
prefix, and a recurrent state cannot be rewound to an arbitrary earlier position.
`--context-shift` is unusable for the same reason.

The correction discussed in #22384 exists in a fork and is not merged upstream.

## Decision

**1. Adopt `spiritbuun/buun-llama-cpp` as a second, pinned llama.cpp runtime —
not as a second engine, and not as a replacement.**

The fork preserves the `llama-server` interface, so `engine` stays `llama.cpp`
and llama-swap continues to be the single lifecycle authority. What is added is a
`variant` discriminator and a typed `provenance` block: source repository, source
revision, optional upstream ancestry, checkpoint-fix evidence, and build
configuration. A runtime is identified by repository + commit + artifact SHA-256
+ backend + GPU target. Never by the directory it was unpacked into.

`source_revision` is constrained to a full 40-hex commit by the schema, so
`master`, `main`, `latest`, `HEAD` and abbreviations cannot be expressed at all.

**Rejected alternative:** a generic runtime `metadata` map, or an `extra_args`
list. Either would have been a single line instead of a schema change, and either
would move the boundary of what the manifest guarantees from "these settings" to
"whatever an operator typed" — which is what ADR 0003 exists to prevent.

**Rejected alternative:** maintaining our own fork, or cherry-picking the #22384
patch onto the pinned upstream build. Both produce a private llama.cpp variant
that nobody else builds, tests, or fixes, and whose provenance is a local patch
file. A pinned public commit can be audited, reproduced, and — when upstream
merges an equivalent fix — retired without restructuring anything.

**2. Do not trust the patch. Prove the behaviour.**

`cia-fork-gate` (`internal/forkgate`, `cmd/cia-fork-gate`) must pass before the
commit is built. Its primary check is executable rather than textual: it compiles
the fork's own checkpoint-selection predicate out of the pinned tree and
interrogates it directly, asserting a semantic invariant rather than a line or a
diff.

- For a recurrent or hybrid model the verdict must not vary with `pos_min` or
  the position threshold **at all** — swept across the whole plausible range of
  both, it must not move. That is the transformer-only comparison #22384
  identifies, and its influence has to be gone, not merely reduced.
- The verdict must vary with the recurrent frontier instead, and reject a
  checkpoint at or beyond the resume position — restoring one would install state
  for tokens that were never processed.
- Transformer selection must be unchanged, so adopting the fork is not a silent
  behaviour change for every non-hybrid model.

Structural checks cover what the probe cannot reach in isolation: short prompts
being checkpointed on a hybrid model, the capture recording the frontier rather
than a range minimum, generation checkpoints surviving into the next turn, every
option the profile emits being defined, and the fork's shipped defaults being
readable. They locate constructs by name, not by line, and they read the source
with comments stripped — this fork documents the upstream defect in prose beside
the code that fixes it, and a grep would find the defect in its own obituary.

A gate that cannot reach a verdict fails. There is no configuration in which a
missing compiler is a pass, and a failing gate is recorded and the commit
abandoned — never patched locally.

**3. Change one variable.**

The first qualification compares upstream against the fork with everything else
held: same GGUF, quantization, context, KV types (`q4_0`), batch, ubatch, tensor
split, MTP settings, sampling, template, fixture and hardware.

This is harder than it looks, because the fork's defaults are not upstream's. It
ships `cache_ram_mib = 8192`, `cache_idle_slots = true`, and dynamic VBR on both
cache sides. Omitting a field therefore does not mean "behave like upstream", it
means "behave like the fork" — and the result would be attributed to context
checkpoints. So:

- `Assert-V2ManifestSemantics` refuses a model on a fork runtime that leaves
  `context_shift`, `kv_unified`, `cache_ram_mib`, `cache_idle_slots`,
  `ctx_checkpoints` or `checkpoint_min_step` undeclared.
- `cache_idle_slots` became three-valued in the generator. Absent emits nothing,
  which is what every model generated before the field existed depends on;
  declared emits `--cache-idle-slots` or `--no-cache-idle-slots`. Silence cannot
  turn off a default that is on.
- An explicit `--cache-type-k q4_0` clears the fork's variable-bitrate alias,
  which the gate asserts, because there is no manifest field for VBR and that
  relationship is the only lever holding the KV format constant.

VBR, TurboQuant, TCQ, the fork's KV types and its auto-fit stay off. They are a
separate experiment against this configuration, never an edit to it.

**4. Context checkpoints are not a prompt cache.**

`cache_ram_mib` is pinned to `0` and `cache_idle_slots` to `false`. What is being
qualified is whole-state checkpoint reuse *inside a live session*, which needs no
host RAM budget, no cross-process persistence, and no `/slots` save or restore.
Leaving the host cache on would add commit pressure — the binding constraint on
this machine — plus a second mechanism to attribute results to.

`/slots` persistence is explicitly out of scope. The runtime is not marked failed
for lacking it, and the limitation is documented rather than worked around.

**5. Qualification is progressive and the gate is the token count.**

Gate A (short context, kernel and runtime), B (60k agentic, six turns), C (128k
then 192k), D (~256k). `Measure-V2AgenticReuse.ps1` drives the scenario and reads
the server's own `cache_n` and `prompt_n` counters — never a rate divided by a
rate — recording per turn the context size, new tokens, processed tokens, reuse
ratio, prefill and decode throughput, turn latency, MTP acceptance, and memory.

A turn after the first fails when processed prompt tokens exceed half the
conversation handed to the server. The threshold is deliberately coarse: the
failure being caught is a full re-prefill, and a number precise enough to argue
about would be a magic constant rather than a gate.
`agentic_turn_efficiency = new_prompt_tokens / processed_prompt_tokens` is
recorded as supporting evidence beside the raw counts, not as the verdict.

`Compare-V2Runtimes.ps1` refuses to compare two reports whose fixture hash, seed,
context, increment, turn count, output cap or threshold differ. A table built
from different inputs looks authoritative and is not.

**6. No special privileges, no special allowances.**

The fork runs under the same loopback binding, the same credentials, the same
ACLs, the same llama-swap lifecycle, and the same single-loaded-model invariant.
Admission control is untouched: the fork profile offloads tensors, so it needs
`peak_commit_gib`, `peak_vram_gib`, `peak_ram_gib` and `device.vram_mib` before
it is admitted at all, and until those are measured on the target GPU it fails
closed with `resource_profile_incomplete`. There is no fork branch anywhere in
`capacity.go`, which is the point.

`provider.public_model` cannot be served by a fork runtime that is not
`qualified` or `enabled`, so adopting the fork stays a decision about one canary
entry.

## Consequences

- The manifest carries no fork runtime yet. The artifact does not exist until it
  is built on the target machine, and inventing a path, a byte count or a hash
  would defeat every check that consumes them. `Build-V2ForkRuntime.ps1` prints
  the entry after gating, building and hashing; adding it is a reviewed change.
- Two llama.cpp builds now coexist. The install directory carries the commit, and
  the build script refuses to write into any directory an existing manifest
  runtime occupies, so the baseline cannot be overwritten by a path collision.
- `/api/v1/status` gained a `runtimes` block and per-model runtime, context and
  checkpoint reporting. Installation paths are deliberately absent: they identify
  the machine, not the runtime. `/v1/models` is unchanged.
- The A/B needs an upstream build that knows the Qwen3.8 architecture, which the
  pinned `amd-rocm-baseline` (b8407) does not. That control is a separate runtime
  entry added the same way, and the comparison is meaningless without it — a fork
  is only worth carrying if the thing it fixes is measurably broken next to it.
- **Retiring this runtime is a manifest edit.** When upstream merges an
  equivalent correction and matches the fork on these gates, the fork entry moves
  to `retired` and the model's `runtime` field changes back. No code path,
  abstraction, or interface has to be unwound, because none was added for it.

## What is not established

Everything about this runtime on this hardware. The gate verifies the correction
exists in the pinned commit at source level; it says nothing about whether the
Gated DeltaNet kernel is GPU-accelerated on gfx1201, what the checkpoint sweep
should settle on, whether MTP accepts at 256k, or what the profile costs in VRAM,
RAM and commit. Those are `unverified on gfx1201` until the gates in
`docs/BENCHMARKS.md` are run on the RX 9070 XT.
