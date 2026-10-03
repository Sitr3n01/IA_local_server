<#
.SYNOPSIS
The head-to-head that decides which MoE stays: Qwen3.6-35B-A3B UD-Q2_K_XL
against Ornith-1.5-35B-A3B IQ2_M.

.DESCRIPTION
The 2026-08-24 campaign compared three quantizations of each model across a
placement census, a throughput sweep, retention and an MTP A/B. It answered
"which rung of which ladder" and left the last question open, because the two
finalists were never measured against each other under identical conditions on
the dimensions that decide a replacement.

This is not that campaign again. It is one pair of artifacts, four phases, and
nothing measured twice that the existing evidence already establishes.

  A  contract    12 probes per model through edge_contract.py, against a direct
                 llama-server. This is the hard gate. Ornith failed it on
                 2026-08-24 with HTTP 500 on `system -> developer -> user`, and
                 runs here with config/chat-templates/ornith15-35b-a3b.jinja.

  B  quality     chat, coding, hard, tools, literal_tools, json at 128k, three
                 seeds per model. Three because the 2026-08-24 run was n=1 and
                 the cases that separated the models -- hard_go_retry_multifile,
                 hard_missing_info, tool_pick_search_many_nested, literal_regex
                 -- are exactly the ones a single sample cannot be trusted on.
                 Qwen3.6 Q2_K_XL has never had this suite at all; its only
                 quality evidence is coding/tools/json at 32k.

  D  performance prefill and decode-at-occupancy at 120k and 240k, three
                 repetitions, with the adapter sampled throughout. Decode
                 against an empty context is not the question -- the 2026-08-24
                 report had to walk back a +32% empty-context reading that
                 became +3-6% under load, and two negative cells at 240k.

Phase C, retention, is deliberately absent. Both models already have a full
retention ramp from this harness, on this hardware, at both 131072 and 262144,
and both scored 1.00 across 720 probes. Re-running it would cost four hours to
reproduce a tie. The template change does not invalidate that evidence:
test_chat_template_contract.py asserts the patched template renders
byte-identical prompts for every message shape upstream already accepted, and
the retention suite sends no developer message.

Placement is each model's own smallest admissible --n-cpu-moe from its own
census, not a shared number. The two do not need the same offload to hold the
same window, and forcing them to would measure the offload rather than the
model. Everything else -- runtime, KV type, context, batch, ubatch, threads,
sampling, seeds, prompts, grading -- is identical by construction.

No speculative decoding. The 2026-08-24 A/B found the MTP head paid in one cell
of nine and cost VRAM in all of them.

.NOTES
Resumable: every phase skips a cell whose output JSON already exists, so an
interrupted run continues where it stopped rather than starting over.
#>
[CmdletBinding()]
param(
    [string]$CampaignRoot = 'C:\IA\IA_local_server\benchmarks\campaign-final-moe-20260825',
    [string]$RepoRoot = 'C:\IA\IA_local_server',
    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [int]$DeviceVramMib = 16304,
    [ValidateSet('A', 'B', 'D', 'all')][string[]]$Phases = @('all'),
    [int[]]$Seeds = @(20260821, 20260825, 20260826),
    [switch]$WhatIf
)

$ErrorActionPreference = 'Continue'
Set-StrictMode -Version Latest

$logPath = Join-Path $CampaignRoot 'final-moe-driver.log'
$donePath = Join-Path $CampaignRoot 'final-moe-driver.DONE'
New-Item -ItemType Directory -Force -Path $CampaignRoot | Out-Null
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

# --------------------------------------------------------------------------
# the two finalists
# --------------------------------------------------------------------------
$MODELS = @(
    [pscustomobject]@{
        id       = 'qwen36-q2kxl'
        path     = 'C:\IA\models\Qwen3.6-35B-A3B-GGUF\Qwen3.6-35B-A3B-UD-Q2_K_XL.gguf'
        template = ''          # its unsloth build already handles the developer role
        moe128   = 4
        moe256   = 8
    }
    [pscustomobject]@{
        id       = 'ornith15-iq2m'
        path     = 'C:\IA\models\Ornith-1.5-35B-A3B-GGUF\Ornith-1.5-35B-A3B-IQ2_M.gguf'
        template = 'C:\IA\IA_local_server\config\chat-templates\ornith15-35b-a3b.jinja'
        moe128   = 2
        moe256   = 6
    }
)

