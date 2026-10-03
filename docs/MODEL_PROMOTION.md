# Model promotion policy

Unsloth is an offline training, quantization, export, and evaluation tool. An Unsloth database entry or successful UI load does not qualify a model for production.

## Required manifest evidence

Every candidate records:

- Stable public ID and display name.
- Exact GGUF path, byte size, SHA-256, upstream repository, immutable revision, filename, and license.
- Exact runtime path, byte size, SHA-256, reported version/build, backend, device selector, and environment.
- Context/output limits, KV types, batch values, Jinja/reasoning policy, concurrency, and tested capabilities.
- Peak VRAM, RAM, and system commit measured under the target context profile.

Missing resource values or untested capabilities remain `null`/`false`; they must not be inferred from a model card.

## Definitive roster and acceptance decision (2026-10-01)

The project owner fixed the scope to the four deployed canary profiles:
`gemma4-12b-qat-ud-q4xl-256k`, `qwen38-27b-deep-32k`,
`qwen38-27b-agent-128k`, and `qwen36-35b-a3b-huge-256k`. No replacement
model is required for this readiness review. "Definitive" identifies the product
roster; `candidate`, `qualified`, and the `final` deployment still describe the
evidence and installation status of each artifact.

The owner explicitly waived the 72-hour soak for this review. It is not a
blocking acceptance criterion for these four profiles, and must be recorded as
`waived`, never as a successful soak. Contract, cancellation, bounded queue,
recovery, artifact identity, and resource-admission checks remain required.
This exception does not waive measured resource reserves or authorize inferred
capabilities. Historical soak requirements below apply to other promotion work.

## Promotion gates

### Candidate

1. Export/download occurs outside the provider and is never triggered by a request.
2. Verify source license and immutable upstream revision.
3. Compute the local GGUF hash and byte size.
4. Add the model with `state: candidate` and `deployments: [canary]` only.
5. Validate the manifest and generate canary configuration.

### Qualified

All of the following evidence is required:

- Responses and/or Chat Completions schema matches each declared capability.
- True SSE streaming, cancellation, function calls, tool selection, structured output, and long-context behavior are tested where declared.
- No broken reasoning markers or template leakage appears.
- Quality suite passes representative coding, repair, tool-use, and retrieval tasks.
- Resource envelope includes worst observed VRAM/RAM/commit plus the admission reserves.
- Edge overhead p95 is below 50 ms and throughput regression is below 5% versus direct runtime.
- A 72-hour, 500-request, 20-load/unload soak succeeds at 99% or better, excluding deliberate policy rejections.
- Secret scan is clean and no duplicate/orphan server remains.

Record benchmark artifacts and reviewer/date in the promotion change. Then set `state: qualified`; do not add `final` yet.

### Enabled

1. Review manifest diff and re-run artifact hashing.
2. Add `final` to each qualified model selected for that environment.
3. Generate and inspect final configuration.
4. Migrate OpenCode, then Codex.
5. After a successful seven-day observation, set `state: enabled`.

### Retired or regressed

Remove the model from every deployment before setting `retired`. A runtime, template, context, quantization, model revision, or serving-flag change invalidates qualification and returns the artifact to `candidate`.

## Capacity rule

A load is admitted only if the measured profile peak plus at least 1 GiB dedicated VRAM reserve and 4 GiB system commit reserve fits current capacity. An unknown resource envelope is never eligible for final deployment.

Both reserves are enforced in `internal/edge/capacity.go`. The commit requirement includes any `cache_ram_mib` the model declares, because llama-server's host-RAM prompt cache is charged against the Windows commit limit exactly like the process working set. The VRAM requirement is compared against `runtimes[].device.vram_mib`; a static device budget is valid only because `provider.max_loaded_models` is pinned to `1`.

A third axis, physical RAM, is enforced separately against `resources.peak_ram_gib` with a 2 GiB reserve. It is deliberately smaller than the 4 GiB commit reserve because the two axes overlap — every allocation charged to commit is charged here too once it is touched — and charging 4 GiB twice would refuse configurations that actually fit. The physical check exists for a failure the commit check cannot see: a model whose weights are partially resident in system RAM can satisfy commit and still have those weights paged out, which turns every token into an SSD read. A model with no measured `peak_ram_gib` is not subject to the check.

## Measuring the resource envelope

The three peaks are not interchangeable and are not all read from the same place:

| Field | What to record | Where |
|---|---|---|
| `peak_vram_gib` | Worst observed dedicated VRAM for the model process | GPU counters |
| `peak_commit_gib` | Worst observed **increase** in system committed bytes, not the absolute total | Commit charge, before minus after |
| `peak_ram_gib` | Worst observed process working set | Process counters |

Two rules make these numbers usable:

1. **Record the idle baseline alongside them.** `peak_commit_gib` is a delta, so a measurement taken on a machine already holding 30 GiB of commit is not comparable to one taken on a quiet machine. Close other GPU and harness workloads first, note the idle committed bytes in the report, and only then load the model.

2. **Measure `peak_commit_gib` with a cold prompt cache** — the first request after the process starts. The gate adds `cache_ram_mib` separately, in full, because `--cache-ram` is a ceiling that fills over the life of a session rather than an allocation made at load; a measurement that already contains a warm cache would charge the same gibibytes twice and reserve headroom that does not need reserving.

## Models that use host memory

A model that uses host memory must present a *complete* resource profile before admission, and the required set is a property of how it runs rather than of its size:

| Execution mode | Required measurements |
|---|---|
| Fully GPU-resident, no prompt cache | `peak_commit_gib` |
| Host-RAM prompt cache (`cache_ram_mib > 0`) | `peak_commit_gib`, `peak_ram_gib` |
| Partial weight offload (`tensor_overrides`) | `peak_commit_gib`, `peak_vram_gib`, `peak_ram_gib`, `runtimes[].device.vram_mib` |

Anything missing fails closed with `resource_profile_incomplete`, and `/api/v1/status` names the absent fields in `missing_profile_fields`. A partial profile is worse than no profile because each missing field silently disables the check that consumes it — so `measured: true` now requires the whole set for that mode, not merely a known commit figure. `null` is never read as zero: an absent measurement means unknown, which is stricter than any number. Measurement is a precondition for offload, not a follow-up task.

Adding such a model touches four files that CI checks against each other, and they must land together or `Test-V2HarnessConfig.ps1` fails:

1. `config/models.yaml` — the runtime entry (with `device.vram_mib`) and the model entry, with real `artifact.bytes` and `artifact.sha256`.
2. `integrations/codex/codex-model-catalog.json` — one entry per non-retired canary model.
3. `integrations/opencode/opencode.local-provider.jsonc` and `opencode.canary-provider.jsonc` — the model lists must have the same cardinality and IDs as the canary manifest.

Declare each capability as `false` until its own contract has passed.
`responses` requires complete native Responses and SSE output, cancellation and
recovery; it does not imply tools. `function_calling` additionally requires
forced calls with exact arguments and namespace round-trips through
`internal/edge/namespace.go`. A model family's serialization is not evidence
for a specific quantization.

The supported tool-choice modes are `auto`, `none`, and `required` (Anthropic
`any`). A named object choice is refused with a clear client error: b10549
ignored that constraint in
`benchmarks/campaign-qwen36-35b-a3b/contract/edge-contract-direct.json`.
The adapter must not return a successful response that silently discarded the
requested selection. Supporting named selection requires a new runtime
contract test, not just a JSON translation.

### `capabilities.reasoning` is an observation, not a guarantee

`reasoning` records that the served artifact emits `reasoning_content` before
its answer. The edge rejects an explicit reasoning request when this capability
is false, just as it rejects other unqualified features. It does not promise the
quality of the reasoning. Declare it `true` only from a
qualification run against that artifact in which the field was present; the
2026-08-23 and 2026-08-25 campaigns are the evidence for the four active
models. It is optional and absent means `false`.

### What `capabilities.function_calling` does and does not mean

The flag is a **deployment guarantee for one artifact**, not a description of what a chat template is capable of. Reading it the other way is easy and has happened: the 2026-08-23 campaign scored `gemma4-12b-qat-ud-q4xl` at 6/7 on the tool suite while its manifest declares `function_calling: false`, and the campaign report recorded the flag as looking over-conservative. It was not wrong — the two are measuring different things.

| | Measures | Sets the flag? |
|---|---|---|
| `scripts/v2/eval/qualify.py` tool suites | Whether the model picks the right function with the right arguments, over HTTP straight to `llama-server`'s `/v1/chat/completions` | **No.** The edge is not in the path. |
| `scripts/v2/Test-V2WorkstationSmoke.ps1` | The same question against the real chat template, as part of an end-to-end profile smoke | No, on its own |
| Stress evaluation through `internal/edge/namespace.go` | Whether a **forced** tool call survives the serving path this deployment actually exposes, for this exact quantization | **Yes.** This is the evidence. |

