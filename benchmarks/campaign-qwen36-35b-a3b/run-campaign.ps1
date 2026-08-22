<#
Qwen3.6-35B-A3B on gfx1201 - physical campaign.

Ordering is by cost of discovering a failure, not by the order the report reads.

Phase 0 is the runtime gate: if b10549 cannot load `qwen35moe`, or loads it onto
the CPU, nothing after it is worth running and the campaign stops at a few
minutes rather than a few hours.

Phase 1 is MoE placement at one small context. This model is 84% expert FFN by
storage, so placement - not `gpu_layers` - is the lever that decides whether a
quantization fits at all. Load-only cells: one server start each, no prefill.

Phase 2 prices throughput only at placements Phase 1 showed can fit, because a
throughput cell on a configuration that pages is a measurement of the pagefile.

Phase 3 varies context and KV precision, Phase 4 asks whether the weights still
write correct code, Phase 5 measures decode once the window is actually full.

Every phase writes one JSON per cell, so an interrupted run is still evidence.
Run a single phase with -Phases 1,2.
#>
[CmdletBinding()]
param(
    [int[]]$Phases = @(0, 1, 2, 3, 4, 5),
    [string]$ModelRoot = 'C:\IA\models\Qwen3.6-35B-A3B-GGUF',
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [string]$OutputRoot = 'C:\IA\IA_local_server\benchmarks\campaign-qwen36-35b-a3b',
    [int[]]$MoeSweep = @(0, 2, 4, 6, 8, 12, 16, 20),
    [int]$DeviceVramMib = 16304
)

$ErrorActionPreference = 'Continue'
$scripts = 'C:\IA\IA_local_server\scripts\v2'
$footprint = Join-Path $scripts 'Measure-V2ContextFootprint.ps1'
$throughput = Join-Path $scripts 'Invoke-V2ThroughputSweep.ps1'
$qualify = Join-Path $scripts 'Invoke-V2ProfileQualification.ps1'

# No tensor override anywhere in this campaign. Qwen3.8's `blk.(6[0-3]).ffn_.*`
# pattern is meaningless here: this model has 40 blocks, and its FFN weight is
# in per-expert tensors that --n-cpu-moe places by layer.
$models = [ordered]@{
    q3kxl = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q3_K_XL.gguf'
    q2kxl = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q2_K_XL.gguf'
    iq4xs = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-IQ4_XS.gguf'
    q3ks  = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q3_K_S.gguf'
}

function Test-Artifact { param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { Write-Host "[skip] absent: $Path"; return $false }
    return $true
}

# ---------------------------------------------------------------- Phase 0
if ($Phases -contains 0) {
    Write-Host "=== Phase 0: runtime gate ==="
    & (Join-Path $OutputRoot 'Test-Qwen36RuntimeGate.ps1') `
        -RuntimeRoot $RuntimeRoot -OutputRoot $OutputRoot `
        -ModelPath $models['q3kxl'] -DeviceVramMib $DeviceVramMib
}

# ---------------------------------------------------------------- Phase 1
# 32768 / q4_0 is the cheapest context that still allocates a real KV cache, so
# the differences between cells are expert placement rather than cache size.
if ($Phases -contains 1) {
    Write-Host "=== Phase 1: MoE placement sweep, ctx 32768, KV q4_0/q4_0 ==="
    foreach ($id in $models.Keys) {
        if (-not (Test-Artifact $models[$id])) { continue }
        foreach ($n in $MoeSweep) {
            $out = Join-Path $OutputRoot ("footprint-{0}-ctx32768-kvq4_0-ncpumoe{1}.json" -f $id, $n)
            if (Test-Path -LiteralPath $out) { Write-Host "[have] $out"; continue }
            Write-Host "--- $id ncpumoe=$n"
            & $footprint -RuntimeRoot $RuntimeRoot -ModelPath $models[$id] `
                -ContextTokens 32768 -CacheTypeK q4_0 -CacheTypeV q4_0 `
                -NCpuMoe $n -DeviceVramMib $DeviceVramMib -OutputPath $out
        }
    }
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
if ($Phases -contains 3) {
    Write-Host "=== Phase 3: context and KV precision ==="
    $cells = Get-Content -Raw -LiteralPath (Join-Path $OutputRoot 'phase3-cells.json') | ConvertFrom-Json
    foreach ($cell in $cells) {
        if (-not (Test-Artifact $models[[string]$cell.model])) { continue }
        $out = Join-Path $OutputRoot ("footprint-{0}-ctx{1}-kv{2}-{3}-ncpumoe{4}.json" -f `
            $cell.model, $cell.context, $cell.ctk, $cell.ctv, $cell.ncpumoe)
        if (Test-Path -LiteralPath $out) { Write-Host "[have] $out"; continue }
        & $footprint -RuntimeRoot $RuntimeRoot -ModelPath $models[[string]$cell.model] `
            -ContextTokens ([int]$cell.context) -CacheTypeK ([string]$cell.ctk) `
            -CacheTypeV ([string]$cell.ctv) -NCpuMoe ([int]$cell.ncpumoe) `
            -DeviceVramMib $DeviceVramMib -OutputPath $out
    }
}

# ---------------------------------------------------------------- Phase 4
if ($Phases -contains 4) {
    Write-Host "=== Phase 4: coding, tool-calling and JSON quality ==="
    $cells = Get-Content -Raw -LiteralPath (Join-Path $OutputRoot 'phase4-cells.json') | ConvertFrom-Json
    foreach ($cell in $cells) {
        if (-not (Test-Artifact $models[[string]$cell.model])) { continue }
        # Defaults for -SystemPolicy, -MaxTokens, -NPredict and -ReasoningBudget
        # on purpose: these are the settings the Qwen3.8 profiles were graded
        # under, and a quality comparison against them is only a comparison if
        # the harness is the same. Any output contract this model turns out to
        # need is derived from what this run shows, not assumed before it.
        #
        # -CaptureDiagnostics writes the raw request/response for every probe, so
        # each capability claimed later can be traced to the exchange that
        # produced it.
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

Write-Host "=== CAMPAIGN PHASES COMPLETE: $($Phases -join ',') ==="
