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

### Two instances side by side

Since ADR 0022 the local instance runs **beside** the signed-in one; CIA no
longer switches Desktop between them, and no CIA code path can stop a Desktop
process. Each Desktop process reads `deploymentMode` once at startup, before it
picks its Electron user-data directory (`%APPDATA%\Claude` for 1P,
`%LOCALAPPDATA%\Claude-3p` for 3P) and takes that directory's single-instance
lock, so a 1P and a 3P instance hold different locks and coexist. Measured on
2026-10-01 with Desktop 2.16120: the signed-in instance kept running untouched
while a 3P instance started next to it, discovered the gateway's models and
selected the public model through its opaque alias.

The tray's flyout has one Claude button, `Claude Local`; the signed-in
instance opens from the Start menu as always (`cia-tray -claude-open` still
foregrounds it from a script). `Claude Local` is live when Desktop was discovered and an authenticated `GET /v1/models` with
the `claude-gateway` credential succeeds. It does not wait for the queue to
drain, because it interrupts nothing.

```powershell
Set-Location C:\IA\IA_local_server
.\scripts\v2\Configure-ClaudeDesktop.ps1 -Instance Local
```

The default is a preview built from the tray's diagnosis and writes nothing.
Open an instance only after edge is installed with `CIA_CLAUDE_GATEWAY_TOKEN`:

```powershell
.\scripts\v2\Configure-ClaudeDesktop.ps1 -Instance Local -Apply
.\scripts\v2\Configure-ClaudeDesktop.ps1 -Instance Anthropic -Apply
```

The wrapper waits for the Windows GUI binary and treats its exit code as the
result. Explicit CLI actions fail with a nonzero code and suppress interactive
error dialogs, so an unattended script cannot report a failed open as
successful.

`Claude Local` performs `precheck -> DPAPI backup -> profile write (only when
the applied profile differs) -> select 3p -> launch -> wait for its window ->
select 1p`. The last step runs whatever happened before it, and keeps
checking for two seconds: Desktop rewrites the same file with its own
preferences when a new window first shows, and a write that read the file at
`3p` would otherwise put `3p` back. So the selector rests at `1p`: the Start menu, a `claude://` link or a Desktop restart keep
opening the signed-in instance. A failed profile write restores each captured
3P file. The backup is user-DPAPI-protected under
`C:\IA\local-ai-v2\state\claude-desktop`, never Git or diagnostics. When a
local instance already runs, the button only brings its window forward; that
instance keeps the profile it started with, so a rotated gateway credential
applies from its next start. `Claude` also returns a selector left at `3p`
(by an interrupted launch or by the switch CIA used to perform) to `1p` before
it opens anything.

Instances are told apart by the `--user-data-dir=` switch that Electron passes
to every helper process (renderer, GPU, utility, crash handler); the main
process that owns the windows is their parent and carries no switch. Command
lines are read natively through `ProcessCommandLineInformation`, and only
processes whose full executable path belongs to the exact discovered Claude
MSIX installation are considered, so Claude Code's own `claude.exe` and every
other application are invisible to this code. The Store build can leave a
freshly activated process at its initial suspend count; once per launch, after
three seconds without a window, CIA resumes only package main processes that
no helper belongs to yet, which a running instance never is.

Both instances share one tray icon design in the Windows notification area.
Quitting Desktop from the wrong icon ends that instance; CIA cannot tell the
two icons apart for the operator.

## Edge contract

| Route | Credential | Notes |
| --- | --- | --- |
| `GET /v1/models[?limit=1..1000]` | inference or `claude-gateway` | normal clients receive real IDs; Claude receives opaque wire aliases plus real IDs/names |
| `POST /v1/messages[?beta=true]` | `claude-gateway` only | native incremental Anthropic SSE adapter; all other query shapes fail closed. A model capacity refuses is answered `400 invalid_request_error` with an explanation in Portuguese (see below) |
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
that command-line switch; it writes only the native 3P registry and selector
the same MSIX consumes. That separation is what lets the two instances run at
once (see above).

