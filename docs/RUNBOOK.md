# Operations runbook

All commands below assume PowerShell in `C:\IA\local-llama`. Scripts are non-destructive by default. `-Apply`, `-Run`, starting tasks, firewall changes, ACL changes, and log deletion are explicit operator actions. Preview firewall targets with `scripts\v2\Set-V2Firewall.ps1` and ACL targets with `scripts\v2\Set-V2Acl.ps1`; apply either only from an elevated PowerShell with `-Apply`.

## 1. Preflight

```powershell
git status --short
.\scripts\v2\Test-V2Manifest.ps1 -VerifyArtifacts
.\scripts\v2\Test-V2HarnessConfig.ps1
.\scripts\v2\Test-V2Installation.ps1 -Environment Canary
```

The installation check is expected to report missing v2 files before the first deployment. It must not report model/runtime hash mismatches or non-loopback listeners.

Do not clean or reset the dirty v1 panel file. Preserve it independently before any later v1 archival.

## 2. Install pinned artifacts

Build from tracked source. Never build an elevated process directly into the
protected `bin` directory. `Build-V2Binaries.ps1` runs the complete Go test
suite and atomically publishes exactly five application artifacts to the
user-writable staging directory: Edge, read-only MCP, administrative MCP,
inference MCP, and the Windows-GUI tray. Their independently reviewed hashes
authorize the later elevated cutover. Run `Test-V2Manifest.ps1`; it requires
the already installed `cia-manifest.exe` and applies
`config/models.schema.json` before semantic and artifact checks.

```powershell
$version = 'v2-canary-YYYYMMDD.N' # replace with one immutable reviewed release label
.\scripts\v2\Build-V2Binaries.ps1 -Environment Canary -Version $version
.\scripts\v2\Build-V2Binaries.ps1 -Environment Canary -Version $version -Apply
```

The apply result must say `tests_passed: true` and list hashes for
`cia-edge.exe`, `cia-mcp.exe`, `cia-mcp-admin.exe`,
`cia-mcp-inference.exe`, and `cia-tray.exe`. Record and independently review
all five SHA-256 values. Do not calculate or substitute them inline in a later
`-Apply` command: each literal reviewed value is an approval boundary. The
script uses a private build directory and rolls back all staging publications
if any of the five cannot be published and reverified.

Obtain `llama-swap_240_windows_amd64.zip` from the official v240 release and verify it before extraction:

```powershell
Get-FileHash .\llama-swap_240_windows_amd64.zip -Algorithm SHA256
```

Expected archive SHA-256:

```text
EBDC3465809923ACACF0064FCAB7DBD1B745CFAE239BE72590C8CD94B172177D
```

After extraction, the expected `llama-swap.exe` SHA-256 is:

```text
60362E63EBAF97CF0DEC791986479022AB87FE7D1350A64D55B256821A437BAA
```

Do not use `latest`, a branch archive, Winget, or an unverified replacement during qualification.

## 3. Initialize credentials

Preview, then initialize only missing `inference`, `admin`, and `router` credentials:

```powershell
.\scripts\v2\Initialize-V2Secrets.ps1
.\scripts\v2\Initialize-V2Secrets.ps1 -Apply
```

The helper stores values in Windows Credential Manager and never prints them during initialization. To rotate one credential, provide a freshly generated value on standard input to `cia-credential.exe set NAME`, then restart affected components in this order:

1. Router token: stop edge, stop router, set token, start router, start edge.
2. Inference token: stop edge, set token, start edge, update harness credential/profile.
3. Admin token: stop edge, set token, start edge, restart admin clients only.

Never paste secret values into shell history, issue trackers, logs, Git, or documentation.

## 4. Generate the canary installation

```powershell
.\scripts\v2\New-V2Config.ps1 -Environment Canary
.\scripts\v2\New-V2Config.ps1 -Environment Canary -Apply
```

Generation verifies the selected runtime and model hashes before writing. It
creates serving configuration, `panel.canary.json`, and hidden launchers under
`C:\IA\local-ai-v2`; it does not download software, start a process, alter a
user config, create a task, or change firewall/ACL rules.

Run the preview normally and the `-Apply` command from an elevated PowerShell;
the script deliberately refuses a non-elevated write to protected
configuration and launcher directories. `Complete-V2Canary.ps1` re-runs this
generation transaction during cutover. The separate apply here is needed on a
first installation so the launchers exist before scheduled-task registration.

