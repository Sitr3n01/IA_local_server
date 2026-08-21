<#
.SYNOPSIS
End-to-end smoke test for a workstation profile against a real llama-server.

.DESCRIPTION
Verifies the sequence an operator depends on and nothing more: the process
starts, the model loads, /health reports ready, a chat completion returns, a
streamed completion produces incremental chunks, a tool call comes back with a
valid name and parseable JSON arguments, the process shuts down, and no child
survives it.

Two of these are load-bearing beyond "it works".

`capabilities.function_calling` in the manifest is a claim, and an untested claim
in a manifest is worse than an absent one: the edge advertises it to a harness
that will then depend on it. This exercises it against the real template.

The orphan check exists because a profiler or a smoke test that leaves a 13 GiB
process behind is worse than none at all. It asserts on the process tree rather
than on an exit code, since llama-server can exit while a child holds the GPU.

Memory is sampled at peak and reported with a pressure verdict, so a smoke run
doubles as evidence for the resources block in the manifest. It never edits the
manifest.

.EXAMPLE
./Test-V2WorkstationSmoke.ps1 -ModelId qwen38-27b-ws-32k
#>
[CmdletBinding()]
param(
    [string]$ManifestPath = (Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'config\models.yaml'),

    [Parameter(Mandatory = $true)]
    [string]$ModelId,

    [ValidateRange(1024, 65535)]
    [int]$Port = 19399,

    [ValidateRange(60, 1800)]
    [int]$StartupTimeoutSeconds = 600,

    [string]$OutputPath,

    [switch]$Quiet
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot 'Common.ps1')
. (Join-Path $PSScriptRoot 'Telemetry.ps1')

$script:Checks = 0
$script:Failures = [System.Collections.Generic.List[string]]::new()

function Assert-Smoke {
    param(
        [Parameter(Mandatory = $true)][bool]$Condition,
        [Parameter(Mandatory = $true)][string]$Message
    )
    $script:Checks++
    if ($Condition) {
        if (-not $Quiet) { Write-Host ("  ok    {0}" -f $Message) }
        return
    }
    $script:Failures.Add($Message)
    Write-Warning ("  FAIL  {0}" -f $Message)
}

$manifest = Read-V2Manifest -Path $ManifestPath
$model = @($manifest.models | Where-Object { $_.id -eq $ModelId })[0]
if ($null -eq $model) { throw "Model '$ModelId' is not in $ManifestPath." }
$runtime = @($manifest.runtimes | Where-Object { $_.id -eq $model.runtime })[0]
if ($null -eq $runtime) { throw "Model '$ModelId' references unknown runtime '$($model.runtime)'." }

$runtimeRoot = Split-Path -Parent $runtime.artifact.path
$serverExe = $runtime.artifact.path
foreach ($required in @($serverExe, $model.artifact.path)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) { throw "Missing artifact: $required" }
}

# The command comes from the same builder the deployment uses, so this tests the
# configuration that would actually ship rather than a hand-written approximation.
# Only the two deployment-owned arguments are substituted: the port placeholder
# llama-swap fills in, and the router credential the supervisor supplies.
$command = New-V2LlamaServerCommand -Runtime $runtime -Model $model -RouterAPIKeyPath 'UNUSED'
$command = $command -replace '\$\{PORT\}', "$Port"
$command = $command -replace '\s--api-key-file\s+"[^"]*"', ''
$argumentText = $command.Substring($command.IndexOf('" ') + 2)

if (-not $Quiet) {
    Write-Host ("Smoke: {0}  ctx={1}  ub={2}  kv={3}" -f $model.id, $model.context_tokens, $model.ubatch_size, $model.cache_type_k)
}

