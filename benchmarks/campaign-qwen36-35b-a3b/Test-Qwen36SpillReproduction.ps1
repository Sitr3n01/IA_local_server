<#
.SYNOPSIS
Re-measures the eight cells whose spill is non-monotonic in `--n-cpu-moe`.

.DESCRIPTION
Spill should fall as experts move to the host, so a cell that spills at `n`
while a lower `n` was clean is unexplained by placement. The budget search
found eight of them, and in all eight the shape is identical: clean at n,
spilling at n+2, clean again at n+4. All eight are q4_0; not one q8_0 cell
does it.

Two hypotheses fit, and they differ in what should be shipped:

  transient - the desktop moved under the measurement. Report section 8.9
              measured other processes holding between 2474 and 4350 MiB, so a
              cell that ran while the browser was busy reads as spilling. If
              this is it, the search skipped a placement that was in fact
              admissible, and the profiles are two layers wider than necessary.

  structural - moving experts to the host pins memory that WDDM accounts as
              shared, so a wider split really can raise shared use. If this is
              it, the skip was correct and the profiles are right.

A repeat measurement separates them: transient spill does not reproduce,
structural spill does. Both readings are kept - the original cell is not
overwritten - because a disagreement between two runs of the same
configuration is itself the finding.
#>
[CmdletBinding()]
param(
    [string]$ModelRoot = 'C:\IA\models\Qwen3.6-35B-A3B-GGUF',
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [string]$OutputRoot = 'C:\IA\IA_local_server\benchmarks\campaign-qwen36-35b-a3b',
    [int]$DeviceVramMib = 16304
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

$cells = @(
    @{ model = 'q2kxl'; context = 131072; n = 2 },
    @{ model = 'q2kxl'; context = 262144; n = 6 },
    @{ model = 'q3ks';  context = 131072; n = 10 },
    @{ model = 'q3ks';  context = 262144; n = 14 },
    @{ model = 'q3kxl'; context = 131072; n = 14 },
    @{ model = 'q3kxl'; context = 262144; n = 18 },
    @{ model = 'iq4xs'; context = 32768;  n = 14 },
    @{ model = 'iq4xs'; context = 262144; n = 18 }
)

$missing = @($cells | ForEach-Object { [string]$_.model } | Sort-Object -Unique |
    Where-Object { -not $paths.ContainsKey($_) -or -not (Test-Path -LiteralPath $paths[$_]) })
if ($missing.Count -gt 0) { throw ("Model(s) not on disk under '{0}': {1}" -f $ModelRoot, ($missing -join ', ')) }

$stray = @(Get-Process -Name 'llama-server', 'llama-bench' -ErrorAction SilentlyContinue)
if ($stray.Count -gt 0) {
    throw ("{0} llama process(es) already running (pids {1}); this test is about shared memory, so a second server invalidates it." -f `
            $stray.Count, (($stray | ForEach-Object { $_.Id }) -join ', '))
}

function Get-Spill {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    $r = Get-Content -Raw -LiteralPath $Path | ConvertFrom-Json
    if ($null -ne $r.failure) { return $null }
    if ($null -eq $r.idle -or $null -eq $r.peak) { return $null }
    return [pscustomobject]@{
        spill    = [Math]::Round($r.peak.vram_shared_mib - $r.idle.vram_shared_mib, 1)
        marginal = $r.marginal_vram_mib
        idle     = $r.idle.vram_dedicated_mib
    }
}

$rows = [System.Collections.Generic.List[object]]::new()
foreach ($cell in $cells) {
    $model = [string]$cell.model
    $ctx = [int]$cell.context
    $n = [int]$cell.n
    $orig = Join-Path $OutputRoot ("footprint-{0}-ctx{1}-kvq4_0-q4_0-ncpumoe{2}.json" -f $model, $ctx, $n)
    if (-not (Test-Path -LiteralPath $orig)) {
        $orig = Join-Path $OutputRoot ("footprint-{0}-ctx{1}-kvq4_0-ncpumoe{2}.json" -f $model, $ctx, $n)
    }
    $repeat = Join-Path $OutputRoot ("footprint-{0}-ctx{1}-kvq4_0-q4_0-ncpumoe{2}-repeat.json" -f $model, $ctx, $n)

    if (-not (Test-Path -LiteralPath $repeat)) {
        & $footprint -RuntimeRoot $RuntimeRoot -ModelPath $paths[$model] `
            -ContextTokens $ctx -CacheTypeK q4_0 -CacheTypeV q4_0 -NCpuMoe $n `
            -DeviceVramMib $DeviceVramMib -OutputPath $repeat -Quiet
    }

    $a = Get-Spill -Path $orig
    $b = Get-Spill -Path $repeat
    $verdict = 'inconclusive'
    if ($null -ne $a -and $null -ne $b) {
        $verdict = if ($b.spill -gt 400) { 'structural' } else { 'transient' }
    }
    $rows.Add([ordered]@{
            model = $model; context = $ctx; ncpumoe = $n
            first_spill_mib = $(if ($a) { $a.spill } else { $null })
            repeat_spill_mib = $(if ($b) { $b.spill } else { $null })
            first_marginal_mib = $(if ($a) { $a.marginal } else { $null })
            repeat_marginal_mib = $(if ($b) { $b.marginal } else { $null })
            first_idle_mib = $(if ($a) { $a.idle } else { $null })
            repeat_idle_mib = $(if ($b) { $b.idle } else { $null })
            verdict = $verdict
        })
    Write-Host ("{0,-6} ctx{1,-7} n={2,-3} first spill {3,8}  repeat spill {4,8}  -> {5}" -f `
            $model, $ctx, $n,
            $(if ($a) { '{0:N0}' -f $a.spill } else { 'n/a' }),
            $(if ($b) { '{0:N0}' -f $b.spill } else { 'n/a' }), $verdict)
}

$outPath = Join-Path $OutputRoot 'summary-spill-reproduction.json'
[ordered]@{
    schema_version = 1
    scenario       = 'qwen36-spill-reproduction'
    generated_utc  = [DateTime]::UtcNow.ToString('o')
    shared_margin_mib = 400
    cells          = @($rows)
} | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $outPath -Encoding UTF8
Write-Host ''
Write-Host ("structural {0}, transient {1}, inconclusive {2}" -f `
    @($rows | Where-Object { $_.verdict -eq 'structural' }).Count,
    @($rows | Where-Object { $_.verdict -eq 'transient' }).Count,
    @($rows | Where-Object { $_.verdict -eq 'inconclusive' }).Count)
Write-Host "wrote $outPath"
