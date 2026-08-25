<#
Ornith-1.5-35B-A3B on gfx1201 - physical campaign.

The question this campaign answers is not "is Ornith good" but "at the VRAM
budget this workstation actually has, is Ornith better than the Qwen3.6-35B-A3B
already on disk". That is why the artifacts are chosen by file size rather than
by quant name (see Get-Ornith15Artifacts.ps1): bartowski's ladder is not
unsloth's UD recipe, so a name-for-name pairing would compare two quantizers,
while a size-for-size pairing compares two models at one budget.

    Qwen3.6 UD-Q2_K_XL  12.29 GB  <->  iq2m    Ornith IQ2_M    12.54 GB
    Qwen3.6 UD-Q3_K_S   15.36 GB  <->  iq3xxs  Ornith IQ3_XXS  15.34 GB
    Qwen3.6 UD-IQ4_XS   17.73 GB  <->  q3kxl   Ornith Q3_K_XL  17.80 GB

Ordering is by cost of discovering a failure, not by the order the report reads.

Phase 0 is the runtime gate: b10549 already serves `qwen35moe` (the Qwen3.6
campaign proved that), but this GGUF is not that GGUF - 41 blocks instead of 40,
with a NextN/MTP head on blk.40 - so the gate re-establishes it rather than
inheriting the claim.

Phase 1 is MoE placement at one small context. This model is 88-91% expert FFN
by storage - more concentrated than Qwen3.6's 84.8% - so placement, not
`gpu_layers`, is the lever that decides whether a quantization fits at all.
Load-only cells: one server start each, no prefill.

Phase 2 prices throughput only at placements Phase 1 showed can fit, because a
throughput cell on a configuration that pages is a measurement of the pagefile.

Phase 3 is the full census - every quantization against every context against
both KV precisions, swept over placement - which is the Qwen3.6 campaign's 24
groups reproduced as 18 here. Phase 4 asks whether the weights still write
correct code, Phase 5 measures decode once the window is actually full.

Phase 6 has no counterpart in the Qwen3.6 campaign: it prices the MTP head this
GGUF carries and Qwen3.6's does not.

Phase 7 is the qualification the current roster was graded under on 2026-08-23 -
the full suite list plus a retention ramp scaled to each profile's window. It is
last because it is the longest, and because a model that fails Phase 4 does not
need it. It is also the phase that makes Ornith comparable to the four models it
would be replacing, rather than only to Qwen3.6.

Every phase writes one JSON per cell, so an interrupted run is still evidence.
Run a single phase with -Phases 1,2.
#>
[CmdletBinding()]
param(
    [int[]]$Phases = @(0, 1, 2, 3, 4, 5, 6, 7),
    [string]$ModelRoot = 'C:\IA\models\Ornith-1.5-35B-A3B-GGUF',
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [string]$OutputRoot = 'C:\IA\IA_local_server\benchmarks\campaign-ornith15-35b-a3b',
    # The census sweeps 0, 2, 4 ... up to this cap and then refines the boundary,
    # so this is a ceiling rather than a cell list. 28 matches the deepest rung
    # the Qwen3.6 census needed (iq4xs at 262144 with q8_0 cache); this model
    # puts a larger share of its bytes in the experts, so the useful range may
    # run deeper still and the cap is what stops it running forever.
    [int]$MaxCpuMoe = 28,
    [int]$DeviceVramMib = 16304
)

$ErrorActionPreference = 'Continue'
$scripts = 'C:\IA\IA_local_server\scripts\v2'
$throughput = Join-Path $scripts 'Invoke-V2ThroughputSweep.ps1'
$qualify = Join-Path $scripts 'Invoke-V2ProfileQualification.ps1'

# No tensor override anywhere in this campaign, for the same reason as Qwen3.6:
# the FFN weight lives in per-expert tensors that --n-cpu-moe places by layer,
# and a hand-written blk regex would fight it.
$models = [ordered]@{
    q3kxl  = Join-Path $ModelRoot 'Ornith-1.5-35B-A3B-Q3_K_XL.gguf'
    iq3xxs = Join-Path $ModelRoot 'Ornith-1.5-35B-A3B-IQ3_XXS.gguf'
    iq2m   = Join-Path $ModelRoot 'Ornith-1.5-35B-A3B-IQ2_M.gguf'
}

function Test-Artifact { param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { Write-Host "[skip] absent: $Path"; return $false }
    return $true
}

# ---------------------------------------------------------------- Phase 0
if ($Phases -contains 0) {
    Write-Host "=== Phase 0: runtime gate ==="
    & (Join-Path $OutputRoot 'Test-Ornith15RuntimeGate.ps1') `
        -RuntimeRoot $RuntimeRoot -OutputRoot $OutputRoot `
        -ModelPath $models['q3kxl'] -DeviceVramMib $DeviceVramMib
}

