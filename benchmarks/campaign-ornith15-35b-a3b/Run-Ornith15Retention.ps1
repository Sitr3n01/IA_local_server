<#
.SYNOPSIS
Retention-first driver, with a wall-clock deadline: measures persistence on
every profile before spending a minute on anything else, then fills the
remaining time with whatever else fits.

.DESCRIPTION
`Run-Ornith15Campaign.ps1` runs the campaign in the order that discovers
failures cheapest. This runs it in the order that answers the *question*
soonest, which is not the same order.

Retention is the one dimension where neither model has a number - Phase 7 here
had not started, and the Qwen3.6 campaign's equivalent phase never ran at all -
so it is worth more than everything still outstanding combined. Running it
through `Invoke-V2ProfileQualification.ps1` with `-Suites retention` isolates it:
the full suite list would put the retention ramp last inside each profile, which
is exactly backwards when the clock is the constraint.

**The deadline is a hard promise, not a target.** The machine has to be usable
at noon. A unit is only started if its estimate fits before -Deadline; units
that do not fit are logged as skipped, by name, so the report can say what was
dropped rather than reading as though it covered everything. Estimates are
deliberately generous - overrunning the deadline costs the user their morning,
while underusing the last half hour costs one measurement.

Work is ordered by what the replacement decision leans on, and each unit writes
its own JSON, so stopping between units loses nothing.

.NOTES
Holds its own sleep assertion for its own lifetime, and releases it on the way
out - including when it stops early at the deadline - so the workstation goes
back to its normal power behaviour without anyone touching the power scheme.
#>
[CmdletBinding()]
param(
    [string]$CampaignRoot = 'C:\IA\IA_local_server\benchmarks\campaign-ornith15-35b-a3b',
    [string]$ModelRoot = 'C:\IA\models\Ornith-1.5-35B-A3B-GGUF',
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [int]$DeviceVramMib = 16304,
    # Local time by which everything must be finished and the GPU idle.
    #
    # The default is the next 11:30 that is still in the future - today's if the
    # run starts before it, tomorrow's otherwise. An earlier version hardcoded
    # Today.AddDays(1) on the assumption of a before-midnight launch, which
    # silently granted a run started at 00:45 an extra 24 hours.
    #
    # Pass it as ONE token. `Start-Process -ArgumentList @('-Deadline','2026-08-25 11:30')`
    # does not quote array elements in Windows PowerShell 5.1: the value splits
    # into two argv tokens, binding fails, and the script exits before its first
    # log line. Use -Command with the value quoted inside instead.
    [datetime]$Deadline = $(
        $target = [datetime]::Today.AddHours(11).AddMinutes(30)
        if ($target -le (Get-Date)) { $target.AddDays(1) } else { $target }
    ),
    [switch]$WhatIf
)

$ErrorActionPreference = 'Continue'
Set-StrictMode -Version Latest

$logPath = Join-Path $CampaignRoot 'retention-driver.log'
$donePath = Join-Path $CampaignRoot 'retention-driver.DONE'
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
    iq2m   = Join-Path $ModelRoot 'Ornith-1.5-35B-A3B-IQ2_M.gguf'
    iq3xxs = Join-Path $ModelRoot 'Ornith-1.5-35B-A3B-IQ3_XXS.gguf'
    q3kxl  = Join-Path $ModelRoot 'Ornith-1.5-35B-A3B-Q3_K_XL.gguf'
}
$qualify = 'C:\IA\IA_local_server\scripts\v2\Invoke-V2ProfileQualification.ps1'

# Ramps kept verbatim from the 2026-08-23 roster campaign so these depths line
# up with the retention numbers the four incumbent models already carry.
$ramp131k = @(16384, 40960, 65536, 90112, 120000)
$ramp262k = @(32768, 81920, 131072, 180224, 240000)
$ramp32k = @(4096, 10240, 16384, 22528, 28000)