$idle = Get-V2MemorySample -ProcessId 0
$previousPath = $env:PATH
$previousHip = $env:HIP_VISIBLE_DEVICES
$env:PATH = "$runtimeRoot;C:\Windows\System32\downlevel;$env:PATH"
foreach ($entry in $runtime.environment.psobject.Properties) {
    Set-Item -Path ("Env:\" + $entry.Name) -Value $entry.Value
}

$process = $null
$peak = $null
$loadSeconds = $null
$endpoint = "http://127.0.0.1:$Port"

try {
    $started = [Diagnostics.Stopwatch]::StartNew()
    $process = Start-Process -FilePath $serverExe -ArgumentList $argumentText -PassThru -WindowStyle Hidden

    # --- startup and model loading ---------------------------------------
    $deadline = [DateTime]::UtcNow.AddSeconds($StartupTimeoutSeconds)
    $ready = $false
    while ([DateTime]::UtcNow -lt $deadline -and -not $process.HasExited) {
        try {
            if ((Invoke-RestMethod -Uri "$endpoint/health" -TimeoutSec 5).status -eq 'ok') { $ready = $true; break }
        }
        catch { Start-Sleep -Seconds 2 }
    }
    $started.Stop()
    $loadSeconds = [Math]::Round($started.Elapsed.TotalSeconds, 1)
    Assert-Smoke -Condition $ready -Message "startup and model loading (${loadSeconds}s)"
    if (-not $ready) { throw "llama-server never became ready; the remaining checks cannot run." }

    Assert-Smoke -Condition ((Invoke-RestMethod -Uri "$endpoint/health").status -eq 'ok') -Message 'health endpoint'

    $models = Invoke-RestMethod -Uri "$endpoint/v1/models"
    Assert-Smoke -Condition (@($models.models).Count -ge 1) -Message 'model list is served'

    # --- non-streaming chat completion -----------------------------------
    # The token budget is deliberately generous. Qwen3.8 emits reasoning before
    # its answer, so a tight budget hits finish_reason=length with an empty
    # content field and a populated reasoning_content one - which looks
    # identical to a model that produced nothing. Measured here, 16 tokens
    # truncated inside the reasoning block and 36 sufficed for the whole reply.
    $chatBody = @{
        messages    = @(@{ role = 'user'; content = 'Reply with exactly the word: READY' })
        max_tokens  = 256
        temperature = 0
        stream      = $false
    } | ConvertTo-Json -Depth 8 -Compress
    $chat = Invoke-RestMethod -Method Post -Uri "$endpoint/v1/chat/completions" `
        -ContentType 'application/json' -Body ([Text.Encoding]::UTF8.GetBytes($chatBody)) -TimeoutSec 180
    $choice = @($chat.choices)[0]
    $content = [string]$choice.message.content
    # Asserted before the content check so a truncated reply is diagnosed as
    # truncation rather than reported as an empty answer.
    Assert-Smoke -Condition ($choice.finish_reason -eq 'stop') -Message "chat completion finished cleanly (finish_reason=$($choice.finish_reason))"
    Assert-Smoke -Condition ($content.Trim().Length -gt 0) -Message 'chat completion returns content'
    Assert-Smoke -Condition ($null -ne $chat.timings) -Message 'native timings counters are present'

    $peakSamples = @(Get-V2MemorySample -ProcessId $process.Id)

    # --- streaming --------------------------------------------------------
    $streamBody = @{
        messages   = @(@{ role = 'user'; content = 'Count from one to five.' })
        max_tokens = 48
        stream     = $true
    } | ConvertTo-Json -Depth 8 -Compress
    $request = [Net.HttpWebRequest]::Create("$endpoint/v1/chat/completions")
    $request.Method = 'POST'
    $request.ContentType = 'application/json'
    $request.Timeout = 180000
    $bytes = [Text.Encoding]::UTF8.GetBytes($streamBody)
    $request.ContentLength = $bytes.Length
    $stream = $request.GetRequestStream()
    $stream.Write($bytes, 0, $bytes.Length)
    $stream.Close()
    $reader = [IO.StreamReader]::new($request.GetResponse().GetResponseStream())
    $chunks = 0
    try {
        while (-not $reader.EndOfStream) {
            $line = $reader.ReadLine()
            if ($line -like 'data: *' -and $line -notlike 'data: [DONE]*') { $chunks++ }
        }
    }
    finally { $reader.Dispose() }
    # More than one chunk is the assertion that matters: a single chunk means the
    # response was buffered and delivered whole, which is not streaming.
    Assert-Smoke -Condition ($chunks -gt 1) -Message "streaming produced $chunks incremental chunks"

    $peakSamples += Get-V2MemorySample -ProcessId $process.Id

    # --- tool calling -----------------------------------------------------
    $toolBody = @{
        messages    = @(@{ role = 'user'; content = 'Read the file report.txt using the available tool.' })
        max_tokens  = 128
        temperature = 0
        tool_choice = 'required'
        tools       = @(@{
                type     = 'function'
                function = @{
                    name        = 'read_workspace_file'
                    description = 'Read a UTF-8 text file from the workspace.'
                    parameters  = @{
                        type       = 'object'
                        properties = @{ path = @{ type = 'string'; description = 'Relative path.' } }
                        required   = @('path')
                    }
                }
            })
        stream      = $false
    } | ConvertTo-Json -Depth 12 -Compress
    $tool = Invoke-RestMethod -Method Post -Uri "$endpoint/v1/chat/completions" `
        -ContentType 'application/json' -Body ([Text.Encoding]::UTF8.GetBytes($toolBody)) -TimeoutSec 180
    $message = @($tool.choices)[0].message
    $toolCallsProperty = $message.PSObject.Properties['tool_calls']
    $calls = @()
    if ($null -ne $toolCallsProperty -and $null -ne $toolCallsProperty.Value) { $calls = @($toolCallsProperty.Value) }
    Assert-Smoke -Condition ($calls.Count -ge 1) -Message 'tool calling produced a call'
    if ($calls.Count -ge 1) {
        Assert-Smoke -Condition ($calls[0].function.name -eq 'read_workspace_file') -Message 'tool call names the declared function'
        $parsed = $null
        try { $parsed = [string]$calls[0].function.arguments | ConvertFrom-Json } catch { }
        Assert-Smoke -Condition ($null -ne $parsed -and $null -ne $parsed.path) -Message 'tool call arguments are valid JSON with the required field'
    }

    $peakSamples += Get-V2MemorySample -ProcessId $process.Id
    $peak = [pscustomobject]@{
        vram_dedicated_mib  = ($peakSamples | ForEach-Object { $_.vram_dedicated_mib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
        vram_shared_mib     = ($peakSamples | ForEach-Object { $_.vram_shared_mib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
        process_ws_gib      = ($peakSamples | ForEach-Object { $_.process_ws_gib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
        process_private_gib = ($peakSamples | ForEach-Object { $_.process_private_gib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
        physical_used_gib   = ($peakSamples | ForEach-Object { $_.physical_used_gib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
    }
}
finally {
    if ($null -ne $process -and -not $process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        $process.WaitForExit(20000) | Out-Null
    }
    $env:PATH = $previousPath
    if ($null -eq $previousHip) { Remove-Item Env:\HIP_VISIBLE_DEVICES -ErrorAction SilentlyContinue }
    else { $env:HIP_VISIBLE_DEVICES = $previousHip }
}

# --- shutdown and orphans -------------------------------------------------
Start-Sleep -Seconds 3
Assert-Smoke -Condition ($null -eq $process -or $process.HasExited) -Message 'shutdown: the server process exited'
$survivors = @(Get-Process -Name 'llama-server' -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $serverExe })
Assert-Smoke -Condition ($survivors.Count -eq 0) -Message 'shutdown: no llama-server survived from this runtime'
$listening = @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue)
Assert-Smoke -Condition ($listening.Count -eq 0) -Message "shutdown: port $Port released"

$pressure = $null
if ($null -ne $peak -and $null -ne $runtime.device.vram_mib) {
    $pressure = Test-V2GpuMemoryPressure -TotalMib ([int]$runtime.device.vram_mib) -Sample ([pscustomobject]@{
            instance      = 'smoke-peak'
            dedicated_mib = $peak.vram_dedicated_mib
            shared_mib    = $(if ($null -ne $peak.vram_shared_mib) { $peak.vram_shared_mib } else { 0 })
        })
}

$report = [ordered]@{
    schema_version = 1
    scenario       = 'workstation-smoke'
    model          = $ModelId
    runtime        = $runtime.id
    started_utc    = [DateTime]::UtcNow.ToString('o')
    load_seconds   = $loadSeconds
    checks         = $script:Checks
    failures       = @($script:Failures)
    verdict        = $(if ($script:Failures.Count -eq 0) { 'smoke_pass' } else { 'smoke_fail' })
    idle           = [ordered]@{
        vram_dedicated_mib = $idle.vram_dedicated_mib
        vram_shared_mib    = $idle.vram_shared_mib
        physical_used_gib  = $idle.physical_used_gib
    }
    peak           = $peak
    gpu_pressure   = $pressure
}

if ($OutputPath) {
    $directory = Split-Path -Parent $OutputPath
    if ($directory) { New-Item -ItemType Directory -Force -Path $directory | Out-Null }
    [IO.File]::WriteAllText($OutputPath, ($report | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
}

$report | ConvertTo-Json -Depth 8
if ($script:Failures.Count -gt 0) {
    throw ("Workstation smoke failed {0} of {1} checks." -f $script:Failures.Count, $script:Checks)
}
