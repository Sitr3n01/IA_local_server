# Changelog

All notable changes are documented here. This project follows Keep a Changelog conventions and will use semantic versioning once v2 leaves canary.

## [Unreleased]

### Added

- Go-based v2 edge, MCP, and Windows Credential Manager helper foundations.
- Single provenance- and checksum-based model/runtime manifest with JSON Schema.
- llama-swap v240 template with lazy loading, 900-second TTL, router authentication, no aliases, one active model, and no retained log buffer.
- Canary/final configuration generator and hidden scheduled-task launchers, all preview-only by default.
- Installation, artifact, listener, side-effect, and secret-pattern verification scripts.
- Architecture, threat model, runbook, promotion, benchmark, security, contribution, licensing, and ADR documentation.
- Windows CI for Go quality gates, PowerShell parsing, manifest validation, vulnerability analysis, and secret scanning.
- Native Win32 `cia-tray` operator panel with live status, explicit lifecycle
  operations, atomic selected-model state, capability-aware harness launchers,
  single-instance protection, and no new listener or UI framework dependency.
- Supported Unsloth canary/final launchers that leave private Studio state
  untouched and never restore the retired external connection.
- Approved-hash, atomic installers for staged Edge/MCP/tray binaries and an
  approved-plan transactional harness installer.
- Separate `cia-mcp-inference` bridge with one bounded `local_ai_delegate`
  tool, direct Credential Manager authentication, and provider-preserving
  global integrations for SOTA Codex, Claude, and OpenCode sessions.
- Metadata-only MCP live probe, DPAPI-backed transactional client registration,
  and an idempotent current-user Startup shortcut for the native panel.
- Python quality-eval and long-context/tool-call stress-eval scripts
  (`scripts/run-profile-quality-eval.py`, `scripts/run-profile-stress-eval.py`),
  mirroring the existing PowerShell quality harness for cross-language reuse
  and adding a needle-in-haystack plus forced-tool-call test the PowerShell
  suite did not have.

- Manifest support for models too large to fit entirely in VRAM: optional typed
  fields `context_shift`, `kv_unified`, `cache_ram_mib`, `ctx_checkpoints`,
  `checkpoint_every_n_tokens`, `cache_idle_slots`, `spec_decoding`, and
  `tensor_overrides`, plus `runtimes[].device.vram_mib`. `additionalProperties`
  stays closed; a generic `extra_args` escape hatch was deliberately rejected
  (ADR 0009).
- `Test-V2ConfigGeneration.ps1`, run in CI, proving that a model declaring none
  of the new fields still generates a byte-identical `llama-server` command line,
  so growing the schema never rewrites a published deployment.
- VRAM dimension in edge admission control: a measured `peak_vram_gib` plus the
  documented 1 GiB reserve is checked against the runtime's declared device
  budget, reported as `insufficient_vram_budget`.
- Physical-RAM dimension in edge admission control, reported as
  `insufficient_physical_memory`. `GlobalMemoryStatusEx` was already being called
  and `AvailablePhysical` already sat in the struct, discarded; `peak_ram_gib`
  likewise already existed in the schema and was never read. Commit bounds what
  may be reserved, physical bounds what may stay resident — for a model with
  weights offloaded to system RAM, only the second predicts throughput.
- Measurement rules for the resource envelope in `docs/MODEL_PROMOTION.md` and
  `docs/BENCHMARKS.md`: `peak_commit_gib` is a delta and must be recorded with
  its idle baseline (`idle_commit_gib`) and with a **cold prompt cache**, since
  admission adds `cache_ram_mib` separately as its full ceiling. Without that
  rule the same gibibytes were charged twice.
- `docs/TUNING.md`: bottleneck diagnosis and tuning. Gives the memory-bandwidth
  ceiling as arithmetic so a measured rate can be judged against its hardware
  limit instead of intuition, a symptom-ordered decision tree, and the tuning
  levers ranked by effect. Quantifies what offload actually costs on this
  platform: the first gibibyte moved off the GPU costs ~37% of decode
  throughput, and a fully resident smaller quant can be ~4.7x faster than an
  offloaded larger one. Section 1.1 works through speculative decoding: MTP
  amortizes weight reads but not arithmetic, so on a CPU-resident portion the
  optimal draft depth collapses to 2-3 and a depth of 7 can be *slower* than not
  speculating at all. Includes a sensitivity table over CPU GEMM throughput, the
  one constant this repository has never measured.
