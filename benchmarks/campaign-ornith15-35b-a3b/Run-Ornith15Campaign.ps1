<#
.SYNOPSIS
Unattended driver for the Ornith-1.5-35B-A3B campaign: waits for the artifacts,
then runs every phase in order with the selection steps in between.

.DESCRIPTION
`run-campaign.ps1` runs phases. This runs the *campaign* - the phases plus the
two selection steps that stand between them, in the only order that works,
without a human in the loop between stages that are hours apart.

Progress goes to `campaign-driver.log` and the sentinel `campaign-driver.DONE`
is written only after the last phase returns, so a session that ends midway can
be resumed by reading those two files rather than by re-deriving the plan.

Every phase is skip-safe: `run-campaign.ps1` skips a footprint cell whose JSON
already exists, so re-running the driver after an interruption resumes rather
than restarts. The exception is Phase 2/4/5/6, whose scripts write into
subdirectories and will re-measure - pass -Phases to skip those explicitly.

Ordering constraints this encodes, none of which are obvious from the phase
numbers alone:

  0 -> 1        the gate is cheap and a failure there makes Phase 1 pointless
  1 -> select   Phases 2, 3 and 6 run on placements Phase 1 admitted
  2, 3 -> select the profile classes need both the throughput curve and the
                context matrix before they can be proposed
  4, 5, 6       the expensive phases, last, on cells the cheap ones justified

.NOTES
Sequential by necessity, not by choice: one llama-server holds the card at a
time, and two concurrent cells would measure each other.
#>
[CmdletBinding()]
param(
    [string]$CampaignRoot = 'C:\IA\IA_local_server\benchmarks\campaign-ornith15-35b-a3b',
    [string]$ModelRoot = 'C:\IA\models\Ornith-1.5-35B-A3B-GGUF',
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [int]$DeviceVramMib = 16304,
    [int[]]$Phases = @(0, 1, 2, 3, 4, 5, 6, 7),
    # The driver refuses to start measuring until every artifact is present.
    # Get-Ornith15Artifacts.ps1 renames <name>.part to <name> only after the
    # SHA-256 matches, so "present" and "verified" are the same condition.
    [string[]]$RequiredArtifacts = @(
        'Ornith-1.5-35B-A3B-IQ2_M.gguf',
        'Ornith-1.5-35B-A3B-IQ3_XXS.gguf',
        'Ornith-1.5-35B-A3B-Q3_K_XL.gguf'
    ),
    [ValidateRange(0, 86400)][int]$WaitForArtifactsSeconds = 14400,
    [switch]$SkipArtifactWait
)

$ErrorActionPreference = 'Continue'
$logPath = Join-Path $CampaignRoot 'campaign-driver.log'
$donePath = Join-Path $CampaignRoot 'campaign-driver.DONE'
New-Item -ItemType Directory -Force -Path $CampaignRoot | Out-Null
Remove-Item -LiteralPath $donePath -Force -ErrorAction SilentlyContinue

function Write-Step {
    param([string]$Message)
    $line = "[{0}] {1}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $Message
    Write-Host $line
    # Retry, because this log is the resume mechanism and a lost line is a lost
    # hour of someone's reconstruction. Anything holding the file open with a
    # deny-write share - a `tail -F`, an editor, an on-access virus scan - makes
    # Add-Content throw, and under $ErrorActionPreference = 'Continue' that
    # failure is silent. Measured once for real: a monitoring tail swallowed 12
    # minutes of progress lines while the campaign itself ran on unaffected.
    for ($attempt = 1; $attempt -le 5; $attempt++) {
        try {
            Add-Content -LiteralPath $logPath -Value $line -Encoding UTF8 -ErrorAction Stop
            return
        }
        catch {
            if ($attempt -eq 5) {
                # Console only. Never throw from the logger: losing a log line
                # must not be able to end a run that is otherwise healthy.
                Write-Host "  (log write failed after $attempt attempts: $($_.Exception.Message))"
                return
            }
            Start-Sleep -Milliseconds (200 * $attempt)
        }
    }
}

$campaignWatch = [Diagnostics.Stopwatch]::StartNew()
Write-Step "=== campaign start; phases: $($Phases -join ',') ==="

# ---- keep the machine awake for as long as this driver runs ---------------
# This workstation sleeps after 2 h without user input on AC, which is shorter
# than any phase after Phase 1 and would suspend a run mid-cell.
#
# Deliberately a per-process assertion rather than a change to the power scheme:
# SetThreadExecutionState is the same mechanism a video player uses, it stores
# nothing, and it lapses the moment this process exits - including if it is
# killed. The user's power settings are left exactly as they were found.
#
# ES_SYSTEM_REQUIRED only. The display is allowed to sleep; nothing here needs
# it, and keeping a monitor lit all night is not this script's business.
if (-not ('CampaignPowerRequest' -as [type])) {
    Add-Type -Namespace Native -Name CampaignPowerRequest -MemberDefinition @'
[DllImport("kernel32.dll", SetLastError = true)]
public static extern uint SetThreadExecutionState(uint esFlags);
'@ -ErrorAction SilentlyContinue
}
$ES_CONTINUOUS = [uint32]'0x80000000'
$ES_SYSTEM_REQUIRED = [uint32]'0x00000001'
$awake = $false
try {
    $previousState = [Native.CampaignPowerRequest]::SetThreadExecutionState($ES_CONTINUOUS -bor $ES_SYSTEM_REQUIRED)
    if ($previousState -ne 0) {
        $awake = $true
        Write-Step 'sleep suppressed for the duration of this run (ES_SYSTEM_REQUIRED)'
    }
    else {
        Write-Step 'WARNING: could not suppress sleep; a 2 h idle timeout may interrupt the run'
    }
}
catch {
    Write-Step "WARNING: could not suppress sleep: $($_.Exception.Message)"
}