Inspect generated files. They must contain `${env.CIA_ROUTER_TOKEN}`, never a literal secret.

## 5. Register startup tasks

```powershell
.\scripts\v2\Install-V2ScheduledTasks.ps1 -Environment Canary
.\scripts\v2\Install-V2ScheduledTasks.ps1 -Environment Canary -Apply
```

The installer creates two hidden, limited, per-user tasks whose direct action is `cia-supervisor.exe`; it does not start them. The supervisor owns a kill-on-close Job Object, so `Stop-ScheduledTask` must remove the corresponding listener and all descendants. If a task already exists, inspect it first; replacement requires the additional `-Replace` switch.

The tasks must exist before `Complete-V2Canary.ps1` is used. On an existing
canary update, retain the reviewed tasks; completion will stop and restart them
in the safe order. Do not manually start them during a completion run, and do
not enable the final tasks while canary validation is in progress.

## 6. Complete the reviewed canary

First preview the harness transaction using the exact target user. Its
`plan_sha256` binds sources, destinations, existing destination hashes, and
replacement actions:

```powershell
$codexHome = 'C:\Users\Sitr3n\.codex'
.\scripts\v2\Install-V2Harness.ps1 -Environment Canary -TargetCodexHome $codexHome -Replace
```

After independently recording that plan hash and the five hashes emitted by
`Build-V2Binaries.ps1`, place only the literal reviewed values in the approval
map. Open an elevated PowerShell, define the map there, and run the first
command as a non-mutating preview. Run the second command in the same shell
only after that preview is identical to the intended cutover:

```powershell
$canaryApproval = @{
    TargetCodexHome               = 'C:\Users\Sitr3n\.codex'
    ExpectedHarnessPlanSha256     = '<reviewed 64-hex harness plan SHA-256>'
    ExpectedEdgeSha256            = '<reviewed 64-hex cia-edge.exe SHA-256>'
    ExpectedMcpSha256             = '<reviewed 64-hex cia-mcp.exe SHA-256>'
    ExpectedMcpAdminSha256        = '<reviewed 64-hex cia-mcp-admin.exe SHA-256>'
    ExpectedMcpInferenceSha256    = '<reviewed 64-hex cia-mcp-inference.exe SHA-256>'
    ExpectedTraySha256            = '<reviewed 64-hex cia-tray.exe SHA-256>'
    Replace                       = $true
}

.\scripts\v2\Complete-V2Canary.ps1 @canaryApproval
.\scripts\v2\Complete-V2Canary.ps1 @canaryApproval -Apply
```

Completion revalidates every staging hash before mutation. It then generates
configuration, installs the harness transaction, atomically installs the three
MCP binaries and tray, stops Edge followed by Router, atomically replaces
Edge, applies ACL and firewall policy, restarts Router followed by Edge, audits
ACLs, and runs the online installation check. Once process cutover begins, the
task restart is attempted from `finally` even if installation fails. The script
does not load a model; first inference remains lazy. Do not replace this
transaction with a hand-written sequence of individual binary installers.

## 7. Install the separate inference MCP integration

`cia-mcp-inference.exe` is separate from the read-only and administrative MCP
servers. It exposes only `local_ai_delegate`, pins the canary Edge and model,
has no tools, files, history, model selection, or administrative access, and
reads the inference credential from Windows Credential Manager only when a
delegation is actually requested. Its tool description instructs harnesses to
invoke it only when the user explicitly asks to use or consult the local
model.

Close the harness applications that will be changed. Run preview and apply as
the normal `Sitr3n` user, not from an elevated account: encrypted backups use
DPAPI `CurrentUser`. With no `-Clients`, the script changes only detected
clients. To require a specific subset, pass the same explicit list to both
commands, choosing only installed clients:

```powershell
$mcpClients = @('Codex', 'ClaudeDesktop', 'ClaudeCode', 'OpenCode')
.\scripts\v2\Install-V2McpInferenceIntegrations.ps1 -Clients $mcpClients

# Copy the literal plan_sha256 from the reviewed preview. Do not derive it inline.
.\scripts\v2\Install-V2McpInferenceIntegrations.ps1 `
    -Clients $mcpClients `
    -ExpectedPlanSha256 '<reviewed 64-hex MCP integration plan SHA-256>' `
    -Apply