Consumers must advertise and enforce the same promise:

- `scripts/v2/New-V2ClientCatalogs.ps1` maps it to the Codex catalog's `supports_parallel_tool_calls`, so a harness reads it before issuing parallel tool calls. `Test-V2HarnessConfig.ps1` asserts the mapping stays exact in both directions.
- `internal/panel` requires the field to be present and carries it verbatim; availability deliberately ignores it, so a weaker model stays selectable by operator choice. Pinned by `internal/panel/capability_contract_test.go`.
- `internal/edge` checks it before reserving an inference slot or contacting the runtime. When it is false, the OpenAI routes reject tools, required tool choices, and tool history with `400 unsupported_feature`; `/v1/messages` rejects a required tool choice or tool history with `invalid_request_error` and omits optional tools (see `docs/CLAUDE_DESKTOP.md`).

So a benchmark score, however good, is not grounds for flipping it. Raising `function_calling` to `true` requires the forced-tool-call evidence above, recorded for that artifact.

## Runtimes that are not upstream release builds

A runtime built from source is qualified as an artifact in its own right, and it
is identified by repository + commit + artifact SHA-256 + backend + GPU target.
A directory name is never identity, and a branch or tag is never a pin — the
schema constrains `provenance.source_revision` to a full 40-hex commit, so
`master`, `main`, `latest` and `HEAD` cannot be written at all.

Such a runtime declares `variant: fork` and a typed `provenance` block: source
repository, source revision, upstream ancestry where the fork publishes one,
evidence for the behaviour it is being adopted for, and the build configuration
(backend, GPU targets, Release, and `llama-server` as the only target).

**Evidence has to be produced, not asserted.** For `spiritbuun/buun-llama-cpp`
the claim is the hybrid/recurrent context-checkpoint correction, and
`cia-fork-gate` establishes it by compiling the fork's own checkpoint predicate
and interrogating its behaviour — the presence of a patch, a commit subject, or a
flag in `--help` is explicitly not evidence, because llama.cpp accepts
`--ctx-checkpoints` on builds where checkpoints are created and discarded. The
gate's report is hashed into `provenance.checkpoint_fix.gate_report_sha256`.

Additional promotion gates apply before such a runtime may leave `candidate`:

1. Artifact and gate-report hash validation.
2. ROCm and GPU-target validation, and no severe kernel fallback to the CPU.
3. The agentic incremental-reuse regression at 60k, 128k, 192k and ~256k, each
   passing `incremental_reuse_pass` with its supporting per-turn counts.
4. An A/B against an upstream build over an identical fixture, produced by
   `Compare-V2Runtimes.ps1`, which refuses reports whose controls differ.
5. A complete measured resource profile for its execution mode, and the ordinary
   soak, tool-calling, streaming and end-to-end harness gates.

A fork runtime may not serve `provider.public_model` while it is `candidate`, and
adopting one must leave every other model on the runtime it already had. Both are
enforced by `Assert-V2ManifestSemantics` rather than left to review.

**Control variables are part of the contract.** A fork whose defaults differ from
the upstream baseline makes an omitted manifest field a changed variable rather
than a neutral one, so a model on a fork runtime must state `context_shift`,
`kv_unified`, `cache_ram_mib`, `cache_idle_slots`, `ctx_checkpoints` and
`checkpoint_min_step` explicitly. Qualification measures one difference — the
runtime — or it measures nothing.

## Current status

- The four definitive profiles above are the active roster. Retired entries
  retain provenance and are excluded from deployment and generated client lists.
  Each active profile remains subject to its declared capabilities and live
  capacity; a fixed roster does not authorize a silent model substitution.
- Unsloth runtime `10225 (d2a74a4a3)`: candidate only; it can replace the AMD baseline only after an independent full comparison.
- `spiritbuun/buun-llama-cpp` (ADR 0010): the qualification path exists and the
  provenance gate passes on commit `799e3995cd4f19aa9f6a3fa9fb5b4674422bf0ee` at
  source level. No runtime entry is in the manifest, because the artifact has not
  been built on the target machine — every figure about it on gfx1201 is
  `unverified on gfx1201`, and no `measured` value may be recorded until it runs
  on the RX 9070 XT.
