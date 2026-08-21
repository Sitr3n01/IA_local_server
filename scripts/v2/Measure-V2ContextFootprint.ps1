<#
.SYNOPSIS
Measures what a declared context window costs in VRAM and host memory, before
any inference runs.

.DESCRIPTION
A context profile is a memory decision made at load time: llama-server allocates
the KV cache and the recurrent state for the full declared window when it starts,
not as a conversation grows into it. Declaring 128k on a model that will see 20k
therefore costs the difference for the whole life of the process, which is the
"reduce RAM allocated for contexts that will not be used" question stated in
docs/TUNING.md section 1.7.

Measuring that with llama-bench would be wasteful and slow: llama-bench derives
its context from the prompt length, so pricing a 128k window would mean actually
prefilling 128k tokens. This starts the server, waits for /health, samples once
the allocation is complete, and stops. A full matrix costs minutes rather than
hours, and the numbers it produces are the ones the manifest needs.

Qwen3.8-class hybrids make this worth measuring rather than computing: only 16 of
64 layers hold a KV cache, so the arithmetic that applies to a dense model
overstates the cost by roughly four times.

The idle sample is taken before the server starts and the peak after it is
serving, so their difference is the model's own footprint rather than the
machine's. On this workstation the desktop holds around 3.0 GiB of VRAM on its
own, which is most of the gap between what llama.cpp reports and what the adapter
shows.

.EXAMPLE
./Measure-V2ContextFootprint.ps1 -RuntimeRoot C:\IA\runtimes\llama.cpp\b10549-rocm-7.14 `
    -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-IQ4_XS.gguf `
    -ContextTokens 32768 -CacheType q8_0 -DeviceVramMib 16304
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$RuntimeRoot,

    [Parameter(Mandatory = $true)]
    [string]$ModelPath,

    [Parameter(Mandatory = $true)]
    [ValidateRange(1024, 1048576)]
    [int]$ContextTokens,

    [ValidateSet('f16', 'q8_0', 'q4_0')]
    [string]$CacheType = 'q8_0',

    # K and V may be quantized independently. Both default to -CacheType, so an
    # existing caller that only sets -CacheType produces a byte-identical command
    # line and its earlier reports stay comparable.
    [ValidateSet('f16', 'q8_0', 'q4_0')]
    [string]$CacheTypeK,

    [ValidateSet('f16', 'q8_0', 'q4_0')]
    [string]$CacheTypeV,

    [string]$TensorOverride = 'blk\.(6[0-3])\.ffn_.*=CPU',

    [ValidateRange(-1, 1024)]
    [int]$NCpuMoe = -1,

    [switch]$CpuMoe,

    [int]$UBatchSize = 288,

    [int]$BatchSize = 2048,

    [int]$NGpuLayers = 99,

    [int]$Threads = 8,

    # provider.max_loaded_models is 1 and the workstation profile serves one
    # inference at a time, so this defaults to the value the manifest pins.
    [ValidateRange(1, 16)]
    [int]$Parallel = 1,

    [ValidateRange(0, 1048576)]
    [int]$DeviceVramMib = 0,

    [ValidateRange(1024, 65535)]
    [int]$Port = 19399,

    [ValidateRange(30, 1800)]
    [int]$StartupTimeoutSeconds = 600,

    [string]$OutputPath,

    [switch]$Quiet
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot 'Telemetry.ps1')

$serverExe = Join-Path $RuntimeRoot 'llama-server.exe'
foreach ($required in @($serverExe, $ModelPath)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Required file is missing: $required"
    }
}

if (-not $CacheTypeK) { $CacheTypeK = $CacheType }
if (-not $CacheTypeV) { $CacheTypeV = $CacheType }
if ($CpuMoe -and $NCpuMoe -ge 0) {
    throw 'CpuMoe and NCpuMoe are mutually exclusive.'
}

$idle = Get-V2MemorySample -ProcessId 0

$arguments = @(
    '--model', $ModelPath,
    '--device', 'ROCm0',
    '--split-mode', 'none',
    '-ngl', "$NGpuLayers",
    '-fa', '1',
    '-c', "$ContextTokens",
    '-b', "$BatchSize",
    '-ub', "$UBatchSize",
    '-ctk', $CacheTypeK,
    '-ctv', $CacheTypeV,
    '-t', "$Threads",
    '--parallel', "$Parallel",
    '--no-context-shift',
    '--host', '127.0.0.1',
    '--port', "$Port",
    '--no-webui'
)
if ($TensorOverride) { $arguments += @('-ot', $TensorOverride) }
if ($CpuMoe) { $arguments += @('--cpu-moe') }
elseif ($NCpuMoe -ge 0) { $arguments += @('--n-cpu-moe', "$NCpuMoe") }

if (-not $Quiet) {
    $moe = if ($CpuMoe) { 'all' } elseif ($NCpuMoe -ge 0) { [string]$NCpuMoe } else { 'default' }
    Write-Host ("Loading ctx={0} kv={1}/{2} ub={3} cpu_moe={4}" -f $ContextTokens, $CacheTypeK, $CacheTypeV, $UBatchSize, $moe)
}

# System32\downlevel supplies the UCRT API sets the ROCm build imports and the
# runtime directory supplies the rocBLAS closure; without both, ggml-hip.dll
# fails to load and the server silently runs on the CPU. HIP_VISIBLE_DEVICES
# isolates the discrete card, which the b10549 build does not do on its own.
$environment = @{
    PATH                = "$RuntimeRoot;C:\Windows\System32\downlevel;$env:PATH"
    HIP_VISIBLE_DEVICES = '1'
}