# ---------------------------------------------------------------- Phase 1
# 32768 / q4_0 is the cheapest context that still allocates a real KV cache, so
# the differences between cells are expert placement rather than cache size.
# This is the group selection reads, which is why it is measured on its own and
# first: everything downstream is chosen from it.
if ($Phases -contains 1) {
    Write-Host "=== Phase 1: MoE placement sweep, ctx 32768, KV q4_0/q4_0 ==="
    & (Join-Path $OutputRoot 'Invoke-Ornith15Census.ps1') `
        -ModelRoot $ModelRoot -RuntimeRoot $RuntimeRoot -OutputRoot $OutputRoot `
        -DeviceVramMib $DeviceVramMib -Contexts @(32768) -CacheTypes @('q4_0') `
        -MaxCpuMoe $MaxCpuMoe
}

# ---------------------------------------------------------------- Phase 2
if ($Phases -contains 2) {
    Write-Host "=== Phase 2: throughput at the surviving placements ==="
    $cells = Get-Content -Raw -LiteralPath (Join-Path $OutputRoot 'phase2-cells.json') | ConvertFrom-Json
    foreach ($cell in $cells) {
        if (-not (Test-Artifact $models[[string]$cell.model])) { continue }
        & $throughput -RuntimeRoot $RuntimeRoot -ModelPath $models[[string]$cell.model] `
            -Label ([string]$cell.label) -ContextTokens ([int]$cell.context) `
            -CacheTypeK ([string]$cell.ctk) -CacheTypeV ([string]$cell.ctv) `
            -NCpuMoe ([int]$cell.ncpumoe) -Repetitions 3 `
            -Tests @('pp:512', 'pp:8192', 'pp:32768', 'tg:128') `
            -DeviceVramMib $DeviceVramMib -OutputRoot (Join-Path $OutputRoot 'throughput')
    }
}

# ---------------------------------------------------------------- Phase 3
# The full census: every quantization against every context against both KV
# precisions, each swept over --n-cpu-moe and refined on the admission boundary.
# 18 groups here (3 x 3 x 2) against the Qwen3.6 campaign's 24, and measured the
# same way, so the two censuses can be read against each other cell by cell.
#
# The 32768/q4_0 cells Phase 1 already wrote are skipped rather than repeated -
# the census is idempotent on what is already on disk.
if ($Phases -contains 3) {
    Write-Host "=== Phase 3: full context x KV x placement census ==="
    & (Join-Path $OutputRoot 'Invoke-Ornith15Census.ps1') `
        -ModelRoot $ModelRoot -RuntimeRoot $RuntimeRoot -OutputRoot $OutputRoot `
        -DeviceVramMib $DeviceVramMib -Contexts @(32768, 131072, 262144) `
        -CacheTypes @('q4_0', 'q8_0') -MaxCpuMoe $MaxCpuMoe
}

# ---------------------------------------------------------------- Phase 4
if ($Phases -contains 4) {
    Write-Host "=== Phase 4: coding, tool-calling and JSON quality ==="
    $cells = Get-Content -Raw -LiteralPath (Join-Path $OutputRoot 'phase4-cells.json') | ConvertFrom-Json
    foreach ($cell in $cells) {
        if (-not (Test-Artifact $models[[string]$cell.model])) { continue }
        # Defaults for -SystemPolicy, -MaxTokens, -NPredict and -ReasoningBudget
        # on purpose: these are the settings the Qwen3.8 profiles and the Qwen3.6
        # campaign were graded under, and a quality comparison against them is
        # only a comparison if the harness is the same.
        & $qualify -RuntimeRoot $RuntimeRoot -ModelPath $models[[string]$cell.model] `
            -Label ([string]$cell.label) -ContextTokens ([int]$cell.context) `
            -CacheTypeK ([string]$cell.ctk) -CacheTypeV ([string]$cell.ctv) `
            -NCpuMoe ([int]$cell.ncpumoe) -Suites 'coding,tools,json' -CaptureDiagnostics `
            -DeviceVramMib $DeviceVramMib -OutputRoot (Join-Path $OutputRoot 'quality')
    }
}

# ---------------------------------------------------------------- Phase 5
if ($Phases -contains 5) {
    Write-Host "=== Phase 5: decode at occupancy, and retention ==="
    $cells = Get-Content -Raw -LiteralPath (Join-Path $OutputRoot 'phase5-cells.json') | ConvertFrom-Json
    foreach ($cell in $cells) {
        if (-not (Test-Artifact $models[[string]$cell.model])) { continue }
        & $throughput -RuntimeRoot $RuntimeRoot -ModelPath $models[[string]$cell.model] `
            -Label ([string]$cell.label) -ContextTokens ([int]$cell.context) `
            -CacheTypeK ([string]$cell.ctk) -CacheTypeV ([string]$cell.ctv) `
            -NCpuMoe ([int]$cell.ncpumoe) -Repetitions 2 `
            -Tests @([string[]]$cell.tests) `
            -DeviceVramMib $DeviceVramMib -OutputRoot (Join-Path $OutputRoot 'filled')
    }
}