```

The integration transaction preserves unrelated configuration, never changes
a provider or primary model, sets prompt/ask approval for the delegated tool,
and stores verified encrypted backups below
`C:\IA\local-ai-v2\state\integration-backups`. A client configuration change
between preview and apply invalidates the plan instead of being overwritten.

Fully exit and restart Claude Desktop after apply; start a new Claude Code,
Codex, or OpenCode session rather than reusing an existing one. These normal
sessions keep their selected cloud or other primary model. When the user says,
for example, "use o modelo local para revisar este trecho", the harness may ask
approval and call `local_ai_delegate`; the result returns to the primary
session. A prompt that does not explicitly request the local model must not
trigger delegation. No provider restart is required merely for a client MCP
configuration change.

Run the SDK-based live probe against the installed stdio server. It reports
only model/finish/usage metadata, output length and SHA-256; it never prints
the prompt, response or credential:

```powershell
go run .\cmd\cia-mcp-smoke `
    -server C:\IA\local-ai-v2\bin\cia-mcp-inference.exe `
    -data-url http://127.0.0.1:18090 `
    -model local-coding `
    -expected CIA_LOCAL_MCP_SMOKE_OK
```

## 8. Restart and verify canary

`Complete-V2Canary.ps1` already restarts Router then Edge and runs the online
check. After it reports success, independently verify the installed hashes,
live state, and ACL boundary:

```powershell
.\scripts\v2\Test-V2Installation.ps1 -Environment Canary -VerifyHashes
.\scripts\v2\Test-V2Installation.ps1 -Environment Canary -Online
.\scripts\v2\Set-V2Acl.ps1 -Audit
```

Then verify manually:

- Router, data, and control listeners are loopback-only.
- `GET /v1/models` does not change the `llama-server` process count.
- An authenticated first inference lazily starts exactly one server.
- An unknown model returns `404` without DNS or external traffic.
- Five concurrent waits are bounded; overflow returns `429` with `Retry-After`.
- Killing `llama-server` yields a visible failed request and recoverable subsequent start.
- Killing a verified edge/router child removes its listener and the supervisor recreates it after the one-minute initial backoff without creating a duplicate.
- Fifteen idle minutes unload the model.

### Harness, MCP, and panel smoke tests

The completion transaction adds
`%CODEX_HOME%\cia-local-canary.config.toml`, copies the
OpenCode canary override and local-only catalog, and installs the three
allowlisted panel launchers below `C:\IA\local-ai-v2\integrations`. It never
edits `~/.codex/config.toml`, an OpenCode global/project config, Unsloth private
state, or any cloud credential. A differing existing canary file is preserved
unless the operator inspects it and supplies `-Replace`.

With Router and Edge canary already healthy, test in this order:

```powershell
.\integrations\opencode\Start-OpenCodeLocalCanary.ps1
.\integrations\codex\Start-CodexLocalCanary.ps1
```

After the installed hashes and ACL audit pass, launch the canary panel manually:

```powershell
wscript.exe C:\IA\local-ai-v2\launchers\tray-canary.vbs
```

Double-click the icon and confirm that all registered and detected GGUFs are
visible, search and details update asynchronously, the loaded state is
independent from the selection, closing returns to the tray, and `Sair` leaves
Router and Edge running. Confirm that no Unsloth action or label is present. Do
not add the panel to logon until this smoke test passes. Afterwards, install the separate
current-user Startup shortcut; this does not create a third scheduled task,
start a process, or load a model:

```powershell
.\scripts\v2\Install-V2PanelStartup.ps1
.\scripts\v2\Install-V2PanelStartup.ps1 -Apply
```

If preview reports `blocked-existing`, inspect the existing shortcut and use
`-Replace` only for the reviewed conflict. A second preview must report
`unchanged`.

Unsloth is intentionally absent from the v2 panel and startup policy. Use it
manually outside CIA Local AI when training or export is intended; do not
automate its private database or browser storage.

1. Complete streaming and tool-call tests in OpenCode. Its launcher sets a
   process-only config and pins `cia-local-canary/local-coding`; the normal cloud
   default remains untouched.
2. Run one real repository edit/test cycle with the separate Codex
   `cia-local-canary` profile. The launcher pins the provider URL and wire API at
   CLI precedence so a project config cannot redirect the canary session.
3. Confirm both clients use data `18090`, control `18091`, and model
   `local-coding`.
