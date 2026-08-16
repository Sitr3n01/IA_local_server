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

A model declaring `tensor_overrides` (partial weight offload) or a non-zero `cache_ram_mib` cannot be admitted on the `canary_resource_measurement_pending` path that covers small candidates. It fails closed with `resource_measurement_required_for_host_memory` until `resources.peak_vram_gib` and `resources.peak_commit_gib` are measured and recorded. Measurement is a precondition for offload, not a follow-up task.

Adding such a model touches four files that CI checks against each other, and they must land together or `Test-V2HarnessConfig.ps1` fails:

1. `config/models.yaml` — the runtime entry (with `device.vram_mib`) and the model entry, with real `artifact.bytes` and `artifact.sha256`.
2. `integrations/codex/codex-model-catalog.json` — one entry per non-retired canary model.
3. `integrations/opencode/opencode.local-provider.jsonc` and `opencode.canary-provider.jsonc` — the model lists must have the same cardinality and IDs as the canary manifest.

Declare `capabilities.function_calling` and `capabilities.responses` as `false` until the stress evaluation demonstrates a valid forced tool call through `internal/edge/namespace.go`. A model family's tool-call serialization is not evidence for a specific quantization of it.

## Current status

- `local-coding` / Ornith 1.0 9B Q4_K_M: canary candidate. Direct Responses and a function call were observed, but the complete gates and soak remain outstanding.
- `local-fast` / Qwen 3.5 4B Q4_K_M and the four additional Qwen/Gemma
  quantizations are canary candidates. They are generated independently and
  remain client-gated until their declared contracts pass.
- Unsloth runtime `10068 (87d9271bd)`: candidate only; it can replace the AMD baseline only after an independent full comparison.