# ---------------------------------------------------------------- Phase 6
# The MTP head is loaded whether or not it is used, so this phase asks what the
# 0.477 GB it occupies buys. Cells come from phase6-cells.json for the same
# reason as Phases 2-5: the placement has to be one Phase 1 showed can fit.
if ($Phases -contains 6) {
    Write-Host "=== Phase 6: MTP speculative decoding A/B ==="
    $cellsPath = Join-Path $OutputRoot 'phase6-cells.json'
    if (-not (Test-Path -LiteralPath $cellsPath)) {
        Write-Host "[skip] $cellsPath absent - select cells from Phase 1 first."
    }
    else {
        $cells = Get-Content -Raw -LiteralPath $cellsPath | ConvertFrom-Json
        foreach ($cell in $cells) {
            if (-not (Test-Artifact $models[[string]$cell.model])) { continue }
            & (Join-Path $OutputRoot 'Test-Ornith15MtpSpeculation.ps1') `
                -RuntimeRoot $RuntimeRoot -ModelPath $models[[string]$cell.model] `
                -Label ([string]$cell.label) -ContextTokens ([int]$cell.context) `
                -CacheTypeK ([string]$cell.ctk) -CacheTypeV ([string]$cell.ctv) `
                -NCpuMoe ([int]$cell.ncpumoe) -DraftNMax ([int[]]$cell.draft_n_max) `
                -DeviceVramMib $DeviceVramMib -OutputRoot (Join-Path $OutputRoot 'mtp')
        }
    }
}

# ---------------------------------------------------------------- Phase 7
# The full qualification, on the profile cells rather than on one 32k cell: a
# retention ramp only means something at the window the profile actually claims.
#
# Suite list and harness settings are the ones the 2026-08-23 campaign used on
# gemma4-12b, qwen38-deep-32k, qwen38-agent-128k and qwen38-huge-256k. They are
# not tuned per model here, because a score that needed its own harness is not a
# score that can be compared with theirs.
#
# Ordered cheapest window first. If the night runs out, what is finished is the
# part the replacement decision leans on hardest.
if ($Phases -contains 7) {
    Write-Host "=== Phase 7: full qualification with retention ramps ==="
    # Its own cell list, because Phase 5 and Phase 7 do not want the same set.
    # Phase 5 is a decode measurement and is cheap enough to run on every
    # (quant, window) profile. Phase 7 is a full suite plus a retention ramp -
    # the 262144 ramp alone measured 3.5 h on one model in the 2026-08-23
    # campaign - so running it on all nine would cost more than a night and buy
    # mostly duplicate answers. phase7-cells.json is the deliberate subset; if
    # it is absent, fall back to Phase 5's list rather than skipping the phase.
    $cellsPath = Join-Path $OutputRoot 'phase7-cells.json'
    if (-not (Test-Path -LiteralPath $cellsPath)) {
        $cellsPath = Join-Path $OutputRoot 'phase5-cells.json'
    }
    if (-not (Test-Path -LiteralPath $cellsPath)) {
        Write-Host "[skip] no phase7-cells.json or phase5-cells.json - profiles have not been selected yet."
    }
    else {
        $cells = @(Get-Content -Raw -LiteralPath $cellsPath | ConvertFrom-Json | Sort-Object { [int]$_.context })
        foreach ($cell in $cells) {
            if (-not (Test-Artifact $models[[string]$cell.model])) { continue }
            # The ramps the 2026-08-23 campaign used, kept verbatim per window so
            # the depths line up with the roster's existing retention numbers.
            $ramp = switch ([int]$cell.context) {
                32768 { @(4096, 10240, 16384, 22528, 28000) }
                131072 { @(16384, 40960, 65536, 90112, 120000) }
                262144 { @(32768, 81920, 131072, 180224, 240000) }
                default {
                    $c = [int]$cell.context
                    @([int]($c / 8), [int]($c / 4), [int]($c / 2), [int]($c * 0.75), [int]($c * 0.915))
                }
            }
            $label = ([string]$cell.label) -replace '^(?<m>[a-z0-9]+)-filled-', '${m}-qualify-'
            Write-Host ("--- $label  ctx $($cell.context)  ramp $($ramp -join ',')")
            & $qualify -RuntimeRoot $RuntimeRoot -ModelPath $models[[string]$cell.model] `
                -Label $label -ContextTokens ([int]$cell.context) `
                -CacheTypeK ([string]$cell.ctk) -CacheTypeV ([string]$cell.ctv) `
                -NCpuMoe ([int]$cell.ncpumoe) `
                -Suites 'chat,coding,hard,tools,literal_tools,json,retention' `
                -RetentionTokens $ramp -CaptureDiagnostics `
                -DeviceVramMib $DeviceVramMib -OutputRoot (Join-Path $OutputRoot 'qualification')
        }
    }
}

Write-Host "=== CAMPAIGN PHASES COMPLETE: $($Phases -join ',') ==="