$CTX_AGENT = 131072
$CTX_HUGE = 262144
$KV = 'q4_0'
$UBATCH = 288
$BATCH = 2048
$THREADS = 8

$qualify = Join-Path $RepoRoot 'scripts\v2\Invoke-V2ProfileQualification.ps1'
$sweep = Join-Path $RepoRoot 'scripts\v2\Invoke-V2ThroughputSweep.ps1'
$contract = Join-Path $RepoRoot 'scripts\v2\eval\edge_contract.py'
$serverExe = Join-Path $RuntimeRoot 'llama-server.exe'

foreach ($dir in @('contract', 'quality', 'perf')) {
    New-Item -ItemType Directory -Force -Path (Join-Path $CampaignRoot $dir) | Out-Null
}

# Keep the box awake for the duration without touching the user's power scheme:
# this is a per-process assertion, released in the finally block below.
if (-not ('Native.FinalMoePower' -as [type])) {
    Add-Type -Namespace Native -Name FinalMoePower -MemberDefinition @'
[DllImport("kernel32.dll", SetLastError = true)]
public static extern uint SetThreadExecutionState(uint esFlags);
'@ -ErrorAction SilentlyContinue
}
$ES_CONTINUOUS = [uint32]'0x80000000'
$ES_SYSTEM_REQUIRED = [uint32]'0x00000001'
$awake = $false

$runPhases = if ($Phases -contains 'all') { @('A', 'B', 'D') } else { $Phases }
$done = [System.Collections.Generic.List[string]]::new()
$skipped = [System.Collections.Generic.List[string]]::new()
$failed = [System.Collections.Generic.List[string]]::new()
$watch = [Diagnostics.Stopwatch]::StartNew()

function Stop-StrayServers {
    # -WhatIf must not touch the machine. This killed a running campaign's
    # llama-server on 2026-08-25 when a WhatIf plan of a second driver was run
    # beside a live one: the plan printed, and the finally block reaped a
    # process it had nothing to do with.
    if ($WhatIf) { return }
    $stray = @(Get-Process llama-server, llama-bench -ErrorAction SilentlyContinue)
    foreach ($p in $stray) {
        Write-Step "  stopping stray $($p.ProcessName) pid $($p.Id)"
        try { Stop-Process -Id $p.Id -Force -ErrorAction Stop } catch { }
    }
    $spin = 0
    while (@(Get-Process llama-server, llama-bench -ErrorAction SilentlyContinue).Count -gt 0 -and $spin -lt 60) {
        Start-Sleep -Seconds 2; $spin++
    }
}

