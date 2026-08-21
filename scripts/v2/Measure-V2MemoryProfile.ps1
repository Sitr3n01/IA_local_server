<#
.SYNOPSIS
Measures throughput and driver-level memory for one llama.cpp configuration.

.DESCRIPTION
docs/BENCHMARKS.md requires peak dedicated VRAM, peak shared GPU memory, and host
memory alongside every throughput figure, and requires that the memory numbers
come from the driver rather than from llama.cpp's own buffer accounting.

The reason is not that llama.cpp's numbers are wrong. Measured against the
adapter they match the model's marginal cost to within 0.4%. The reason is that
they describe one process on a shared device: this workstation's desktop holds
roughly 3.0 GiB before a model loads, so a configuration llama.cpp reports as
comfortably resident can be sitting at 97-98% of the adapter and paging over
PCIe, which costs a factor of three on prompt processing and raises no error
(benchmarks/REPORT-qwen38-27b-gfx1201-20260821.md).

Both an idle sample and a peak sample are therefore recorded. Their difference is
the model's own cost; the peak alone is what the device budget applies to. A
report carrying only one of the two cannot distinguish a large model from a busy
desktop.

This runs llama-bench for the throughput columns and samples the adapter and the
host concurrently for the memory columns, so a single report answers both halves
of the question. It emits JSON only; it never edits a manifest. The values it
produces are the ones docs/MODEL_PROMOTION.md expects an operator to copy into
resources.peak_vram_gib, resources.peak_ram_gib and resources.peak_commit_gib
after reviewing them.

Peaks skip failed samples rather than counting them as zero, and a run where
every sample failed reports null. A null here means "not measured", which the
edge admission gate treats as stricter than any number.