$previousPath = $env:PATH
$previousHip = $env:HIP_VISIBLE_DEVICES
$env:PATH = $environment.PATH
$env:HIP_VISIBLE_DEVICES = $environment.HIP_VISIBLE_DEVICES

$process = $null
$peak = $null
$loadSeconds = $null
$failure = $null
try {
    $started = [Diagnostics.Stopwatch]::StartNew()
    $process = Start-Process -FilePath $serverExe -ArgumentList $arguments -PassThru -WindowStyle Hidden

    $deadline = [DateTime]::UtcNow.AddSeconds($StartupTimeoutSeconds)
    $ready = $false
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($process.HasExited) {
            throw "llama-server exited with code $($process.ExitCode) before becoming ready. The context or the split does not fit."
        }
        try {
            $health = Invoke-RestMethod -Uri "http://127.0.0.1:$Port/health" -TimeoutSec 5
            if ($health.status -eq 'ok') { $ready = $true; break }
        }
        catch {
            Start-Sleep -Seconds 2
        }
    }
    $started.Stop()
    if (-not $ready) { throw "llama-server did not become ready within $StartupTimeoutSeconds seconds." }
    $loadSeconds = [Math]::Round($started.Elapsed.TotalSeconds, 1)

    # Allocation is complete once /health reports ok, but the driver's counters
    # lag it slightly. A few samples spaced over a couple of seconds is enough to
    # catch the settled value without polling in a loop.
    $samples = @()
    for ($index = 0; $index -lt 4; $index++) {
        Start-Sleep -Milliseconds 700
        $samples += Get-V2MemorySample -ProcessId $process.Id
    }
    $peak = [pscustomobject]@{
        vram_dedicated_mib = ($samples | ForEach-Object { $_.vram_dedicated_mib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
        vram_shared_mib    = ($samples | ForEach-Object { $_.vram_shared_mib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
        process_vram_mib   = ($samples | ForEach-Object { $_.process_vram_mib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
        process_ws_gib     = ($samples | ForEach-Object { $_.process_ws_gib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
        # This is what resources.peak_commit_gib takes: the model's own commit
        # demand, which the edge compares against available commit headroom.
        # commit_gib below is the system-wide level and is context for a reader,
        # not an input to the gate.
        process_private_gib = ($samples | ForEach-Object { $_.process_private_gib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
        commit_gib         = ($samples | ForEach-Object { $_.commit_gib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
        physical_used_gib  = ($samples | ForEach-Object { $_.physical_used_gib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
    }
}
catch {
    $failure = $_.Exception.Message
}
finally {
    # A profiler that leaves a 13 GiB process behind is worse than no profiler.
    if ($null -ne $process -and -not $process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        $process.WaitForExit(15000) | Out-Null
    }
    $env:PATH = $previousPath
    if ($null -eq $previousHip) {
        Remove-Item Env:\HIP_VISIBLE_DEVICES -ErrorAction SilentlyContinue
    }
    else {
        $env:HIP_VISIBLE_DEVICES = $previousHip
    }
}

$marginalVram = $null
if ($null -ne $peak -and $null -ne $idle.vram_dedicated_mib -and $null -ne $peak.vram_dedicated_mib) {
    $marginalVram = [Math]::Round($peak.vram_dedicated_mib - $idle.vram_dedicated_mib, 1)
}

$pressure = $null
if ($DeviceVramMib -gt 0 -and $null -ne $peak -and $null -ne $peak.vram_dedicated_mib) {
    $pressure = Test-V2GpuMemoryPressure -TotalMib $DeviceVramMib -Sample ([pscustomobject]@{
            instance      = 'load-peak'
            dedicated_mib = $peak.vram_dedicated_mib
            shared_mib    = $(if ($null -ne $peak.vram_shared_mib) { $peak.vram_shared_mib } else { 0 })
        })
}

$report = [ordered]@{
    schema_version = 1
    scenario       = 'context-footprint-at-load'
    started_utc    = [DateTime]::UtcNow.ToString('o')
    configuration  = [ordered]@{
        model_path      = $ModelPath
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
    }
    load_seconds   = $loadSeconds
    idle           = [ordered]@{
        vram_dedicated_mib = $idle.vram_dedicated_mib
        vram_shared_mib    = $idle.vram_shared_mib
        commit_gib         = $idle.commit_gib
        physical_used_gib  = $idle.physical_used_gib
    }
    peak           = $peak
    # The model's own VRAM cost, with the desktop's share removed. This is the
    # quantity resources.peak_vram_gib is meant to hold; peak.vram_dedicated_mib
    # is what the device budget applies to.
    marginal_vram_mib = $marginalVram
    gpu_pressure   = $pressure
    failure        = $failure
}

if ($OutputPath) {
    $directory = Split-Path -Parent $OutputPath
    if ($directory) { New-Item -ItemType Directory -Force -Path $directory | Out-Null }
    $report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $OutputPath -Encoding UTF8
}

if (-not $Quiet) {
    if ($failure) {
        Write-Warning ("  FAILED: {0}" -f $failure)
    }
    else {
        Write-Host ("  load {0}s | adapter {1:N0} MiB (marginal {2:N0}) | shared {3:N0} MiB | ws {4:N2} GiB | commit {5:N2} GiB | {6}" -f `
                $loadSeconds, $peak.vram_dedicated_mib, $marginalVram, $peak.vram_shared_mib, $peak.process_ws_gib, $peak.commit_gib, `
            $(if ($null -ne $pressure) { $pressure.state } else { 'unclassified' }))
    }
}

$report
