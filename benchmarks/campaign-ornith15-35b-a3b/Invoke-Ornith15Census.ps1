<#
.SYNOPSIS
The footprint census: every (quantization, context, KV precision) group, swept
over `--n-cpu-moe` until the admission boundary is crossed and then refined on
top of it.

.DESCRIPTION
This is the Ornith equivalent of what the Qwen3.6 campaign accumulated across
its Phase 1, Phase 3, placement-refinement and two-budget-search steps: 270
cells over 24 groups. Rather than transcribe that cell list - which was itself
the residue of several passes, and whose odd-numbered entries only exist because
a boundary happened to fall there - this sweeps the same space adaptively, so
the refinement lands wherever *this* model's boundary actually is.

Per group, the sweep is:

  1. `--n-cpu-moe` 0, 2, 4, ... up to -MaxCpuMoe.
  2. Stop early once -AdmissibleStreak consecutive cells are admissible.
     Footprint falls monotonically as expert layers move to the CPU, so cells
     past the boundary only re-measure a curve that is already established;
     the streak is what keeps the census at ~12 cells per group instead of 15
     and buys back roughly an hour across the campaign.
  3. Refine the boundary: measure the odd `--n-cpu-moe` values between the last
     rejected cell and the first admitted one. This is where a profile is
     chosen, so it is the one place resolution is worth paying for.

Admission uses the same two conditions as Select-Ornith15Cells.ps1, and for the
same reason: dedicated VRAM saturates rather than failing on this adapter, so
marginal *shared* memory is what separates a healthy cell from a paging one. The
thresholds are not re-derived here - they are read from the same defaults, so a
change to policy moves both.

File naming matches the Qwen3.6 campaign exactly, including the special case
that the 32768/q4_0 group is written as `-kvq4_0-` with no second cache token.
Select-Ornith15Cells.ps1 keys its Phase 1 regex on that shape.

.NOTES
Cells already on disk are skipped, so an interrupted census resumes. A cell that
fails to load is recorded and the sweep continues upward: failing at
`--n-cpu-moe 0` says nothing about whether the same model fits at 16.
#>
[CmdletBinding()]
param(
    [string]$ModelRoot = 'C:\IA\models\Ornith-1.5-35B-A3B-GGUF',
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [string]$OutputRoot = 'C:\IA\IA_local_server\benchmarks\campaign-ornith15-35b-a3b',
    [int]$DeviceVramMib = 16304,
    [string[]]$Models = @('q3kxl', 'iq3xxs', 'iq2m'),
    [int[]]$Contexts = @(32768, 131072, 262144),
    [string[]]$CacheTypes = @('q4_0', 'q8_0'),
    [int]$MaxCpuMoe = 28,
    [int]$Step = 2,
    # docs/MODEL_PROMOTION.md floor, plus the drift margin Select-*Cells.ps1
    # documents at length. Kept identical so census and selection agree.
    [int]$VramReserveMib = 1024,
    [int]$ReserveMarginMib = 512,
    [int]$SharedMarginMib = 400,
    [ValidateRange(1, 8)][int]$AdmissibleStreak = 3,
    [switch]$NoRefine,
    # Groups only. Use to resume a specific slice without re-walking the rest.
    [switch]$WhatIf
)

$ErrorActionPreference = 'Continue'
Set-StrictMode -Version Latest

$footprint = Join-Path 'C:\IA\IA_local_server\scripts\v2' 'Measure-V2ContextFootprint.ps1'
if (-not (Test-Path -LiteralPath $footprint)) { throw "Missing: $footprint" }

$artifacts = [ordered]@{
    q3kxl  = Join-Path $ModelRoot 'Ornith-1.5-35B-A3B-Q3_K_XL.gguf'
    iq3xxs = Join-Path $ModelRoot 'Ornith-1.5-35B-A3B-IQ3_XXS.gguf'
    iq2m   = Join-Path $ModelRoot 'Ornith-1.5-35B-A3B-IQ2_M.gguf'
}

function Get-CellPath {
    param([string]$Model, [int]$Context, [string]$Cache, [int]$N)
    # The 32768/q4_0 group carries the Phase 1 name, which has one cache token.
    # Every other group carries the Phase 3 name, which has two. This is not
    # cosmetic: Select-Ornith15Cells.ps1 matches Phase 1 by that exact shape.
    if ($Context -eq 32768 -and $Cache -eq 'q4_0') {
        return Join-Path $OutputRoot ("footprint-{0}-ctx32768-kvq4_0-ncpumoe{1}.json" -f $Model, $N)
    }
    return Join-Path $OutputRoot ("footprint-{0}-ctx{1}-kv{2}-{3}-ncpumoe{4}.json" -f $Model, $Context, $Cache, $Cache, $N)
}

