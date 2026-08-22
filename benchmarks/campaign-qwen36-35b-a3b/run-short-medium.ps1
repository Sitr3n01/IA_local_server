<#
.SYNOPSIS
The short and medium half of the campaign, chained. Runs after Phase 2.

.DESCRIPTION
Everything here finishes in minutes to tens of minutes per stage, so it can be
watched. The expensive half - filled-context decode and the retention ramps -
is in `run-overnight.ps1`.

Order is by dependency, then by cost:

Stage 1  Re-measure two throughput cells whose validity is in doubt, under
         distinct labels so the original figures survive for comparison rather
         than being overwritten. See CONTAMINATED-CELLS.md.
Stage 2  Narrow each placement threshold from +-4 layers to exact. Load-only
         cells; this is the cheapest stage and it feeds every later decision,
         because Phase 2 measured decode falling with every host layer.
Stage 3  `--load-mode` A/B, which the runtime itself suggests when experts are
         offloaded with mmap enabled.
Stage 4  Derive the profile candidates from Phases 2 and 3, then grade coding,
         tool-calling and JSON on each distinct weight set.

Stage 4 is the only one that can take more than half an hour, and it is the one
whose result decides whether any profile is worth shipping.

Nothing here writes to config/models.yaml.
#>
[CmdletBinding()]
param(
    [int[]]$Stages = @(1, 2, 3, 4),
    [string]$ModelRoot = 'C:\IA\models\Qwen3.6-35B-A3B-GGUF',
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [string]$OutputRoot = 'C:\IA\IA_local_server\benchmarks\campaign-qwen36-35b-a3b',
    [int]$DeviceVramMib = 16304
)

$ErrorActionPreference = 'Continue'
Set-StrictMode -Version Latest

$campaign = $OutputRoot
$scripts = 'C:\IA\IA_local_server\scripts\v2'
$throughput = Join-Path $scripts 'Invoke-V2ThroughputSweep.ps1'

$paths = @{
    q3kxl = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q3_K_XL.gguf'
    q2kxl = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q2_K_XL.gguf'
    iq4xs = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-IQ4_XS.gguf'
    q3ks  = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q3_K_S.gguf'
}

$preExisting = @(Get-Process -Name 'llama-server', 'llama-bench' -ErrorAction SilentlyContinue |
        ForEach-Object { $_.Id })
if ($preExisting.Count -gt 0) {
    throw ("{0} llama process(es) are already running (pids {1}). A measurement taken beside one is not valid; stop them first." -f `
            $preExisting.Count, ($preExisting -join ', '))
}

$started = [DateTime]::Now
function Write-Stage { param([string]$Text)
    Write-Host ''
    Write-Host ("=== {0}  (+{1:hh\:mm\:ss}) ===" -f $Text, ([DateTime]::Now - $started))
}

# ---------------------------------------------------------------- Stage 1
if ($Stages -contains 1) {
    Write-Stage 'Stage 1: re-measure the doubtful throughput cells'
    # Distinct labels: the point is to compare against the originals, not to
    # replace them. A silent overwrite would destroy the only evidence that says
    # whether the first figure was wrong.
    # iq4xs/16 is re-run in full, not just its pp32768. The contamination was
    # first identified there, but its tg128 started at 12:20:00 - seconds after
    # the intruding server was killed - and came back at 14.3% relative
    # standard deviation against 1.2-5.1% everywhere else in the campaign. The
    # blast radius was wider than the one cell first attributed to it.
    $remeasure = @(
        @{ model = 'iq4xs'; ncpumoe = 16; tests = @('pp:512', 'pp:8192', 'pp:32768', 'tg:128'); why = 'contaminated by a concurrent llama-server; tg128 spread 14.3% against 1.2-5.1% elsewhere' },
        @{ model = 'q3ks'; ncpumoe = 8; tests = @('pp:512', 'pp:8192', 'pp:32768', 'tg:128'); why = 'a teardown bug may have killed it mid-run' }
    )
    foreach ($cell in $remeasure) {
        $model = [string]$cell.model
        if (-not $paths.ContainsKey($model) -or -not (Test-Path -LiteralPath $paths[$model])) {
            throw ("Re-measure names model '{0}', which is not on disk under '{1}'." -f $model, $ModelRoot)
        }
        $label = "{0}-ctx32768-kvq4-ncpumoe{1}-remeasure" -f $model, $cell.ncpumoe
        $out = Join-Path $campaign ('throughput\throughput-' + $label + '.json')
        if (Test-Path -LiteralPath $out) { Write-Host "[have] $label"; continue }
        Write-Host ("--- {0}: {1}" -f $label, $cell.why)
        & $throughput -RuntimeRoot $RuntimeRoot -ModelPath $paths[$model] `
            -Label $label -ContextTokens 32768 -CacheTypeK q4_0 -CacheTypeV q4_0 `
            -NCpuMoe ([int]$cell.ncpumoe) -Repetitions 3 -Tests @([string[]]$cell.tests) `
            -DeviceVramMib $DeviceVramMib -OutputRoot (Join-Path $campaign 'throughput')
    }
}

