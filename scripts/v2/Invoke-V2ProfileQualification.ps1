<#
.SYNOPSIS
Starts one llama-server profile, runs the qualification battery against it while
sampling memory, and stops it.

.DESCRIPTION
One cell of the campaign matrix, end to end. The flag list mirrors
`New-V2LlamaServerCommand` in Common.ps1 — same order, same defaults — minus the
router credential and the log suppression, because a qualification run wants the
server log and production wants neither. If the two drift, a result measured here
stops describing what the manifest would actually serve, so the mirroring is
deliberate rather than incidental.

Memory is sampled on a timer in a background job for the whole life of the
server, not just at load. The distinction matters on this workstation: dedicated
VRAM is set at load and barely moves, while shared GPU memory — the paging signal
— climbs as a long context is actually filled, and a load-time sample cannot see
it.

.EXAMPLE
./Invoke-V2ProfileQualification.ps1 -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-Q3_K_XL.gguf `
    -Label q3kxl-32k-kv-q4 -ContextTokens 32768 -CacheTypeK q4_0 -CacheTypeV q4_0 `
    -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-256k
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$ModelPath,
    [Parameter(Mandatory = $true)][string]$Label,
    [Parameter(Mandatory = $true)][string]$OutputRoot,

    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [ValidateRange(1024, 1048576)][int]$ContextTokens = 32768,
    [ValidateSet('f16', 'q8_0', 'q4_0')][string]$CacheTypeK = 'q4_0',
    [ValidateSet('f16', 'q8_0', 'q4_0')][string]$CacheTypeV = 'q4_0',
    [string]$TensorOverride = 'blk\.(6[0-3])\.ffn_.*=CPU',
    [int]$UBatchSize = 288,
    [int]$BatchSize = 2048,
    [int]$NGpuLayers = 99,
    [int]$Threads = 8,
    [ValidateRange(-1, 1024)][int]$NCpuMoe = -1,
    [switch]$CpuMoe,
    [ValidateRange(1, 16)][int]$Parallel = 1,
    [int]$DeviceVramMib = 16304,
    [ValidateRange(1024, 65535)][int]$Port = 19399,
    [ValidateRange(30, 3600)][int]$StartupTimeoutSeconds = 900,

    [string]$Suites = 'coding,tools,json',
    [int[]]$RetentionTokens = @(),
    [string]$OnlyCodingTasks = '',
    [ValidateRange(1, 30)][int]$SampleIntervalSeconds = 3
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($CpuMoe -and $NCpuMoe -ge 0) {
    throw 'CpuMoe and NCpuMoe are mutually exclusive.'
}

. (Join-Path $PSScriptRoot 'Telemetry.ps1')

$serverExe = Join-Path $RuntimeRoot 'llama-server.exe'
$qualify = Join-Path $PSScriptRoot 'eval\qualify.py'
foreach ($required in @($serverExe, $ModelPath, $qualify)) {
    if (-not (Test-Path -LiteralPath $required)) { throw "Required file is missing: $required" }
}

New-Item -ItemType Directory -Force -Path $OutputRoot | Out-Null
$workdir = Join-Path $OutputRoot ("work-" + $Label)
$serverLog = Join-Path $OutputRoot ("server-" + $Label + ".log")
$evalOut = Join-Path $OutputRoot ("qualify-" + $Label + ".json")
$finalOut = Join-Path $OutputRoot ("profile-" + $Label + ".json")

if ($Suites -match 'retention' -and $RetentionTokens.Count -eq 0) {
    throw "Suites includes retention but -RetentionTokens was not given."
}

$arguments = @(
    '--model', $ModelPath,
    '--host', '127.0.0.1',
    '--port', "$Port",
    '--alias', 'local',
    '--device', 'ROCm0',
    '--split-mode', 'none',
    '--gpu-layers', "$NGpuLayers",
    '--flash-attn', 'on',
    '--ctx-size', "$ContextTokens",
    '--batch-size', "$BatchSize",
    '--ubatch-size', "$UBatchSize",
    '--cache-type-k', $CacheTypeK,
    '--cache-type-v', $CacheTypeV,
    '--parallel', "$Parallel",
    '--cont-batching',
    '--no-context-shift',
    '--threads', "$Threads"
)
if ($TensorOverride) { $arguments += @('-ot', $TensorOverride) }
if ($CpuMoe) { $arguments += @('--cpu-moe') }
elseif ($NCpuMoe -ge 0) { $arguments += @('--n-cpu-moe', "$NCpuMoe") }
$arguments += @('--jinja', '--warmup', '--metrics', '--no-webui')

