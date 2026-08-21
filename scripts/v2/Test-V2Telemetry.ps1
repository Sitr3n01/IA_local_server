<#
.SYNOPSIS
Exercises Telemetry.ps1 against the measured configurations it was calibrated
from, with no GPU and no model.

.DESCRIPTION
Test-V2GpuMemoryPressure exists to catch a degradation that raises no error and
moves no llama.cpp counter. A classifier for an invisible failure needs its own
gate, because the two ways it can be wrong are both silent: a threshold that
never fires certifies a paging configuration as healthy, and one that always
fires trains the operator to ignore it.

The cases below are the four adapter states actually measured on this
workstation, recorded in benchmarks/REPORT-qwen38-27b-gfx1201-20260821.md. The
hard requirement is the pair in the middle: the 4-block split at a short context
sits at 96.4% dedicated and was the fastest configuration measured, while the
same split at 32k sits at 97.6% and had collapsed to 195 t/s. Occupancy cannot
separate them. If a future change makes this test pass by widening the occupancy
band alone, it has removed the only signal that distinguishes them.
#>
[CmdletBinding()]
param(
    [switch]$Quiet
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot 'Telemetry.ps1')

$script:Failures = 0
$script:Checks = 0

function Assert-Equal {
    param(
        [Parameter(Mandatory = $true)][AllowNull()][object]$Expected,
        [Parameter(Mandatory = $true)][AllowNull()][object]$Actual,
        [Parameter(Mandatory = $true)][string]$Message
    )
    $script:Checks++
    if ("$Expected" -ne "$Actual") {
        $script:Failures++
        Write-Warning ("FAIL  {0}`n      expected '{1}', got '{2}'" -f $Message, $Expected, $Actual)
        return
    }
    if (-not $Quiet) { Write-Host ("  ok    {0}" -f $Message) }
}

function New-Sample {
    param([double]$DedicatedMib, [double]$SharedMib)
    return [pscustomobject]@{
        instance      = 'test'
        dedicated_mib = $DedicatedMib
        shared_mib    = $SharedMib
    }
}

$deviceMib = 16304

# --- Calibration cases, all measured on this hardware -----------------------

$cases = @(
    @{
        Name     = 'idle desktop, no model loaded'
        Sample   = New-Sample -DedicatedMib 3122 -SharedMib 412
        Expected = 'ok'
        Why      = 'a machine with no model must never be reported as pressured'
    },
    @{
        Name     = '4-block split at 512 ctx (1034 t/s pp512, healthy)'
        Sample   = New-Sample -DedicatedMib 15720 -SharedMib 514
        Expected = 'elevated'
        Why      = 'near the budget by design, but not paging: must not read as pressured'
    },
    @{
        Name     = '4-block split at 32k ctx (195 t/s pp32768, degraded)'
        Sample   = New-Sample -DedicatedMib 15916 -SharedMib 1253
        Expected = 'pressured'
        Why      = 'the degradation this classifier exists to surface'
    },
    @{
        Name     = 'full residency at 512 ctx (270 t/s pp512, degraded)'
        Sample   = New-Sample -DedicatedMib 16071 -SharedMib 1350
        Expected = 'pressured'
        Why      = 'worst measured configuration; must not read as healthy'
    }
)

Write-Host 'Test-V2GpuMemoryPressure: measured calibration cases'
foreach ($case in $cases) {
    $verdict = Test-V2GpuMemoryPressure -Sample $case.Sample -TotalMib $deviceMib
    Assert-Equal -Expected $case.Expected -Actual $verdict.state -Message $case.Name
}

# --- Boundary and degenerate inputs ----------------------------------------

Write-Host 'Test-V2GpuMemoryPressure: boundaries'

$null_ = Test-V2GpuMemoryPressure -Sample $null -TotalMib $deviceMib
Assert-Equal -Expected 'unknown' -Actual $null_.state -Message 'a null sample is unknown, never ok'

# High shared usage on its own is ordinary desktop behaviour. Reporting it as
# pressure would fire on any machine running a browser.
$sharedOnly = Test-V2GpuMemoryPressure -Sample (New-Sample -DedicatedMib 6000 -SharedMib 2048) -TotalMib $deviceMib
Assert-Equal -Expected 'ok' -Actual $sharedOnly.state -Message 'shared usage alone is not pressure'

# Occupancy on its own is the intended state of a configuration sized to the card.
$occupancyOnly = Test-V2GpuMemoryPressure -Sample (New-Sample -DedicatedMib 16000 -SharedMib 200) -TotalMib $deviceMib
Assert-Equal -Expected 'elevated' -Actual $occupancyOnly.state -Message 'occupancy alone is elevated, not pressured'

$empty = Test-V2GpuMemoryPressure -Sample (New-Sample -DedicatedMib 0 -SharedMib 0) -TotalMib $deviceMib
Assert-Equal -Expected 'ok' -Actual $empty.state -Message 'an empty adapter is ok'

# --- Host sampling must not silently return null on a localized Windows -----

Write-Host 'Get-V2HostMemorySample: locale independence'

$hostSample = Get-V2HostMemorySample
Assert-Equal -Expected $true -Actual ($null -ne $hostSample.commit_gib) `
    -Message 'commit_gib resolves (the localized \Memory\Committed Bytes path does not)'
Assert-Equal -Expected $true -Actual ($null -ne $hostSample.physical_total_gib) `
    -Message 'physical_total_gib resolves'
Assert-Equal -Expected $true -Actual ($hostSample.physical_used_gib -gt 0) `
    -Message 'physical_used_gib is a positive quantity'
Assert-Equal -Expected $true -Actual ($hostSample.commit_gib -le $hostSample.commit_limit_gib) `
    -Message 'commit does not exceed the commit limit'

# --- Combined sample keeps its columns on one instant -----------------------

Write-Host 'Get-V2MemorySample: shape'

$combined = Get-V2MemorySample -ProcessId 0
foreach ($field in @('vram_dedicated_mib', 'vram_shared_mib', 'commit_gib', 'physical_used_gib')) {
    Assert-Equal -Expected $true -Actual ($null -ne $combined.PSObject.Properties[$field]) `
        -Message ("combined sample exposes {0}" -f $field)
}
# A process id of zero has no process; those columns must be null rather than
# zero, so a report can tell "not sampled" from "sampled and empty".
Assert-Equal -Expected $null -Actual $combined.process_ws_gib `
    -Message 'process columns are null when no process id was given'

Write-Host ''
if ($script:Failures -gt 0) {
    throw ("Telemetry self-test failed: {0} of {1} checks." -f $script:Failures, $script:Checks)
}
Write-Host ("Telemetry self-test passed: {0} checks." -f $script:Checks)
