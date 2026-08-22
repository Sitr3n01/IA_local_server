<#
.SYNOPSIS
Narrows each placement threshold from +-4 layers to exact, one layer at a time.

.DESCRIPTION
Phases 1 and 3 stepped `--n-cpu-moe` in fours, which is the right resolution for
finding where a quantization stops paging but the wrong one for shipping a
profile. Three of the 262144 thresholds sit within 300 MiB of the 1 GiB reserve
at the step below the one that passed - Q3_K_XL at `ncpumoe 16` leaves 927.7 MiB,
96 MiB short - so the true minimum is somewhere inside an unmeasured gap.

That gap is not free. Section 8.3 of the report measures decode falling with
every layer moved to the host: 14% across four layers for IQ4_XS, and 36% across
six for Q2_K_XL. Shipping the coarse figure spends throughput that a finer sweep
recovers.

Admissibility here is the reserve *plus* a margin, not the bare reserve. Q3_K_S
at `ncpumoe 8` cleared 1024 MiB by 106 MiB and then measured 672.59 and 397.78
t/s at pp512 on two runs of the same configuration; the desktop's idle shared
memory moved 41 MiB between them. Resolving a boundary against the bare floor
would hand back exactly that kind of cell, so the windows below extend past the
coarse answer rather than stopping at it.

Load-only cells, so a boundary costs a few minutes rather than a benchmark.
Cells that already exist are skipped, and the search for a boundary stops at the
first layer count that satisfies every test - going wider only confirms what is
already known.
#>
[CmdletBinding()]
param(
    [string]$ModelRoot = 'C:\IA\models\Qwen3.6-35B-A3B-GGUF',
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [string]$OutputRoot = 'C:\IA\IA_local_server\benchmarks\campaign-qwen36-35b-a3b',
    [int]$DeviceVramMib = 16304,
    [int]$VramReserveMib = 1024,
    [int]$SharedMarginMib = 400,

    # Margin over the reserve. See Select-Qwen36Cells.ps1 for the measurement
    # behind it: Q3_K_S at cpu_moe 8 cleared the reserve by 106 MiB and then
    # measured 41% apart on two runs of the same configuration. A boundary
    # resolved against the bare floor would hand back exactly that kind of cell,
    # which is the opposite of what refining it is for.
    [int]$ReserveMarginMib = 512,

    # `coarse` is what the step-of-four sweep answered under the bare reserve.
    # The window runs from below it to past it, because the stricter rule can
    # land either side: lower if the coarse step overshot, higher if the coarse
    # answer itself has too thin a margin.
    [object[]]$Boundaries = @(
        @{ model = 'q2kxl'; context = 262144; ctk = 'q4_0'; ctv = 'q4_0'; coarse = 8; from = 5; to = 10 },
        @{ model = 'q3ks'; context = 262144; ctk = 'q4_0'; ctv = 'q4_0'; coarse = 16; from = 13; to = 18 },
        @{ model = 'q3kxl'; context = 262144; ctk = 'q4_0'; ctv = 'q4_0'; coarse = 20; from = 17; to = 22 },
        @{ model = 'iq4xs'; context = 262144; ctk = 'q4_0'; ctv = 'q4_0'; coarse = 20; from = 17; to = 22 },
        @{ model = 'q3kxl'; context = 131072; ctk = 'q4_0'; ctv = 'q4_0'; coarse = 16; from = 13; to = 18 },
        @{ model = 'q3ks'; context = 131072; ctk = 'q4_0'; ctv = 'q4_0'; coarse = 12; from = 9; to = 14 },
        @{ model = 'q2kxl'; context = 131072; ctk = 'q4_0'; ctv = 'q4_0'; coarse = 4; from = 1; to = 6 },
        @{ model = 'q3kxl'; context = 32768; ctk = 'q4_0'; ctv = 'q4_0'; coarse = 12; from = 9; to = 14 },
        @{ model = 'iq4xs'; context = 32768; ctk = 'q4_0'; ctv = 'q4_0'; coarse = 16; from = 13; to = 18 },
        @{ model = 'q3ks'; context = 32768; ctk = 'q4_0'; ctv = 'q4_0'; coarse = 8; from = 5; to = 12 }
    )
)

$ErrorActionPreference = 'Continue'
Set-StrictMode -Version Latest

$footprint = 'C:\IA\IA_local_server\scripts\v2\Measure-V2ContextFootprint.ps1'
if (-not (Test-Path -LiteralPath $footprint)) { throw "Missing $footprint" }