# Placements are the census minima already measured - see summary-placement and
# the Phase 3 census. Nothing here re-derives them.
$units = @(
    # 1. Retention at the agent window, all three quants. The core comparison,
    #    and the depths the roster's gemma4-12b and qwen38-agent-128k share.
    @{ order = 1; model = 'iq2m';   label = 'iq2m-retention-ctx131072-ncpumoe2';    ctx = 131072; ctk = 'q4_0'; n = 2;  suites = 'retention'; ramp = $ramp131k; est = 70 }
    @{ order = 1; model = 'iq3xxs'; label = 'iq3xxs-retention-ctx131072-ncpumoe10'; ctx = 131072; ctk = 'q4_0'; n = 10; suites = 'retention'; ramp = $ramp131k; est = 80 }
    @{ order = 1; model = 'q3kxl';  label = 'q3kxl-retention-ctx131072-ncpumoe20';  ctx = 131072; ctk = 'q4_0'; n = 20; suites = 'retention'; ramp = $ramp131k; est = 90 }

    # 2. Retention at the full window. The 240k probe alone measured 49 minutes
    #    of TTFT on qwen38-huge-256k, so this is priced high on purpose.
    @{ order = 2; model = 'q3kxl';  label = 'q3kxl-retention-ctx262144-ncpumoe18';  ctx = 262144; ctk = 'q4_0'; n = 18; suites = 'retention'; ramp = $ramp262k; est = 150 }

    # 3. The quality cells the corrected cell list left owing - cheap, and the
    #    only thing standing between two of three quants and a graded suite.
    @{ order = 3; model = 'iq2m';   label = 'iq2m-quality32k-ncpumoe0';   ctx = 32768; ctk = 'q4_0'; n = 0;  suites = 'coding,tools,json'; ramp = @(); est = 20 }
    @{ order = 3; model = 'iq3xxs'; label = 'iq3xxs-quality32k-ncpumoe8'; ctx = 32768; ctk = 'q4_0'; n = 8;  suites = 'coding,tools,json'; ramp = @(); est = 25 }

    # 4. The rest of the suite list at the agent window, which is what makes
    #    these three directly comparable to the incumbent roster's grades.
    @{ order = 4; model = 'iq2m';   label = 'iq2m-full128k-ncpumoe2';    ctx = 131072; ctk = 'q4_0'; n = 2;  suites = 'chat,coding,hard,tools,literal_tools,json'; ramp = @(); est = 60 }
    @{ order = 4; model = 'iq3xxs'; label = 'iq3xxs-full128k-ncpumoe10'; ctx = 131072; ctk = 'q4_0'; n = 10; suites = 'chat,coding,hard,tools,literal_tools,json'; ramp = @(); est = 70 }
    @{ order = 4; model = 'q3kxl';  label = 'q3kxl-full128k-ncpumoe20';  ctx = 131072; ctk = 'q4_0'; n = 20; suites = 'chat,coding,hard,tools,literal_tools,json'; ramp = @(); est = 80 }

    # 5. Full-window retention on the other two quants. Last because one probe
    #    at 240k costs more than an entire agent-window profile.
    @{ order = 5; model = 'iq3xxs'; label = 'iq3xxs-retention-ctx262144-ncpumoe14'; ctx = 262144; ctk = 'q4_0'; n = 14; suites = 'retention'; ramp = $ramp262k; est = 150 }
    @{ order = 5; model = 'iq2m';   label = 'iq2m-retention-ctx262144-ncpumoe6';    ctx = 262144; ctk = 'q4_0'; n = 6;  suites = 'retention'; ramp = $ramp262k; est = 130 }
)

# ---- sleep suppression, scoped to this process --------------------------
if (-not ('Native.RetentionPowerRequest' -as [type])) {
    Add-Type -Namespace Native -Name RetentionPowerRequest -MemberDefinition @'
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
        $prev = [Native.RetentionPowerRequest]::SetThreadExecutionState($ES_CONTINUOUS -bor $ES_SYSTEM_REQUIRED)
        if ($prev -ne 0) { $awake = $true }
    }
    Write-Step "=== retention-first run; deadline $($Deadline.ToString('yyyy-MM-dd HH:mm')) ==="
    Write-Step ("sleep suppressed: {0}" -f $awake)

    # Do not start on top of whatever the previous driver left mid-flight.
    $waited = 0
    while (@(Get-Process llama-server, llama-bench -ErrorAction SilentlyContinue).Count -gt 0 -and $waited -lt 900) {
        Write-Step 'waiting for the previous run to release the GPU'
        Start-Sleep -Seconds 30
        $waited += 30
    }

    # Iterated in declaration order, deliberately unsorted. Windows PowerShell
    # 5.1's Sort-Object is not stable, so sorting by `order` alone reshuffles
    # units within a band - it put the slowest quant first, which is the wrong
    # end to start from when the clock decides what gets measured at all. The
    # array is already written cheapest-first inside each band.
    foreach ($unit in $units) {
        $remaining = ($Deadline - (Get-Date)).TotalMinutes
        if ($remaining -le 0) {
            $skipped.Add("$($unit.label) (deadline passed)")
            Write-Step "SKIP $($unit.label): deadline passed"
            continue
        }
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
            Write-Step ("PLAN {0}  suites={1}  est {2} min  ({3} min left)" -f $unit.label, $unit.suites, $unit.est, [int]$remaining)
            continue
        }

        Write-Step ("--- begin {0} | suites {1} | ctx {2} | n_cpu_moe {3} | est {4} min | {5} min to deadline" -f `
                $unit.label, $unit.suites, $unit.ctx, $unit.n, $unit.est, [int]$remaining)
        $unitWatch = [Diagnostics.Stopwatch]::StartNew()

        $splat = @{
            RuntimeRoot    = $RuntimeRoot
            ModelPath      = $artifacts[$unit.model]
            Label          = $unit.label
            ContextTokens  = $unit.ctx
            CacheTypeK     = $unit.ctk
            CacheTypeV     = $unit.ctk
            NCpuMoe        = $unit.n
            Suites         = $unit.suites
            DeviceVramMib  = $DeviceVramMib
            OutputRoot     = $outputRoot
        }
        if ($unit.ramp.Count -gt 0) { $splat['RetentionTokens'] = [int[]]$unit.ramp }

        & $qualify @splat 2>&1 |
            Tee-Object -FilePath (Join-Path $outputRoot ("run-{0}.log" -f $unit.label)) | Out-Null

        $unitWatch.Stop()
        $done.Add($unit.label)
        Write-Step ("--- end {0} after {1:N1} min" -f $unit.label, $unitWatch.Elapsed.TotalMinutes)
    }
}
finally {
    if ($awake) {
        try { [void][Native.RetentionPowerRequest]::SetThreadExecutionState($ES_CONTINUOUS) }
        catch { }
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
