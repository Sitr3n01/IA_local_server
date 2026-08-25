<#
.SYNOPSIS
Turns the Phase 2 throughput sweep and the Phase 3 context matrix into the
quality (Phase 4) and filled-context (Phase 5) cell lists.

.DESCRIPTION
Phases 4 and 5 are the expensive ones - a quality pass is tens of generations
and a filled-context decode prefills the whole window - so what they run has to
be decided by the cheap phases rather than by a guess written in advance.

Three candidate purposes are proposed here, and each is only proposed if the
measurements support it:

- Deep: the highest-quality weights that still hold a 32k window with q8_0 cache
  inside the VRAM reserve.
- Agent: the best decode-per-byte point that holds 128k with q4_0.
- Huge: whatever holds 262144 with q4_0 at all.

If no cell satisfies one of them, that class is not emitted. A profile exists
because a measurement supports it, not so the set has three entries.
#>
[CmdletBinding()]
param(
    [string]$OutputRoot = 'C:\IA\IA_local_server\benchmarks\campaign-ornith15-35b-a3b',
    [int]$DeviceVramMib = 16304,
    [int]$VramReserveMib = 1024,
    [int]$SharedMarginMib = 400,
    # See the comment on this parameter in Select-Qwen36Cells.ps1: the reserve is
    # a floor, and a cell clearing it by less than the desktop's own drift did
    # not reproduce.
    [int]$ReserveMarginMib = 512
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# ---- Phase 2: decode and prefill per placement -----------------------------
$throughput = [System.Collections.Generic.List[object]]::new()
$throughputRoot = Join-Path $OutputRoot 'throughput'
if (Test-Path -LiteralPath $throughputRoot) {
    foreach ($file in Get-ChildItem -LiteralPath $throughputRoot -Filter 'throughput-*.json' -File) {
        $report = Get-Content -Raw -LiteralPath $file.FullName | ConvertFrom-Json
        if ($file.BaseName -notmatch 'throughput-(?<model>[a-z0-9]+)-ctx(?<ctx>\d+)-kvq4-ncpumoe(?<n>\d+)') { continue }
        # Rows are keyed by kind+tokens+depth, the same three fields the runner
        # builds its tag from. "pp512" here means depth 0; a depth-qualified row
        # is a different measurement and must not collapse onto it.
        $byTest = @{}
        foreach ($row in @($report.results)) {
            if ($null -eq $row.tokens_per_second -or [int]$row.depth -ne 0) { continue }
            $byTest[("{0}{1}" -f $row.kind, $row.n)] = [double]$row.tokens_per_second
        }
        $throughput.Add([pscustomobject]@{
                model   = $Matches['model']
                ncpumoe = [int]$Matches['n']
                pp512   = $(if ($byTest.ContainsKey('pp512')) { $byTest['pp512'] } else { $null })
                pp8192  = $(if ($byTest.ContainsKey('pp8192')) { $byTest['pp8192'] } else { $null })
                pp32768 = $(if ($byTest.ContainsKey('pp32768')) { $byTest['pp32768'] } else { $null })
                tg128   = $(if ($byTest.ContainsKey('tg128')) { $byTest['tg128'] } else { $null })
            })
    }
}

# ---- Phase 3: which (model, context, KV, placement) cells actually fit ------
$fit = [System.Collections.Generic.List[object]]::new()
foreach ($file in Get-ChildItem -LiteralPath $OutputRoot -Filter 'footprint-*.json' -File) {
    if ($file.BaseName -notmatch '^footprint-(?<model>[a-z0-9]+)-ctx(?<ctx>\d+)-kv(?<ctk>[a-z0-9_]+)-(?<ctv>[a-z0-9_]+)-ncpumoe(?<n>\d+)$' -and
        $file.BaseName -notmatch '^footprint-(?<model>[a-z0-9]+)-ctx(?<ctx>\d+)-kv(?<ctk>[a-z0-9_]+)-ncpumoe(?<n>\d+)$') { continue }
    $report = Get-Content -Raw -LiteralPath $file.FullName | ConvertFrom-Json
    $dedicated = $null
    if ($null -ne $report.peak -and $null -ne $report.peak.vram_dedicated_mib) { $dedicated = [double]$report.peak.vram_dedicated_mib }
    $ctk = [string]$report.configuration.cache_type_k
    $ctv = [string]$report.configuration.cache_type_v
    $fit.Add([pscustomobject]@{
            model         = $Matches['model']
            context       = [int]$report.configuration.context_tokens
            ctk           = $ctk
            ctv           = $ctv
            ncpumoe       = [int]$Matches['n']
            loaded        = ($null -eq $report.failure)
            dedicated_mib = $dedicated
            headroom_mib  = $(if ($null -ne $dedicated) { [Math]::Round($DeviceVramMib - $dedicated, 1) } else { $null })
            commit_gib    = $(if ($null -ne $report.peak) { $report.peak.process_private_gib } else { $null })
            ws_gib        = $(if ($null -ne $report.peak) { $report.peak.process_ws_gib } else { $null })
            pressure      = $(if ($null -ne $report.gpu_pressure) { [string]$report.gpu_pressure.state } else { $null })
            shared_margin_mib = $(
                if ($null -ne $report.idle -and $null -ne $report.idle.vram_shared_mib -and
                    $null -ne $report.peak -and $null -ne $report.peak.vram_shared_mib) {
                    [Math]::Round($report.peak.vram_shared_mib - $report.idle.vram_shared_mib, 1)
                } else { $null })
        })
}

# Same three conditions Select-Qwen36Cells.ps1 applies, and for the same reason:
# the VRAM reserve, the ADR 0011 classifier, and marginal shared memory each
# reject cells the other two accept. The comment on $admissible in that script
# carries the measurements behind the shared-margin threshold.
function Get-Fitting {
    param([int]$Context, [string]$Ctk, [string]$Ctv)
    return @($fit | Where-Object {
            $_.loaded -and $_.context -eq $Context -and $_.ctk -eq $Ctk -and $_.ctv -eq $Ctv -and
            $null -ne $_.headroom_mib -and
            $_.headroom_mib -ge ($VramReserveMib + $ReserveMarginMib) -and
            $_.pressure -ne 'pressured' -and
            ($null -eq $_.shared_margin_mib -or $_.shared_margin_mib -le $SharedMarginMib)
        })
}

function Get-Decode {
    param([string]$Model, [int]$NCpuMoe)
    $row = @($throughput | Where-Object { $_.model -eq $Model -and $_.ncpumoe -eq $NCpuMoe }) | Select-Object -First 1
    if ($row) { return $row.tg128 }
    return $null
}

# Weight quality order, worst to best, from the tensor census rather than from
# the file name: this is the order the census's expert-tensor mix puts them in.
$quality = @{ q2kxl = 1; q3ks = 2; q3km = 3; q3kxl = 4; iq4xs = 5; iq4nl = 6 }

$profiles = [ordered]@{}

# Deep: best weights that hold 32k at q8_0/q8_0.
$deepCandidates = Get-Fitting -Context 32768 -Ctk 'q8_0' -Ctv 'q8_0'
if (@($deepCandidates).Count -gt 0) {
    $profiles['deep'] = @($deepCandidates | Sort-Object `
        @{ Expression = { if ($quality.ContainsKey($_.model)) { -$quality[$_.model] } else { 0 } } },
        @{ Expression = { $_.ncpumoe } }) | Select-Object -First 1
}

# Agent: 128k at q4_0, chosen on measured decode rather than on weight size.
$agentCandidates = Get-Fitting -Context 131072 -Ctk 'q4_0' -Ctv 'q4_0'
if (@($agentCandidates).Count -gt 0) {
    $ranked = @(foreach ($row in $agentCandidates) {
        [pscustomobject]@{ row = $row; tg128 = Get-Decode -Model $row.model -NCpuMoe $row.ncpumoe }
    })
    $withDecode = @($ranked | Where-Object { $null -ne $_.tg128 } | Sort-Object -Property tg128 -Descending)
    if (@($withDecode).Count -gt 0) { $profiles['agent'] = $withDecode[0].row }
    else {
        # No throughput measured for any 128k-fitting placement: fall back to the
        # best weights, and say so rather than presenting it as a decode choice.
        Write-Host 'agent: no Phase 2 decode figure matched a 128k-fitting placement; ranking by weight quality instead.'
        $profiles['agent'] = @($agentCandidates | Sort-Object `
            @{ Expression = { if ($quality.ContainsKey($_.model)) { -$quality[$_.model] } else { 0 } } },
            @{ Expression = { $_.ncpumoe } }) | Select-Object -First 1
    }
}