function Test-Admissible {
    param([string]$Path)
    # Returns $null when the cell has no usable adapter sample, so the caller can
    # tell "not admissible" from "cannot say".
    #
    # A path that does not exist is one of those "cannot say" cases and it used
    # to pass silently. It is loud now, because the way this function fails when
    # handed a bad path is to quietly report every cell as inadmissible, which
    # reads as a model that fits nowhere rather than as a broken caller.
    if ([string]::IsNullOrWhiteSpace($Path)) {
        Write-Host '  (admissibility: empty path handed to Test-Admissible)'
        return $null
    }
    if (-not (Test-Path -LiteralPath $Path)) {
        Write-Host "  (admissibility: no cell file at $Path)"
        return $null
    }
    try { $r = Get-Content -Raw -LiteralPath $Path | ConvertFrom-Json } catch { return $null }
    if ($null -ne $r.failure) { return $false }
    if ($null -eq $r.peak -or $null -eq $r.peak.vram_dedicated_mib) { return $null }
    $headroom = $DeviceVramMib - [double]$r.peak.vram_dedicated_mib
    if ($headroom -lt ($VramReserveMib + $ReserveMarginMib)) { return $false }
    if ($null -ne $r.gpu_pressure -and [string]$r.gpu_pressure.state -eq 'pressured') { return $false }
    if ($null -ne $r.idle -and $null -ne $r.idle.vram_shared_mib -and $null -ne $r.peak.vram_shared_mib) {
        if (([double]$r.peak.vram_shared_mib - [double]$r.idle.vram_shared_mib) -gt $SharedMarginMib) { return $false }
    }
    return $true
}

function Invoke-Cell {
    param([string]$Model, [int]$Context, [string]$Cache, [int]$N)
    $out = Get-CellPath -Model $Model -Context $Context -Cache $Cache -N $N
    if (Test-Path -LiteralPath $out) {
        Write-Host ("[have] {0} ctx{1} kv{2} n{3}" -f $Model, $Context, $Cache, $N)
        return $out
    }
    if ($WhatIf) {
        Write-Host ("[plan] {0} ctx{1} kv{2} n{3}" -f $Model, $Context, $Cache, $N)
        return $null
    }
    Write-Host ("[cell] {0} ctx{1} kv{2} n{3}" -f $Model, $Context, $Cache, $N)
    # $null = on purpose. Measure-V2ContextFootprint.ps1 ends with a bare
    # `$report`, so it emits the whole report object regardless of -Quiet, and
    # an uncaptured call would make this function return @($report, $out)
    # instead of a path. Test-Admissible would then stringify that array into a
    # path that does not exist, return $null for every freshly measured cell,
    # and silently disable both the early stop and the boundary refinement -
    # which is exactly what happened to the Phase 1 sweep before this was
    # caught: 15 cells per group measured, zero refinement cells.
    $null = & $footprint -RuntimeRoot $RuntimeRoot -ModelPath $artifacts[$Model] `
        -ContextTokens $Context -CacheTypeK $Cache -CacheTypeV $Cache `
        -NCpuMoe $N -DeviceVramMib $DeviceVramMib -OutputPath $out -Quiet
    return $out
}

$planned = 0
$measured = 0
$groups = 0
$watch = [Diagnostics.Stopwatch]::StartNew()

foreach ($model in $Models) {
    if (-not (Test-Path -LiteralPath $artifacts[$model])) {
        Write-Host "[skip] absent artifact for $model"
        continue
    }
    foreach ($context in $Contexts) {
        foreach ($cache in $CacheTypes) {
            $groups++
            Write-Host ""
            Write-Host ("=== census group: {0} | ctx {1} | KV {2} ===" -f $model, $context, $cache)

            $streak = 0
            $lastRejected = $null
            $firstAdmitted = $null

            for ($n = 0; $n -le $MaxCpuMoe; $n += $Step) {
                $out = Invoke-Cell -Model $model -Context $context -Cache $cache -N $n
                $planned++
                if ($null -eq $out) { continue }
                if (-not $WhatIf) { $measured++ }

                $ok = Test-Admissible -Path $out
                if ($ok -eq $true) {
                    if ($null -eq $firstAdmitted) { $firstAdmitted = $n }
                    $streak++
                    if ($streak -ge $AdmissibleStreak) {
                        Write-Host ("[stop] {0} admissible cells in a row; group closed at n{1}" -f $streak, $n)
                        break
                    }
                }
                else {
                    # A rejected cell after an admitted one means the curve is
                    # not behaving monotonically - keep the streak honest rather
                    # than closing the group on a coincidence.
                    $streak = 0
                    $lastRejected = $n
                }
            }

            if (-not $NoRefine -and $null -ne $firstAdmitted -and $null -ne $lastRejected -and
                ($firstAdmitted - $lastRejected) -gt 1) {
                Write-Host ("--- refining boundary between n{0} (rejected) and n{1} (admitted) ---" -f $lastRejected, $firstAdmitted)
                for ($n = $lastRejected + 1; $n -lt $firstAdmitted; $n++) {
                    $null = Invoke-Cell -Model $model -Context $context -Cache $cache -N $n
                    $planned++
                    if (-not $WhatIf) { $measured++ }
                }
            }
        }
    }
}

$watch.Stop()
Write-Host ""
Write-Host ("=== census complete: {0} groups, {1} cells touched, {2} measured, {3:N1} min ===" -f `
        $groups, $planned, $measured, $watch.Elapsed.TotalMinutes)