$commandLine = ($serverExe + ' ' + ($arguments -join ' '))
$moe = if ($CpuMoe) { 'all' } elseif ($NCpuMoe -ge 0) { [string]$NCpuMoe } else { 'default' }
Write-Host ("[{0}] ctx={1} kv={2}/{3} ub={4} split='{5}' cpu_moe={6}" -f $Label, $ContextTokens, $CacheTypeK, $CacheTypeV, $UBatchSize, $TensorOverride, $moe)

$idle = Get-V2MemorySample -ProcessId 0

$previousPath = $env:PATH
$previousHip = $env:HIP_VISIBLE_DEVICES
$env:PATH = "$RuntimeRoot;C:\Windows\System32\downlevel;$env:PATH"
$env:HIP_VISIBLE_DEVICES = '1'

$process = $null
$sampler = $null
$failure = $null
$loadSeconds = $null
$evalExit = $null
$samplePath = Join-Path $OutputRoot ("samples-" + $Label + ".jsonl")
if (Test-Path -LiteralPath $samplePath) { Remove-Item -LiteralPath $samplePath -Force }

try {
    $started = [Diagnostics.Stopwatch]::StartNew()
    $process = Start-Process -FilePath $serverExe -ArgumentList $arguments -PassThru `
        -RedirectStandardOutput $serverLog -RedirectStandardError ($serverLog + '.err') `
        -WindowStyle Hidden

    # The sampler runs for the whole session. A load-time-only sample cannot see
    # shared memory climbing as a long context is filled, which is the number
    # that decides whether a context level is servable.
    $sampler = Start-Job -ScriptBlock {
        param($telemetryPath, $pid_, $outPath, $interval)
        . $telemetryPath
        while ($true) {
            try {
                $s = Get-V2MemorySample -ProcessId $pid_
                ($s | ConvertTo-Json -Compress -Depth 4) | Add-Content -LiteralPath $outPath -Encoding UTF8
            }
            catch { }
            Start-Sleep -Seconds $interval
        }
    } -ArgumentList (Join-Path $PSScriptRoot 'Telemetry.ps1'), $process.Id, $samplePath, $SampleIntervalSeconds

    $deadline = [DateTime]::UtcNow.AddSeconds($StartupTimeoutSeconds)
    $ready = $false
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($process.HasExited) {
            throw "llama-server exited with code $($process.ExitCode) before becoming ready. See $serverLog.err"
        }
        try {
            $health = Invoke-RestMethod -Uri "http://127.0.0.1:$Port/health" -TimeoutSec 5
            if ($health.status -eq 'ok') { $ready = $true; break }
        }
        catch { Start-Sleep -Seconds 2 }
    }
    $started.Stop()
    if (-not $ready) { throw "llama-server did not become ready within $StartupTimeoutSeconds seconds." }
    $loadSeconds = [Math]::Round($started.Elapsed.TotalSeconds, 1)
    Write-Host ("  loaded in {0}s" -f $loadSeconds)

    $pyArgs = @(
        $qualify,
        '--base-url', "http://127.0.0.1:$Port",
        '--alias', 'local',
        '--label', $Label,
        '--workdir', $workdir,
        '--out', $evalOut,
        '--suites', $Suites
    )
    foreach ($t in $RetentionTokens) { $pyArgs += @('--retention-tokens', "$t") }
    if ($OnlyCodingTasks) { $pyArgs += @('--only', $OnlyCodingTasks) }

    & python @pyArgs
    $evalExit = $LASTEXITCODE
}
catch {
    $failure = $_.Exception.Message
    Write-Warning ("  FAILED: {0}" -f $failure)
}
finally {
    if ($null -ne $sampler) {
        Stop-Job -Job $sampler -ErrorAction SilentlyContinue
        Remove-Job -Job $sampler -Force -ErrorAction SilentlyContinue
    }
    if ($null -ne $process -and -not $process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        $process.WaitForExit(30000) | Out-Null
    }
    $env:PATH = $previousPath
    if ($null -eq $previousHip) { Remove-Item Env:\HIP_VISIBLE_DEVICES -ErrorAction SilentlyContinue }
    else { $env:HIP_VISIBLE_DEVICES = $previousHip }
}