4. In a normal cloud/other-model session, explicitly request the local model,
   approve `local_ai_delegate`, and confirm the structured result reports
   `local-coding`; then confirm an ordinary prompt does not call it.
5. Confirm `cia-mcp.exe` remains the read-only operational server,
   `cia-mcp-inference.exe` is registered separately for explicit delegation,
   and `cia-mcp-admin.exe` is absent from normal harness configurations.
6. Confirm cloud configurations, sessions, primary models, and defaults remain
   unchanged.

The provider must never compensate for a harness failure by routing to another model or service.

## 9. Audit or recover the installed ACL boundary

`Complete-V2Canary.ps1` already applies the ACL policy and performs its
independent audit. After normal completion, use audit mode only:

```powershell
.\scripts\v2\Set-V2Acl.ps1 -Audit
```

For standalone recovery or a first installation that has not used completion,
preview the exact targets before any ACL mutation:

```powershell
.\scripts\v2\Set-V2Acl.ps1
```

The policy removes inherited access from every item under the exact
`C:\IA\local-ai-v2` root, assigns ownership to the built-in Administrators
group, grants `Sitr3n` read/execute access to the installation, and grants that
user `Modify` only under `state` and `logs`. `Administrators` and `SYSTEM`
retain `FullControl` for elevated recovery. Reparse points, relative paths,
filesystem roots, and any root other than `C:\IA\local-ai-v2` are rejected.

Apply only from an elevated PowerShell when the standalone preview is the
intended policy, then run the independent read-only audit:

```powershell
.\scripts\v2\Set-V2Acl.ps1 -Apply
.\scripts\v2\Set-V2Acl.ps1 -Audit
```

Apply writes a timestamped pre-change SDDL recovery record beneath
`state\acl-backups` before changing exact child items. Subsequent deployment
changes must run from a reviewed elevated maintenance session. The script never
changes ACLs outside the v2 installation. Consequently, the runtime and GGUF
paths currently referenced outside that root still require relocation into a
protected v2 artifact directory or a separately reviewed, exact-target
hardening procedure before final cutover.

## 10. Promotion and final cutover

Complete the checklist in `MODEL_PROMOTION.md` and the soak in `BENCHMARKS.md`. Only then change `local-coding` to `qualified`/`enabled` and add `final` to its deployments.

Generate final files and tasks with the same preview/apply sequence. Migrate OpenCode first, then Codex. Observe final operation for seven days before archiving v1.

Final harness installation has its own gate and refuses to proceed until
`deployment.final.json` exists:

```powershell
$codexHome = 'C:\Users\Sitr3n\.codex'
.\scripts\v2\Install-V2Harness.ps1 -Environment Final -TargetCodexHome $codexHome -Replace
.\scripts\v2\Install-V2Harness.ps1 -Environment Final -TargetCodexHome $codexHome -ExpectedPlanSha256 '<reviewed final plan_sha256>' -Apply -Replace
```

## 11. Onboard a hybrid model with partial offload (Qwen3.8-27B)

This procedure applies to any model too large to sit entirely in VRAM. It is written against Qwen3.8-27B IQ4_XS on the RX 9070 XT (16304 MiB) because that is the case it was built for.

### 11.0 Reclaim the idle commit baseline first

This step comes before the kernel measurement because it can decide the outcome on its own, and because every later number is a delta against it.

The 2026-07-20 canary validation recorded **31.82 GiB of committed memory with no model loaded**, against a commit limit of 42.30 GiB. That leaves 10.48 GiB of headroom before llama-server starts, and a 9B consumed 8.06 GiB of it. The binding constraint on a 27B is not the model — it is what the machine is already holding.

```powershell
# Idle committed bytes, in GiB. Run with the desktop in its normal state, then
# again after closing other workloads, and record both.
$os = Get-CimInstance Win32_OperatingSystem
[pscustomobject]@{
  CommitLimitGiB = [math]::Round($os.TotalVirtualMemorySize / 1MB, 2)
  CommitFreeGiB  = [math]::Round($os.FreeVirtualMemory   / 1MB, 2)
  PhysFreeGiB    = [math]::Round($os.FreePhysicalMemory  / 1MB, 2)
}
```

Close Unsloth Studio, harness sessions, and the browser before measuring. Record the idle figure in the benchmark report as `idle_commit_gib`; without it `peak_commit_gib` is not reproducible across machine states.

