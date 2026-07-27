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

## Current status

- `local-coding` / Ornith 1.0 9B Q4_K_M: canary candidate. Direct Responses and a function call were observed, but the complete gates and soak remain outstanding.
- `local-fast` / Qwen 3.5 4B Q4_K_M and the four additional Qwen/Gemma
  quantizations are canary candidates. They are generated independently and
  remain client-gated until their declared contracts pass.
- Unsloth runtime `10068 (87d9271bd)`: candidate only; it can replace the AMD baseline only after an independent full comparison.
