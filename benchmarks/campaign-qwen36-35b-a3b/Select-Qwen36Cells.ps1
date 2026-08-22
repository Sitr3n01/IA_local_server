<#
.SYNOPSIS
Turns the Phase 1 placement sweep into the cell lists the later phases run.

.DESCRIPTION
The point of separating this from `run-campaign.ps1` is that the later phases
must not be able to run on a configuration Phase 1 already showed cannot fit.
Selection is therefore a function of measurements on disk, and it is written
down as JSON so a reader can see which cells were chosen and, from the sweep
files next to them, which were rejected and why.

Admission reserves come from `docs/MODEL_PROMOTION.md` and are applied here
rather than at the end: a cell that loads while leaving under 1 GiB of dedicated
VRAM free is not a workstation profile, and spending an hour of prefill on it
would only produce a number that cannot be shipped.
#>
[CmdletBinding()]
param(
    [string]$OutputRoot = 'C:\IA\IA_local_server\benchmarks\campaign-qwen36-35b-a3b',
    [int]$DeviceVramMib = 16304,
    # docs/MODEL_PROMOTION.md: at least 1 GiB dedicated VRAM must remain free.
    [int]$VramReserveMib = 1024,
    # Above this the prefill cliff measured in REPORT-qwen38-27b-q3-q2-kvq4 is in
    # play. Cells beyond it are kept but flagged, never silently dropped.
    [double]$OccupancyWarnPercent = 95.0,

    [ValidateRange(2, 16)]
    [int]$MaxThroughputCellsPerModel = 4,

    # Shared GPU memory a cell may hold above the idle baseline before it counts
    # as paging. Set between the two clusters this campaign measured - healthy
    # cells sit at ~100 MiB, paging ones at 880 MiB and above - so the threshold
    # is not near any observation.
    [int]$SharedMarginMib = 400,

    # How far a cell must clear the VRAM reserve, on top of clearing it.
    #
    # The reserve is a floor. It is not a margin, and treating it as one produced
    # the one non-reproducible result in this campaign: Q3_K_S at cpu_moe 8
    # cleared 1024 MiB by 106 MiB, and measured 672.59 t/s at pp512 on one run
    # and 397.78 t/s on another - same GGUF, same flags, 41% apart. Between the
    # two runs the desktop's idle shared memory moved by 41 MiB, which was enough
    # to carry a 96%-occupancy configuration to 98% and double its spill.
    #
    # 512 MiB is set from the drift actually observed here: idle dedicated
    # ranged 2474-2700 MiB across the placement sweep and 152 MiB during the
    # runtime gate, so the desktop alone accounts for several hundred MiB. A cell
    # whose margin is smaller than that is not a configuration, it is a coin
    # flip. REPORT-qwen38-27b-q3-q2-kvq4 section 8.1 records the same failure on
    # a different model - 956 versus 281 t/s on one GGUF - which is why this is
    # treated as structural rather than as one bad cell.
    [int]$ReserveMarginMib = 512
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$rows = [System.Collections.Generic.List[object]]::new()
foreach ($file in Get-ChildItem -LiteralPath $OutputRoot -Filter 'footprint-*-ctx32768-kvq4_0-ncpumoe*.json' -File) {
    $report = Get-Content -Raw -LiteralPath $file.FullName | ConvertFrom-Json
    if ($file.BaseName -notmatch '^footprint-(?<model>[a-z0-9]+)-ctx32768-kvq4_0-ncpumoe(?<n>\d+)$') { continue }
    $dedicated = $null
    if ($null -ne $report.peak -and $null -ne $report.peak.vram_dedicated_mib) { $dedicated = [double]$report.peak.vram_dedicated_mib }
    $rows.Add([pscustomobject]@{
            model         = $Matches['model']
            ncpumoe       = [int]$Matches['n']
            loaded        = ($null -eq $report.failure)
            failure       = $report.failure
            load_seconds  = $report.load_seconds
            dedicated_mib = $dedicated
            marginal_mib  = $report.marginal_vram_mib
            shared_mib    = $(if ($null -ne $report.peak) { $report.peak.vram_shared_mib } else { $null })
            commit_gib    = $(if ($null -ne $report.peak) { $report.peak.process_private_gib } else { $null })
            ws_gib        = $(if ($null -ne $report.peak) { $report.peak.process_ws_gib } else { $null })
            headroom_mib  = $(if ($null -ne $dedicated) { [Math]::Round($DeviceVramMib - $dedicated, 1) } else { $null })
            occupancy_pct = $(if ($null -ne $dedicated) { [Math]::Round(100.0 * $dedicated / $DeviceVramMib, 1) } else { $null })
            idle_vram_mib = $(if ($null -ne $report.idle) { $report.idle.vram_dedicated_mib } else { $null })
            pressure      = $(if ($null -ne $report.gpu_pressure) { [string]$report.gpu_pressure.state } else { $null })
            # Shared GPU memory above what the desktop already held. This is the
            # model's own spill, and it is the cleanest paging signal measured in
            # this campaign - see the comment on $admissible below.
            shared_margin_mib = $(
                if ($null -ne $report.idle -and $null -ne $report.idle.vram_shared_mib -and
                    $null -ne $report.peak -and $null -ne $report.peak.vram_shared_mib) {
                    [Math]::Round($report.peak.vram_shared_mib - $report.idle.vram_shared_mib, 1)
                } else { $null })
        })
}

if ($rows.Count -eq 0) { throw "No Phase 1 footprint files in $OutputRoot; run Phase 1 first." }

# Two conditions, because on this adapter neither alone separates a healthy
# configuration from a paging one.
#
# Dedicated VRAM is the wrong discriminator by itself: the AMD driver satisfies
# an oversized request partly from dedicated and partly from shared rather than
# failing it, so dedicated saturates near the card's size and stops moving while
# the configuration gets steadily worse. Measured here at ctx 32768: Q3_K_XL
# reports ~15.9 GiB dedicated at every one of cpu_moe 0, 2 and 4, while shared
# falls 3147 -> 2491 -> 1927 MiB. Only the second column is tracking the change.
#
# Shared alone is also not enough, because the desktop holds some at all times.
# ADR 0011's classifier combines them and is applied here rather than restated.
#
# It is not sufficient on its own for this model, and the campaign measured why.
# The classifier reaches 'pressured' only when occupancy >= 95% AND shared >=
# 1024 MiB, and 'elevated' on occupancy alone - shared never raises a verdict by
# itself. On Qwen3.8 the two moved together. Here they do not: heavy MoE offload
# lowers occupancy while the remainder still spills, so a cell can page with
# occupancy under the threshold.
#
# Marginal shared memory - peak minus the idle baseline taken immediately before
# the cell - separates the 50 cells in this campaign cleanly, with no overlap:
#
#   healthy configurations   ~100 MiB
#   paging configurations    880 - 3720 MiB
#
# One cell disagrees with the classifier because of it: q3ks at 131072 / q8_0 /
# cpu_moe 8 spills 1515 MiB of shared memory at 93.6% occupancy and is reported
# 'ok'. Admitting it would have priced a paging configuration as a profile.
#
# This rule is applied campaign-side and is deliberately NOT proposed for
# internal/edge/gpumemory.go: the edge samples a running adapter and has no idle
# baseline to subtract, so it cannot compute this quantity. Recalibrating the
# edge's own thresholds is a separate question on separate evidence.
$admissible = @($rows | Where-Object {
        $_.loaded -and $null -ne $_.headroom_mib -and
        $_.headroom_mib -ge ($VramReserveMib + $ReserveMarginMib) -and
        $_.pressure -ne 'pressured' -and
        ($null -eq $_.shared_margin_mib -or $_.shared_margin_mib -le $SharedMarginMib)
    })

# The smallest CPU-MoE count that still leaves the reserve. Fewer CPU layers
# means more expert weight on the GPU, which is the direction that helps decode;
# the sweep is what says how far that can be pushed per quantization.
$chosen = [ordered]@{}
foreach ($model in ($admissible | Select-Object -ExpandProperty model -Unique)) {
    $best = @($admissible | Where-Object { $_.model -eq $model } | Sort-Object ncpumoe) | Select-Object -First 1
    if ($best) { $chosen[$model] = $best }
}

$format = "{0,-7} {1,7} {2,10} {3,9} {4,6} {5,8} {6,10} {7,9} {8,-10} {9}"
Write-Host ($format -f 'model', 'ncpumoe', 'dedicated', 'headroom', 'occ%', 'shared', 'shrdMargin', 'marginal', 'pressure', 'note')
foreach ($row in ($rows | Sort-Object model, ncpumoe)) {
    $note = ''
    if (-not $row.loaded) { $note = 'LOAD FAILED' }
    elseif ($null -eq $row.headroom_mib) { $note = 'no adapter sample' }
    elseif ($row.headroom_mib -lt $VramReserveMib) { $note = 'rejected: under VRAM reserve' }
    elseif ($row.pressure -eq 'pressured') { $note = 'rejected: adapter paging' }
    elseif ($null -ne $row.shared_margin_mib -and $row.shared_margin_mib -gt $SharedMarginMib) {
        $note = 'rejected: spills shared memory (classifier says ok)'
    }
    elseif ($row.occupancy_pct -ge $OccupancyWarnPercent) { $note = 'above occupancy warn line' }
    if ($chosen.Contains($row.model) -and $chosen[$row.model].ncpumoe -eq $row.ncpumoe) { $note = ('SELECTED. ' + $note).Trim() }
    Write-Host ($format -f $row.model, $row.ncpumoe, $row.dedicated_mib, $row.headroom_mib, `
            $row.occupancy_pct, $row.shared_mib, $row.shared_margin_mib, $row.marginal_mib, $row.pressure, $note)
}

function Write-Cells {
    param([string]$Name, [object]$Value)
    $path = Join-Path $OutputRoot $Name
    ($Value | ConvertTo-Json -Depth 8) | Set-Content -LiteralPath $path -Encoding UTF8
    Write-Host "wrote $path"
}

# Phase 2 measures each admissible placement, not only the selected one: the
# question "how many MoE layers should stay on CPU" is answered by the shape of
# the throughput curve, and one point has no shape.
#
# Subsampled to at most $MaxThroughputCellsPerModel per model. A pp32768 cell is
# minutes, not seconds, so measuring eight placements for a quantization whose
# whole admissible range fits on the card buys resolution the decision does not
# use. The narrowest admissible split is always kept, because that is the one
# with the most expert weight on the GPU and therefore the candidate; the rest
# are spread across the range so the curve still has a shape.
$phase2 = [System.Collections.Generic.List[object]]::new()
foreach ($model in ($admissible | Select-Object -ExpandProperty model -Unique)) {
    $forModel = @($admissible | Where-Object { $_.model -eq $model } | Sort-Object ncpumoe)
    $picked = $forModel
    if ($forModel.Count -gt $MaxThroughputCellsPerModel) {
        $step = [Math]::Ceiling(($forModel.Count - 1) / [double]($MaxThroughputCellsPerModel - 1))
        $indexes = @(0)
        for ($i = $step; $i -lt $forModel.Count; $i += $step) { $indexes += [int]$i }
        if ($indexes[-1] -ne $forModel.Count - 1) { $indexes += $forModel.Count - 1 }
        $picked = @($indexes | Select-Object -Unique | ForEach-Object { $forModel[$_] })
    }
    foreach ($row in $picked) {
        $phase2.Add([ordered]@{ model = $row.model; label = ("{0}-ctx32768-kvq4-ncpumoe{1}" -f $row.model, $row.ncpumoe)
                context = 32768; ctk = 'q4_0'; ctv = 'q4_0'; ncpumoe = $row.ncpumoe; contrast = $false })
    }
}

# One rejected cell per model as a contrast, deliberately. The reserve and the
# pressure classifier are policy; what an operator actually needs to see is what
# the policy is buying, and that only appears when a paging configuration is
# measured next to a healthy one. Chosen as the widest rejected split, which is
# the closest one to the boundary and therefore the least strawman.
foreach ($model in ($rows | Select-Object -ExpandProperty model -Unique)) {
    $rejected = @($rows | Where-Object { $_.model -eq $model -and $_.loaded } |
            Where-Object { $_.pressure -eq 'pressured' -or ($null -ne $_.headroom_mib -and $_.headroom_mib -lt $VramReserveMib) } |
            Sort-Object ncpumoe -Descending) | Select-Object -First 1
    if ($rejected) {
        $phase2.Add([ordered]@{ model = $model; label = ("{0}-ctx32768-kvq4-ncpumoe{1}" -f $model, $rejected.ncpumoe)
                context = 32768; ctk = 'q4_0'; ctv = 'q4_0'; ncpumoe = $rejected.ncpumoe; contrast = $true })
    }
}
Write-Cells 'phase2-cells.json' @($phase2)

# Phase 3 varies context and KV precision at each model's selected placement,
# plus a wider placement at the largest window because the KV cache competes
# with the experts for the same card.
$phase3 = [System.Collections.Generic.List[object]]::new()
foreach ($model in $chosen.Keys) {
    $n = $chosen[$model].ncpumoe
    foreach ($cell in @(
            @{ ctx = 32768; ctk = 'q8_0'; ctv = 'q8_0'; n = $n },
            @{ ctx = 32768; ctk = 'q4_0'; ctv = 'q4_0'; n = $n },
            @{ ctx = 131072; ctk = 'q8_0'; ctv = 'q8_0'; n = $n },
            @{ ctx = 131072; ctk = 'q4_0'; ctv = 'q4_0'; n = $n },
            # The cache competes with the experts for the same card, so the
            # placement that just fits at 32k need not fit at 128k. Carrying only
            # the 32k winner forward would report "does not fit" for a context
            # that fits perfectly well four layers wider.
            @{ ctx = 131072; ctk = 'q4_0'; ctv = 'q4_0'; n = [Math]::Min($n + 4, 40) },
            @{ ctx = 262144; ctk = 'q4_0'; ctv = 'q4_0'; n = $n },
            @{ ctx = 262144; ctk = 'q4_0'; ctv = 'q4_0'; n = [Math]::Min($n + 4, 40) },
            @{ ctx = 262144; ctk = 'q4_0'; ctv = 'q4_0'; n = [Math]::Min($n + 8, 40) }
        )) {
        $phase3.Add([ordered]@{ model = $model; context = $cell.ctx; ctk = $cell.ctk; ctv = $cell.ctv; ncpumoe = $cell.n })
    }
}
Write-Cells 'phase3-cells.json' @($phase3)

Write-Host ''
Write-Host 'phase4-cells.json and phase5-cells.json are written by Select-Qwen36Profiles.ps1,'
Write-Host 'which needs the Phase 2 throughput and the Phase 3 context results.'

$summary = [ordered]@{
    schema_version   = 1
    scenario         = 'qwen36-placement-selection'
    generated_utc    = [DateTime]::UtcNow.ToString('o')
    device_vram_mib  = $DeviceVramMib
    vram_reserve_mib = $VramReserveMib
    rows             = @($rows | Sort-Object model, ncpumoe)
    selected         = $chosen
}
($summary | ConvertTo-Json -Depth 10) | Set-Content -LiteralPath (Join-Path $OutputRoot 'summary-placement-ctx32768-kvq4_0.json') -Encoding UTF8
Write-Host ("wrote " + (Join-Path $OutputRoot 'summary-placement-ctx32768-kvq4_0.json'))