# ---- wait for the artifacts ----------------------------------------------
if (-not $SkipArtifactWait) {
    $deadline = [DateTime]::UtcNow.AddSeconds($WaitForArtifactsSeconds)
    while ($true) {
        $missing = @($RequiredArtifacts | Where-Object {
                -not (Test-Path -LiteralPath (Join-Path $ModelRoot $_)) })
        if ($missing.Count -eq 0) { break }
        if ([DateTime]::UtcNow -ge $deadline) {
            Write-Step "ABORT: still missing after $WaitForArtifactsSeconds s: $($missing -join ', ')"
            return
        }
        Write-Step "waiting for $($missing.Count) artifact(s): $($missing -join ', ')"
        Start-Sleep -Seconds 120
    }
    Write-Step 'all artifacts present and hash-verified'
}

foreach ($name in $RequiredArtifacts) {
    $path = Join-Path $ModelRoot $name
    if (Test-Path -LiteralPath $path) {
        Write-Step ("artifact {0}: {1:N0} bytes" -f $name, (Get-Item -LiteralPath $path).Length)
    }
}

$runCampaign = Join-Path $CampaignRoot 'run-campaign.ps1'
$selectCells = Join-Path $CampaignRoot 'Select-Ornith15Cells.ps1'
$selectProfiles = Join-Path $CampaignRoot 'Select-Ornith15Profiles.ps1'

function Invoke-Phase {
    param([int[]]$Only, [string]$Name)
    $wanted = @($Only | Where-Object { $Phases -contains $_ })
    if ($wanted.Count -eq 0) { Write-Step "skip $Name (not in -Phases)"; return }
    Write-Step "--- begin $Name ---"
    $watch = [Diagnostics.Stopwatch]::StartNew()
    & $runCampaign -Phases $wanted -ModelRoot $ModelRoot -RuntimeRoot $RuntimeRoot `
        -OutputRoot $CampaignRoot -DeviceVramMib $DeviceVramMib 2>&1 |
        Tee-Object -FilePath (Join-Path $CampaignRoot ("driver-phase-{0}.log" -f ($wanted -join '_'))) |
        Out-Null
    $watch.Stop()
    Write-Step ("--- end $Name after {0:N1} min ---" -f $watch.Elapsed.TotalMinutes)
}

Invoke-Phase -Only @(0) -Name 'Phase 0 (runtime gate)'

# A failed gate is the one result that should stop the campaign: every later
# phase measures a server this one could not stand up.
$gatePath = Join-Path $CampaignRoot 'runtime-gate.json'
if (($Phases -contains 0) -and (Test-Path -LiteralPath $gatePath)) {
    $gate = Get-Content -Raw -LiteralPath $gatePath | ConvertFrom-Json
    Write-Step "gate verdict: $($gate.verdict)"
    if ($gate.verdict -eq 'FAIL') {
        Write-Step 'ABORT: runtime gate FAILED; nothing after it is worth measuring.'
        Set-Content -LiteralPath $donePath -Value "ABORTED at gate: $($gate.failure)" -Encoding UTF8
        return
    }
}

Invoke-Phase -Only @(1) -Name 'Phase 1 (MoE placement sweep)'

if ($Phases -contains 1 -or -not (Test-Path -LiteralPath (Join-Path $CampaignRoot 'phase2-cells.json'))) {
    Write-Step '--- selecting cells from the Phase 1 census ---'
    & $selectCells -OutputRoot $CampaignRoot -DeviceVramMib $DeviceVramMib 2>&1 |
        Tee-Object -FilePath (Join-Path $CampaignRoot 'driver-select-cells.log') | Out-Null
    Write-Step '--- cells selected ---'
}

Invoke-Phase -Only @(2) -Name 'Phase 2 (throughput)'
Invoke-Phase -Only @(3) -Name 'Phase 3 (context and KV)'

if (($Phases -contains 4) -or ($Phases -contains 5) -or ($Phases -contains 7)) {
    Write-Step '--- proposing profiles from Phases 2 and 3 ---'
    & $selectProfiles -OutputRoot $CampaignRoot -DeviceVramMib $DeviceVramMib 2>&1 |
        Tee-Object -FilePath (Join-Path $CampaignRoot 'driver-select-profiles.log') | Out-Null
    Write-Step '--- profiles proposed ---'
}

# Ordered by how much the replacement decision leans on each, not by number.
# Quality first: a model that cannot write correct code does not need its decode
# curve measured. The MTP A/B is next because it is short and it is the only
# axis on which Ornith and Qwen3.6 are not comparable. The two long ones last.
Invoke-Phase -Only @(4) -Name 'Phase 4 (coding, tools, JSON quality)'
Invoke-Phase -Only @(6) -Name 'Phase 6 (MTP speculation A/B)'
Invoke-Phase -Only @(5) -Name 'Phase 5 (filled-context decode)'
Invoke-Phase -Only @(7) -Name 'Phase 7 (full qualification with retention)'

$campaignWatch.Stop()

# Release the sleep assertion. It would lapse on exit anyway; doing it here
# means the machine can sleep during whatever gap follows rather than waiting
# for this process to be reaped.
if ($awake) {
    try {
        [void][Native.CampaignPowerRequest]::SetThreadExecutionState($ES_CONTINUOUS)
        Write-Step 'sleep suppression released'
    }
    catch { Write-Step "note: could not release sleep suppression: $($_.Exception.Message)" }
}

$summary = "campaign complete in {0:N2} h; phases {1}" -f $campaignWatch.Elapsed.TotalHours, ($Phases -join ',')
Write-Step "=== $summary ==="
Set-Content -LiteralPath $donePath -Value $summary -Encoding UTF8
