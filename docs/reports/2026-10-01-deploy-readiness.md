# Deployment readiness — 2026-10-01 review, updated 2026-10-02

This review keeps the four active models as the project's final roster. The
project owner waived the 72-hour soak; its result is recorded as `waived`, with
no simulated evidence of prolonged stability. The RAM, commit and VRAM reserves
and the contract criteria remain in force.

## Fixes applied

- The defects in Anthropic streaming, queue capacity consumed by slow request
  reads, cancellation of the administrative transport and capability validation
  are described in [Audit fixes](2026-10-01-senior-audit-fixes.md).
- Targeted update of the three vulnerable frontend dependencies. The npm audit
  went from three high findings to zero, without updating the rest of the
  dependency tree.
- The Codex, OpenCode and Unsloth launchers no longer default to the retired
  alias `local-coding`. An explicit selection is still honoured.
- Codex checks Responses, streaming and function calling in the installed
  manifest before it starts. Its explicit default is Qwen Agent, separate from
  the provider's plain-inference default, Gemma.
- The Codex catalog derives `supported_in_api` from the three required
  qualifications. The OpenCode catalog declares `tool_call` and `reasoning`
  according to the manifest, and disables attachments. These fields are defined
  in the [official OpenCode schema](https://opencode.ai/config.json).
- The OpenCode launcher also refuses the compact form `-m<model-id>`, which
  could otherwise override the pinned local selection.
- Three fixtures with absolute Windows paths now build absolute paths for the
  platform the test runs on. The same checks now pass on Windows and in the
  Linux job with the race detector.
- New Responses contract test, whose reports contain no generated text or
  credentials. Its negative controls prevent it from passing empty output,
  truncated SSE, a mid-stream error or altered tool arguments.
- Admission during a model swap no longer treats the previous process's
  historical peak as memory that is guaranteed to be released. The router
  unloads the process, the edge confirms the unload and measures RAM and commit
  again before forwarding the inference. OpenAI, Anthropic and administrative
  swaps use the same check; a failed unload or a failed memory query refuses
  the load.
- `CIA_EDGE_MAX_ACTIVE` must be `1`, consistent with the manifest and the
  router. No other inference may overlap the unload of a model in use.
- Named tool choice is refused explicitly. There is evidence that runtime
  b10549 ignores that field; forwarding it as if it were supported would
  violate the contract. `auto`, `none` and `required`/`any` remain available.
- Five scripts no longer compute paths from `$PSScriptRoot` inside parameter
  defaults. That initialization failed when the script was run directly under
  Windows PowerShell 5.1. Paths are now resolved in the script body, and
  explicit arguments are still honoured.
- The runtime build refuses a missing manifest before touching the checkout,
  because the manifest carries the guard against overwriting existing runtimes.
  The smoke test also refuses unknown models and runtimes before loading them.

## Evidence completed in this review

| Criterion | Result |
|---|---|
| Go, Windows | 728 tests and subtests passed; six optional tests not run |
| Go, Linux with `-race` | 653 tests and subtests, 18 packages passed, no races reported |
| Frontend | TypeScript, ESLint, Stylelint, 315 tests, build and bundle inspection passed |
| Active monitor | 11 tests passed |
| Go vet / Staticcheck / govulncheck | Passed; zero vulnerabilities found |
| npm audit | Zero vulnerabilities |
| Gitleaks on the current tree | Zero findings |
| Integrity of the four GGUFs | Sizes and SHA-256 match the manifest |
| Provenance of the four GGUFs | Revision, size, LFS SHA-256 and declared license match at the pinned sources |
| Integrity of the two active runtimes | Executable SHA-256 matches |
| Manifest and configuration generation | Structural and semantic validation passed |
| Native script entry points | 15 regressions passed on Windows PowerShell 5.1, included in CI |
| Release previews | Canary configuration, harness and publication of both runtimes run with the default paths; nothing applied |
| Release transaction / rollback | 11 checks passed |
| Artifact publication | 11 checks passed in a temporary root |
| Existing canary installation, online | Verification passed; discovery without starting another runtime |
| Review build | 12 Go commands and the icon generator compiled; no installed binary was replaced |
| Reproducibility | Edge and administrative MCP rebuilt with identical SHA-256; Go modules verified |
| Current Codex client | Go fixture fixed, tests and `go.mod` preserved, run verified and session closed |
| Revised edge overhead | 30 pairs of real warm inferences: p95 of 18.245 ms; throughput difference of −0.085%, within noise |

The installation results describe the release that is already installed; the
software tests describe the corrected tree. The two states must not be
confused: this review did not publish the new binaries or cut over to
production. The six Windows tests not run are optional integrations of the
frozen WebView2 console; the table does not count them as passes. A complete
inventory of the runtimes' DLLs belongs to publication into the protected
store; the executable check above is not a check of that whole tree.
In the harness preview, `deployment_ready=false` correctly records that the
installed manifest still belongs to the previous release. The preview
describes the plan; that refusal still prevents an integration from being
applied over an incompatible configuration. The runtime previews describe
copies not yet published to the protected store.

## Real model contracts

The inferences use synthetic inputs and the same runtime, GGUF and context
profile as the installation. A temporary edge applies the same memory reserves
and forwards to the router already installed, which remains solely responsible
for the model lifecycle. A provisional capability in the temporary manifest is
only a test input, not an automatic promotion.

| Profile | Current evidence |
|---|---|
| Gemma 4 12B QAT, 256k | Plain Responses, SSE, cancellation and recovery passed. In the diagnostic tool battery, one of the ten forced-tool cases (`namespace_tool_9`) failed; `function_calling` stays false. |
| Qwen 3.8 Deep, 32k | 27 checks passed: Responses, namespaced tools, literal arguments, continuation, SSE, cancellation, recovery and refusal of remote state. |
| Qwen 3.8 Agent, 128k | 28 checks passed on the revised edge, including the tool-selection controls. A real Codex session completed in 114.469 s, with four commands, an initial failing test, a fix, passing tests and an exact final answer. |
| Qwen 3.6 Huge, 256k | Current physical revalidation pending: the gate requires 18.2 GiB of free RAM and refused the load with the memory available. No reserve was reduced. |

Only capabilities backed by real tests are changed in the manifest. The
evidence, with no generated text, is versioned in
[`benchmarks/readiness-20261001`](../../benchmarks/readiness-20261001).
The local logs and review binaries are in
`C:\IA\tmp\readiness-fixes-20261001`. The reproducible tests are
`scripts/v2/eval/responses_contract.py` and `scripts/v2/eval/edge_overhead.py`.

The pinned sources used for the provenance check are the pages for
[Gemma QAT](https://huggingface.co/unsloth/gemma-4-12B-it-qat-GGUF/blob/980b060c40a8539ac159e0501a3e0f66a6365af3/README.md),
[Qwen 3.8](https://huggingface.co/unsloth/Qwen3.8-27B-GGUF/blob/4ca720788d1e01f1bff70c033e0d0028fd02e502/README.md) and
[Qwen 3.6](https://huggingface.co/unsloth/Qwen3.6-35B-A3B-GGUF/blob/a483e9e6cbd595906af30beda3187c2663a1118c/README.md).

The benchmark alternates router/edge order to reduce warm-up bias, separates
the warmups, compares identical token counts and subtracts the compute time
reported by the runtime when measuring overhead. The throughput regression is
computed from the full request duration. The result holds for short, warm
inferences on Gemma; it does not measure full-context performance.

## Scope and limits

- Gemma's known tool-call failure is reflected in its disabled capability.
- The waived soak is not turned into evidence of prolonged stability.
- The four models are the fixed roster; no replacement model is required.
- Beyond the scans in the evidence table, the security evidence covers
  role-specific credentials and refusals issued before a request reaches the
  runtime.

## Open gates

The Codex client gate passed; the Huge RAM block remains. The query at
03:39 UTC on 2026-10-02 recorded 15.56 GiB free; the gate requires 18.2 GiB and
refuses the load. The owner reported that they cannot free memory at this
time; the running applications and the reserves were left as they are. The
four models and the active runtimes remain `candidate`; final publication
requires qualification and the protected-artifact boundary the project
specifies.

To close the review, three things remain: run Huge's declared contracts once
enough memory is available, record the qualification of the models and
runtimes from the complete evidence, and prepare the final configuration
against verified copies in the protected store. Applying the configuration and
cutting over remain a step separate from preparing the code. The gate is not
lowered, and no state is changed to simulate completion.

The Codex test configuration uses an explicit local provider and an ephemeral
session, following the [official configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference)
and the [non-interactive execution documentation](https://learn.chatgpt.com/docs/non-interactive-mode).
No catalog result, on its own, replaces the client test.