.EXAMPLE
./Measure-V2MemoryProfile.ps1 -RuntimeRoot C:\IA\runtimes\llama.cpp\b10549-rocm-7.14 `
    -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-IQ4_XS.gguf `
    -Label ws-ub288 -UBatchSize 288 -TensorOverride 'blk\.(6[0-3])\.ffn_.*=CPU' `
    -DeviceVramMib 16304 -OutputPath .\benchmarks\memory-ws-ub288.json
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$RuntimeRoot,

    [Parameter(Mandatory = $true)]
    [string]$ModelPath,

    [Parameter(Mandatory = $true)]
    [string]$Label,

    # Prompt lengths to measure, in tokens. The default set is the one the
    # weight-split characterization used, so a new report is comparable to it.
    [int[]]$PromptTokens = @(512, 8192),

    [int]$GenTokens = 128,

    [int]$BatchSize = 2048,

    [int]$UBatchSize = 512,

    [int]$NGpuLayers = 99,

    [int]$Threads = 8,

    [ValidateSet('f16', 'q8_0', 'q4_0')]
    [string]$CacheTypeK = 'q8_0',

    [ValidateSet('f16', 'q8_0', 'q4_0')]
    [string]$CacheTypeV = 'q8_0',

    # A single -ot expression, already in llama.cpp's "pattern=BUFFER" form.
    # Empty means full residency.
    [string]$TensorOverride = '',

    [int]$Repetitions = 3,

    # The adapter's dedicated budget, so the pressure verdict is computed against
    # the same number the edge admission gate uses.
    [ValidateRange(0, 1048576)]
    [int]$DeviceVramMib = 0,

    [ValidateRange(1, 30)]
    [int]$SampleIntervalSeconds = 3,

    # 65..256 is a measured throughput collapse on hybrid Gated DeltaNet models.
    # models.schema.json refuses the band outright, so a value inside it can be
    # characterized but can never be shipped. Requiring the switch keeps an
    # accidental sweep from producing a report that looks promotable.
    [switch]$AllowUBatchDeadBand,

    [string]$OutputPath,

    [switch]$Quiet
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot 'Telemetry.ps1')

if ($UBatchSize -ge 65 -and $UBatchSize -le 256 -and -not $AllowUBatchDeadBand) {
    throw "UBatchSize $UBatchSize is inside the 65..256 throughput dead band that models.schema.json refuses. Pass -AllowUBatchDeadBand to characterize it anyway; the result cannot be promoted to a manifest."
}

$benchExe = Join-Path $RuntimeRoot 'llama-bench.exe'
foreach ($required in @($benchExe, $ModelPath)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Required file is missing: $required"
    }
}

# Sampling runs in this process while llama-bench runs in a job, so the memory
# columns cover the whole run rather than a moment after it.
$benchBlock = {
    param([string]$Exe, [string]$RuntimeRoot, [string[]]$Arguments)

    # System32\downlevel carries the UCRT API sets the ROCm build imports; the
    # runtime directory carries the rocBLAS closure. Without both, ggml-hip.dll
    # fails to load and the run silently falls back to CPU.
    $env:PATH = "$RuntimeRoot;C:\Windows\System32\downlevel;$env:PATH"
    # The b10549 ROCm build enumerates the integrated GPU as ROCm0, so the
    # discrete card has to be isolated before --device ROCm0 means the right
    # device. The pinned b8407 build did not expose the iGPU at all.
    $env:HIP_VISIBLE_DEVICES = '1'
    & $Exe @Arguments 2>&1 | Out-String
}

$results = [System.Collections.Generic.List[object]]::new()
$samples = [System.Collections.Generic.List[object]]::new()

$idle = Get-V2MemorySample -ProcessId 0

foreach ($prompt in $PromptTokens) {
    $arguments = @(
        '--model', $ModelPath,
        '--device', 'ROCm0',
        '--split-mode', 'none',
        '-ngl', "$NGpuLayers",
        '-fa', '1',
        '-p', "$prompt",
        '-n', "$GenTokens",
        '-b', "$BatchSize",
        '-ub', "$UBatchSize",
        '-ctk', $CacheTypeK,
        '-ctv', $CacheTypeV,
        '-t', "$Threads",
        '-r', "$Repetitions"
    )
    if ($TensorOverride) { $arguments += @('-ot', $TensorOverride) }

    if (-not $Quiet) { Write-Host ("Running {0}: p={1} ub={2} kv={3}" -f $Label, $prompt, $UBatchSize, $CacheTypeK) }

    $job = Start-Job -ScriptBlock $benchBlock -ArgumentList $benchExe, $RuntimeRoot, $arguments
    try {
        while ($job.State -eq 'Running') {
            Start-Sleep -Seconds $SampleIntervalSeconds
            $process = @(Get-Process -Name 'llama-bench' -ErrorAction SilentlyContinue)
            $pid_ = if ($process.Count -gt 0) { $process[0].Id } else { 0 }
            $sample = Get-V2MemorySample -ProcessId $pid_
            $samples.Add($sample)
        }
        $output = Receive-Job $job
    }
    finally {
        Remove-Job $job -Force -ErrorAction SilentlyContinue
    }

    # llama-bench prints a markdown table; the throughput cell is second from the
    # right and the test name is third. Parsing the table rather than recomputing
    # anything keeps this from becoming a second source of truth for the numbers.
    foreach ($line in ($output -split "`r?`n")) {
        if ($line -notmatch '\|\s*(pp\d+|tg\d+)\s*\|') { continue }
        $cells = @(($line -split '\|') | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne '' })
        if ($cells.Count -lt 2) { continue }
        # "270.29 +/- 1.81", where the separator is a plus-minus sign that does
        # not survive every console encoding intact. Matching the numbers and
        # ignoring whatever sits between them is what makes the parse portable;
        # anchoring on the separator produced a silent null on this host.
        $value = $cells[-1]
        $mean = $null
        $stddev = $null
        $numbers = @([regex]::Matches($value, '\d+(?:\.\d+)?') | ForEach-Object { $_.Value })
        if ($numbers.Count -ge 1) {
            $mean = [double]::Parse($numbers[0], [Globalization.CultureInfo]::InvariantCulture)
        }
        if ($numbers.Count -ge 2) {
            $stddev = [double]::Parse($numbers[-1], [Globalization.CultureInfo]::InvariantCulture)
        }
        if ($null -eq $mean) {
            throw "Could not parse a throughput value from llama-bench row '$line'. Refusing to record a null rather than reporting a measurement that did not happen."
        }
        # Each llama-bench invocation emits its own tg row, so the generation
        # figure appears once per prompt length. Recording which invocation a row
        # came from keeps two identically-named tg128 rows distinguishable.
        $results.Add([ordered]@{
            test            = $cells[-2]
            measured_at_pp  = $prompt
            tokens_per_sec  = $mean
            stddev          = $stddev
        })
    }
}

function Get-PeakSample {
    param([string]$Field)
    $values = @($samples | ForEach-Object { $_.$Field } | Where-Object { $null -ne $_ })
    if ($values.Count -eq 0) { return $null }
    return ($values | Measure-Object -Maximum).Maximum
}

$peakDedicated = Get-PeakSample -Field 'vram_dedicated_mib'
$peakShared = Get-PeakSample -Field 'vram_shared_mib'

$pressure = $null
if ($DeviceVramMib -gt 0 -and $null -ne $peakDedicated) {
    $worst = [pscustomobject]@{
        instance      = 'run-peak'
        dedicated_mib = $peakDedicated
        shared_mib    = $(if ($null -ne $peakShared) { $peakShared } else { 0 })
    }
    $pressure = Test-V2GpuMemoryPressure -Sample $worst -TotalMib $DeviceVramMib
}

$report = [ordered]@{
    schema_version = 1
    scenario       = 'memory-and-throughput-profile'
    label          = $Label
    started_utc    = [DateTime]::UtcNow.ToString('o')
    configuration  = [ordered]@{
        model_path      = $ModelPath
        runtime_root    = $RuntimeRoot
        prompt_tokens   = @($PromptTokens)
        gen_tokens      = $GenTokens
        batch_size      = $BatchSize
        ubatch_size     = $UBatchSize
        gpu_layers      = $NGpuLayers
        threads         = $Threads
        cache_type_k    = $CacheTypeK
        cache_type_v    = $CacheTypeV
        tensor_override = $TensorOverride
        repetitions     = $Repetitions
        device_vram_mib = $DeviceVramMib
    }
    throughput     = @($results)
    # Idle is recorded so a reader can separate what the model cost from what the
    # desktop was already holding. This machine sits at roughly 3 GiB dedicated
    # and 400 MiB shared with nothing loaded.
    idle           = [ordered]@{
        vram_dedicated_mib = $idle.vram_dedicated_mib
        vram_shared_mib    = $idle.vram_shared_mib
        commit_gib         = $idle.commit_gib
        physical_used_gib  = $idle.physical_used_gib
    }
    peak           = [ordered]@{
        vram_dedicated_mib  = $peakDedicated
        vram_shared_mib     = $peakShared
        process_vram_mib    = Get-PeakSample -Field 'process_vram_mib'
        process_ws_gib      = Get-PeakSample -Field 'process_ws_gib'
        process_private_gib = Get-PeakSample -Field 'process_private_gib'
        commit_gib          = Get-PeakSample -Field 'commit_gib'
        physical_used_gib   = Get-PeakSample -Field 'physical_used_gib'
    }
    samples_taken  = $samples.Count
    gpu_pressure   = $pressure
}

if ($OutputPath) {
    $directory = Split-Path -Parent $OutputPath
    if ($directory) { New-Item -ItemType Directory -Force -Path $directory | Out-Null }
    $report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $OutputPath -Encoding UTF8
    if (-not $Quiet) { Write-Host ("Report written: {0}" -f $OutputPath) }
}

if (-not $Quiet) {
    foreach ($row in $results) {
        Write-Host ("  {0,-8} {1,10:N2} t/s" -f $row.test, $row.tokens_per_sec)
    }
    Write-Host ("  peak VRAM {0:N0} MiB dedicated, {1:N0} MiB shared" -f $peakDedicated, $peakShared)
    if ($null -ne $pressure) { Write-Host ("  pressure: {0}" -f $pressure.state) }
}

$report
