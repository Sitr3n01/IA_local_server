<#
.SYNOPSIS
The extensive, unattended half of the campaign. Hours, not minutes.

.DESCRIPTION
Everything here prefills a large window before it measures anything, which is
why it is separated from the short and medium phases rather than run beside
them. A single 240k-token filled-context cell costs roughly ten minutes per
repetition, and the retention ramps prefill their corpus once per level.

Ordered so that the most decision-relevant result lands first. If the run is
interrupted at any point, everything before that point is still evidence:
`-Stages` re-enters at a chosen stage, and every stage skips cells whose output
already exists.

Stage 1  Filled-context decode. `tg128` from an empty window is not the number
         an agentic profile is judged on; a configuration that decodes at 40 t/s
         cold and 8 t/s with 240k resident is not a 256k profile. This is the
         measurement that separates them.
Stage 2  Long-context retention. Whether the model can still find and use
         material placed deep in a filled window, at each level the profiles
         claim.
Stage 3  Repeat of the short-prompt throughput cells. Purely a stability
         control: the same cells measured hours later, on a machine that has
         been loading and unloading multi-gigabyte models all evening, either
         reproduce or they do not. §7.5 of the report puts load-time
         repeatability at 0.16%; nothing yet says the same of throughput.

Nothing here writes to config/models.yaml.
#>
[CmdletBinding()]
param(
    [int[]]$Stages = @(1, 2, 3),
    [string]$ModelRoot = 'C:\IA\models\Qwen3.6-35B-A3B-GGUF',
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [string]$OutputRoot = 'C:\IA\IA_local_server\benchmarks\campaign-qwen36-35b-a3b',
    [int]$DeviceVramMib = 16304,

    # A depth sweep at 262144 prefills a quarter of a million tokens per
    # repetition. Two hours is generous rather than optimistic, and it exists so
    # a hang cannot consume the whole night.
    [int]$PerTestTimeoutSeconds = 7200
)

$ErrorActionPreference = 'Continue'
Set-StrictMode -Version Latest

$scripts = 'C:\IA\IA_local_server\scripts\v2'
$throughput = Join-Path $scripts 'Invoke-V2ThroughputSweep.ps1'
$qualify = Join-Path $scripts 'Invoke-V2ProfileQualification.ps1'

$paths = @{
    q3kxl = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q3_K_XL.gguf'
    q2kxl = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q2_K_XL.gguf'
    iq4xs = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-IQ4_XS.gguf'
    q3ks  = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q3_K_S.gguf'
}

$started = [DateTime]::Now
function Write-Stage { param([string]$Text)
    Write-Host ''
    Write-Host ("=== {0}  (+{1:hh\:mm\:ss}) ===" -f $Text, ([DateTime]::Now - $started))
}

# Processes that were already running before this script started. The teardown
# below must not touch them.
#
# An earlier revision ended with an unconditional `Get-Process llama-server,
# llama-bench | Stop-Process -Force`, which is correct for a script that owns the
# machine and wrong for one run while another phase is in flight - it killed a
# live benchmark during a dry run of this file. Recording the pre-existing set
# makes the teardown mean "clean up after me" instead of "clean up".
$script:PreExistingLlamaPids = @(
    Get-Process -Name 'llama-server', 'llama-bench' -ErrorAction SilentlyContinue |
        ForEach-Object { $_.Id })
if ($script:PreExistingLlamaPids.Count -gt 0) {
    Write-Warning ("{0} llama process(es) were already running when this started: {1}. They will be left alone, but a measurement taken beside them is not valid - stop them and re-run if that was not intended." -f `
            $script:PreExistingLlamaPids.Count, ($script:PreExistingLlamaPids -join ', '))
}

# ---------------------------------------------------------------- Stage 1
if ($Stages -contains 1) {
    Write-Stage 'Stage 1: filled-context decode'
    $cellsPath = Join-Path $OutputRoot 'phase5-cells.json'
    if (-not (Test-Path -LiteralPath $cellsPath)) {
        Write-Warning "Missing $cellsPath. Run Select-Qwen36Profiles.ps1 after Phase 2 and Phase 4."
    }
    else {
        foreach ($cell in @(Get-Content -Raw -LiteralPath $cellsPath | ConvertFrom-Json)) {
            $model = [string]$cell.model
            if (-not $paths.ContainsKey($model) -or -not (Test-Path -LiteralPath $paths[$model])) {
                throw ("phase5-cells.json names model '{0}', which is not on disk under '{1}'." -f $model, $ModelRoot)
            }
            $out = Join-Path $OutputRoot ('filled\throughput-' + [string]$cell.label + '.json')
            if (Test-Path -LiteralPath $out) { Write-Host ("[have] " + $cell.label); continue }
            Write-Stage ("filled: " + $cell.label)
            & $throughput -RuntimeRoot $RuntimeRoot -ModelPath $paths[$model] `
                -Label ([string]$cell.label) -ContextTokens ([int]$cell.context) `
                -CacheTypeK ([string]$cell.ctk) -CacheTypeV ([string]$cell.ctv) `
                -NCpuMoe ([int]$cell.ncpumoe) -Repetitions 2 `
                -Tests @([string[]]$cell.tests) -PerTestTimeoutSeconds $PerTestTimeoutSeconds `
                -DeviceVramMib $DeviceVramMib -OutputRoot (Join-Path $OutputRoot 'filled')
        }
    }
}

