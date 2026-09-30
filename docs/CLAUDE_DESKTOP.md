# Claude Desktop 1P and local 3P

CIA supports the Windows MSIX Claude Desktop as a native third-party-inference
client. It does not start Ollama, does not use a cloud fallback, and accepts
Desktop requests only through the `cia-edge` loopback data plane.

## Captured Windows contract

The contract was recaptured from the installed MSIX `Claude 1.37937.1.0`, not
inferred from the Ollama integration page.

| Concern | Observed contract |
| --- | --- |
| Package | resolve `Get-AppxPackage -Name Claude`; never encode a versioned path |
| Launch identity | resolve the App User Model ID through `Get-StartApps` |
| 1P data | `%APPDATA%\Claude`; CIA never writes it or its config JSON |
| 3P data | `%LOCALAPPDATA%\Claude-3p` |
| 3P registry | `configLibrary\_meta.json` selects a UUID-named JSON profile |
| 1P/3P selector | `Claude-3p\claude_desktop_config.json`, `deploymentMode` |
| Discovery | omit `inferenceModels`; Gateway requests `GET /v1/models?limit=1000` with `anthropic-version: 2023-06-01` |
| Inference | Anthropic Messages API at `POST /v1/messages?beta=true` (plain `/v1/messages` remains accepted) |
| Model eligibility | discovery accepts `anthropic_family_tier`, but Desktop 1.37937 later rejects IDs containing known non-Anthropic family names |

`internal/claudedesktop` implements this contract. Its tests assert that the
1P `claude_desktop_config.json` remains byte-identical through Local,
Anthropic, failure, and rollback paths.

## Operation

Install the updated CIA binaries first. The release must update
`cia-edge.exe`, `cia-supervisor.exe`, and `cia-credential.exe` together. The
scheduled supervisor and the standalone helper both pass the four credentials
directly to the edge process, so no secret is placed in a launcher command line
or PowerShell variable. Credential initialization creates an
additional `claude-gateway` entry in Windows Credential Manager. It is distinct
from `inference`, `admin`, and `router`.

```powershell
Set-Location C:\IA\IA_local_server
.\scripts\v2\Configure-ClaudeDesktop.ps1 -Mode Local
```

The default is a preview and writes nothing. Apply only after edge is installed
with `CIA_CLAUDE_GATEWAY_TOKEN`:

```powershell
.\scripts\v2\Configure-ClaudeDesktop.ps1 -Mode Local -Apply
.\scripts\v2\Configure-ClaudeDesktop.ps1 -Mode Anthropic -Apply
```

The wrapper waits for the Windows GUI binary and treats its exit code as the
transaction result. Explicit CLI actions fail with a nonzero code and suppress
interactive error dialogs, so an unattended script cannot report a failed
switch as successful.

The sequence is `precheck -> DPAPI backup -> atomic write -> restart -> verify
-> commit`. Any failure restores each captured 3P file and restarts Claude.
The backup is user-DPAPI-protected under
`C:\IA\local-ai-v2\state\claude-desktop`, never Git or diagnostics. The
Win32 tray exposes the same two choices plus `Abrir Claude Desktop`. It disables
Local while inference/queue is active or authenticated `GET /v1/models` fails;
that precheck avoids changing 3P state or restarting Claude for a known gateway
configuration error.

Restart and foreground operations are restricted to processes whose full
executable path belongs to the exact discovered Claude MSIX installation.
They never use an image-name-wide `taskkill /IM claude.exe`. Verification also
waits until a package-owned renderer reports the requested `deploymentMode`;
file writes alone are not treated as a successful switch.

## Edge contract

| Route | Credential | Notes |
| --- | --- | --- |
| `GET /v1/models[?limit=1..1000]` | inference or `claude-gateway` | normal clients receive real IDs; Claude receives opaque wire aliases plus real IDs/names |
| `POST /v1/messages[?beta=true]` | `claude-gateway` only | native incremental Anthropic SSE adapter; all other query shapes fail closed |
| OpenAI routes | inference only | existing behavior remains isolated |