# Huge: anything that holds the full native window.
$hugeCandidates = Get-Fitting -Context 262144 -Ctk 'q4_0' -Ctv 'q4_0'
if (@($hugeCandidates).Count -gt 0) {
    $profiles['huge'] = @($hugeCandidates | Sort-Object `
        @{ Expression = { if ($quality.ContainsKey($_.model)) { -$quality[$_.model] } else { 0 } } },
        @{ Expression = { $_.ncpumoe } }) | Select-Object -First 1
}

Write-Host ''
Write-Host 'proposed profile classes (a class with no fitting cell is not emitted):'
foreach ($class in @('deep', 'agent', 'huge')) {
    if ($profiles.Contains($class)) {
        $p = $profiles[$class]
        Write-Host ("  {0,-6} {1,-7} ctx {2,-7} kv {3}/{4} ncpumoe {5,-3} dedicated {6} MiB, headroom {7} MiB" -f `
                $class, $p.model, $p.context, $p.ctk, $p.ctv, $p.ncpumoe, $p.dedicated_mib, $p.headroom_mib)
    }
    else { Write-Host ("  {0,-6} NOT SUPPORTED BY ANY MEASURED CELL" -f $class) }
}

# Phase 4 grades every distinct weight set once, at 32k, so the comparison is
# between quantizations rather than between contexts. Cheapest context that is
# still representative; a weight set that writes broken code at 32k does not
# need a context ramp.
$graded = @{}
$phase4 = [System.Collections.Generic.List[object]]::new()
foreach ($class in $profiles.Keys) {
    $p = $profiles[$class]
    if ($graded.ContainsKey($p.model)) { continue }
    $graded[$p.model] = $true
    $phase4.Add([ordered]@{
            model = $p.model
            label = ("{0}-quality32k-ncpumoe{1}" -f $p.model, $p.ncpumoe)
            context = 32768; ctk = 'q4_0'; ctv = 'q4_0'; ncpumoe = $p.ncpumoe
        })
}
($phase4 | ConvertTo-Json -Depth 8) | Set-Content -LiteralPath (Join-Path $OutputRoot 'phase4-cells.json') -Encoding UTF8
Write-Host ("wrote " + (Join-Path $OutputRoot 'phase4-cells.json'))

