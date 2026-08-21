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

### 11.0b Choose the build, not just the quantization level

The authoritative reference for this model is Alibaba's own repository, **`Qwen/Qwen3.8-27B`** (Apache-2.0). Architecture, context handling, and recommended sampling settings come from there. Every GGUF — first-party or community — is a *derived* artifact: record its publisher and immutable revision in `source`, and verify its per-tensor choices yourself. Check the official card for a first-party GGUF before reaching for a rebuild.

Then read `TUNING.md` §1.3. Three checks made here outweigh every serving flag later:

- **`blk.64` must be Q5_K or higher.** The MTP head on this model is block 64, 15 tensors after the 64 main blocks. Builds that quantize it to Q4_K measure **0% draft acceptance** — speculation fails completely and silently — while builds keeping it at Q5_K–Q8_0 reach 73–74%. The nominal quantization label does not tell you which you have:

```powershell
$gguf = 'C:\IA\models\Qwen3.8-27B-GGUF\<file>.gguf'
gguf-dump $gguf | Select-String -Pattern 'blk\.64\.'      # every row must be Q5_K or better
gguf-dump $gguf | Select-String -Pattern 'nextn|mtp'       # head present at all
```

  Expect a `nextn_predict_layers` metadata key and `blk.N.nextn.*` tensors. A build missing them loads and serves normally and simply never speculates.

- **Do not select on file size alone.** Compact builds are exactly the ones likely to have quantized `blk.64` down. A slightly larger build that passes the check beats a smaller one that fails it by roughly 2x. Among builds that pass, prefer imatrix calibration with per-tensor overrides over a uniform quantization at the same nominal level.

- **Prefer KV `q4_0` over `q8_0`.** It buys longer context *and* less offload at once (§1.2). Validate recall with the stress eval rather than assuming `q8_0` is the safer default.

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

Compute the envelope first — `TUNING.md` §1.2 gives the arithmetic, and it rejects configurations before a sweep costs you an afternoon. Two results from it matter here:

- The template's 96k / KV `q8_0` combination needs roughly **5.1 GiB** offloaded, not 4.4: the KV cache alone is 3.19 GiB and usable VRAM is ~14.8, not 15.92.
- **128k with KV `q4_0` needs less offload than 96k with `q8_0`** (2.25 vs 3.19 GiB of cache), so it is both longer-context and faster. On a VRAM-bound hybrid, KV precision costs throughput indirectly by forcing weights off the card. Measure quality on both before assuming `q8_0` is the safer default.

