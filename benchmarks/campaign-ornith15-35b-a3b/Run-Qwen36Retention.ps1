<#
.SYNOPSIS
Measures retention on Qwen3.6-35B-A3B, so the Ornith numbers have something to
be compared against.

.DESCRIPTION
The Qwen3.6 campaign of 2026-08-22 never ran its retention phase - the report's
own progress table lists it as queued for an overnight block that did not
happen. That left the replacement decision resting on a dimension where neither
side had a number, which is the worst possible place for a gap.

Nothing about the model has changed since, the artifacts are still on disk at
the same paths and hashes, and the harness here is the one that produced the
Ornith figures. So this is not a new campaign: it is the missing half of an
existing comparison, measured under conditions that make the two halves
comparable.

Pairing is by file size, matching the Ornith campaign's design:

    q2kxl  11.45 GiB  <->  Ornith IQ2_M    11.68 GiB
    q3ks   14.30 GiB  <->  Ornith IQ3_XXS  14.29 GiB
    iq4xs  16.51 GiB  <->  Ornith Q3_K_XL  16.58 GiB

Placements are the smallest admissible `--n-cpu-moe` from the Qwen3.6 campaign's
own census at that (context, cache) group - not the Ornith placements. The two
models do not need the same offload to hold the same window, and forcing them to
would measure the wrong thing.

Ramps and suite are identical to the Ornith runs and to the 2026-08-23 roster
campaign, so all three sets of retention figures sit on the same axis.

.NOTES
Deadline handling, sleep suppression and skip-logging behave exactly as in
Run-Ornith15Retention.ps1; see that script for why each is there.
#>
[CmdletBinding()]
param(
    [string]$CampaignRoot = 'C:\IA\IA_local_server\benchmarks\campaign-ornith15-35b-a3b',
    [string]$ModelRoot = 'C:\IA\models\Qwen3.6-35B-A3B-GGUF',
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [int]$DeviceVramMib = 16304,
    [datetime]$Deadline = $(
        $target = [datetime]::Today.AddHours(11).AddMinutes(30)
        if ($target -le (Get-Date)) { $target.AddDays(1) } else { $target }
    ),
    # Wait for this sentinel before touching the GPU. The Ornith driver and this
    # one must never overlap: one llama-server holds the card, and two would
    # measure each other.
    [string]$WaitForSentinel = 'C:\IA\IA_local_server\benchmarks\campaign-ornith15-35b-a3b\retention-driver.DONE',
    [int]$WaitMinutes = 480,
    [switch]$WhatIf
)

$ErrorActionPreference = 'Continue'
Set-StrictMode -Version Latest

$logPath = Join-Path $CampaignRoot 'qwen36-retention-driver.log'
$donePath = Join-Path $CampaignRoot 'qwen36-retention-driver.DONE'
Remove-Item -LiteralPath $donePath -Force -ErrorAction SilentlyContinue

function Write-Step {
    param([string]$Message)
    $line = "[{0}] {1}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $Message
    Write-Host $line
    for ($attempt = 1; $attempt -le 5; $attempt++) {
        try { Add-Content -LiteralPath $logPath -Value $line -Encoding UTF8 -ErrorAction Stop; return }
        catch {
            if ($attempt -eq 5) { Write-Host "  (log write failed: $($_.Exception.Message))"; return }
            Start-Sleep -Milliseconds (200 * $attempt)
        }
    }
}

$artifacts = @{
    q2kxl = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q2_K_XL.gguf'
    q3ks  = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-Q3_K_S.gguf'
    iq4xs = Join-Path $ModelRoot 'Qwen3.6-35B-A3B-UD-IQ4_XS.gguf'
}
$qualify = 'C:\IA\IA_local_server\scripts\v2\Invoke-V2ProfileQualification.ps1'
$ramp131k = @(16384, 40960, 65536, 90112, 120000)
$ramp262k = @(32768, 81920, 131072, 180224, 240000)

# Estimates revised down from the Ornith run's first measurement: a retention
# ramp is five prefills and twelve short answers, not a full suite. iq2m at
# 131072 took 8.3 minutes against a 70-minute guess. These are still padded,
# because Qwen3.6 needs deeper offload for the same window and offload is what
# costs prefill.
$units = @(
    @{ model = 'q2kxl'; label = 'qwen36-q2kxl-retention-ctx131072-ncpumoe4';  ctx = 131072; n = 4;  ramp = $ramp131k; est = 25 }
    @{ model = 'q3ks';  label = 'qwen36-q3ks-retention-ctx131072-ncpumoe12';  ctx = 131072; n = 12; ramp = $ramp131k; est = 35 }
    @{ model = 'iq4xs'; label = 'qwen36-iq4xs-retention-ctx131072-ncpumoe18'; ctx = 131072; n = 18; ramp = $ramp131k; est = 45 }
    @{ model = 'q2kxl'; label = 'qwen36-q2kxl-retention-ctx262144-ncpumoe8';  ctx = 262144; n = 8;  ramp = $ramp262k; est = 70 }
    @{ model = 'q3ks';  label = 'qwen36-q3ks-retention-ctx262144-ncpumoe16';  ctx = 262144; n = 16; ramp = $ramp262k; est = 90 }
    @{ model = 'iq4xs'; label = 'qwen36-iq4xs-retention-ctx262144-ncpumoe20'; ctx = 262144; n = 20; ramp = $ramp262k; est = 110 }
)