- `docs/TUNING.md` §1.2: how to estimate the VRAM/RAM/commit envelope from the
  model card plus the one measured anchor, before downloading anything. Surfaces
  that Windows commit tracks **VRAM**, not RAM — the 9B consumed 8.06 GiB of
  commit against 6.66 GiB of VRAM with zero CPU-resident weights, so a 27B costs
  ~16 GiB of commit even with no offload and no prompt cache. Also shows the
  template's 96k / KV `q8_0` split needs ~5.1 GiB offloaded rather than 4.4, and
  that 128k with `q4_0` KV needs *less* offload than 96k with `q8_0`.
- `docs/TUNING.md` §1.3: the quality/speed frontier for choosing a quantization.
  Community IQ4_XS builds of this model span roughly a gibibyte, which on this
  hardware is ~60% of decode throughput, so build selection outweighs every
  serving flag. Records that `Q3_K_XL` on 27B-class Qwen measures above 0.1 KLD
  with 85-90% top-token agreement, against a 0.08 "quality drops" threshold, so
  dropping a level is a real cost rather than a free win. Also documents two
  silent MTP killers: quantization tooling dropping the `nextn` head entirely,
  and draft acceptance collapsing at particular `--ctx-size` values on a
  ~2048-token period (llama.cpp #23658).
- The `blk.64` rule, the most consequential per-tensor fact about this model: the
  MTP head must be quantized to Q5_K or higher. Reported measurements put a
  Q4_K MTP block at **0% draft acceptance** — speculation fails completely and
  silently — against 73-74% for builds keeping it at Q5_K-Q8_0. The nominal
  quantization label does not reveal which you have, so `gguf-dump` verification
  is now a precondition in `RUNBOOK.md` §11.0b and `BENCHMARKS.md`. This also
  reverses earlier guidance to prefer the most compact build: compact builds are
  precisely the ones likely to have quantized `blk.64` down.
- `Qwen/Qwen3.8-27B` (Alibaba, Apache-2.0) recorded as the authoritative
  reference for the family across `TUNING.md`, `RUNBOOK.md`, and
  `model-test-matrix.json`. Every GGUF is a derived artifact whose publisher,
  revision, and per-tensor choices are recorded and verified separately; the
  matrix no longer names a GGUF publisher until one passes the `blk.64` check.
- `docs/RUNBOOK.md` §11.0b: choose the build and verify the MTP head survived
  quantization before downloading or benchmarking anything.
- `docs/RUNBOOK.md` §11.0: reclaim the idle commit baseline before measuring
  anything else. The 2026-07-20 validation recorded 31.82 GiB committed with no
  model loaded against a 42.30 GiB limit, which constrains a 27B more than the
  choice of quantization does.
- Hybrid Gated DeltaNet benchmark protocol in `docs/BENCHMARKS.md`, including the
  kernel go/no-go gate, the sweep matrix, and a cache-restoration measurement;
  matching `qwen38-27b-*` profiles in `model-test-matrix.json`.
- `docs/RUNBOOK.md` section 11: onboarding procedure for a partially offloaded
  hybrid model, and a capacity-`reason` interpretation table.

### Changed

- `--parallel` is now emitted from `model.parallel` instead of a hardcoded `1`.
  The schema still pins the value to `1`, so generated output is unchanged.
- `--context-shift` is no longer emitted unconditionally. Models with a recurrent
  state cannot be shifted, and a model that sets `context_shift: false` now gets
  an explicit `--no-context-shift`.
- Required commit for admission now includes `cache_ram_mib`. llama-server's
  host-RAM prompt cache is charged against the Windows commit limit, and the gate
  previously could not see it at all.
- The `canary_resource_measurement_pending` escape hatch no longer covers models
  that declare `tensor_overrides` or a non-zero `cache_ram_mib`; those fail closed
  with `resource_measurement_required_for_host_memory` until measured.
- `healthCheckTimeout` 180s to 600s and Codex `stream_idle_timeout_ms` 300s to
  900s. Both were sized for models that load and prefill entirely in VRAM.
- `systemCommitHeadroomGiB` becomes `systemMemoryStatus`, returning a
  `memorySnapshot` with both host memory axes; the `Server.commitHeadroom` hook
  becomes `Server.memoryStatus`. Signature change only — the injection point the
  tests use is unchanged.
- The `cache_ram_mib` starting value in the RUNBOOK §11.3 template drops from
  6144 to 2048. It was chosen against the nominal 10 GB RAM budget rather than
  against the 10.48 GiB of commit headroom actually measured, and the gate adds
  it in full on top of the model's own peak.
- `scripts/bench-llama.ps1` accepts `-RuntimeRoot`, `-NGpuLayers`, `-Label`, and
  `-RocBlasUseHipBlasLt`, and refuses `-UBatchSize` in the 65..256 band that
  collapses throughput on hybrid models.
- MTP draft depth for offloaded profiles drops from 5 to 3, in the RUNBOOK
  template and the `qwen38-27b-*` test-matrix profiles. 5 was carried over from
  resident-model guidance; speculation amortizes weight reads but not
  arithmetic, so a CPU-resident portion is compute-bound and each extra drafted
  token past a shallow depth costs more than it returns. `--threads` becomes a
  tuning variable for the same reason and is now part of the sweep matrix.

### Fixed

- Tray dashboard `Abrir Codex`/`Abrir OpenCode` only enabled for the model with
  the *persisted* selection (`Selecionar`) instead of whichever model was
  highlighted in the list; launch now targets the highlighted row.
- Codex canary launcher's console closed instantly on completion or error,
  hiding any output, because `powershell.exe` ran without `-NoExit`. Confirmed
  `codex.exe` is a genuine CLI that needs a real console (not a packaged-app
  shim) and restored `CREATE_NEW_CONSOLE` for it.

### Changed

- Removed `Abrir Codex` from the tray dashboard and tray context menu: the
  unified ChatGPT desktop app's bundled Codex mode has no GUI provider picker
  for custom/local providers (tracked upstream as `openai/codex#29156`); the
  CLI-only path remains available via `Start-CodexLocalCanary.ps1`.
- Registered the five non-Ornith canary models with validated
  `chat_completions` support (Qwen 3.5 ×3, Gemma 4 12B ×2) in the Codex and
  OpenCode local-only catalogs, with per-model `reasoning`/`tool_call`
  capability flags; left the two unvalidated Gemma 4 31B variants out
  (`chat_completions`/`streaming` not yet confirmed for them).
- Added the local provider to the personal default OpenCode configuration
  alongside existing cloud providers, previously reachable only through the
  isolated canary launcher; the OpenCode Start Menu shortcut now routes
  through the credential helper so the local provider authenticates without a
  standing plaintext environment variable.
- Raised `cia-mcp-inference` output-token ceiling (4096 → 65536 tokens) and
  the edge's upstream response timeout (2 min → 30 min), which were silently
  truncating and timing out ordinary delegate responses; added a configurable
  `temperature` (default 0.2, previously unset).
- Raised `local-coding` KV cache precision (`q4_0` → `q8_0`) and weight
  quantization (`Q4_K_M` → `Q8_0`, 9.53 GB); ~10.8 GiB VRAM measured with the
  full 131072-token context resident (within the operator's stated headroom).
- Lowered `local-coding` weight quantization again in the source manifest
  (`Q8_0` → `Q5_K_M`, 6.47 GB) to recover memory headroom, keeping KV cache at
  `q8_0`/`q8_0`; publication to the installed canary is staged and still
  awaits an elevated `New-V2Config.ps1 -Apply` run. llama.cpp's own
  device-memory fit dropped from 10764 MiB to 8210 MiB (~2.5 GiB recovered) at
  the full 131072-token context. A same-conditions A/B against the prior
  `Q8_0`/`q8_0` config (identical port, context, and quality-eval battery)
  showed no regression: 4/4 on the instruction-following/arithmetic/
  list-reasoning/config-recall suite with reasoning enabled, and a matching
  pass on a new long-context stress test (106745-token haystack, a fact
  planted at ~50% depth, correctly retrieved and reused as a forced
  tool-call argument).
- Removed the two never-validated Gemma 4 31B candidates (`gemma4-31b-q4km`,
  `gemma4-31b-ud-q4xl`; `chat_completions`/`streaming` had never been
  confirmed for either) and the superseded `Ornith 1.0 9B Q4_K_M` long-context
  test profiles from the manifest and test matrix after deleting their weight
  files (~39.8 GiB reclaimed); regenerated the Codex/OpenCode local-only
  catalogs from the cleaned manifest and updated canary validation/hash state
  to match.
- Documented the pinned executor model's demonstrated limitations and a
  capability rating directly in the `local_ai_delegate` tool description and
  `integrations/mcp-inference/README.md`, based on repeated canary testing.

### Security

- Removed cloud fallback and client-authorization forwarding from the v2 design.
- Separated inference, administration, and router credentials.
- Established metadata-only logging, fail-closed routing, loopback-only listeners, decompression limits, and explicit incident handling.
- Removed administrative credentials from status polling and default read-only
  MCP registrations; mutations read the admin secret only on demand.
- Added bounded switch completion after client cancellation, Explorer tray-icon
  recovery, idempotent panel startup, and rollback-safe elevated publication.
- Kept inference delegation isolated from the read-only and administrative MCP
  surfaces; endpoint/model are pinned, redirects/proxies are rejected, and no
  client configuration contains an inference credential.

### Migration

- v1 remains untouched and is not a v2 dependency.
- `local-coding` is a canary candidate only; final generation remains blocked until qualification.
- `local-fast` is recorded but disabled.