Then sweep `-NGpuLayers` and `-ot` patterns and read `llm_load_tensors: CPU buffer size` from the load log to confirm the estimate. Offload FFN tensors of a contiguous tail only — never reduce `--n-gpu-layers`, which would evict full-attention layers (`blk.3, 7, 11, … 63`) and push their KV cache into system RAM. Starting pattern:

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
  "source": { "repository": "<GGUF publisher>/Qwen3.8-27B-GGUF", "revision": "<40-hex upstream commit>",
              "filename": "<file that passed the blk.64 check>.gguf", "license": "Apache-2.0" },
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
  // cache_ram_mib / ctx_checkpoints / checkpoint_min_step / cache_idle_slots
  // are deliberately omitted: context checkpoints are upstream-reported
  // non-functional on hybrid/recurrent models (TUNING.md 1.4), and the gate
  // charges cache_ram_mib to commit in full, so an inert cache costs headroom.
  // Add them only after qwen38-27b-iq4xs-agentic-restore passes.
  // "threads": <winner of the {8, 16} sweep>,
  "spec_decoding": { "type": "draft-mtp", "draft_n_max": 3 },
  "tensor_overrides": [
    { "pattern": "blk\\.(4[4-9]|5[0-9]|6[0-3])\\.ffn_.*", "buffer": "CPU" }
  ],
  "capabilities": { "responses": false, "chat_completions": true, "streaming": true,
                    "function_calling": false, "structured_output": false },
  "resources": { "peak_vram_gib": null, "peak_commit_gib": null, "peak_ram_gib": null }
}
```

`context_shift: false` is mandatory: the recurrent state cannot be shifted, and the schema refuses `spec_decoding` without it. `cache_ram_mib` requires a measured `peak_commit_gib`, and `tensor_overrides` requires a measured `peak_vram_gib` — both are validated at generation time, and the edge refuses admission until they are present.

**On the prompt cache.** llama.cpp issues #24055 and #22384 report that context checkpoints are created and immediately invalidated on hybrid/recurrent models, with the fix unmerged. Until `qwen38-27b-iq4xs-agentic-restore` demonstrates real restoration on the candidate runtime, `cache_ram_mib` buys nothing here and is worse than neutral: admission charges it to commit in full, and commit is the binding constraint on this machine. When it does become usable, size it against the headroom measured in 11.0 rather than the nominal RAM budget, and remember a cache that forces the pagefile is worse than no cache.

`spec_decoding.draft_n_max: 3` is set for an **offloaded** split, not copied from the resident-model optimum of 7. Speculation amortizes weight reads but not arithmetic, and the CPU-resident portion is compute-bound, so past a shallow depth each extra drafted token costs more than it returns — at the pessimistic end a depth of 7 is slower than not speculating at all. The optimum moves whenever the `-ot` pattern moves; sweep the two together. See `TUNING.md` §1.1.

Measure `peak_vram_gib`, `peak_commit_gib`, and `peak_ram_gib` from a live load and fill them in, following the measurement rules in `MODEL_PROMOTION.md` — in particular, capture `peak_commit_gib` on the first request after start, with a cold prompt cache.

### 11.4 Validate and generate

```powershell
.\scripts\v2\Test-V2Manifest.ps1 -SchemaValidatorPath 'C:\IA\local-ai-v2\bin\cia-manifest.exe'
.\scripts\v2\Test-V2ConfigGeneration.ps1
.\scripts\v2\New-V2Config.ps1 -Environment Canary
```

In the preview, confirm the new model's `cmd:` contains `--no-context-shift`, `--cache-ram`, `--spec-type draft-mtp`, and one `-ot` per override — and that every other model's `cmd:` is unchanged.

### 11.5 Qualify

Run the profiles in `model-test-matrix.json` (`qwen38-27b-*`), then the quality and stress evaluations. Record the MTP draft acceptance rate at the exact `context_tokens` you intend to ship: llama.cpp issue #23658 documents acceptance collapsing to near zero at specific context sizes on a ~2048-token period, unfixed and independent of quantization. If acceptance is poor, try +/-256 and +/-2048 before concluding MTP does not work here. Leave `function_calling` false until `run-profile-stress-eval.py` produces a valid forced tool call. Then follow `MODEL_PROMOTION.md` as usual.

If throughput disappoints, do not re-quantize on instinct: `TUNING.md` gives the bandwidth ceiling for a given weight split, so you can tell whether the configuration is at its hardware limit or something is actually broken. On this hardware the first gibibyte of offload costs ~37% of decode throughput, which frequently makes a smaller fully-resident quant the faster choice.

## 12. Adopt the buun-llama-cpp agentic runtime (Qwen3.8 only)

The upstream build cannot restore context checkpoints on a hybrid/recurrent
model (§`TUNING.md` 1.4), so an agentic session re-prefills its whole context
every turn. ADR 0010 adopts a pinned commit of `spiritbuun/buun-llama-cpp` as a
**second** runtime for that one case. The upstream runtime, its hashes, its
paths, the models bound to it and `provider.public_model` are not touched by any
step below.

### 12.1 Pin the commit and prove the correction

Never pin `master`, `main`, `latest` or `HEAD` — the schema refuses them, and so
should you. Resolve an exact commit, then gate it before compiling anything:

```powershell
go build -trimpath -o C:\IA\local-ai-v2\bin\cia-fork-gate.exe .\cmd\cia-fork-gate
git ls-remote https://github.com/spiritbuun/buun-llama-cpp master   # read the SHA, then pin it
C:\IA\local-ai-v2\bin\cia-fork-gate.exe `
  --source C:\IA\src\buun-llama-cpp `
  --revision <40-hex commit> `
  --compiler clang++ `
  --report C:\IA\local-llama\amd\fork-gate-report.json