# --------------------------------------------------------------------------
# Phase A - contract gate
# --------------------------------------------------------------------------
function Invoke-ContractGate {
    param([Parameter(Mandatory = $true)][object]$Model)

    $label = "contract-$($Model.id)"
    $outJson = Join-Path $CampaignRoot "contract\$label.json"
    if (Test-Path -LiteralPath $outJson) {
        Write-Step "SKIP $label (already measured)"
        $skipped.Add($label); return
    }
    if (-not (Test-Path -LiteralPath $Model.path)) {
        Write-Step "SKIP $label (artifact absent)"; $skipped.Add("$label (artifact absent)"); return
    }
    if ($WhatIf) { Write-Step "PLAN $label"; return }

    Write-Step "--- begin $label"
    $port = 19407
    $spec = New-V2BenchmarkModelSpec -ModelPath $Model.path -Alias 'local' -ContextTokens 32768 `
        -CacheTypeK $KV -CacheTypeV $KV -UBatchSize $UBATCH -BatchSize $BATCH `
        -NGpuLayers 99 -Threads $THREADS -Parallel 1 -NCpuMoe 0 `
        -ChatTemplateFile $Model.template
    $arguments = New-V2LlamaServerArguments -Model $spec -Port "$port" -Alias 'local' `
        -IncludeJinja -IncludeWarmup -IncludeMetrics -IncludeNoWebui

    # Windows PowerShell 5.1's Start-Process does not quote array elements that
    # contain spaces, so hand it one already-quoted string.
    $argumentLine = ConvertTo-V2CommandLine -Arguments $arguments
    $serverLog = Join-Path $CampaignRoot "contract\server-$label.log"
    Set-Content -LiteralPath (Join-Path $CampaignRoot "contract\command-$label.txt") `
        -Value (ConvertTo-V2CommandLine -Arguments (@($serverExe) + @($arguments))) -Encoding UTF8

    # Device isolation is not optional on b10549: it enumerates the integrated
    # gfx1036 first, so --device ROCm0 without this selects the iGPU. rocBLAS
    # then dies looking for a gfx1036 TensileLibrary the moment the first token
    # is decoded -- the server starts, answers /health, and drops the first real
    # request. Phases B and D get this from the scripts they delegate to; this
    # phase starts its own server and has to set it itself.
    $previousPath = $env:PATH
    $previousHip = $env:HIP_VISIBLE_DEVICES
    $env:PATH = "$RuntimeRoot;C:\Windows\System32\downlevel;$env:PATH"
    $env:HIP_VISIBLE_DEVICES = '1'

    $process = Start-Process -FilePath $serverExe -ArgumentList $argumentLine -PassThru `
        -RedirectStandardOutput $serverLog -RedirectStandardError "$serverLog.err" `
        -WindowStyle Hidden
    try {
        $ready = $false
        for ($i = 0; $i -lt 900; $i++) {
            if ($process.HasExited) { break }
            try {
                $health = Invoke-WebRequest -Uri "http://127.0.0.1:$port/health" -TimeoutSec 5 -UseBasicParsing
                if ($health.StatusCode -eq 200) { $ready = $true; break }
            }
            catch { }
            Start-Sleep -Seconds 1
        }
        if (-not $ready) {
            Write-Step "  FAIL $label : server never became ready (see $serverLog.err)"
            $failed.Add($label); return
        }
        Write-Step "  server ready; running 12 contract probes"
        & python $contract --base-url "http://127.0.0.1:$port" --alias 'local' `
            --label $label --out $outJson --max-tokens 2048 --timeout 600 2>&1 |
            Tee-Object -FilePath (Join-Path $CampaignRoot "contract\run-$label.log") | Out-Null
        if (Test-Path -LiteralPath $outJson) {
            $r = Get-Content -LiteralPath $outJson -Raw -Encoding UTF8 | ConvertFrom-Json
            $passed = @($r.probes.PSObject.Properties | Where-Object { $_.Value.passed -eq $true }).Count
            $total = @($r.probes.PSObject.Properties).Count
            Write-Step "  $label : $passed/$total probes passed"
            $done.Add("$label ($passed/$total)")
        }
        else {
            Write-Step "  FAIL $label : no report written"; $failed.Add($label)
        }
    }
    finally {
        if (-not $process.HasExited) { try { Stop-Process -Id $process.Id -Force } catch { } }
        Stop-StrayServers
        $env:PATH = $previousPath
        if ($null -eq $previousHip) { Remove-Item Env:\HIP_VISIBLE_DEVICES -ErrorAction SilentlyContinue }
        else { $env:HIP_VISIBLE_DEVICES = $previousHip }
    }
    Write-Step "--- end $label"
}