# ---------------------------------------------------------------- Stage 2
if ($Stages -contains 2) {
    Write-Stage 'Stage 2: long-context retention'
    $cellsPath = Join-Path $OutputRoot 'phase5-cells.json'
    if (-not (Test-Path -LiteralPath $cellsPath)) {
        Write-Warning "Missing $cellsPath; skipping retention."
    }
    else {
        foreach ($cell in @(Get-Content -Raw -LiteralPath $cellsPath | ConvertFrom-Json)) {
            $model = [string]$cell.model
            if (-not (Test-Path -LiteralPath $paths[$model])) { continue }
            $context = [int]$cell.context
            # Levels the profile actually claims, ending near but not at the
            # declared window: a retention probe still needs room for its own
            # question and answer.
            $levels = switch ($context) {
                32768 { @(8192, 16384, 28000) }
                131072 { @(32768, 65536, 120000) }
                262144 { @(32768, 131072, 196608, 240000) }
                default { @([Math]::Floor($context * 0.9)) }
            }
            $label = ([string]$cell.label) + '-retention'
            $out = Join-Path $OutputRoot ('retention\qualify-' + $label + '.json')
            if (Test-Path -LiteralPath $out) { Write-Host "[have] $label"; continue }
            Write-Stage ("retention: $label at " + ($levels -join ', '))
            & $qualify -RuntimeRoot $RuntimeRoot -ModelPath $paths[$model] `
                -Label $label -ContextTokens $context `
                -CacheTypeK ([string]$cell.ctk) -CacheTypeV ([string]$cell.ctv) `
                -NCpuMoe ([int]$cell.ncpumoe) -Suites 'retention' `
                -RetentionTokens $levels -CaptureDiagnostics `
                -DeviceVramMib $DeviceVramMib -OutputRoot (Join-Path $OutputRoot 'retention')
        }
    }
}

# ---------------------------------------------------------------- Stage 3
if ($Stages -contains 3) {
    Write-Stage 'Stage 3: throughput stability re-measurement'
    $cellsPath = Join-Path $OutputRoot 'phase2-cells.json'
    if (-not (Test-Path -LiteralPath $cellsPath)) {
        Write-Warning "Missing $cellsPath; skipping stability run."
    }
    else {
        # Only the selected placements, not the whole Phase 2 matrix: this is a
        # reproducibility check on the cells a profile would ship, not a second
        # campaign.
        $selected = @(Get-Content -Raw -LiteralPath $cellsPath | ConvertFrom-Json |
                Where-Object { -not $_.contrast } |
                Group-Object -Property model |
                ForEach-Object { $_.Group | Sort-Object ncpumoe | Select-Object -First 1 })
        foreach ($cell in $selected) {
            $model = [string]$cell.model
            if (-not (Test-Path -LiteralPath $paths[$model])) { continue }
            $label = ([string]$cell.label) + '-repeat'
            $out = Join-Path $OutputRoot ('stability\throughput-' + $label + '.json')
            if (Test-Path -LiteralPath $out) { Write-Host "[have] $label"; continue }
            Write-Stage ("stability: $label")
            & $throughput -RuntimeRoot $RuntimeRoot -ModelPath $paths[$model] `
                -Label $label -ContextTokens ([int]$cell.context) `
                -CacheTypeK ([string]$cell.ctk) -CacheTypeV ([string]$cell.ctv) `
                -NCpuMoe ([int]$cell.ncpumoe) -Repetitions 3 `
                -Tests @('pp:512', 'pp:8192', 'tg:128') `
                -PerTestTimeoutSeconds $PerTestTimeoutSeconds `
                -DeviceVramMib $DeviceVramMib -OutputRoot (Join-Path $OutputRoot 'stability')
        }
    }
}

Write-Stage ("OVERNIGHT RUN COMPLETE: stages " + ($Stages -join ','))
Write-Host ("total elapsed {0:hh\:mm\:ss}" -f ([DateTime]::Now - $started))

# A run that leaves a multi-gigabyte process behind is worse than no run - but
# only the ones this run started are its to clean up.
$stray = @(Get-Process -Name 'llama-server', 'llama-bench' -ErrorAction SilentlyContinue |
        Where-Object { $script:PreExistingLlamaPids -notcontains $_.Id })
if ($stray.Count -gt 0) {
    Write-Warning ("{0} stray process(es) from this run survived; stopping them." -f $stray.Count)
    $stray | ForEach-Object { Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue }
}
else { Write-Host 'no stray llama processes from this run' }