```

The gate compiles the fork's own checkpoint predicate and asserts that recurrent
selection ignores `pos_min` and the position threshold entirely, decides on the
recurrent frontier instead, and leaves transformer selection unchanged. It also
reads the fork's shipped defaults, which the profile has to pin.

**If it fails, stop.** Do not patch the fork locally and do not build it anyway.
Record the reason in the change and qualify a different commit; a private variant
of llama.cpp is not a dependency this project is willing to carry.

### 12.2 Build for gfx1201 only

```powershell
.\scripts\v2\Build-V2ForkRuntime.ps1 -Revision <40-hex commit>          # preview
.\scripts\v2\Build-V2ForkRuntime.ps1 -Revision <40-hex commit> -Apply
```

Release, ROCm/HIP, `gfx1201`, target `llama-server` and nothing else. The install
directory carries the commit, and the script refuses to write into any directory
an existing manifest runtime occupies, so the baseline cannot be overwritten. It
prints the runtime entry with the artifact SHA-256 and the gate report hash in it.

### 12.3 Review the entry in, and pin every control variable

Add the printed entry as a **new** `runtimes[]` element. Do not edit the existing
ones. The model that uses it must state every setting whose fork default differs
from upstream — `Assert-V2ManifestSemantics` refuses it otherwise:

```json
"context_shift": false,
"kv_unified": true,
"cache_ram_mib": 0,
"cache_idle_slots": false,
"ctx_checkpoints": 64,
"checkpoint_min_step": 512,
"cache_type_k": "q4_0",
"cache_type_v": "q4_0",
"parallel": 1,
"batch_size": 2048
```

`cache_ram_mib: 0` and `cache_idle_slots: false` are not decoration: the fork
ships an 8 GiB host prompt cache enabled and idle-slot saving on. Omitting either
turns them on, adds commit pressure, and makes the comparison measure two things.
The host prompt cache and `/slots` persistence are both out of scope for this
qualification — what is being tested is checkpoint reuse inside a live session.

Then validate and generate as in §11.4. `Test-V2Manifest.ps1` and
`Test-V2ConfigGeneration.ps1` both cover the fork shape.

### 12.4 Qualify progressively, against an upstream control

Gates A → B → C → D from `BENCHMARKS.md`. Stop at the first one that fails; do
not start at 256k.

```powershell
# B side: the fork
.\scripts\v2\Measure-V2AgenticReuse.ps1 -BaseUrl http://127.0.0.1:19300 `
  -Model qwen38-27b-buun -RuntimeLabel buun -BaseContextTokens 60000 `
  -ServerProcessId <llama-server pid> -OutputPath .\buun-60k.json

# A side: upstream, same fixture, same everything else
.\scripts\v2\Measure-V2AgenticReuse.ps1 -BaseUrl http://127.0.0.1:19301 `
  -Model qwen38-27b-upstream -RuntimeLabel upstream -BaseContextTokens 60000 `
  -ServerProcessId <llama-server pid> -OutputPath .\upstream-60k.json

.\scripts\v2\Compare-V2Runtimes.ps1 -BaselineReport .\upstream-60k.json `
  -CandidateReport .\buun-60k.json -OutputPath .\ab-60k.json
