<#
.SYNOPSIS
Decides whether Gemma 4 12B QAT can be offered at 256k on this machine, or at
what smaller window it can.

.DESCRIPTION
gemma4-12b-qat-ud-q4xl is the public model and is served at 131072. The
question here is not whether llama-server can start with --ctx-size 262144 --
it can start with a great many numbers -- but whether the profile still
remembers, still answers, and still fits with the context actually occupied.

Same GGUF, same path, same SHA-256 as the 128k profile. Nothing is duplicated on
disk. The candidate differs from the public profile in exactly one field,
context_tokens, so what is measured here is context and not a bundle of changes.

  G1  admission    Measure-V2ContextFootprint at 262144, before anything long
                   runs. If the placement is not admissible there is no campaign
                   to run, and finding that out costs two minutes rather than
                   two hours.

  G2  retention    One ramp at 262144 over 32768 / 65536 / 120000 / 196608 /
                   240000. The retention JSON records ttft_prefill_ms,
                   prefill_tps and decode_tps_at_occupancy per level, and the
                   profile JSON records peak VRAM, RAM and commit, so a single
                   ramp answers both the memory question and most of the
                   performance one.

  G3  quality      chat, coding, hard, tools, literal_tools, json at 262144.
                   The same suites the 2026-08-23 campaign ran against the 128k
                   profile, so the two are directly comparable.

                   Running the tools suite does NOT license flipping
                   capabilities.function_calling. That flag is a deployment
                   guarantee about a forced tool call through
                   internal/edge/namespace.go, it stays false, and a score here
                   is not evidence for it -- docs/MODEL_PROMOTION.md is explicit
                   about that, and the 2026-08-23 campaign already scored 6/7 on
                   this suite with the flag correctly left alone.

A parse failure is not a memory failure. The 2026-08-23 baseline scored 0/12 at
32768 with finish_reason "length", truncated true and an empty answer after
11,743 characters of reasoning -- the model had spent its whole budget thinking.
That is a budget defect, and the fix for it is already in this branch's history.
The report separates the two by reading finish_reason and truncated out of each
level rather than by reading the score.

.NOTES
Resumable: a cell whose output JSON exists is skipped.
#>
[CmdletBinding()]
param(
    [string]$CampaignRoot = 'C:\IA\IA_local_server\benchmarks\campaign-gemma4-256k-20260825',
    [string]$RepoRoot = 'C:\IA\IA_local_server',
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [string]$ModelPath = 'C:\IA\models\gemma-4-12B-it-qat-GGUF\gemma-4-12B-it-qat-UD-Q4_K_XL.gguf',
    [int]$DeviceVramMib = 16304,
    [ValidateSet('G1', 'G2', 'G3', 'all')][string[]]$Phases = @('all'),
    # Descending on purpose: if 262144 is not admissible the fallback tiers are
    # measured in the order that finds the largest healthy one first.
    [int[]]$ContextLadder = @(262144, 229376, 196608, 163840),
    [switch]$WhatIf
)

$ErrorActionPreference = 'Continue'
Set-StrictMode -Version Latest

New-Item -ItemType Directory -Force -Path $CampaignRoot | Out-Null
$logPath = Join-Path $CampaignRoot 'gemma-256k-driver.log'
$donePath = Join-Path $CampaignRoot 'gemma-256k-driver.DONE'
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

$KV = 'q4_0'
$UBATCH = 512
$BATCH = 2048
$GPU_LAYERS = 99
$RAMP = @(32768, 65536, 120000, 196608, 240000)

$footprint = Join-Path $RepoRoot 'scripts\v2\Measure-V2ContextFootprint.ps1'
$qualify = Join-Path $RepoRoot 'scripts\v2\Invoke-V2ProfileQualification.ps1'

foreach ($dir in @('admission', 'retention', 'quality')) {
    New-Item -ItemType Directory -Force -Path (Join-Path $CampaignRoot $dir) | Out-Null
}