Desktop checks the provider's health on every start and on "Verificar
novamente" (`recheckConfigHealth`): it discovers the models, then sends
`{"max_tokens":1,"messages":[{"role":"user","content":"."}]}` to the first one
discovery returns, within a ten-second budget and one retry. A cold load of the
public model takes about 20 s here (measured 2026-10-01), so with nothing loaded
the check gives up, the edge records two `499`s, and the instance opens with
"Não foi possível alcançar 127.0.0.1:18090" although the gateway is healthy.
`Claude Local` therefore loads the first published model before it opens the
instance when no model is loaded. A model that is already loaded is left alone;
if it is not the first one, the check can still time out while the first model
would have to replace it. The router unloads an idle model after 15 minutes, so
for a later "Verificar novamente" press `Claude Local` in the tray first.

`/v1/messages/count_tokens` is deliberately not advertised yet. The MSIX has
client code for it, but no live Desktop 3P request has been observed here; a
tokenizer endpoint will not be invented before that request is captured.

## Required smoke and limits

The source-level protocol and tray action sequence tests pass, including an
automated cycle that opens the local instance beside a running signed-in one,
shows each again, and checks that exactly one 3P launch happened, the selector
rests at `1p` and the 1P configuration remains byte-identical. On 2026-08-27,
before ADR 0022 and through the restart-based switch it replaced, release
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

When admission refuses a model, `/v1/messages` answers `400
invalid_request_error`, not `503`. Desktop's agent retries a 5xx ten times and
shows only "Solicitação falhou. Tentando novamente…", hiding the reason; memory
does not free itself in seconds, so the refusal is final for that request and
its message says why, for example: "Não há RAM livre para o modelo
qwen36-35b-a3b-huge-256k agora: ele precisa de 20,2 GiB (com 2,0 GiB de
reserva) e há 17,9 GiB, já contando os 9,9 GiB que descarregar o modelo atual
libera. Faltam 2,3 GiB. Feche programas que estejam usando RAM (agora os que
mais usam são claude 3,8 GiB, firefox 2,1 GiB e RazerAppEngine 1,2 GiB) ou
escolha um modelo menor." The applications are summed by executable name, only
for a memory refusal, and never include Windows services or this deployment.
They are ranked by resident memory for a physical-memory refusal and by private
commit for a commit refusal: Cowork's virtual machine (`vmmem`, named as such)
reserved 4.0 GiB of commit while only 1.3 GiB of it was resident. The table is
read in one `NtQuerySystemInformation` call, which also counts processes this
account cannot open; opening each process had silently skipped that VM.
The OpenAI routes keep `503 insufficient_capacity` with the same text.

The probe is intentionally not part of tray refresh: it can load a multi-GiB
model and run real inference. A `503 Service Unavailable` must be checked
against that model's `model_statuses[].capacity` in the control status. It is
not protocol success, and bypassing the physical-memory gate is not an
acceptable workaround; a forced Qwen Deep load on 2026-08-27 fell to 1.2 t/s
with only 0.66 GiB physical memory free.

After deployment run a side-by-side smoke while the signed-in instance is open:

1. Record the 1P config hash and the signed-in main process ID.
2. Open `Claude Local`; verify discovery, text, streaming and a tool-capable
   model in the new window.
3. Confirm the signed-in main process ID is unchanged, `deploymentMode` reads
   `1p`, and the 1P config hash is the original.
4. Open Claude from the Start menu; the signed-in window must come forward.

Never delete `configLibrary` during this smoke. A policy under
`HKLM`/`HKCU\SOFTWARE\Policies\Claude` that controls inference stops CIA before
backup or write; CIA will not override it.

For a deterministic tray-path check without navigating the UI, the installed
binary also exposes:

```powershell
C:\IA\local-ai-v2\bin\cia-tray.exe -config C:\IA\local-ai-v2\config\panel.canary.json -claude-open
C:\IA\local-ai-v2\bin\cia-tray.exe -config C:\IA\local-ai-v2\config\panel.canary.json -claude-local
```

A read-only check that both instances are identified from the real process
table, without launching or foregrounding anything:

```powershell
$env:CIA_CLAUDE_LIVE_TEST = '1'
go test ./internal/claudedesktop/ -run TestLiveInstances -v -count=1
```