$paths = @{
    q3kxl = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q3_K_XL.gguf'
    q2kxl = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q2_K_XL.gguf'
    iq4xs = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-IQ4_XS.gguf'
    q3ks  = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q3_K_S.gguf'
}

function Test-CellAdmissible {
    param([string]$Path)
    $report = Get-Content -Raw -LiteralPath $Path | ConvertFrom-Json
    if ($null -ne $report.failure) { return $false }
    if ($null -eq $report.peak -or $null -eq $report.peak.vram_dedicated_mib) { return $false }
    $headroom = $DeviceVramMib - $report.peak.vram_dedicated_mib
    if ($headroom -lt ($VramReserveMib + $ReserveMarginMib)) { return $false }
    if ($null -ne $report.gpu_pressure -and [string]$report.gpu_pressure.state -eq 'pressured') { return $false }
    if ($null -ne $report.idle -and $null -ne $report.idle.vram_shared_mib -and $null -ne $report.peak.vram_shared_mib) {
        if (($report.peak.vram_shared_mib - $report.idle.vram_shared_mib) -gt $SharedMarginMib) { return $false }
    }
    return $true
}

# See Invoke-Qwen36BudgetSearch.ps1: a boundary naming a model that is not on
# disk is a caller error, and skipping it produces a summary that looks like a
# completed search of a shorter list.
$missing = @($Boundaries | ForEach-Object { [string]$_.model } | Sort-Object -Unique |
    Where-Object { -not $paths.ContainsKey($_) -or -not (Test-Path -LiteralPath $paths[$_]) })
if ($missing.Count -gt 0) {
    throw ("Boundary model(s) not found under '{0}': {1}" -f $ModelRoot, ($missing -join ', '))
}

$found = [System.Collections.Generic.List[object]]::new()
foreach ($boundary in $Boundaries) {
    $model = [string]$boundary.model
    Write-Host ("=== {0} ctx {1} kv {2}/{3}, testing {4}..{5} ===" -f `
            $model, $boundary.context, $boundary.ctk, $boundary.ctv, $boundary.from, $boundary.to)

    $winner = $null
    foreach ($n in [int]$boundary.from..[int]$boundary.to) {
        $out = Join-Path $OutputRoot ("footprint-{0}-ctx{1}-kv{2}-{3}-ncpumoe{4}.json" -f `
                $model, $boundary.context, $boundary.ctk, $boundary.ctv, $n)
        if (-not (Test-Path -LiteralPath $out)) {
            & $footprint -RuntimeRoot $RuntimeRoot -ModelPath $paths[$model] `
                -ContextTokens ([int]$boundary.context) -CacheTypeK ([string]$boundary.ctk) `
                -CacheTypeV ([string]$boundary.ctv) -NCpuMoe $n `
                -DeviceVramMib $DeviceVramMib -OutputPath $out -Quiet
        }
        if (-not (Test-Path -LiteralPath $out)) { Write-Host "  n=$n  NO OUTPUT"; continue }
        $ok = Test-CellAdmissible -Path $out
        Write-Host ("  n={0,-3} admissible={1}" -f $n, $ok)
        if ($ok) { $winner = $n; break }
    }

    $found.Add([ordered]@{
            model = $model; context = [int]$boundary.context
            ctk = [string]$boundary.ctk; ctv = [string]$boundary.ctv
            coarse_answer = [int]$boundary.coarse
            refined_answer = $winner
            layers_recovered = $(if ($null -ne $winner) { [int]$boundary.coarse - $winner } else { $null })
        })
}

Write-Host ''
Write-Host ("{0,-8} {1,8} {2,8} {3,9} {4}" -f 'model', 'ctx', 'coarse', 'refined', 'layers recovered')
foreach ($row in $found) {
    Write-Host ("{0,-8} {1,8} {2,8} {3,9} {4}" -f `
            $row.model, $row.context, $row.coarse_answer,
            $(if ($null -ne $row.refined_answer) { $row.refined_answer } else { 'none' }),
            $row.layers_recovered)
}

$outPath = Join-Path $OutputRoot 'summary-placement-refinement.json'
[ordered]@{
    schema_version   = 1
    scenario         = 'qwen36-placement-refinement'
    generated_utc    = [DateTime]::UtcNow.ToString('o')
    device_vram_mib  = $DeviceVramMib
    vram_reserve_mib = $VramReserveMib
    reserve_margin_mib = $ReserveMarginMib
    shared_margin_mib = $SharedMarginMib
    boundaries       = @($found)
} | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $outPath -Encoding UTF8
Write-Host "wrote $outPath"