if (-not ('Native.Gemma256Power' -as [type])) {
    Add-Type -Namespace Native -Name Gemma256Power -MemberDefinition @'
[DllImport("kernel32.dll", SetLastError = true)]
public static extern uint SetThreadExecutionState(uint esFlags);
'@ -ErrorAction SilentlyContinue
}
$ES_CONTINUOUS = [uint32]'0x80000000'
$ES_SYSTEM_REQUIRED = [uint32]'0x00000001'
$awake = $false

$runPhases = if ($Phases -contains 'all') { @('G1', 'G2', 'G3') } else { $Phases }
$done = [System.Collections.Generic.List[string]]::new()
$skipped = [System.Collections.Generic.List[string]]::new()
$failed = [System.Collections.Generic.List[string]]::new()
$watch = [Diagnostics.Stopwatch]::StartNew()
$script:ChosenContext = 0

function Stop-StrayServers {
    # -WhatIf must not touch the machine. This killed a running campaign's
    # llama-server on 2026-08-25 when a WhatIf plan of a second driver was run
    # beside a live one: the plan printed, and the finally block reaped a
    # process it had nothing to do with.
    if ($WhatIf) { return }
    foreach ($p in @(Get-Process llama-server, llama-bench -ErrorAction SilentlyContinue)) {
        try { Stop-Process -Id $p.Id -Force -ErrorAction Stop } catch { }
    }
    $spin = 0
    while (@(Get-Process llama-server, llama-bench -ErrorAction SilentlyContinue).Count -gt 0 -and $spin -lt 60) {
        Start-Sleep -Seconds 2; $spin++
    }
}