The adapter sends one direct request to llama-swap; it never loopbacks to the
edge's OpenAI endpoint. It strips client credentials/cookies and translates
system, mid-conversation system messages, message blocks, tools, tool results,
usage, stop reasons, errors and streaming. Unknown beta envelope fields are
not forwarded: the adapter decodes its supported subset and rebuilds the
canonical upstream request. Known fields still receive strict type and value
validation.

Cowork includes tool definitions even for a plain text prompt. When the chosen
manifest model has `function_calling: false`, the adapter omits those
definitions only while the conversation has no tool-use history, and records
`claude.tools.omitted`. A request containing `tool_use` or `tool_result`
history still fails closed instead of pretending the model can continue a tool
loop. This fallback makes text-only models usable in Cowork; it does not grant
them tool capability.

When authenticated with `claude-gateway`, the catalog adds
`anthropic_family_tier: sonnet` and a deterministic `claude-local-<sha256>`
wire ID solely for Claude Desktop compatibility. Desktop 1.37937 accepts the
tier during discovery, then applies a second gateway-model guard that rejects
IDs containing names such as `qwen`, `gemma`, or `llama`. The catalog therefore
also returns the real identity as `cia_real_model_id` and keeps the real
`display_name`. `POST /v1/messages` resolves the opaque alias back to that exact
real ID before capability checks, admission, logging, or llama-swap routing;
unknown aliases fail closed and never select another model. The normal
inference-token catalog is unchanged.

The installed MSIX itself starts 3P mode with `%LOCALAPPDATA%\Claude-3p` as its
Electron user-data directory. CIA does not create a second application or pass
that command-line switch; it writes only the native 3P registry the same MSIX
consumes. This separation is a current Claude Desktop limitation, not a CIA
architecture choice.

`/v1/messages/count_tokens` is deliberately not advertised yet. The MSIX has
client code for it, but no live Desktop 3P request has been observed here; a
tokenizer endpoint will not be invented before that request is captured.

## Required smoke and limits

The source-level protocol, transaction, and tray action sequence tests pass,
including an automated `1P -> Local -> open -> 1P` cycle that checks the 1P
configuration remains byte-identical. On 2026-08-27, release
`claude-tray-e2e-20260826-10` also passed a disposable live Windows MSIX smoke:
Claude discovered and displayed the real Gemma name through its opaque wire
alias, streamed a response through `POST /v1/messages?beta=true`, and returned
the requested marker `CIA_CLAUDE_LOCAL_OK`. The tray then restored the signed-in
1P profile with the original config SHA-256 unchanged. UI tool execution and
token counting remain **not measured**; catalog visibility alone is still not
end-to-end generation proof.

An explicit per-model generation probe is available through the credential
helper. It keeps the dedicated bearer credential inside the helper process,
includes a harmless Cowork-style tool definition, and reports only content
block kinds and the stop reason (never the generated text or a credential):

```powershell
C:\IA\local-ai-v2\bin\cia-credential.exe probe-claude qwen38-27b-agent-128k
```

The probe is intentionally not part of tray refresh: it can load a multi-GiB
model and run real inference. A `503 Service Unavailable` must be checked
against that model's `model_statuses[].capacity` in the control status. It is
not protocol success, and bypassing the physical-memory gate is not an
acceptable workaround; a forced Qwen Deep load on 2026-08-27 fell to 1.2 t/s
with only 0.66 GiB physical memory free.

After deployment run a disposable `Anthropic -> Local -> Anthropic` smoke:

1. Record the 1P config hash and ensure edge activity/queue are zero.
2. Apply Local; verify discovery, text, streaming and a tool-capable model.
3. Apply Anthropic; confirm the original 1P config hash and signed-in profile.

Never delete `configLibrary` during this smoke. A policy under
`HKLM`/`HKCU\SOFTWARE\Policies\Claude` that controls inference stops CIA before
backup or write; CIA will not override it.

For a deterministic tray-path check without navigating the UI, the installed
binary also exposes:

```powershell
C:\IA\local-ai-v2\bin\cia-tray.exe -config C:\IA\local-ai-v2\config\panel.canary.json -claude-open
```
