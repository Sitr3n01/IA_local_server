<#
.SYNOPSIS
Starts one manifest profile on its pinned runtime, runs Measure-V2AgenticReuse.ps1
against it (Gate B of docs/BENCHMARKS.md), and stops it.

.DESCRIPTION
Answers one question for the b10549 runtime: does a live session reuse its
accumulated context across agentic turns on a hybrid (Gated DeltaNet) model, or
does it re-prefill the whole conversation every turn (llama.cpp #24055/#22384)?

The server command line is built by New-V2LlamaServerArguments from the
manifest entry itself - the same builder New-V2Config.ps1 uses - minus the
router credential and log suppression, exactly as Invoke-V2ProfileQualification.ps1
does. So the "shipped" run measures what the canary actually serves, including
every llama-server default the manifest leaves unpinned (--cache-ram,
--ctx-checkpoints, --checkpoint-min-step, --cache-idle-slots). -ExtraArguments
appends flags for a controlled variant; the label must say so.

-SummarizeOnly rebuilds summary-<label>.json and checkpoint-lines-<label>.txt
from the files a previous run left behind, without loading anything.

Safety: refuses to start while any llama-server process exists (the canary
router may own the GPU), never stops a process it did not start, and holds a
per-process no-sleep assertion that lapses when this script exits.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$ModelId,
    [Parameter(Mandatory = $true)][string]$Label,
    [string]$OutputRoot = $PSScriptRoot,
    [string]$ManifestPath = 'C:\IA\IA_local_server\config\models.yaml',
    [ValidateRange(1024, 65535)][int]$Port = 19310,
    [ValidateRange(1024, 400000)][int]$BaseContextTokens = 60000,
    [ValidateRange(128, 32768)][int]$IncrementTokens = 2000,
    [ValidateRange(2, 64)][int]$Turns = 6,
    [ValidateRange(16, 4096)][int]$MaxOutputTokens = 128,
    [ValidateRange(60, 3600)][int]$StartupTimeoutSeconds = 900,
    [ValidateRange(60, 7200)][int]$RequestTimeoutSeconds = 1800,
    [string[]]$ExtraArguments = @(),
    [switch]$SummarizeOnly
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$scriptsRoot = 'C:\IA\IA_local_server\scripts\v2'
. (Join-Path $scriptsRoot 'Common.ps1')
. (Join-Path $scriptsRoot 'Telemetry.ps1')
$measureScript = Join-Path $scriptsRoot 'Measure-V2AgenticReuse.ps1'

function Write-Step {
    param([string]$Message)
    $line = '[{0}] {1}' -f (Get-Date -Format 'HH:mm:ss'), $Message
    Write-Host $line
    # A reader tailing this file can hold it open without write sharing for a
    # moment; losing a log line must never abort a run that owns a GPU process.
    for ($attempt = 0; $attempt -lt 10; $attempt++) {
        try { Add-Content -LiteralPath $script:driverLog -Value $line -Encoding UTF8 -ErrorAction Stop; return }
        catch { Start-Sleep -Milliseconds 200 }
    }
}

New-Item -ItemType Directory -Force -Path $OutputRoot | Out-Null
$script:driverLog = Join-Path $OutputRoot ("driver-" + $Label + ".log")
$serverOut = Join-Path $OutputRoot ("server-" + $Label + ".log")
$serverErr = $serverOut + '.err'
$reportPath = Join-Path $OutputRoot ("reuse-" + $Label + ".json")
$summaryPath = Join-Path $OutputRoot ("summary-" + $Label + ".json")
$checkpointPath = Join-Path $OutputRoot ("checkpoint-lines-" + $Label + ".txt")

# --- resolve the manifest entry and its runtime ---
$manifest = Get-Content -LiteralPath $ManifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
$model = @($manifest.models | Where-Object { $_.id -eq $ModelId })
if ($model.Count -ne 1) { throw "Model '$ModelId' not found exactly once in $ManifestPath." }
$model = $model[0]
$runtime = @($manifest.runtimes | Where-Object { $_.id -eq $model.runtime })
if ($runtime.Count -ne 1) { throw "Runtime '$($model.runtime)' not found exactly once." }
$runtime = $runtime[0]
$serverExe = [string]$runtime.artifact.path
$runtimeRoot = Split-Path -Parent $serverExe

$idle = $null
$process = $null
$measureExit = $null
$measureError = $null
$loadSeconds = $null
$started = [DateTime]::UtcNow

if (-not $SummarizeOnly) {
    foreach ($required in @($serverExe, [string]$model.artifact.path, $measureScript)) {
        if (-not (Test-Path -LiteralPath $required)) { throw "Required file is missing: $required" }
    }
    # --- guards: never contend with, or stop, a server this script did not start ---
    $existing = @(Get-CimInstance Win32_Process -Filter "Name = 'llama-server.exe'")
    if ($existing.Count -gt 0) {
        throw ("Refusing to start: llama-server already running (PID {0}). It may belong to the canary router." -f (($existing | ForEach-Object { $_.ProcessId }) -join ', '))
    }
    $listening = @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue)
    if ($listening.Count -gt 0) { throw "Port $Port is already in use." }

    $arguments = @(New-V2LlamaServerArguments -Model $model -Port "$Port" `
            -IncludeJinja -IncludeWarmup -IncludeMetrics -IncludeNoWebui) + @($ExtraArguments)
    $argumentLine = ConvertTo-V2CommandLine -Arguments ([string[]]$arguments)
    $commandLine = ConvertTo-V2CommandLine -Arguments ([string[]](@($serverExe) + $arguments))
    Set-Content -LiteralPath (Join-Path $OutputRoot ("command-" + $Label + ".txt")) -Value $commandLine -Encoding UTF8
    Write-Step ("model={0} runtime={1} ({2}) port={3}" -f $ModelId, $runtime.id, $runtime.version_label, $Port)
    Write-Step ("command: {0}" -f $commandLine)

    $idle = Get-V2MemorySample -ProcessId 0
    Write-Step ("idle: vram_dedicated={0} MiB shared={1} MiB physical_free={2} GiB commit={3}/{4} GiB" -f `
            $idle.vram_dedicated_mib, $idle.vram_shared_mib, $idle.physical_free_gib, $idle.commit_gib, $idle.commit_limit_gib)

    # --- no-sleep assertion for this process only (lapses on exit) ---
    if (-not ('CiaBench.NoSleep' -as [type])) {
        Add-Type -Namespace 'CiaBench' -Name 'NoSleep' -MemberDefinition '[DllImport("kernel32.dll")] public static extern uint SetThreadExecutionState(uint esFlags);'
    }
    [void][CiaBench.NoSleep]::SetThreadExecutionState([uint32]2147483649)   # ES_CONTINUOUS | ES_SYSTEM_REQUIRED

    $savedEnv = @{}
    $envNames = @('PATH') + @($runtime.environment.PSObject.Properties | ForEach-Object { $_.Name })
    foreach ($name in $envNames) { $savedEnv[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
    $env:PATH = "$runtimeRoot;C:\Windows\System32\downlevel;$env:PATH"
    foreach ($property in $runtime.environment.PSObject.Properties) {
        [Environment]::SetEnvironmentVariable($property.Name, [string]$property.Value, 'Process')
    }

    try {
        $stopwatch = [Diagnostics.Stopwatch]::StartNew()
        $process = Start-Process -FilePath $serverExe -ArgumentList $argumentLine -PassThru `
            -RedirectStandardOutput $serverOut -RedirectStandardError $serverErr -WindowStyle Hidden
        Write-Step ("llama-server PID {0} starting" -f $process.Id)

        $deadline = [DateTime]::UtcNow.AddSeconds($StartupTimeoutSeconds)
        $ready = $false
        while ([DateTime]::UtcNow -lt $deadline) {
            if ($process.HasExited) { throw "llama-server exited with code $($process.ExitCode) before becoming ready. See $serverErr" }
            try {
                $health = Invoke-RestMethod -Uri "http://127.0.0.1:$Port/health" -TimeoutSec 5
                if ($health.status -eq 'ok') { $ready = $true; break }
            }
            catch { Start-Sleep -Seconds 2 }
        }
        if (-not $ready) { throw "llama-server did not become ready within $StartupTimeoutSeconds s." }
        $stopwatch.Stop()
        $loadSeconds = [Math]::Round($stopwatch.Elapsed.TotalSeconds, 1)
        $loaded = Get-V2MemorySample -ProcessId $process.Id
        Write-Step ("ready in {0}s: vram_dedicated={1} MiB shared={2} MiB process_private={3} GiB" -f `
                $loadSeconds, $loaded.vram_dedicated_mib, $loaded.vram_shared_mib, $loaded.process_private_gib)

        Write-Step ("measuring: base={0} increment={1} turns={2} max_output={3}" -f $BaseContextTokens, $IncrementTokens, $Turns, $MaxOutputTokens)
        # The measurement only sets an exit code when it fails (exit 1); a pass
        # leaves $LASTEXITCODE untouched, which StrictMode refuses to read if no
        # native command ever ran in this session.
        $global:LASTEXITCODE = 0
        try {
            & $measureScript -BaseUrl "http://127.0.0.1:$Port" -Model $ModelId -RuntimeLabel $Label `
                -BaseContextTokens $BaseContextTokens -IncrementTokens $IncrementTokens -Turns $Turns `
                -MaxOutputTokens $MaxOutputTokens -ServerProcessId $process.Id `
                -DeviceVramMib ([int]$runtime.device.vram_mib) -TimeoutSeconds $RequestTimeoutSeconds `
                -OutputPath $reportPath -Quiet
            $measureExit = $global:LASTEXITCODE
        }
        catch {
            # Measure-V2AgenticReuse.ps1 writes its report before Write-Error, which
            # becomes terminating under its Stop preference. A failed verdict is a
            # result, not a crash of this driver.
            $measureError = $_.Exception.Message
            $measureExit = 1
        }
        Write-Step ("measurement finished: exit={0} {1}" -f $measureExit, $measureError)
    }
    catch {
        $measureError = $_.Exception.Message
        Write-Step ("FAILED: {0}" -f $measureError)
    }
    finally {
        if ($null -ne $process -and -not $process.HasExited) {
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
            $process.WaitForExit(30000) | Out-Null
            Write-Step ("llama-server PID {0} stopped" -f $process.Id)
        }
        foreach ($name in $savedEnv.Keys) { [Environment]::SetEnvironmentVariable($name, $savedEnv[$name], 'Process') }
        [void][CiaBench.NoSleep]::SetThreadExecutionState([uint32]2147483648)   # ES_CONTINUOUS: release
    }
}

# --- evidence from the server's own log: what the checkpoint machinery did ---
$pattern = 'checkpoint|forcing full prompt|re-processing|prompt cache|cache state|restor|erased invalidated|memory_seq_rm|get_availabl|prompt eval time'
$logLines = @()
foreach ($path in @($serverErr, $serverOut)) {
    if (Test-Path -LiteralPath $path) { $logLines += @(Select-String -LiteralPath $path -Pattern $pattern | ForEach-Object { $_.Line }) }
}
Set-Content -LiteralPath $checkpointPath -Value $logLines -Encoding UTF8
$counts = [ordered]@{
    checkpoint_lines      = @($logLines | Where-Object { $_ -match 'checkpoint' }).Count
    forced_full_reprefill = @($logLines | Where-Object { $_ -match 'forcing full prompt re-processing' }).Count
    slot_selected_by_lcp  = @($logLines | Where-Object { $_ -match 'selected slot by LCP' }).Count
}

$verdict = $null
$turnRows = @()
if (Test-Path -LiteralPath $reportPath) {
    $report = Get-Content -LiteralPath $reportPath -Raw -Encoding UTF8 | ConvertFrom-Json
    $verdict = $report.verdict
    $turnRows = @($report.turns | ForEach-Object {
            [ordered]@{ turn = $_.turn; kind = $_.kind; context = $_.context_tokens; processed = $_.processed_prompt_tokens
                cached = $_.cached_prompt_tokens; prompt_ms = $_.prompt_ms; turn_latency_ms = $_.turn_latency_ms
                decode_tps = $_.decode_tokens_per_second; private_gib = $_.memory.process_private_gib
                physical_free_gib = $_.memory.physical_free_gib }
        })
}

$summary = [ordered]@{
    label           = $Label
    model           = $ModelId
    runtime         = [ordered]@{ id = $runtime.id; version_label = $runtime.version_label; build_commit = $runtime.build_commit; sha256 = $runtime.artifact.sha256 }
    extra_arguments = @($ExtraArguments)
    summarize_only  = [bool]$SummarizeOnly
    started_utc     = $started.ToString('o')
    load_seconds    = $loadSeconds
    idle_before     = $idle
    verdict         = $verdict
    measure_exit    = $measureExit
    measure_error   = $measureError
    log_counts      = $counts
    turns           = $turnRows
}
Set-Content -LiteralPath $summaryPath -Value ($summary | ConvertTo-Json -Depth 6) -Encoding UTF8
Write-Step ("verdict={0} counts={1}" -f $verdict, (($counts.GetEnumerator() | ForEach-Object { "$($_.Key)=$($_.Value)" }) -join ' '))