# --------------------------------------------------------------------------
# G1 - can the window be held at all, and with what headroom
# --------------------------------------------------------------------------
function Test-Admissible {
    param([Parameter(Mandatory = $true)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    try { $r = Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json } catch { return $null }
    return $r
}

function Invoke-Admission {
    param([Parameter(Mandatory = $true)][int]$Context)

    $label = "admission-ctx$Context"
    $outJson = Join-Path $CampaignRoot "admission\footprint-gemma4-12b-ctx$Context-kv$KV.json"
    if ($WhatIf) { Write-Step "PLAN $label"; return $null }

    if (-not (Test-Path -LiteralPath $outJson)) {
        Write-Step "--- begin $label"
        # The footprint script ends on a bare $report, which PowerShell writes to
        # the pipeline. Swallow it: a caller that lets it through gets an array
        # back from this function instead of the object it asked for.
        $null = & $footprint -RuntimeRoot $RuntimeRoot -ModelPath $ModelPath `
            -ContextTokens $Context -CacheTypeK $KV -CacheTypeV $KV `
            -UBatchSize $UBATCH -BatchSize $BATCH -NGpuLayers $GPU_LAYERS `
            -DeviceVramMib $DeviceVramMib -OutputPath $outJson -Quiet 2>&1
    }
    $report = Test-Admissible -Path $outJson
    if ($null -eq $report) {
        Write-Step "  $label : NO REPORT (see $outJson)"
        $failed.Add($label); return $null
    }
    $ok = $report.admissible
    Write-Step ("  {0} : admissible={1} headroom={2} MiB shared_margin={3} MiB pressure={4}" -f `
            $label, $ok, $report.headroom_mib, $report.shared_margin_mib, $report.gpu_pressure.state)
    $done.Add("$label (admissible=$ok)")
    return $report
}

# --------------------------------------------------------------------------
# G2/G3 - one server load each, at whatever context G1 cleared
# --------------------------------------------------------------------------
function Invoke-GemmaCell {
    param(
        [Parameter(Mandatory = $true)][int]$Context,
        [Parameter(Mandatory = $true)][string]$Kind,
        [Parameter(Mandatory = $true)][string]$Suites,
        [int[]]$Ramp = @()
    )

    $label = "gemma4-12b-$Kind-ctx$Context"
    $dir = if ($Kind -eq 'retention') { 'retention' } else { 'quality' }
    $outJson = Join-Path $CampaignRoot "$dir\qualify-$label.json"
    if (Test-Path -LiteralPath $outJson) { Write-Step "SKIP $label (already measured)"; $skipped.Add($label); return }
    if ($WhatIf) { Write-Step "PLAN $label  suites=$Suites  ramp=$($Ramp -join ',')"; return }

    Write-Step "--- begin $label | suites $Suites"
    $cell = [Diagnostics.Stopwatch]::StartNew()
    $splat = @{
        RuntimeRoot    = $RuntimeRoot
        ModelPath      = $ModelPath
        Label          = $label
        ContextTokens  = $Context
        CacheTypeK     = $KV
        CacheTypeV     = $KV
        UBatchSize     = $UBATCH
        BatchSize      = $BATCH
        NGpuLayers     = $GPU_LAYERS
        Suites         = $Suites
        DeviceVramMib  = $DeviceVramMib
        OutputRoot     = (Join-Path $CampaignRoot $dir)
        # The public profile's own output ceiling. Passing it explicitly is what
        # keeps the 2026-08-23 failure from recurring: that run left the request
        # cap at the harness default, the model spent the whole of it inside
        # reasoning_content, and a level scored 0/12 with an empty answer.
        NPredict       = 8192
    }
    if ($Ramp.Count -gt 0) { $splat['RetentionTokens'] = [int[]]$Ramp }
    & $qualify @splat 2>&1 | Tee-Object -FilePath (Join-Path $CampaignRoot "$dir\run-$label.log") | Out-Null
    $cell.Stop()
    if (Test-Path -LiteralPath $outJson) { $done.Add($label) } else { $failed.Add($label) }
    Write-Step ("--- end {0} after {1:N1} min" -f $label, $cell.Elapsed.TotalMinutes)
    Stop-StrayServers
}

# --------------------------------------------------------------------------
try {
    if (-not $WhatIf) {
        $prev = [Native.Gemma256Power]::SetThreadExecutionState($ES_CONTINUOUS -bor $ES_SYSTEM_REQUIRED)
        if ($prev -ne 0) { $awake = $true }
    }
    Write-Step "=== Gemma 4 12B 256k campaign; phases $($runPhases -join ',') ==="
    Stop-StrayServers

    if ($runPhases -contains 'G1') {
        Write-Step '### PHASE G1 - admission'
        foreach ($ctx in $ContextLadder) {
            $report = Invoke-Admission -Context $ctx
            if ($null -ne $report -and $report.admissible) {
                $script:ChosenContext = $ctx
                Write-Step "  largest admissible context: $ctx"
                break
            }
            Write-Step "  $ctx is not admissible; stepping down"
        }
        if ($script:ChosenContext -eq 0 -and -not $WhatIf) {
            Write-Step '  NO admissible context on the ladder; stopping'
        }
    }
    if ($script:ChosenContext -eq 0) { $script:ChosenContext = $ContextLadder[0] }

    if ($runPhases -contains 'G2') {
        Write-Step "### PHASE G2 - retention ramp at $($script:ChosenContext)"
        Invoke-GemmaCell -Context $script:ChosenContext -Kind 'retention' -Suites 'retention' -Ramp $RAMP
    }
    if ($runPhases -contains 'G3') {
        Write-Step "### PHASE G3 - quality at $($script:ChosenContext)"
        Invoke-GemmaCell -Context $script:ChosenContext -Kind 'quality' `
            -Suites 'chat,coding,hard,tools,literal_tools,json'
    }
}
finally {
    Stop-StrayServers
    if ($awake) {
        try { [void][Native.Gemma256Power]::SetThreadExecutionState($ES_CONTINUOUS) } catch { }
        Write-Step 'sleep suppression released'
    }
    $watch.Stop()
    $summary = "context={0} done={1} skipped={2} failed={3} elapsed={4:N2} h" -f `
        $script:ChosenContext, $done.Count, $skipped.Count, $failed.Count, $watch.Elapsed.TotalHours
    Write-Step "=== $summary ==="
    foreach ($f in $failed) { Write-Step "  FAILED: $f" }
    if (-not $WhatIf) {
        Set-Content -LiteralPath $donePath -Encoding UTF8 -Value (@(
                $summary
                'completed:'
                $done | ForEach-Object { "  $_" }
                'skipped:'
                $skipped | ForEach-Object { "  $_" }
                'failed:'
                $failed | ForEach-Object { "  $_" }
            ) -join [Environment]::NewLine)
    }
}
