<#
.SYNOPSIS
Drives llama-bench for prefill, decode, and decode-at-occupancy, sampling the
adapter while it runs.

.DESCRIPTION
Two things this adds over calling llama-bench directly.

First, `-d/--n-depth`. Decode measured against an empty context answers the wrong
question for an agentic profile: a configuration that decodes at 30 t/s cold and
8 t/s with 240k tokens resident is not a 256k profile, and only the depth sweep
separates them. Every decode row here is measured at a stated occupancy.

Second, memory. llama-bench reports throughput and nothing about the driver, and
on this adapter throughput collapses because shared memory climbs, not because
the kernel got slower. Sampling concurrently is what makes the two readable
together.

Device isolation is not optional on b10549: it enumerates the integrated gfx1036
first, so without HIP_VISIBLE_DEVICES=1 this benchmarks the iGPU.

.EXAMPLE
./Invoke-V2ThroughputSweep.ps1 -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-Q3_K_XL.gguf `
    -Label q3kxl-kvq4 -CacheTypeK q4_0 -CacheTypeV q4_0 `
    -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-256k
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$ModelPath,
    [Parameter(Mandatory = $true)][string]$Label,
    [Parameter(Mandatory = $true)][string]$OutputRoot,

    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [ValidateSet('f16', 'q8_0', 'q4_0')][string]$CacheTypeK = 'q4_0',
    [ValidateSet('f16', 'q8_0', 'q4_0')][string]$CacheTypeV = 'q4_0',
    [string]$TensorOverride = 'blk\.(6[0-3])\.ffn_.*=CPU',
    [int]$UBatchSize = 288,
    [int]$BatchSize = 2048,
    [int]$NGpuLayers = 99,
    [int]$Threads = 8,
    [int]$Repetitions = 3,
    [int]$DeviceVramMib = 16304,

    [ValidateRange(-1, 1024)]
    [int]$NCpuMoe = -1,

    [switch]$CpuMoe,

    # "pp:<n>" prefill, "tg:<n>" decode from empty, "tg:<n>@<depth>" decode with
    # the context already occupied.
    [string[]]$Tests = @('pp:512', 'pp:8192', 'pp:32768', 'tg:128'),

    [ValidateRange(1, 30)][int]$SampleIntervalSeconds = 2,

    # A depth sweep at 262144 prefills a quarter of a million tokens per
    # repetition, so it is legitimately slow and must not be mistaken for a
    # hang. It must also not be able to stall an unattended campaign forever,
    # which an untimed WaitForExit can.
    [ValidateRange(60, 86400)][int]$PerTestTimeoutSeconds = 7200
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($CpuMoe -and $NCpuMoe -ge 0) {
    throw 'CpuMoe and NCpuMoe are mutually exclusive.'
}

. (Join-Path $PSScriptRoot 'Telemetry.ps1')

$benchExe = Join-Path $RuntimeRoot 'llama-bench.exe'
foreach ($required in @($benchExe, $ModelPath)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) { throw "Missing: $required" }
}
New-Item -ItemType Directory -Force -Path $OutputRoot | Out-Null

$previousPath = $env:PATH
$previousHip = $env:HIP_VISIBLE_DEVICES
$env:PATH = "$RuntimeRoot;C:\Windows\System32\downlevel;$env:PATH"
$env:HIP_VISIBLE_DEVICES = '1'

