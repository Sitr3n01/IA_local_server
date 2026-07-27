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

## Health interpretation

| Observation | Meaning | Action |
|---|---|---|
| `/livez` fails | Edge process unavailable | Inspect task state and metadata-only events |
| `/livez` passes, `/readyz` fails | Router/config/model admission unavailable | Check router task, hashes, credentials, capacity |
| `429` | Queue or wait policy intentionally enforced | Harness retries with backoff or operator reduces load |
| `503` | Local provider cannot serve safely | Fix local dependency; do not enable fallback |
| Model process absent while idle | Expected lazy state | No action |
| Model starts after `/v1/models` | Contract regression | Stop cutover and file a blocking defect |

## Safe rollback

- Stop/disable v2 Edge, then Router.
- Restore harnesses to their prior explicit cloud profile or temporarily use direct `llama-server` on loopback for local work.
- Preserve v2 configs, sanitized logs, hashes, and failure timestamps for diagnosis.
- Do not reactivate the v1 panel/proxy or its cloud fallback.

## Incident evidence and cleanup

Never print a secret during investigation. Record event time, request ID, process identity, listener, exit code, artifact hash, and redacted stack signature. Deleting contaminated v1 artifacts, adding firewall rules, or tightening ACLs requires separate confirmation with exact resolved targets.
