# Gemma 4 26B A4B on gfx1201 - initial discovery and MoE harness support

Date: 2026-08-21
Host target: AMD Radeon RX 9070 XT gfx1201, 16304 MiB VRAM; Ryzen 7 7700X; ~31 GiB RAM

This is not a promotion report. It records A-D from the campaign plan:
runtime/architecture discovery, schema/generic MoE support, GGUF candidate
discovery, and header/tensor inspection. No Gemma 26B artifact has been
downloaded or promoted.

## Executive summary

MEASURED:

- The installed `amd-rocm-qwen38` runtime at `C:\IA\runtimes\llama.cpp\b10549-rocm-7.14\llama-server.exe` advertises `--cpu-moe`, `--n-cpu-moe`, quantized KV (`q8_0`, `q4_0`), flash attention, Jinja chat templates, reasoning flags, and built-in tool flags in `--help`.
- GGUF range probes succeeded without downloading weights. The probed 26B A4B files report `general.architecture=gemma4`, `block_count=30`, `context_length=262144`, and 658 tensors.
- `unsloth/gemma-4-26B-A4B-it-GGUF::gemma-4-26B-A4B-it-UD-Q3_K_XL.gguf` has most of its stored tensor span in `expert_ffn`, making typed MoE placement the right first lever.
- Manifest schema, semantic validation, command generation, context/throughput/quality runners, and campaign summarization now support generic `moe_offload` / `--n-cpu-moe` / `--cpu-moe`.
- Regression tests passed: `Test-V2ConfigGeneration.ps1`, `Test-V2Manifest.ps1`, Python compile, and `go test ./...`.

INFERRED:

- Gemma Deep/Agent/Huge should start from MoE placement sweeps rather than `-ngl` reductions, because the tensor census shows expert FFN weights dominate the GGUF storage.
- MTP/speculative decoding is not equivalent to Qwen3.8. Main GGUFs probed here contain no MTP tensors; Gemma 4 publishes assistant/draft artifacts separately.

NOT TESTED:

- No Gemma 26B load, memory sweep, throughput sweep, quality gate, tool calling,
structured output, retention, agentic loop, workstation test, or Qwen comparison
has been run yet.
- No Gemma 26B model entry was added to `config/models.yaml`; doing so before
download, hash verification, load test, and resource measurement would be a
false deployment signal.

## Model architecture

Official Google sources:

- Gemma 4 overview: <https://ai.google.dev/gemma/docs/core>
- Gemma 4 model card: <https://ai.google.dev/gemma/docs/core/model_card_4>

MEASURED from GGUF headers:

| Field | Value |
| --- | --- |
| `general.architecture` | `gemma4` |
| block count | 30 |
| native context | 262144 |
| tensor count | 658 |

OFFICIAL SOURCE, not locally tested:

| Property | 26B A4B MoE |
| --- | --- |
| total params | 25.2B |
| active params | 3.8B |
| layers | 30 |
| context length | 256K |
| experts | 8 active / 128 total + 1 shared |
| modalities | text, image |

## Runtime support

Local `llama-server --help` on b10549 reports:

- `--cpu-moe`: keep all MoE weights in CPU.
- `--n-cpu-moe N`: keep MoE weights of the first N layers in CPU.
- `--cache-type-k` / `--cache-type-v`: includes `q8_0` and `q4_0`.
- `--flash-attn [on|off|auto]`.
- `--jinja`.
- `--reasoning`, `--reasoning-budget`, `--reasoning-budget-message`.
- Built-in tools flags exist, but remain untrusted and not enabled by the v2 harness.

Primary llama.cpp reference: <https://github.com/ggml-org/llama.cpp/issues/20757>
states that `--cpu-moe` and `--n-cpu-moe` exist in all tools, including
`llama-cli`, `llama-server`, and `llama-bench`. Local help confirms this for the
installed runtime.

## GGUF candidates

Raw discovery: `benchmarks/campaign-gemma4-26b/artifact-discovery.json`.