**Decision point.** With ~32 GiB of physical RAM, an idle baseline that will not come down to roughly 20 GiB leaves too little headroom for a 27B in any quantization. That is a machine-state conclusion, not a model conclusion — reaching it here costs minutes, whereas reaching it after downloading 15 GiB and running the full sweep costs hours.

### 11.1 Gate zero: measure the Gated DeltaNet kernel before anything else

The pinned `amd-rocm-baseline` runtime is b8407 and does not know this architecture; the GGUFs were quantized with b10419. Download an official ggml-org ROCm build for Windows (`llama-b<N>-windows-rocm-7.2.x-gfx110X-gfx115X-gfx120X-x64`, `N >= 10419`) and unpack it *outside* the repository, then measure without touching the manifest:

```powershell
.\scripts\bench-llama.ps1 `
  -ModelPath 'C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-IQ4_XS.gguf' `
  -RuntimeRoot 'C:\IA\local-llama\amd\<unpacked build directory>' `
  -Label 'qwen38-sanity' -CacheTypeK q8_0 -CacheTypeV q8_0 -UBatchSize 512
```

Confirm the load log lists a ROCm device and that the Gated DeltaNet op is not falling back. Compare `tg128` against the 9B baselines in `benchmarks/`. If it lands near CPU-fallback speed, stop here and record the negative result — the rest of this procedure cannot recover it. See the hybrid section of `BENCHMARKS.md`.

### 11.2 Find the split

Sweep `-NGpuLayers` and `-ot` patterns and read `llm_load_tensors: CPU buffer size` from the load log. Target roughly 4 GiB resident in system RAM. Offload FFN tensors of a contiguous tail only — never reduce `--n-gpu-layers`, which would evict full-attention layers (`blk.3, 7, 11, … 63`) and push their KV cache into system RAM. Starting pattern:

```
blk\.(4[4-9]|5[0-9]|6[0-3])\.ffn_.*
```

### 11.3 Record the manifest entry

Add the runtime and model to `config/models.yaml` together with the three catalog files listed in `MODEL_PROMOTION.md`; `Test-V2HarnessConfig.ps1` compares their cardinality against the manifest and fails if they drift. Fill `artifact.bytes` and `artifact.sha256` from the real files — `Assert-V2Artifact` verifies both on `-Apply`.

```jsonc
{
  "id": "qwen38-27b-iq4xs",
  "display_name": "Qwen 3.8 27B IQ4_XS - hybrid GDN, 96k context",
  "state": "candidate",
  "deployments": ["canary"],
  "runtime": "amd-rocm-qwen38",
  "artifact": { "path": "C:\\IA\\models\\Qwen3.8-27B-GGUF\\Qwen3.8-27B-IQ4_XS.gguf",
                "bytes": 0, "sha256": "<compute with Get-FileHash>" },
  "source": { "repository": "unsloth/Qwen3.8-27B-GGUF", "revision": "<40-hex upstream commit>",
              "filename": "Qwen3.8-27B-IQ4_XS.gguf", "license": "Apache-2.0" },
  "context_tokens": 98304,
  "max_output_tokens": 16384,
  "cache_type_k": "q8_0",
  "cache_type_v": "q8_0",
  "parallel": 1,
  "batch_size": 2048,
  "ubatch_size": 512,
  "gpu_layers": 99,
  "jinja": true,
  "reasoning": "auto",
  "context_shift": false,
  "kv_unified": true,
  "cache_ram_mib": 2048,
  "ctx_checkpoints": 64,
  "checkpoint_every_n_tokens": 8192,
  "cache_idle_slots": true,
  "spec_decoding": { "type": "draft-mtp", "draft_n_max": 5 },
  "tensor_overrides": [
    { "pattern": "blk\\.(4[4-9]|5[0-9]|6[0-3])\\.ffn_.*", "buffer": "CPU" }
  ],
  "capabilities": { "responses": false, "chat_completions": true, "streaming": true,
                    "function_calling": false, "structured_output": false },
  "resources": { "peak_vram_gib": null, "peak_commit_gib": null, "peak_ram_gib": null }
}
```

`context_shift: false` is mandatory: the recurrent state cannot be shifted, and the schema refuses `spec_decoding` without it. `cache_ram_mib` requires a measured `peak_commit_gib`, and `tensor_overrides` requires a measured `peak_vram_gib` — both are validated at generation time, and the edge refuses admission until they are present.

`cache_ram_mib: 2048` is sized against the **measured** headroom from step 11.0, not against the nominal RAM budget: the gate adds the value in full on top of `peak_commit_gib`, so a 6 GiB cache consumes more than half of a 10.48 GiB headroom before the weights are counted. Raise it only after 11.0 shows the idle baseline actually came down; a larger cache that forces the pagefile is worse than no cache, because a checkpoint restored from disk competes with the very prefill it was meant to avoid.

Measure `peak_vram_gib`, `peak_commit_gib`, and `peak_ram_gib` from a live load and fill them in, following the measurement rules in `MODEL_PROMOTION.md` — in particular, capture `peak_commit_gib` on the first request after start, with a cold prompt cache.

### 11.4 Validate and generate

```powershell
.\scripts\v2\Test-V2Manifest.ps1 -SchemaValidatorPath 'C:\IA\local-ai-v2\bin\cia-manifest.exe'
.\scripts\v2\Test-V2ConfigGeneration.ps1
.\scripts\v2\New-V2Config.ps1 -Environment Canary
```

In the preview, confirm the new model's `cmd:` contains `--no-context-shift`, `--cache-ram`, `--spec-type draft-mtp`, and one `-ot` per override — and that every other model's `cmd:` is unchanged.

### 11.5 Qualify

Run the profiles in `model-test-matrix.json` (`qwen38-27b-*`), then the quality and stress evaluations. Leave `function_calling` false until `run-profile-stress-eval.py` produces a valid forced tool call. Then follow `MODEL_PROMOTION.md` as usual.

If throughput disappoints, do not re-quantize on instinct: `TUNING.md` gives the bandwidth ceiling for a given weight split, so you can tell whether the configuration is at its hardware limit or something is actually broken. On this hardware the first gibibyte of offload costs ~37% of decode throughput, which frequently makes a smaller fully-resident quant the faster choice.

## Health interpretation

| Observation | Meaning | Action |
|---|---|---|
| `/livez` fails | Edge process unavailable | Inspect task state and metadata-only events |
| `/livez` passes, `/readyz` fails | Router/config/model admission unavailable | Check router task, hashes, credentials, capacity |
| `429` | Queue or wait policy intentionally enforced | Harness retries with backoff or operator reduces load |
| `503` | Local provider cannot serve safely | Fix local dependency; do not enable fallback |
| Model process absent while idle | Expected lazy state | No action |
| Model starts after `/v1/models` | Contract regression | Stop cutover and file a blocking defect |

Capacity `reason` values from `/api/v1/status` (for slowness rather than refusal, see `TUNING.md`):

| Reason | Meaning | Action |
|---|---|---|
| `commit_headroom_available` | Measured profile fits, including any declared prompt cache | No action |
| `insufficient_commit_headroom` | Measured commit plus `cache_ram_mib` plus the 4 GiB reserve exceeds free commit | Free commit (step 11.0), lower `cache_ram_mib`, or raise the pagefile |
| `insufficient_physical_memory` | Measured `peak_ram_gib` plus the 2 GiB reserve exceeds free physical RAM | Free RAM or offload less; raising the pagefile does **not** fix this and makes it slower |
| `insufficient_vram_budget` | Measured `peak_vram_gib` plus the 1 GiB reserve exceeds `device.vram_mib` | Shrink context, drop the KV type, or offload more weight |
| `resource_measurement_required_for_host_memory` | Model declares offload or a prompt cache but has no measured profile | Measure and record `resources.peak_*`; this never resolves on its own |
| `canary_resource_measurement_pending` | Unmeasured canary candidate that uses no host memory | Acceptable for small candidates; measure before qualifying |
| `resource_measurement_required` | Unmeasured model outside the canary escape hatch | Measure and record `resources.peak_*` |

## Safe rollback

- Stop/disable v2 Edge, then Router.
- Restore harnesses to their prior explicit cloud profile or temporarily use direct `llama-server` on loopback for local work.
- Preserve v2 configs, sanitized logs, hashes, and failure timestamps for diagnosis.
- Do not reactivate the v1 panel/proxy or its cloud fallback.

## Incident evidence and cleanup

Never print a secret during investigation. Record event time, request ID, process identity, listener, exit code, artifact hash, and redacted stack signature. Deleting contaminated v1 artifacts, adding firewall rules, or tightening ACLs requires separate confirmation with exact resolved targets.
