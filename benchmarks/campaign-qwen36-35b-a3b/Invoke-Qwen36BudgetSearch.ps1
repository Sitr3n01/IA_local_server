<#
.SYNOPSIS
Finds the narrowest admissible MoE placement for each (weights, context, cache)
under a stated desktop allowance, measuring only the cells that are missing.

.DESCRIPTION
The campaign ships two profile sets because §8.9 measured the desktop holding
between 2474 and 4350 MiB of VRAM - a swing larger than the whole 1 GiB
admission reserve. A placement selected against a quiet desktop is not safe on a
busy one, and one selected against a busy desktop gives away throughput when the
machine is only serving the model.

Selection is on **marginal** VRAM plus a stated allowance, not on the raw
headroom a cell recorded:

    marginal + desktop_allowance + reserve <= device_vram

Raw headroom carries whatever desktop was present when that cell ran, so two
cells measured hours apart are not comparable on it. Marginal subtracts the idle
baseline taken immediately before the cell, and §7.5 measured it reproducing to
0.16% between independent runs.

That substitution has one precondition, and getting it wrong produced a false
conclusion earlier in this campaign: **marginal is only trustworthy for a cell
that is not spilling.** Once the driver satisfies part of an allocation from
shared memory, marginal dedicated *falls* while the configuration gets worse -
Q2_K_XL at 262144 reads 11878 MiB at `ncpumoe 8` on a quiet desktop and 10695
MiB at `ncpumoe 7` on a busy one, which looks like the wider split costing more.
It is not; the narrower one is spilling 1667 MiB. Cells failing the shared test
are therefore discarded before their marginal figure is read, never after.