$rows = @()
try {
    foreach ($test in $Tests) {
        $depth = 0
        $spec = $test
        if ($spec -match '^(.*)@(\d+)$') { $spec = $Matches[1]; $depth = [int]$Matches[2] }
        $parts = $spec.Split(':')
        if ($parts.Count -ne 2) { throw "Malformed test '$test'" }
        $kind = $parts[0]
        $n = [int]$parts[1]

        $args = @(
            '-m', $ModelPath,
            '-dev', 'ROCm0',
            '--split-mode', 'none',
            '-ngl', "$NGpuLayers",
            '-fa', 'on',
            '-b', "$BatchSize",
            '-ub', "$UBatchSize",
            '-ctk', $CacheTypeK,
            '-ctv', $CacheTypeV,
            '-t', "$Threads",
            '-r', "$Repetitions",
            '-o', 'json'
        )
        if ($TensorOverride) { $args += @('-ot', $TensorOverride) }
        if ($CpuMoe) { $args += @('--cpu-moe') }
        elseif ($NCpuMoe -ge 0) { $args += @('--n-cpu-moe', "$NCpuMoe") }
        if ($kind -eq 'pp') { $args += @('-p', "$n", '-n', '0') }
        elseif ($kind -eq 'tg') { $args += @('-p', '0', '-n', "$n") }
        else { throw "Unknown test kind '$kind'" }
        if ($depth -gt 0) { $args += @('-d', "$depth") }

        $tag = "{0}-{1}{2}{3}" -f $Label, $kind, $n, $(if ($depth) { "-d$depth" } else { '' })
        Write-Host ("RUN   {0}" -f $tag)

        $samplePath = Join-Path $OutputRoot ("bench-samples-$tag.jsonl")
        if (Test-Path -LiteralPath $samplePath) { Remove-Item -LiteralPath $samplePath -Force }
        $stdout = Join-Path $OutputRoot ("bench-$tag.json")
        $stderr = Join-Path $OutputRoot ("bench-$tag.err")

        $started = [Diagnostics.Stopwatch]::StartNew()
        $proc = Start-Process -FilePath $benchExe -ArgumentList $args -PassThru `
            -RedirectStandardOutput $stdout -RedirectStandardError $stderr -WindowStyle Hidden

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
        } -ArgumentList (Join-Path $PSScriptRoot 'Telemetry.ps1'), $proc.Id, $samplePath, $SampleIntervalSeconds

        if (-not $proc.WaitForExit($PerTestTimeoutSeconds * 1000)) {
            Write-Warning ("  {0}: exceeded {1}s, killing" -f $tag, $PerTestTimeoutSeconds)
            Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
            $proc.WaitForExit(30000) | Out-Null
            $timedOut = $true
        }
        else {
            $timedOut = $false
            # The timeout overload of WaitForExit returns as soon as the process
            # signals and leaves ExitCode unpopulated on a -PassThru object. The
            # parameterless call flushes it. Without this every completed run is
            # recorded as a failure with a blank exit code, which is what
            # happened to the first phase-B roll-up.
            $proc.WaitForExit()
        }
        $started.Stop()
        Stop-Job -Job $sampler -ErrorAction SilentlyContinue
        Remove-Job -Job $sampler -Force -ErrorAction SilentlyContinue

        $value = $null
        $stddev = $null
        $failure = $null
        # Parse first and judge second. A readable result file is stronger
        # evidence that the run succeeded than an exit code that the process
        # object may not have captured.
        $parsedOk = $false
        try {
            $parsed = Get-Content -LiteralPath $stdout -Raw | ConvertFrom-Json
            $entry = @($parsed)[-1]
            $value = [Math]::Round([double]$entry.avg_ts, 2)
            $stddev = [Math]::Round([double]$entry.stddev_ts, 2)
            $parsedOk = $true
        }
        catch { $parsedOk = $false }

        if ($timedOut) {
            $failure = "timed out after ${PerTestTimeoutSeconds}s"
        }
        elseif (-not $parsedOk) {
            $code = if ($null -ne $proc.ExitCode) { $proc.ExitCode } else { 'unknown' }
            $failure = "llama-bench produced no parseable result (exit $code); see $stderr"
        }

        $peak = $null
        if (Test-Path -LiteralPath $samplePath) {
            $samples = @(Get-Content -LiteralPath $samplePath | ForEach-Object {
                    try { $_ | ConvertFrom-Json } catch { $null } } | Where-Object { $null -ne $_ })
            if ($samples.Count -gt 0) {
                $peak = [ordered]@{
                    vram_dedicated_mib = ($samples | ForEach-Object { $_.vram_dedicated_mib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
                    vram_shared_mib    = ($samples | ForEach-Object { $_.vram_shared_mib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
                    process_ws_gib     = ($samples | ForEach-Object { $_.process_ws_gib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
                    physical_used_gib  = ($samples | ForEach-Object { $_.physical_used_gib } | Where-Object { $null -ne $_ } | Measure-Object -Maximum).Maximum
                }
            }
        }

        $rows += [ordered]@{
            tag = $tag; kind = $kind; n = $n; depth = $depth
            tokens_per_second = $value; stddev = $stddev; timed_out = $timedOut
            wall_s = [Math]::Round($started.Elapsed.TotalSeconds, 1)
            peak = $peak; failure = $failure
        }
        if ($failure) { Write-Warning ("  {0}" -f $failure) }
        else {
            Write-Host ("  {0,10:N2} +/- {1,-6:N2} t/s | dedicated {2,6:N0} | shared {3,6:N0} MiB | {4:N0}s" -f `
                    $value, $stddev, $(if ($peak) { $peak.vram_dedicated_mib } else { 0 }), `
                    $(if ($peak) { $peak.vram_shared_mib } else { 0 }), $started.Elapsed.TotalSeconds)
        }
        Start-Sleep -Seconds 5
    }
}
finally {
    $env:PATH = $previousPath
    if ($null -eq $previousHip) { Remove-Item Env:\HIP_VISIBLE_DEVICES -ErrorAction SilentlyContinue }
    else { $env:HIP_VISIBLE_DEVICES = $previousHip }
}

$summaryPath = Join-Path $OutputRoot ("throughput-$Label.json")
[ordered]@{
    schema_version = 1
    scenario = 'throughput-sweep'
    label = $Label
    started_utc = [DateTime]::UtcNow.ToString('o')
    configuration = [ordered]@{
        model_path = $ModelPath
        model_bytes = (Get-Item -LiteralPath $ModelPath).Length
        runtime_root = $RuntimeRoot
        cache_type_k = $CacheTypeK
        cache_type_v = $CacheTypeV
        tensor_override = $TensorOverride
        ubatch_size = $UBatchSize
        batch_size = $BatchSize
        gpu_layers = $NGpuLayers
        threads = $Threads
        repetitions = $Repetitions
        device_vram_mib = $DeviceVramMib
        cpu_moe = [bool]$CpuMoe
        n_cpu_moe = $(if ($NCpuMoe -ge 0) { $NCpuMoe } else { $null })
    }
    results = $rows
} | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $summaryPath -Encoding UTF8

Write-Host ""
Write-Host ("Summary -> {0}" -f $summaryPath)
$rows | ForEach-Object { [pscustomobject]$_ } | Format-Table -AutoSize tag, tokens_per_second, stddev, wall_s