# ---------------------------------------------------------------- Stage 2
if ($Stages -contains 2) {
    Write-Stage 'Stage 2: placement refinement, one layer at a time'
    & (Join-Path $campaign 'Invoke-Qwen36PlacementRefinement.ps1') `
        -ModelRoot $ModelRoot -RuntimeRoot $RuntimeRoot -OutputRoot $campaign `
        -DeviceVramMib $DeviceVramMib
}

# ---------------------------------------------------------------- Stage 3
if ($Stages -contains 3) {
    Write-Stage 'Stage 3: --load-mode A/B'
    # Q3_K_XL at its own narrowest admissible split: a middle amount of expert
    # weight on the host, which is where the mmap question actually bites.
    & (Join-Path $campaign 'Test-Qwen36LoadMode.ps1') `
        -RuntimeRoot $RuntimeRoot -ModelPath $paths['q3kxl'] -OutputRoot $campaign `
        -NCpuMoe 12 -DeviceVramMib $DeviceVramMib
}

# ---------------------------------------------------------------- Stage 3b
if ($Stages -contains 3) {
    Write-Stage 'Stage 3b: thread count, with experts on the host'
    # docs/BENCHMARKS.md requires this sweep whenever part of the model is
    # CPU-resident, and every cell in this campaign so far ran at -t 8.
    #
    # Phase 2 gives a reason to expect it matters. Decode cost rises about
    # 0.77-1.03 ms per host layer depending on the quantization, but only 8 of
    # 256 experts fire per token, so the bytes actually read per layer are
    # 7.8-11.3 MiB. That implies roughly 13.5 GB/s of effective throughput -
    # far below what dual-channel DDR5 delivers - which points at CPU matmul
    # rather than memory bandwidth as the binding constraint. If that reading is
    # right, thread count is a live lever; if 16 is no better than 8, SMT
    # contention on this 8C/16T part is the explanation and 8 stays.
    foreach ($threads in @(8, 16)) {
        $label = "q3kxl-ctx32768-kvq4-ncpumoe12-t$threads"
        $out = Join-Path $campaign ('threads\throughput-' + $label + '.json')
        if (Test-Path -LiteralPath $out) { Write-Host "[have] $label"; continue }
        Write-Host "--- $label"
        & $throughput -RuntimeRoot $RuntimeRoot -ModelPath $paths['q3kxl'] `
            -Label $label -ContextTokens 32768 -CacheTypeK q4_0 -CacheTypeV q4_0 `
            -NCpuMoe 12 -Threads $threads -Repetitions 3 `
            -Tests @('pp:512', 'pp:8192', 'tg:128') `
            -DeviceVramMib $DeviceVramMib -OutputRoot (Join-Path $campaign 'threads')
    }
}

# ---------------------------------------------------------------- Stage 4
if ($Stages -contains 4) {
    Write-Stage 'Stage 4: derive profile candidates, then grade them'
    & (Join-Path $campaign 'Select-Qwen36Profiles.ps1') -OutputRoot $campaign -DeviceVramMib $DeviceVramMib
    if (Test-Path -LiteralPath (Join-Path $campaign 'phase4-cells.json')) {
        & (Join-Path $campaign 'run-campaign.ps1') -Phases 4 `
            -ModelRoot $ModelRoot -RuntimeRoot $RuntimeRoot -OutputRoot $campaign `
            -DeviceVramMib $DeviceVramMib
    }
    else {
        Write-Warning 'Select-Qwen36Profiles.ps1 produced no phase4-cells.json; nothing to grade.'
    }
}

Write-Stage ("SHORT/MEDIUM RUN COMPLETE: stages " + ($Stages -join ','))
Write-Host ("total elapsed {0:hh\:mm\:ss}" -f ([DateTime]::Now - $started))

$stray = @(Get-Process -Name 'llama-server', 'llama-bench' -ErrorAction SilentlyContinue)
if ($stray.Count -gt 0) {
    Write-Warning ("{0} stray process(es) survived; stopping them." -f $stray.Count)
    $stray | ForEach-Object { Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue }
}
else { Write-Host 'no stray llama processes' }