if (-not ('Native.Qwen36PowerRequest' -as [type])) {
    Add-Type -Namespace Native -Name Qwen36PowerRequest -MemberDefinition @'
[DllImport("kernel32.dll", SetLastError = true)]
public static extern uint SetThreadExecutionState(uint esFlags);
'@ -ErrorAction SilentlyContinue
}
$ES_CONTINUOUS = [uint32]'0x80000000'
$ES_SYSTEM_REQUIRED = [uint32]'0x00000001'
$awake = $false

$outputRoot = Join-Path $CampaignRoot 'qualification'
New-Item -ItemType Directory -Force -Path $outputRoot | Out-Null

$done = [System.Collections.Generic.List[string]]::new()
$skipped = [System.Collections.Generic.List[string]]::new()
$watch = [Diagnostics.Stopwatch]::StartNew()

try {
    if (-not $WhatIf) {
        $prev = [Native.Qwen36PowerRequest]::SetThreadExecutionState($ES_CONTINUOUS -bor $ES_SYSTEM_REQUIRED)
        if ($prev -ne 0) { $awake = $true }
    }
    Write-Step "=== Qwen3.6 retention; deadline $($Deadline.ToString('yyyy-MM-dd HH:mm')) ==="

    if (-not $WhatIf -and $WaitForSentinel) {
        $waited = 0
        while (-not (Test-Path -LiteralPath $WaitForSentinel) -and $waited -lt ($WaitMinutes * 60)) {
            if ((Get-Date) -ge $Deadline) { break }
            Start-Sleep -Seconds 60
            $waited += 60
            if ($waited % 900 -eq 0) { Write-Step "waiting for the Ornith run to finish ($([int]($waited/60)) min)" }
        }
        # Belt and braces: the sentinel means the driver returned, not that a
        # child llama-server has exited yet.
        while (@(Get-Process llama-server, llama-bench -ErrorAction SilentlyContinue).Count -gt 0) {
            Start-Sleep -Seconds 20
        }
        Write-Step 'GPU is free; starting'
    }

    foreach ($unit in $units) {
        $remaining = ($Deadline - (Get-Date)).TotalMinutes
        if ($unit.est -gt $remaining) {
            $skipped.Add("$($unit.label) (needs ~$($unit.est) min, $([int]$remaining) left)")
            Write-Step ("SKIP {0}: estimate {1} min does not fit in {2} min left" -f $unit.label, $unit.est, [int]$remaining)
            continue
        }
        if (-not (Test-Path -LiteralPath $artifacts[$unit.model])) {
            $skipped.Add("$($unit.label) (artifact absent)")
            Write-Step "SKIP $($unit.label): artifact absent"
            continue
        }
        if ($WhatIf) {
            Write-Step ("PLAN {0}  est {1} min  ({2} min left)" -f $unit.label, $unit.est, [int]$remaining)
            continue
        }

        Write-Step ("--- begin {0} | ctx {1} | n_cpu_moe {2} | est {3} min | {4} min to deadline" -f `
                $unit.label, $unit.ctx, $unit.n, $unit.est, [int]$remaining)
        $unitWatch = [Diagnostics.Stopwatch]::StartNew()

        & $qualify -RuntimeRoot $RuntimeRoot -ModelPath $artifacts[$unit.model] `
            -Label $unit.label -ContextTokens $unit.ctx -CacheTypeK 'q4_0' -CacheTypeV 'q4_0' `
            -NCpuMoe $unit.n -Suites 'retention' -RetentionTokens ([int[]]$unit.ramp) `
            -DeviceVramMib $DeviceVramMib -OutputRoot $outputRoot 2>&1 |
            Tee-Object -FilePath (Join-Path $outputRoot ("run-{0}.log" -f $unit.label)) | Out-Null

        $unitWatch.Stop()
        $done.Add($unit.label)
        Write-Step ("--- end {0} after {1:N1} min" -f $unit.label, $unitWatch.Elapsed.TotalMinutes)
    }
}
finally {
    if ($awake) {
        try { [void][Native.Qwen36PowerRequest]::SetThreadExecutionState($ES_CONTINUOUS) } catch { }
        Write-Step 'sleep suppression released'
    }
    $watch.Stop()
    $summary = "done={0} skipped={1} elapsed={2:N2} h" -f $done.Count, $skipped.Count, $watch.Elapsed.TotalHours
    Write-Step "=== $summary ==="
    foreach ($s in $skipped) { Write-Step "  not run: $s" }
    if (-not $WhatIf) {
        Set-Content -LiteralPath $donePath -Encoding UTF8 -Value (@(
                $summary
                'completed:'
                $done | ForEach-Object { "  $_" }
                'not run:'
                $skipped | ForEach-Object { "  $_" }
            ) -join [Environment]::NewLine)
    }
}
