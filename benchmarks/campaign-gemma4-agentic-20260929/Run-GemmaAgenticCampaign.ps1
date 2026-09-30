<#
.SYNOPSIS
Decides whether the Gemma 4 12B Agentic fine-tune can be offered at 256k on this
machine, and where its retention stops holding.

.DESCRIPTION
gemma4-12b-agentic-q4km-256k is yuxinlu1's agentic fine-tune of google/gemma-4-12B-it
(Q4_K_M). Its model card recommends a 16,384-token window and states no training
sequence length; the GGUF header still declares n_ctx_train 262144, inherited from
the base. Whether the fine-tune remembers anything useful at 64k, 120k or 240k is
therefore an open question, and this campaign exists to answer it with numbers
rather than with the card.

It mirrors campaign-gemma4-agentic-20260929 on purpose, so the two models are
directly comparable: same runtime, same KV type, same suites, same output ceiling.
The differences are the weights and a finer retention ramp below 64k, where the
author's own recommendation sits.

  G1  admission    Measure-V2ContextFootprint at 262144. Two minutes. Also
                   replaces the provisional resources in config/models.yaml.
  G2  retention    Ramp over 8192 / 16384 / 32768 / 65536 / 120000 / 196608 /
                   240000, so the first level that fails is visible.
  G3  quality      chat, coding, hard, tools, literal_tools, json.

Running the tools suite does NOT license flipping capabilities.function_calling;
see docs/MODEL_PROMOTION.md.

.NOTES
Resumable: a cell whose output JSON exists is skipped.
#>
[CmdletBinding()]
param(
    [string]$CampaignRoot = 'C:\IA\IA_local_server\benchmarks\campaign-gemma4-agentic-20260929',
    [string]$RepoRoot = 'C:\IA\IA_local_server',
    # The runtime the profile declares. The QAT Gemma 256k campaign ran on the
    # same one, so a difference here is the weights and not the engine.
    [string]$RuntimeRoot = 'C:\Users\Sitr3n\.unsloth\llama.cpp\build\bin\Release',
    [string]$ModelPath = 'C:\IA\models\gemma-4-12B-agentic-GGUF\gemma4-v2-Q4_K_M.gguf',
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
$logPath = Join-Path $CampaignRoot 'gemma-agentic-driver.log'
$donePath = Join-Path $CampaignRoot 'gemma-agentic-driver.DONE'
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
$RAMP = @(8192, 16384, 32768, 65536, 120000, 196608, 240000)

$footprint = Join-Path $RepoRoot 'scripts\v2\Measure-V2ContextFootprint.ps1'
$qualify = Join-Path $RepoRoot 'scripts\v2\Invoke-V2ProfileQualification.ps1'

foreach ($dir in @('admission', 'retention', 'quality')) {
    New-Item -ItemType Directory -Force -Path (Join-Path $CampaignRoot $dir) | Out-Null
}

if (-not ('Native.GemmaAgenticPower' -as [type])) {
    Add-Type -Namespace Native -Name GemmaAgenticPower -MemberDefinition @'
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
# The footprint report states what it measured; it does not judge. The judgement
# is the project's admission policy, and this is the same predicate the Ornith
# census used, so a cell called admissible here means the same thing it meant
# there. Returns $null for "cannot say", which is deliberately different from
# $false: a missing or unparseable report must not read as a model that fits
# nowhere.
function Test-Admissible {
    param([Parameter(Mandatory = $true)][string]$Path,
        [int]$VramReserveMib = 1024,
        [int]$ReserveMarginMib = 512,
        [int]$SharedMarginMib = 400)
    if (-not (Test-Path -LiteralPath $Path)) {
        Write-Step "  (admissibility: no report at $Path)"
        return $null
    }
    try { $r = Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json }
    catch { Write-Step "  (admissibility: unparseable report)"; return $null }
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

function Invoke-Admission {
    param([Parameter(Mandatory = $true)][int]$Context)

    $label = "admission-ctx$Context"
    $outJson = Join-Path $CampaignRoot "admission\footprint-gemma4-12b-agentic-ctx$Context-kv$KV.json"
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
    $ok = Test-Admissible -Path $outJson
    if ($null -eq $ok) {
        Write-Step "  $label : CANNOT SAY (no usable report at $outJson)"
        $failed.Add($label); return $null
    }
    # Report the figures the verdict was reached on, not just the verdict.
    $r = Get-Content -LiteralPath $outJson -Raw -Encoding UTF8 | ConvertFrom-Json
    $headroom = $DeviceVramMib - [double]$r.peak.vram_dedicated_mib
    $sharedDelta = [double]$r.peak.vram_shared_mib - [double]$r.idle.vram_shared_mib
    Write-Step ("  {0} : admissible={1}  peak_vram={2} MiB  headroom={3} MiB  shared_delta={4} MiB  pressure={5}  load={6}s" -f `
            $label, $ok, $r.peak.vram_dedicated_mib, [math]::Round($headroom, 1),
            [math]::Round($sharedDelta, 1), $r.gpu_pressure.state, $r.load_seconds)
    $done.Add("$label (admissible=$ok)")
    return $ok
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

    $label = "gemma4-12b-agentic-$Kind-ctx$Context"
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
        $prev = [Native.GemmaAgenticPower]::SetThreadExecutionState($ES_CONTINUOUS -bor $ES_SYSTEM_REQUIRED)
        if ($prev -ne 0) { $awake = $true }
    }
    Write-Step "=== Gemma 4 12B Agentic campaign; phases $($runPhases -join ',') ==="
    Stop-StrayServers

    if ($runPhases -contains 'G1') {
        Write-Step '### PHASE G1 - admission'
        foreach ($ctx in $ContextLadder) {
            $ok = Invoke-Admission -Context $ctx
            if ($ok -eq $true) {
                $script:ChosenContext = $ctx
                Write-Step "  largest admissible context: $ctx"
                break
            }
            Write-Step "  $ctx is not admissible; stepping down"
        }
        if ($script:ChosenContext -eq 0 -and -not $WhatIf) {
            Write-Step '  NO admissible context on the ladder; stopping'
            # Nothing on the ladder holds, so there is no cell to measure. Fall
            # through with 0 rather than defaulting to the top of the ladder,
            # which would run a suite against a window just judged unusable.
            return
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
        try { [void][Native.GemmaAgenticPower]::SetThreadExecutionState($ES_CONTINUOUS) } catch { }
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