# Phase 5 measures decode with the window actually occupied, which is the number
# an agentic profile is judged on. tg128 from an empty context is not that.
$phase5 = [System.Collections.Generic.List[object]]::new()
foreach ($class in $profiles.Keys) {
    $p = $profiles[$class]
    $depths = switch ($p.context) {
        32768 { @('tg:128@30000') }
        131072 { @('tg:128@32768', 'tg:128@120000') }
        262144 { @('tg:128@131072', 'tg:128@240000') }
        default { @('tg:128') }
    }
    $phase5.Add([ordered]@{
            model = $p.model
            label = ("{0}-filled-{1}-ctx{2}-ncpumoe{3}" -f $p.model, $class, $p.context, $p.ncpumoe)
            context = $p.context; ctk = $p.ctk; ctv = $p.ctv; ncpumoe = $p.ncpumoe; tests = $depths
        })
}
($phase5 | ConvertTo-Json -Depth 8) | Set-Content -LiteralPath (Join-Path $OutputRoot 'phase5-cells.json') -Encoding UTF8
Write-Host ("wrote " + (Join-Path $OutputRoot 'phase5-cells.json'))

$summary = [ordered]@{
    schema_version   = 1
    scenario         = 'ornith15-profile-selection'
    generated_utc    = [DateTime]::UtcNow.ToString('o')
    device_vram_mib  = $DeviceVramMib
    vram_reserve_mib = $VramReserveMib
    throughput       = @($throughput | Sort-Object model, ncpumoe)
    fit              = @($fit | Sort-Object model, context, ctk, ncpumoe)
    proposed         = $profiles
}
($summary | ConvertTo-Json -Depth 10) | Set-Content -LiteralPath (Join-Path $OutputRoot 'summary-profile-selection.json') -Encoding UTF8
Write-Host ("wrote " + (Join-Path $OutputRoot 'summary-profile-selection.json'))