| Role | Repository | Revision | File | Bytes | SHA-256 |
| --- | --- | --- | --- | ---: | --- |
| official QAT baseline | `google/gemma-4-26B-A4B-it-qat-q4_0-gguf` | `d1c082be9cf3c8a514acf63b8761f4b41935842e` | `gemma-4-26B_q4_0-it.gguf` | 14439363584 | `3eca3b8f6d7baf218a7dd6bba5fb59a56ee25fe2d567b6f5f589b4f697eca51d` |
| ggml-org Q4 baseline | `ggml-org/gemma-4-26B-A4B-it-GGUF` | `bb4531cda34d1ea09d9814959ed4d5833cf2a4c8` | `gemma-4-26B-A4B-it-Q4_0.gguf` | 14618145824 | `d208665ab1cd3a69f7a9a4bc59430e8448c8093d9b06334f566ac59d6d504a03` |
| QAT high-quality local candidate | `unsloth/gemma-4-26B-A4B-it-qat-GGUF` | `7b92b5b28818151e8669af2e45e88d6086f490dd` | `gemma-4-26B-A4B-it-qat-UD-Q4_K_XL.gguf` | 14249047104 | `a7c5bc715f5ff8e99a3e8901ce7d2b42b402c669bf24f7c5250747633d0f5891` |
| Deep candidate | `unsloth/gemma-4-26B-A4B-it-GGUF` | `c099eb48e663fd284577b04978a94ffccb261841` | `gemma-4-26B-A4B-it-UD-Q5_K_M.gguf` | 21150365408 | `769d386a69d43782321c1bad04d41d29a2e84b2c06e6a277cd99fd6265ec0e80` |
| Agent candidate | `unsloth/gemma-4-26B-A4B-it-GGUF` | `c099eb48e663fd284577b04978a94ffccb261841` | `gemma-4-26B-A4B-it-UD-Q3_K_XL.gguf` | 12907280096 | `90a918830420e6a36e01c0d219e563f1d0ca7f223dffd90ad4e02ef4f3253fde` |
| Huge candidate | `unsloth/gemma-4-26B-A4B-it-GGUF` | `c099eb48e663fd284577b04978a94ffccb261841` | `gemma-4-26B-A4B-it-UD-Q2_K_XL.gguf` | 10546934240 | `2a1d26dfe6ea00a467940a5728316af6edb366bbdba950d65b85d232392fb658` |

## Tensor census

Representative probe:
`benchmarks/campaign-gemma4-26b/probe-unsloth-gemma4-26b-ud-q3kxl.json`

Full tensor census:
`benchmarks/campaign-gemma4-26b/tensor-census-unsloth-gemma4-26b-ud-q3kxl.jsonl`

| Category | Tensors | Span bytes |
| --- | ---: | ---: |
| attention | 115 | 1179566080 |
| embedding | 1 | 784334848 |
| expert_ffn | 90 | 10312793088 |
| norm | 270 | 2425856 |
| other | 31 | 1984 |
| output | 1 | 11264 |
| router | 60 | 43591680 |
| shared_ffn | 90 | 568719360 |

Interpretation: `expert_ffn` dominates storage. First sweep should vary
`--n-cpu-moe` before lowering `--gpu-layers`, while keeping attention, router,
KV, and latency-sensitive tensors on GPU when the runtime permits it.

## Harness implementation

Implemented:

- `config/models.schema.json`
  - New `moe_offload` object.
  - `cpu_layers` emits `--n-cpu-moe N`.
  - `cpu_all=true` emits `--cpu-moe`.
  - Strict `additionalProperties: false` preserved.
- `scripts/v2/Common.ps1`
  - Semantic rejection for `cpu_all + cpu_layers`.
  - Rejection when MoE placement lacks measured `resources.peak_vram_gib` for full layer GPU profiles.
  - Deterministic flag emission.
  - Existing runtime `--help` capability gate catches unsupported flags during config generation.
- Campaign runners
  - `Measure-V2ContextFootprint.ps1`
  - `Invoke-V2KvContextMatrix.ps1`
  - `Invoke-V2ThroughputSweep.ps1`
  - `Invoke-V2ProfileQualification.ps1`
  - `summarize_campaign.py`
