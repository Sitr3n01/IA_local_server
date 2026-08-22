# MoE and Gemma Readiness Hardening

Date: 2026-08-21

## Findings fixed

- `moe_offload` was parsed by PowerShell generation but not by edge admission. A profile with `moe_offload.cpu_layers: 4` could therefore look like a normal full-GPU model to `OffloadsTensors`.
- `cpu_layers: 0` needed to remain a deliberate full-GPU control cell, not a host-RAM offload.
- Generic benchmark runners inherited the Qwen3.8 tensor override by default.
- `llama-server` and `llama-bench` capabilities were treated too broadly. On the installed b10549 runtime, `llama-server` supports `--cpu-moe` and `--n-cpu-moe`; `llama-bench` supports `--n-cpu-moe` but not `--cpu-moe`.
- Output contract fields were individually valid but lacked semantic invariants across `max_output_tokens`, `n_predict`, `reasoning_budget`, `reasoning_budget_message`, `compact_threshold_tokens`, and `context_tokens`.

## Files changed

- `internal/edge/config.go`
- `internal/edge/manifest.go`
- `internal/edge/manifest_test.go`
- `internal/edge/capacity_test.go`
- `scripts/v2/Common.ps1`
- `scripts/v2/Invoke-V2ProfileQualification.ps1`
- `scripts/v2/Measure-V2ContextFootprint.ps1`
- `scripts/v2/Invoke-V2ThroughputSweep.ps1`
- `scripts/v2/Invoke-V2KvContextMatrix.ps1`
- `scripts/v2/Test-V2ConfigGeneration.ps1`
- `scripts/v2/Test-V2Manifest.ps1`
- `scripts/v2/Test-V2HarnessConfig.ps1`
- `scripts/v2/Test-V2McpInferenceIntegrations.ps1`
- `scripts/v2/Test-V2ProfileContracts.ps1`
- `benchmarks/campaign-256k/run-campaign.ps1`
- `benchmarks/campaign-256k/resume-campaign.ps1`
- `benchmarks/campaign-256k/run-phase2.ps1`
- `benchmarks/contract-qwen38-27b-deep-32k-hardening-20260821.json`
- `benchmarks/contract-qwen38-27b-agent-128k-hardening-20260821.json`
- `benchmarks/contract-qwen38-27b-huge-256k-hardening-20260821.json`
- `benchmarks/REPORT-gemma4-26b-a4b-gfx1201-20260821.md`

## Admission-control changes

- The edge now parses typed `moe_offload.cpu_layers` and `moe_offload.cpu_all`.
- `OffloadsTensors` is true for `tensor_overrides`, `moe_offload.cpu_layers > 0`, or `moe_offload.cpu_all=true`.
- `moe_offload.cpu_layers=0` is preserved as a declared full-GPU control and does not require physical RAM solely because the object exists.
- Real MoE offload enters the same profile-completeness regime as tensor offload: `resources.peak_commit_gib`, `resources.peak_vram_gib`, `resources.peak_ram_gib`, and `runtimes[].device.vram_mib` are required before admission.
- `/api/v1/status` profile summaries now expose `moe_offload`; `/v1/models` remains unchanged.

## Command-builder refactor

- `New-V2LlamaServerArguments` is the shared `llama-server` argument source.
- `New-V2LlamaServerCommand` is now a formatting wrapper over that shared builder.
- `Invoke-V2ProfileQualification.ps1` and `Measure-V2ContextFootprint.ps1` build their server arguments through the same shared path.
- `New-V2BenchmarkModelSpec` lets benchmark runners construct the same model-shaped input used by production generation.
- `New-V2LlamaBenchArguments` keeps `llama-bench` separate because its flag surface is not identical to `llama-server`.

## Runtime capability checks

- `llama-server.exe --help`: `--cpu-moe` present, `--n-cpu-moe` present.
- `llama-bench.exe --help`: `--cpu-moe` absent, `--n-cpu-moe` present.
- Throughput sweeps do not emit `--cpu-moe` on this `llama-bench` build. All-CPU-MoE bench cells must use a GGUF-validated `--n-cpu-moe <all-moe-layers>` or be recorded as NOT TESTED.

## Output-contract invariants

- `n_predict >= max_output_tokens`.
- `reasoning_budget <= n_predict` when both are positive.
- `reasoning_budget_message` requires a positive `reasoning_budget`.
- Profiles with a positive reasoning budget must reserve at least `min(8192, n_predict)` answer tokens after reasoning.
- `compact_threshold_tokens < context_tokens`.
- `compact_threshold_tokens + max_output_tokens < context_tokens`.
- Current Qwen Deep, Agent, and Huge profiles are explicitly accepted by semantic tests.

## Tests added

- Go coverage for MoE admission and status:
  - `cpu_layers` absent
  - `cpu_layers=0`
  - `cpu_layers=1`
  - `cpu_layers=4`
  - `cpu_all=true`
  - tensor override only
  - tensor override plus `cpu_layers`
- PowerShell semantic tests for MoE profile completeness and output-contract rejection.
- PowerShell generation tests for:
  - shared Gemma production/qualification tuning arguments
  - generic benchmark default with no Qwen tensor override
  - `llama-bench` partial MoE support
  - refusal to emit unsupported `--cpu-moe` for bench

## Regression results

- `PowerShell` parse checks: PASS.
- `gofmt -l .`: PASS.
- `go test ./...`: PASS.
- `go vet ./...`: PASS.
- `Test-V2ConfigGeneration.ps1`: PASS.
- `Test-V2Manifest.ps1`: PASS.
- `Test-V2HarnessConfig.ps1`: PASS.
- `Test-V2Telemetry.ps1`: PASS.
- `Test-V2AgenticHarness.ps1`: PASS.
- `Test-V2McpInferenceIntegrations.ps1`: PASS.
- `Test-V2ProfileContracts.ps1 -ModelId qwen38-27b-deep-32k`: PASS, 8/8 checks.
- `Test-V2ProfileContracts.ps1 -ModelId qwen38-27b-agent-128k`: PASS, 8/8 checks.
- `Test-V2ProfileContracts.ps1 -ModelId qwen38-27b-huge-256k`: PASS, 8/8 checks.
- Post-run process check: no `llama-server` or `llama-bench` process remained.
- `provider.public_model`: unchanged at `local-coding`.
- Manifest Gemma 26B profile entries: none.

## CI status

- `.github/workflows/ci.yml` already runs `Test-V2Manifest.ps1`, `Test-V2ConfigGeneration.ps1`, harness config, telemetry, agentic harness, Go tests, vet, staticcheck, govulncheck, race tests, fork gate, and secret scan.
- Remote GitHub Actions were not observed in this local run.
- CI status: local tests PASS; remote CI NOT YET OBSERVED.

## Remaining risks

- No Gemma 26B artifact was downloaded in this hardening step.
- No Gemma load, MoE sweep, throughput sweep, chat template smoke, tool-calling test, quality suite, or long-context run has been executed.
- All-CPU-MoE for `llama-bench` still needs metadata-backed layer-count validation before use.

## Readiness

The infrastructure fixes required before Gemma physical benchmarks are implemented and locally validated by static/unit gates. The physical campaign itself has not started.

READY FOR GEMMA PHYSICAL BENCHMARK