# --------------------------------------------------------------------------
# Phase B - quality, three seeds per model
# --------------------------------------------------------------------------
function Invoke-QualityCell {
    param([Parameter(Mandatory = $true)][object]$Model, [Parameter(Mandatory = $true)][int]$Seed)

    $label = "quality-$($Model.id)-ctx131072-seed$Seed"
    $outJson = Join-Path $CampaignRoot "quality\qualify-$label.json"
    if (Test-Path -LiteralPath $outJson) { Write-Step "SKIP $label (already measured)"; $skipped.Add($label); return }
    if ($WhatIf) { Write-Step "PLAN $label"; return }

    Write-Step "--- begin $label | ctx $CTX_AGENT | n_cpu_moe $($Model.moe128)"
    $cell = [Diagnostics.Stopwatch]::StartNew()
    & $qualify -RuntimeRoot $RuntimeRoot -ModelPath $Model.path -Label $label `
        -ContextTokens $CTX_AGENT -CacheTypeK $KV -CacheTypeV $KV `
        -UBatchSize $UBATCH -BatchSize $BATCH -Threads $THREADS `
        -NCpuMoe $Model.moe128 -ChatTemplateFile $Model.template `
        -Suites 'chat,coding,hard,tools,literal_tools,json' -Seed $Seed `
        -DeviceVramMib $DeviceVramMib -OutputRoot (Join-Path $CampaignRoot 'quality') 2>&1 |
        Tee-Object -FilePath (Join-Path $CampaignRoot "quality\run-$label.log") | Out-Null
    $cell.Stop()
    if (Test-Path -LiteralPath $outJson) { $done.Add($label) } else { $failed.Add($label) }
    Write-Step ("--- end {0} after {1:N1} min" -f $label, $cell.Elapsed.TotalMinutes)
    Stop-StrayServers
}

# --------------------------------------------------------------------------
# Phase D - prefill and decode with the context actually occupied
# --------------------------------------------------------------------------
function Invoke-PerfCell {
    param(
        [Parameter(Mandatory = $true)][object]$Model,
        [Parameter(Mandatory = $true)][int]$Context,
        [Parameter(Mandatory = $true)][int]$CpuMoeLayers,
        [Parameter(Mandatory = $true)][int]$Depth
    )

    $label = "perf-$($Model.id)-ctx$Context-d$Depth"
    $outJson = Join-Path $CampaignRoot "perf\throughput-$label.json"
    if (Test-Path -LiteralPath $outJson) { Write-Step "SKIP $label (already measured)"; $skipped.Add($label); return }
    if ($WhatIf) { Write-Step "PLAN $label"; return }

    Write-Step "--- begin $label | n_cpu_moe $CpuMoeLayers | 3 repetitions"
    $cell = [Diagnostics.Stopwatch]::StartNew()
    & $sweep -RuntimeRoot $RuntimeRoot -ModelPath $Model.path -Label $label `
        -ContextTokens $Context -CacheTypeK $KV -CacheTypeV $KV `
        -UBatchSize $UBATCH -BatchSize $BATCH -Threads $THREADS `
        -NCpuMoe $CpuMoeLayers -Repetitions 3 -DeviceVramMib $DeviceVramMib `
        -Tests @('pp:8192', "tg:128@$Depth") `
        -OutputRoot (Join-Path $CampaignRoot 'perf') 2>&1 |
        Tee-Object -FilePath (Join-Path $CampaignRoot "perf\run-$label.log") | Out-Null
    $cell.Stop()
    if (Test-Path -LiteralPath $outJson) { $done.Add($label) } else { $failed.Add($label) }
    Write-Step ("--- end {0} after {1:N1} min" -f $label, $cell.Elapsed.TotalMinutes)
    Stop-StrayServers
}

# --------------------------------------------------------------------------
try {
    if (-not $WhatIf) {
        $prev = [Native.FinalMoePower]::SetThreadExecutionState($ES_CONTINUOUS -bor $ES_SYSTEM_REQUIRED)
        if ($prev -ne 0) { $awake = $true }
    }
    . (Join-Path $RepoRoot 'scripts\v2\Common.ps1')
    Write-Step "=== final MoE head-to-head; phases $($runPhases -join ',') ==="
    Stop-StrayServers

    if ($runPhases -contains 'A') {
        Write-Step '### PHASE A - contract gate (hard gate)'
        foreach ($m in $MODELS) { Invoke-ContractGate -Model $m }
    }
    if ($runPhases -contains 'B') {
        Write-Step '### PHASE B - quality, three seeds per model'
        # Interleaved by seed rather than grouped by model, so a run that is cut
        # short still has both models at the same number of samples.
        foreach ($seed in $Seeds) {
            foreach ($m in $MODELS) { Invoke-QualityCell -Model $m -Seed $seed }
        }
    }
    if ($runPhases -contains 'D') {
        Write-Step '### PHASE D - prefill and decode at occupancy'
        foreach ($m in $MODELS) {
            Invoke-PerfCell -Model $m -Context $CTX_AGENT -CpuMoeLayers $m.moe128 -Depth 120000
        }
        foreach ($m in $MODELS) {
            Invoke-PerfCell -Model $m -Context $CTX_HUGE -CpuMoeLayers $m.moe256 -Depth 240000
        }
    }
}
finally {
    Stop-StrayServers
    if ($awake) {
        try { [void][Native.FinalMoePower]::SetThreadExecutionState($ES_CONTINUOUS) } catch { }
        Write-Step 'sleep suppression released'
    }
    $watch.Stop()
    $summary = "done={0} skipped={1} failed={2} elapsed={3:N2} h" -f `
        $done.Count, $skipped.Count, $failed.Count, $watch.Elapsed.TotalHours
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