# Reduce the sample stream to the peaks the manifest and the abort criteria use.
$peak = $null
$sampleCount = 0
if (Test-Path -LiteralPath $samplePath) {
    $rows = @(Get-Content -LiteralPath $samplePath | ForEach-Object {
            try { $_ | ConvertFrom-Json } catch { $null }
        } | Where-Object { $null -ne $_ })
    $sampleCount = $rows.Count
    if ($sampleCount -gt 0) {
        function Peak([string]$name) {
            $vals = @($rows | ForEach-Object { $_.$name } | Where-Object { $null -ne $_ })
            if ($vals.Count -eq 0) { return $null }
            return ($vals | Measure-Object -Maximum).Maximum
        }
        $peak = [ordered]@{
            vram_dedicated_mib  = Peak 'vram_dedicated_mib'
            vram_shared_mib     = Peak 'vram_shared_mib'
            process_vram_mib    = Peak 'process_vram_mib'
            process_ws_gib      = Peak 'process_ws_gib'
            process_private_gib = Peak 'process_private_gib'
            commit_gib          = Peak 'commit_gib'
            physical_used_gib   = Peak 'physical_used_gib'
        }
    }
}

$pressure = $null
if ($null -ne $peak -and $null -ne $peak.vram_dedicated_mib) {
    $pressure = Test-V2GpuMemoryPressure -TotalMib $DeviceVramMib -Sample ([pscustomobject]@{
            instance      = 'session-peak'
            dedicated_mib = $peak.vram_dedicated_mib
            shared_mib    = $(if ($null -ne $peak.vram_shared_mib) { $peak.vram_shared_mib } else { 0 })
        })
}

$evalReport = $null
if (Test-Path -LiteralPath $evalOut) {
    try { $evalReport = Get-Content -LiteralPath $evalOut -Raw | ConvertFrom-Json } catch { }
}

$report = [ordered]@{
    schema_version = 1
    scenario       = 'profile-qualification'
    label          = $Label
    started_utc    = [DateTime]::UtcNow.ToString('o')
    configuration  = [ordered]@{
        model_path      = $ModelPath
        model_bytes     = (Get-Item -LiteralPath $ModelPath).Length
        runtime_root    = $RuntimeRoot
        context_tokens  = $ContextTokens
        cache_type_k    = $CacheTypeK
        cache_type_v    = $CacheTypeV
        ubatch_size     = $UBatchSize
        batch_size      = $BatchSize
        gpu_layers      = $NGpuLayers
        threads         = $Threads
        parallel        = $Parallel
        tensor_override = $TensorOverride
        cpu_moe         = [bool]$CpuMoe
        n_cpu_moe       = $(if ($NCpuMoe -ge 0) { $NCpuMoe } else { $null })
        device_vram_mib = $DeviceVramMib
        command_line    = $commandLine
    }
    load_seconds   = $loadSeconds
    idle           = [ordered]@{
        vram_dedicated_mib = $idle.vram_dedicated_mib
        vram_shared_mib    = $idle.vram_shared_mib
        commit_gib         = $idle.commit_gib
        physical_used_gib  = $idle.physical_used_gib
    }
    session_peak   = $peak
    marginal_vram_mib = $(if ($null -ne $peak -and $null -ne $idle.vram_dedicated_mib -and $null -ne $peak.vram_dedicated_mib) {
            [Math]::Round($peak.vram_dedicated_mib - $idle.vram_dedicated_mib, 1)
        } else { $null })
    memory_samples = $sampleCount
    gpu_pressure   = $pressure
    eval_exit_code = $evalExit
    failure        = $failure
    eval           = $evalReport
}

$report | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $finalOut -Encoding UTF8
Write-Host ("  -> {0}" -f $finalOut)
if ($null -ne $peak) {
    Write-Host ("  peak: dedicated {0:N0} MiB | shared {1:N0} MiB | ws {2:N2} GiB | private {3:N2} GiB | {4}" -f `
            $peak.vram_dedicated_mib, $peak.vram_shared_mib, $peak.process_ws_gib, $peak.process_private_gib, `
        $(if ($null -ne $pressure) { $pressure.state } else { 'unclassified' }))
}
$report