```

The A side needs an upstream build that knows the Qwen3.8 architecture; the
pinned `amd-rocm-baseline` (b8407) does not, so it is a separate runtime entry
added the same way. Without it there is no comparison, only a measurement.

Sweep `ctx_checkpoints` {32, 64, 128} and `checkpoint_min_step` {256, 512, 1024,
2048} one variable at a time, and take the **smallest** checkpoint count that
holds reuse without a relevant re-prefill. Sweep `spec_draft_n_max` {0, 2, 3, 5,
7} separately, recording draft acceptance beside throughput. Near 256k, sweep the
context size itself — ship the value with the better MTP acceptance rather than
the round one.

### 12.5 Measure the envelope, then promote deliberately

The fork profile offloads tensors, so it stays refused with
`resource_profile_incomplete` until `peak_commit_gib`, `peak_vram_gib` and
`peak_ram_gib` are all measured on the RX 9070 XT under §11.0 discipline. There
is no fork-specific allowance in admission and none will be added.

The runtime starts `candidate`. It reaches `qualified` only after artifact and
hash validation, the ROCm/gfx1201 and Gated DeltaNet gates, short context, the
agentic checkpoint regression, 128k, 192k, ~256k, the MTP sweep, the memory
measurement, resource admission, tool calling, streaming, Codex/OpenCode
end-to-end, and the soak. `enabled` and any change to `provider.public_model`
remain explicit operator decisions.

### 12.6 Return to upstream

Point the model's `runtime` field back at the upstream entry, set the fork
runtime to `retired`, and regenerate. Nothing else changes: no code path knows
the fork by name, and no abstraction was added for it. Do this when upstream
merges an equivalent correction and matches the fork on these gates — or at any
point the fork stops being worth its maintenance.

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
| `insufficient_vram_budget` | Measured `peak_vram_gib` plus the 3 GiB reserve exceeds `device.vram_mib` | Shrink context, drop the KV type, or offload more weight |
| `resource_profile_incomplete` | Model uses host memory and at least one required measurement is missing; `missing_profile_fields` names them | Measure and record the named fields; this never resolves on its own |
| `canary_resource_measurement_pending` | Unmeasured canary candidate that uses no host memory | Acceptable for small candidates; measure before qualifying |
| `resource_measurement_required` | Unmeasured model outside the canary escape hatch | Measure and record `resources.peak_*` |

## 13. Choosing a Qwen3.8 profile

`provider.public_model` is unchanged; these are selectable canary models, and
switching between them is a llama-swap model switch, not a reconfiguration.

Three profiles, three different jobs. One configuration cannot serve all three,
because the thing each optimises for costs the others something measured.

| Profile | Weights | KV | Context | Output | Use it when |
|---|---|---|---:|---:|---|
| `qwen38-27b-deep-32k` | UD-IQ4_XS | `q8_0`/`q8_0` | 32768 | 8192 | Hardest localized tasks: algorithms, architecture, a complex bug in a few highly relevant files. Reasoning matters more than how much context you can hold. |
| **`qwen38-27b-agent-128k`** | UD-Q3_K_XL | `q4_0`/`q4_0` | 131072 | 8192 | **Daily default.** Codex, Claude Code, OpenCode, Unity work, refactors, features, repo investigation, tool loops. |
| `qwen38-27b-huge-256k` | UD-Q2_K_XL | `q4_0`/`q4_0` | 262144 | 32768 | Huge active context: very large repositories, long investigations, long histories, many tool calls. Explicitly a **huge-context / high-thinking-budget** profile, not the highest-quality one. |

### The selection rule

- **Need maximum reasoning reliability?** → Deep
- **Normal coding-agent work?** → Agent
- **Need huge active context?** → Huge

Choose Huge when the *working set* exceeds what Agent holds — not when the task
is merely hard. A hard, localized problem is a Deep problem. A normal or large
agentic problem is an Agent problem. Only a problem that is enormous *in context*
is a Huge problem.

**Agent is the daily default, not Deep.** A coding harness spends tens of
thousands of tokens on the system prompt, tool definitions, file contents, logs
and history before any of your problem arrives. 32k is opt-in for localized work,
not a starting point.

### Why Agent uses 3-bit weights

Measured in `benchmarks/REPORT-qwen38-27b-q3-q2-kvq4-20260821.md`, at 32k with KV
`q4_0/q4_0`, against an IQ4_XS control on identical settings:

| | IQ4_XS | Q3_K_XL |
|---|---:|---:|
| Compiled coding suite | 9/10 | 9/10 |
| Incorrect answers | 1 | **0** |
| Tool calling | 4/4 | 4/4 |
| `tg128` | 16.04 t/s | **23.12 t/s** |
| `pp8192` | 258.80 t/s | **743.34 t/s** |
| Shared GPU memory at 128k, at load | 4094 MiB | **3507 MiB** (`ok`) |

Q3_K_XL matched IQ4_XS on coding quality in this suite and was substantially
easier to keep below this workstation's VRAM occupancy cliff — those are two
different claims and only the second is about the model being smaller. It is
**not** established that Q3 is universally better than IQ4; what is established
is that on this adapter, at these settings, it measured equal on quality and
better on throughput and stability.

### Known limitations

- **Long-context retention is not yet validated** for either Agent or Huge.
  The memory envelope up to 256k is measured; retrieval accuracy and
  decode throughput with the window actually filled are not. Both profiles are
  `candidate` in `canary` for exactly this reason.
- **The occupancy cliff is real and moves with your desktop.** Prefill collapses
  above roughly 96% adapter occupancy. IQ4_XS sits on that boundary here, so the
  Deep profile's prefill throughput depends on what else is on screen; the same
  GGUF measured 956 t/s and 281 t/s at pp512 in two runs that differed only in
  how much VRAM the desktop held. Agent carries about two percentage points of
  margin, Huge about eighteen.
- **Qwen3.8 reasons before it answers, at length.** Measured across thirty
  generations, reasoning ran from 1404 to 35570 characters before the first line
  of the answer. A harness configured with a 2–4k output budget will see empty
  replies from this model and misdiagnose it as broken.

### The Huge profile's thinking budget

Huge exists because Q2_K_XL was measured returning nothing on three of ten
coding tasks: it spent the whole 8192-token output allowance inside
`reasoning_content` and never began the answer. It never produced *incorrect*
code — at 32k it preserved tool calling, constraint adherence and correctness on
the answers it completed, but exceeded the shipped 8192-token output budget on
the more implementation-heavy tasks.

The profile therefore splits the budget explicitly, using flags the runtime
really has (`llama-server --help` on b10549):

```
--n-predict 32768            hard ceiling on generated tokens
--reasoning-budget 24576     tokens the model may spend thinking
--reasoning-budget-message   injected to force the answer to begin
```

24576 thinking + roughly 8192 for the answer, 32768 total. Long reasoning on this
profile is **not** a failure and must not be treated as a hang — but generous is
not unlimited. On reaching 32768 without a useful answer, that is recorded as a
real operational result. It is never silently raised to 64k and never retried
indefinitely.

Deep and Agent keep an 8192-token ceiling and leave reasoning unrestricted; the
Q2 behaviour is specific to Q2 and the other two profiles were qualified without
a reasoning budget.

### Context and output accounting

Each profile reserves room for generation, so a harness can never fill the window
with input and leave nothing to answer with.

| Profile | Context | Output reserve | Compact before | Codex `effective_context_window_percent` |
|---|---:|---:|---:|---:|
| Deep | 32768 | 8192 | 23552 | 71 |
| Agent | 131072 | 8192 | 114688 | 87 |
| Huge | 262144 | 32768 | 221184 | 84 |

`compact_threshold_tokens` in `config/models.yaml` is the source of truth and is
derived as `context_tokens − max_output_tokens − safety margin`.
`New-V2ClientCatalogs.ps1` converts it into the percentage Codex expects. Models
that declare no threshold keep the previous flat 85%.

**Switching requires a model reload.** `--ctx-size` is fixed for the life of a
llama-server process; llama.cpp has no hot resize and none is simulated here.
Request the profile by model id and llama-swap unloads the current one first,
because `provider.max_loaded_models` is 1. Expect a cold load of roughly ten
seconds.

### Retired profiles

The five `qwen38-27b-ws-*` profiles are `state: retired` with no deployments.
They remain in the manifest because the benchmark reports reference them by id;
they are not offered to a harness.

| Retired | Replaced by | Why |
|---|---|---|
| `qwen38-27b-ws-32k` | `qwen38-27b-deep-32k` | Same configuration, clearer name |
| `qwen38-27b-ws-64k` | `qwen38-27b-agent-128k` | No distinct role once Deep and Agent exist |
| `qwen38-27b-ws-128k` | `qwen38-27b-agent-128k` | Agent is better on every measured axis for this job |
| `qwen38-27b-ws-8k-prefill` | — | Its only unique property was being the sole `ok`-pressure profile; Agent at 128k and Huge through 128k now reach `ok` |
| `qwen38-27b-ws-32k-kv-q4` | `qwen38-27b-agent-128k` | The IQ4-versus-Q3 comparison at `q4_0` it existed to enable is complete |

### Reading the GPU pressure verdict

`GET /api/v1/status` carries a `gpu_memory` block, and the metrics endpoint
exposes `cia_edge_gpu_dedicated_mib`, `cia_edge_gpu_shared_mib`,
`cia_edge_gpu_occupancy_ratio` and `cia_edge_gpu_memory_pressure`
(-1 unknown, 0 ok, 1 elevated, 2 pressured).

| State | Meaning | Action |
|---|---|---|
| `ok` | Adapter below 95% dedicated | None |
| `elevated` | At or above 95% dedicated, shared below 1024 MiB | None required. The fastest configuration measured on this hardware sits here. |
| `pressured` | At or above 95% dedicated **and** shared at or above 1024 MiB | The driver is likely paging over PCIe. Prompt processing degrades with no error. Switch to a smaller context profile, or close other GPU consumers. |
| `unknown` | No adapter counters, or no declared device budget | Not a clean bill of health. Investigate before trusting a memory figure. |

This is a diagnosis, never an action: nothing refuses a request or unloads a
model on it. A verdict of `pressured` on the default profile with a browser and
an IDE open is expected on this hardware and is not by itself a fault.

**Close GPU consumers before blaming the model.** The desktop holds roughly
3.0 GiB of dedicated VRAM on this workstation before anything loads, which is
most of the difference between what llama.cpp's load log reports and what the
adapter shows. `scripts/v2/Measure-V2ContextFootprint.ps1` records an idle sample
alongside the peak precisely so the two can be told apart.

### Re-measure after any change to the split, context, or KV type

```powershell
& C:\IA\local-llama\scripts\v2\Test-V2WorkstationSmoke.ps1 -ModelId qwen38-27b-agent-128k `
    -OutputPath C:\IA\local-llama\benchmarks\smoke-qwen38-27b-agent-128k.json
```

The smoke report's `peak` block is what `resources.peak_vram_gib`,
`peak_ram_gib` and `peak_commit_gib` should be set from. Take them from a serving
run, not from a load-time footprint: measured on the default profile the working
set under load was 14.24 GiB against 12.73 GiB at load, and admission control
gates on the larger figure.

## Safe rollback

- Stop/disable v2 Edge, then Router.
- Restore harnesses to their prior explicit cloud profile or temporarily use direct `llama-server` on loopback for local work.
- Preserve v2 configs, sanitized logs, hashes, and failure timestamps for diagnosis.
- Do not reactivate the v1 panel/proxy or its cloud fallback.

## Incident evidence and cleanup

Never print a secret during investigation. Record event time, request ID, process identity, listener, exit code, artifact hash, and redacted stack signature. Deleting contaminated v1 artifacts, adding firewall rules, or tightening ACLs requires separate confirmation with exact resolved targets.