Load-only cells. Existing measurements are reused, so re-running is cheap.
#>
[CmdletBinding()]
param(
    [string]$ModelRoot = 'C:\IA\models\Qwen3.6-35B-A3B-GGUF',
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [string]$OutputRoot = 'C:\IA\IA_local_server\benchmarks\campaign-qwen36-35b-a3b',
    [int]$DeviceVramMib = 16304,
    [int]$VramReserveMib = 1024,
    [int]$SharedMarginMib = 400,

    # Measured on this machine, not chosen. 4400 MiB is the per-process sum with
    # the usual applications open (Radeon Software 1858, Firefox 997, dwm 671,
    # Discord 254, explorer 163, and smaller clients); 2700 MiB is the highest
    # idle observed across the quiet Phase 1 sweep.
    [hashtable]$Budgets = @{ workstation = 4400; dedicated = 2700 },

    # Searched in ascending order; the first admissible count wins, because more
    # experts on the device is better for decode (§8.3).
    [int[]]$Placements = @(0, 2, 4, 6, 8, 10, 12, 14, 16, 18, 20, 22, 24, 26, 28),

    [object[]]$Targets = @(
        @{ context = 32768; ctk = 'q8_0'; ctv = 'q8_0' },
        @{ context = 32768; ctk = 'q4_0'; ctv = 'q4_0' },
        @{ context = 131072; ctk = 'q4_0'; ctv = 'q4_0' },
        @{ context = 262144; ctk = 'q4_0'; ctv = 'q4_0' }
    ),
    [string[]]$Models = @('q2kxl', 'q3ks', 'q3kxl', 'iq4xs')
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

# A named model whose file is not on disk is a caller error - a typo in -Models,
# or a -ModelRoot that did not survive the call - not a condition to work
# around. Skipping it silently is how one run of this script measured nothing,
# wrote an empty summary over 255 good rows, and still exited 0: invoked as
# `powershell -File`, which stringifies its arguments, -ModelRoot arrived as
# garbage and all four paths missed. Validate before spending GPU time, and name
# every missing file rather than stopping at the first.
$unknown = @($Models | Where-Object { -not $paths.ContainsKey($_) })
if ($unknown.Count -gt 0) {
    throw ("Unknown model id(s): {0}. Known ids: {1}" -f `
        ($unknown -join ', '), (($paths.Keys | Sort-Object) -join ', '))
}
$absent = @($Models | Where-Object { -not (Test-Path -LiteralPath $paths[$_]) })
if ($absent.Count -gt 0) {
    throw ("Model file(s) not found under '{0}': {1}" -f `
        $ModelRoot, (($absent | ForEach-Object { "$_ -> $($paths[$_])" }) -join '; '))
}

# The same call bug with a different symptom: a stringified -Targets arrives as
# one "System.Collections.Hashtable" per element, and those have no .context.
foreach ($t in $Targets) {
    if ($t -isnot [hashtable] -or -not $t.ContainsKey('context') -or
        -not $t.ContainsKey('ctk') -or -not $t.ContainsKey('ctv')) {
        throw ("Malformed target (needs context/ctk/ctv): '{0}'. Invoke this script with `&`, not with `powershell -File`." -f $t)
    }
}

$stray = @(Get-Process -Name 'llama-server', 'llama-bench' -ErrorAction SilentlyContinue)
if ($stray.Count -gt 0) {
    throw ("{0} llama process(es) already running (pids {1}); a footprint measured beside one is not valid." -f `
            $stray.Count, (($stray | ForEach-Object { $_.Id }) -join ', '))
}

function Get-CellPath {
    param([string]$Model, [int]$Context, [string]$Ctk, [string]$Ctv, [int]$N)
    # Phase 1 wrote the 32768/q4_0 sweep without the second cache type in the
    # name. Both spellings are accepted so those cells are reused rather than
    # re-measured.
    $withCtv = Join-Path $OutputRoot ("footprint-{0}-ctx{1}-kv{2}-{3}-ncpumoe{4}.json" -f $Model, $Context, $Ctk, $Ctv, $N)
    if (Test-Path -LiteralPath $withCtv) { return $withCtv }
    $legacy = Join-Path $OutputRoot ("footprint-{0}-ctx{1}-kv{2}-ncpumoe{3}.json" -f $Model, $Context, $Ctk, $N)
    if (Test-Path -LiteralPath $legacy) { return $legacy }
    return $withCtv
}

function Read-Cell {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    $r = Get-Content -Raw -LiteralPath $Path | ConvertFrom-Json
    if ($null -ne $r.failure) { return [pscustomobject]@{ usable = $false; reason = 'load failed' } }
    if ($null -eq $r.marginal_vram_mib) { return [pscustomobject]@{ usable = $false; reason = 'no adapter sample' } }
    $spill = $null
    if ($null -ne $r.idle -and $null -ne $r.idle.vram_shared_mib -and
        $null -ne $r.peak -and $null -ne $r.peak.vram_shared_mib) {
        $spill = [Math]::Round($r.peak.vram_shared_mib - $r.idle.vram_shared_mib, 1)
    }
    return [pscustomobject]@{
        usable   = $true
        marginal = [double]$r.marginal_vram_mib
        spill    = $spill
        idle     = $(if ($null -ne $r.idle) { $r.idle.vram_dedicated_mib } else { $null })
        commit   = $(if ($null -ne $r.peak) { $r.peak.process_private_gib } else { $null })
        ws       = $(if ($null -ne $r.peak) { $r.peak.process_ws_gib } else { $null })
    }
}

$results = [System.Collections.Generic.List[object]]::new()
$measured = 0
$reused = 0

foreach ($budgetName in ($Budgets.Keys | Sort-Object)) {
    $allowance = [int]$Budgets[$budgetName]
    $cap = $DeviceVramMib - $allowance - $VramReserveMib
    Write-Host ""
    Write-Host ("=== {0}: desktop {1} MiB + reserve {2} MiB -> marginal must be <= {3} MiB ===" -f `
            $budgetName, $allowance, $VramReserveMib, $cap)

    foreach ($model in $Models) {
        foreach ($target in $Targets) {
            $winner = $null
            $trace = [System.Collections.Generic.List[string]]::new()
            foreach ($n in $Placements) {
                $path = Get-CellPath -Model $model -Context ([int]$target.context) -Ctk ([string]$target.ctk) -Ctv ([string]$target.ctv) -N $n
                $cell = Read-Cell -Path $path
                if ($null -eq $cell) {
                    & $footprint -RuntimeRoot $RuntimeRoot -ModelPath $paths[$model] `
                        -ContextTokens ([int]$target.context) -CacheTypeK ([string]$target.ctk) `
                        -CacheTypeV ([string]$target.ctv) -NCpuMoe $n `
                        -DeviceVramMib $DeviceVramMib -OutputPath $path -Quiet
                    $measured++
                    $cell = Read-Cell -Path $path
                }
                else { $reused++ }
                if ($null -eq $cell -or -not $cell.usable) { $trace.Add("n${n}:unusable"); continue }
                if ($null -ne $cell.spill -and $cell.spill -gt $SharedMarginMib) {
                    $trace.Add(("n{0}:spill{1:N0}" -f $n, $cell.spill)); continue
                }
                $trace.Add(("n{0}:{1:N0}" -f $n, $cell.marginal))
                if ($cell.marginal -le $cap) { $winner = [pscustomobject]@{ n = $n; cell = $cell }; break }
            }
            $row = [ordered]@{
                budget = $budgetName; model = $model
                context = [int]$target.context; ctk = [string]$target.ctk; ctv = [string]$target.ctv
                ncpumoe = $(if ($winner) { $winner.n } else { $null })
                marginal_mib = $(if ($winner) { $winner.cell.marginal } else { $null })
                spill_mib = $(if ($winner) { $winner.cell.spill } else { $null })
                commit_gib = $(if ($winner) { $winner.cell.commit } else { $null })
                ws_gib = $(if ($winner) { $winner.cell.ws } else { $null })
                trace = ($trace -join ' ')
            }
            $results.Add($row)
            Write-Host ("  {0,-7} ctx {1,-7} kv {2,-5} -> {3,-8} {4}" -f `
                    $model, $target.context, $target.ctk,
                    $(if ($winner) { "n=$($winner.n)" } else { 'NONE' }), ($trace -join ' '))
        }
    }
}

$outPath = Join-Path $OutputRoot 'summary-budget-search.json'

# Never overwrite a good summary with an empty one. This file is derived - the
# footprint cells are the primary record - but `"results": []` beside
# `cells_measured: 0` reads as "searched, found nothing" rather than "did not
# search", and that is the more expensive of the two lies.
if ($results.Count -eq 0) {
    throw "Search produced no rows; refusing to overwrite $outPath. Check -Models and -Targets."
}

[ordered]@{
    schema_version   = 1
    scenario         = 'qwen36-budget-search'
    generated_utc    = [DateTime]::UtcNow.ToString('o')
    device_vram_mib  = $DeviceVramMib
    vram_reserve_mib = $VramReserveMib
    shared_margin_mib = $SharedMarginMib
    budgets          = $Budgets
    cells_measured   = $measured
    cells_reused     = $reused
    results          = @($results)
} | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $outPath -Encoding UTF8

Write-Host ""
Write-Host ("measured {0} new cells, reused {1}" -f $measured, $reused)
Write-Host "wrote $outPath"