- `gguf_probe.py`
  - Header-only range probe.
  - Tensor classification and JSONL census.

Existing `tensor_overrides` remains intact as the advanced path.

## MoE placement sweep

NOT TESTED.

Initial sweep matrix once at least one candidate is downloaded and hash-verified:

| Weights | ctx | KV | CPU MoE layers | dedicated | shared | RAM | pp | tg |
| --- | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Q5/Q4/Q3/Q2 candidate | 32768 | q8_0/q8_0 or q4_0/q4_0 | 0 | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED |
| Q5/Q4/Q3/Q2 candidate | 32768 | q8_0/q8_0 or q4_0/q4_0 | 2 | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED |
| Q5/Q4/Q3/Q2 candidate | 32768 | q8_0/q8_0 or q4_0/q4_0 | 4 | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED |
| Q5/Q4/Q3/Q2 candidate | 32768 | q8_0/q8_0 or q4_0/q4_0 | 6 | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED |
| Q5/Q4/Q3/Q2 candidate | 32768 | q8_0/q8_0 or q4_0/q4_0 | 8 | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED |
| Q5/Q4/Q3/Q2 candidate | 32768 | q8_0/q8_0 or q4_0/q4_0 | 12 | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED |

Use `--cpu-moe` only if partial offload still saturates VRAM or if a targeted
all-CPU expert baseline is needed.

## Quality

NOT TESTED.

Required table for later campaign:

| Model | quant | coding | tools | constraints | hallucinations | dangerous errors |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Gemma Deep candidate | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED |
| Gemma Agent candidate | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED |
| Gemma Huge candidate | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED | NOT TESTED |

## Final profile table

NOT SELECTED.

| Profile | weights | KV | context | CPU MoE | output | quality | tg filled | RAM | verdict |
| --- | --- | --- | ---: | --- | ---: | --- | ---: | ---: | --- |
| `gemma4-26b-deep-32k` | NOT SELECTED | target q8_0/q8_0 | 32768 | NOT TESTED | 8192 target | NOT TESTED | NOT TESTED | NOT TESTED | not promotable |
| `gemma4-26b-agent-128k` | NOT SELECTED | target q4_0/q4_0 | 131072 | NOT TESTED | 8192 target | NOT TESTED | NOT TESTED | NOT TESTED | not promotable |
| `gemma4-26b-huge-256k` | NOT SELECTED | target q4_0/q4_0 | 262144 | NOT TESTED | 32768 target | NOT TESTED | NOT TESTED | NOT TESTED | not promotable |

## Known limitations

- Hugging Face Xet headers expose LFS SHA-256 via `X-Linked-ETag`; this report
  records that value as the candidate artifact SHA-256. Download verification
  must still run after any selected artifact is fetched.
- GGUF `span_bytes` is inferred from tensor offsets and includes alignment
  padding. It is good for storage distribution and placement decisions; it is
  not a replacement for runtime memory measurement.
- No ROCm/gfx1201 Gemma load has been attempted yet.
- No chat template, function calling, structured output, or thinking behavior
  has been validated beyond runtime flag availability and official docs.

## Next execution order

1. Download at most two candidates per profile role, starting with:
   - Deep: `UD-Q5_K_M`, `UD-Q4_K_XL` or QAT `UD-Q4_K_XL`.
   - Agent: `UD-Q3_K_XL`, QAT `UD-Q4_K_XL`.
   - Huge: `UD-Q2_K_XL`, `UD-Q3_K_XL`.
2. Verify file bytes and SHA-256.
3. Run `gguf_probe.py --local --census-out` on downloaded artifacts.
4. Run footprint matrix with `-NCpuMoe 0,2,4,6,8,12`.
5. Run throughput sweep for survivors: `pp512`, `pp8192`, `pp32768`, `tg128`, and filled-context decode.
6. Run quality gates and long-context retention only after memory/load cells survive.
7. Add `config/models.yaml` candidate/canary entries only for measured, hash-verified profiles.
8. Regenerate Codex/OpenCode catalogs only after manifest entries exist.

No `provider.public_model` change is allowed in this campaign.
