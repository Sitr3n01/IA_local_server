<#
.SYNOPSIS
Starts one llama-server profile, runs the qualification battery against it while
sampling memory, and stops it.

.DESCRIPTION
One cell of the campaign matrix, end to end. The flag list mirrors
`New-V2LlamaServerCommand` in Common.ps1 — same order, same defaults — minus the
router credential and the log suppression, because a qualification run wants the
server log and production wants neither. If the two drift, a result measured here
stops describing what the manifest would actually serve, so the mirroring is
deliberate rather than incidental.

The HTTP request ceiling is derived, not defaulted. `-MaxTokens` wins when an
operator supplies one; otherwise a positive `-NPredict` -- the profile's own
generation contract -- becomes the ceiling; otherwise each suite keeps its
fixture default. Leaving that to the fixtures is what made the 2026-08-23
qwen38-27b-huge-256k run measure a 32768-token profile through an 8192-token
coding-fixture cap while the server had been told to spend up to 24576 tokens
thinking. A ceiling that cannot hold the profile's answer reserve is refused
before the model loads unless -ConstrainedRequestBudgetDiagnostic names the
constrained measurement as the point of the run.

Memory is sampled on a timer in a background job for the whole life of the
server, not just at load. The distinction matters on this workstation: dedicated
VRAM is set at load and barely moves, while shared GPU memory — the paging signal
— climbs as a long context is actually filled, and a load-time sample cannot see
it.

`-DryRun` resolves the budget, builds both command lines, prints them and stops
without loading anything, so the plan for a multi-hour cell can be reviewed --
and asserted on by a fast test -- before the night it costs hours.

.EXAMPLE
./Invoke-V2ProfileQualification.ps1 -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-Q3_K_XL.gguf `
    -Label q3kxl-32k-kv-q4 -ContextTokens 32768 -CacheTypeK q4_0 -CacheTypeV q4_0 `
    -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-256k
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$ModelPath,
    [Parameter(Mandatory = $true)][string]$Label,
    [Parameter(Mandatory = $true)][string]$OutputRoot,

    [string]$RuntimeRoot = 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14',
    [ValidateRange(1024, 1048576)][int]$ContextTokens = 32768,
    [ValidateSet('f16', 'q8_0', 'q4_0')][string]$CacheTypeK = 'q4_0',
    [ValidateSet('f16', 'q8_0', 'q4_0')][string]$CacheTypeV = 'q4_0',
    [string]$TensorOverride = '',
    [int]$UBatchSize = 288,
    [int]$BatchSize = 2048,
    [int]$NGpuLayers = 99,
    [int]$Threads = 8,
    [ValidateRange(-1, 1024)][int]$NCpuMoe = -1,
    [switch]$CpuMoe,
    [ValidateRange(1, 16)][int]$Parallel = 1,
    [int]$DeviceVramMib = 16304,
    [ValidateRange(1024, 65535)][int]$Port = 19399,
    [ValidateRange(30, 3600)][int]$StartupTimeoutSeconds = 900,
    [ValidateRange(1, 262144)][int]$NPredict = 0,
    [ValidateRange(-1, 262144)][int]$ReasoningBudget = -1,
    [string]$ReasoningBudgetMessage = '',

    [string]$Suites = 'coding,tools,json',
    [int[]]$RetentionTokens = @(),
    [string]$OnlyCodingTasks = '',
    [string]$OnlyToolTasks = '',
    [ValidateRange(0, 262144)][int]$MaxTokens = 0,
    [double]$Temperature = 0.0,
    [int]$Seed = 20260821,
    [ValidateSet('current', 'agentic')][string]$SystemPolicy = 'current',
    [switch]$StrictToolPolicy,
    [switch]$ToolSchemaPolicy,
    # Measuring a request cap that cannot hold the profile's own answer reserve
    # is a legitimate experiment and an illegitimate baseline. Naming it keeps
    # the two apart: without this switch the combination is refused before the
    # model loads, and with it the report is stamped as a diagnostic cell.
    [switch]$ConstrainedRequestBudgetDiagnostic,
    # Resolve and print everything this cell would run, then stop. Nothing is
    # loaded, no port is bound, no GPU is touched. It exists so the argument
    # vector and the generation budget of a multi-hour run can be reviewed --
    # and asserted on by a fast test -- before the night it costs hours.
    [switch]$DryRun,
    [switch]$CaptureDiagnostics,
    [ValidateRange(1, 30)][int]$SampleIntervalSeconds = 3
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($CpuMoe -and $NCpuMoe -ge 0) {
    throw 'CpuMoe and NCpuMoe are mutually exclusive.'
}

. (Join-Path $PSScriptRoot 'Telemetry.ps1')
. (Join-Path $PSScriptRoot 'Common.ps1')

$serverExe = Join-Path $RuntimeRoot 'llama-server.exe'
$qualify = Join-Path $PSScriptRoot 'eval\qualify.py'
foreach ($required in @($serverExe, $ModelPath, $qualify)) {
    if (Test-Path -LiteralPath $required) { continue }
    # A dry run is a review of what would happen, and it has to be reviewable on
    # a machine that holds neither the weights nor the runtime -- CI, for one.
    # The missing path is still reported, as a warning rather than a stop.
    if ($DryRun -and $required -ne $qualify) {
        Write-Warning ("Not present on this machine (dry run): {0}" -f $required)
        continue
    }
    throw "Required file is missing: $required"
}

if (-not $DryRun) { New-Item -ItemType Directory -Force -Path $OutputRoot | Out-Null }
$workdir = Join-Path $OutputRoot ("work-" + $Label)
$serverLog = Join-Path $OutputRoot ("server-" + $Label + ".log")
$evalOut = Join-Path $OutputRoot ("qualify-" + $Label + ".json")
$finalOut = Join-Path $OutputRoot ("profile-" + $Label + ".json")

if ($Suites -match 'retention' -and $RetentionTokens.Count -eq 0) {
    throw "Suites includes retention but -RetentionTokens was not given."
}

$modelSpec = New-V2BenchmarkModelSpec -ModelPath $ModelPath -Alias 'local' -ContextTokens $ContextTokens `
    -CacheTypeK $CacheTypeK -CacheTypeV $CacheTypeV -UBatchSize $UBatchSize -BatchSize $BatchSize `
    -NGpuLayers $NGpuLayers -Threads $Threads -Parallel $Parallel -TensorOverride $TensorOverride `
    -NCpuMoe $NCpuMoe -CpuMoe:$CpuMoe
if ($NPredict -gt 0) {
    $modelSpec | Add-Member -NotePropertyName 'n_predict' -NotePropertyValue $NPredict
}
if ($ReasoningBudget -ge 0) {
    $modelSpec | Add-Member -NotePropertyName 'reasoning_budget' -NotePropertyValue $ReasoningBudget
}
if (-not [string]::IsNullOrWhiteSpace($ReasoningBudgetMessage)) {
    if ($ReasoningBudget -le 0) {
        throw '-ReasoningBudgetMessage requires a positive -ReasoningBudget.'
    }
    $modelSpec | Add-Member -NotePropertyName 'reasoning_budget_message' -NotePropertyValue $ReasoningBudgetMessage
}
$arguments = New-V2LlamaServerArguments -Model $modelSpec -Port "$Port" -Alias 'local' `
    -IncludeJinja -IncludeWarmup -IncludeMetrics -IncludeNoWebui

# Resolved before the model is loaded, because the one failure this prevents --
# a request ceiling too small to hold the profile's own answer reserve -- costs
# hours of GPU time to discover and produces a report that measures the harness
# rather than the model.
$budget = Resolve-V2QualificationRequestBudget -ExplicitMaxTokens $MaxTokens -NPredict $NPredict `
    -ReasoningBudget $ReasoningBudget -Diagnostic:$ConstrainedRequestBudgetDiagnostic

$commandLine = ConvertTo-V2CommandLine -Arguments (@($serverExe) + @($arguments))
$moe = if ($CpuMoe) { 'all' } elseif ($NCpuMoe -ge 0) { [string]$NCpuMoe } else { 'default' }
Write-Host ("[{0}] ctx={1} kv={2}/{3} ub={4} split='{5}' cpu_moe={6}" -f $Label, $ContextTokens, $CacheTypeK, $CacheTypeV, $UBatchSize, $TensorOverride, $moe)
Write-Host ("  request max_tokens={0} ({1}) reasoning_budget={2} answer_reserve={3}{4}" -f `
    $(if ($budget.max_tokens -gt 0) { $budget.max_tokens } else { 'fixture default' }), `
    $budget.source, `
    $(if ($null -ne $budget.reasoning_budget) { $budget.reasoning_budget } else { 'none' }), `
    $(if ($null -ne $budget.answer_reserve) { $budget.answer_reserve } else { 'n/a' }), `
    $(if ($budget.diagnostic) { ' [CONSTRAINED DIAGNOSTIC]' } else { '' }))

# Built here rather than inside the try block so -DryRun prints the same vector
# the real run executes. Two constructions would let the reviewed command and
# the executed command differ, which is the class of defect this whole file has
# already paid for once.
$pyArgs = @(
    $qualify,
    '--base-url', "http://127.0.0.1:$Port",
    '--alias', 'local',
    '--label', $Label,
    '--workdir', $workdir,
    '--out', $evalOut,
    '--suites', $Suites
)
foreach ($t in $RetentionTokens) { $pyArgs += @('--retention-tokens', "$t") }
if ($OnlyCodingTasks) { $pyArgs += @('--only', $OnlyCodingTasks) }
if ($OnlyToolTasks) { $pyArgs += @('--only-tools', $OnlyToolTasks) }
# Always forwarded when a ceiling was resolved, not only when an operator typed
# one. Silence here is what let qwen38-27b-huge-256k benchmark its declared
# 32768-token contract through an 8192-token coding-fixture default.
if ($budget.max_tokens -gt 0) { $pyArgs += @('--max-tokens', "$($budget.max_tokens)") }
$pyArgs += @('--max-tokens-source', $budget.source)
if ($null -ne $budget.reasoning_budget) { $pyArgs += @('--reasoning-budget', "$($budget.reasoning_budget)") }
if ($ConstrainedRequestBudgetDiagnostic) { $pyArgs += '--allow-constrained-request-budget' }
$pyArgs += @('--temperature', "$Temperature", '--seed', "$Seed", '--system-policy', $SystemPolicy)
if ($StrictToolPolicy) { $pyArgs += '--strict-tool-policy' }
if ($ToolSchemaPolicy) { $pyArgs += '--tool-schema-policy' }
if ($CaptureDiagnostics) {
    $pyArgs += @('--capture-dir', (Join-Path $OutputRoot ("capture-" + $Label)))
}

if ($DryRun) {
    $plan = [ordered]@{
        schema_version   = 1
        scenario         = 'profile-qualification-dry-run'
        label            = $Label
        server_command   = $commandLine
        qualify_command  = ConvertTo-V2CommandLine -Arguments (@('python') + [string[]]$pyArgs)
        qualify_arguments = [string[]]$pyArgs
        request_budget   = [ordered]@{
            effective_max_tokens   = $(if ($budget.max_tokens -gt 0) { $budget.max_tokens } else { $null })
            source                 = $budget.source
            reasoning_budget       = $budget.reasoning_budget
            answer_reserve         = $budget.answer_reserve
            minimum_answer_reserve = $budget.minimum_answer_reserve
            reserve_ok             = $budget.reserve_ok
            constrained_diagnostic = $budget.diagnostic
        }
        suites           = $Suites
        retention_tokens = [int[]]$RetentionTokens
        started          = $false
    }
    return [pscustomobject]$plan
}

$idle = Get-V2MemorySample -ProcessId 0

$previousPath = $env:PATH
$previousHip = $env:HIP_VISIBLE_DEVICES
$env:PATH = "$RuntimeRoot;C:\Windows\System32\downlevel;$env:PATH"
$env:HIP_VISIBLE_DEVICES = '1'

$process = $null
$sampler = $null
$failure = $null
$loadSeconds = $null
$evalExit = $null
$samplePath = Join-Path $OutputRoot ("samples-" + $Label + ".jsonl")
if (Test-Path -LiteralPath $samplePath) { Remove-Item -LiteralPath $samplePath -Force }

try {
    $started = [Diagnostics.Stopwatch]::StartNew()
    # Windows PowerShell 5.1's Start-Process does not quote array elements that
    # contain embedded spaces before joining them into the child command line, so
    # a multi-word value (e.g. -ReasoningBudgetMessage) gets silently split into
    # multiple argv tokens and llama-server's parser chokes on the stray words.
    # ConvertTo-V2CommandLine already quotes correctly -- it's what builds the
    # display $commandLine above -- so reuse it to pass one pre-quoted string.
    $argumentLine = ConvertTo-V2CommandLine -Arguments $arguments
    $process = Start-Process -FilePath $serverExe -ArgumentList $argumentLine -PassThru `
        -RedirectStandardOutput $serverLog -RedirectStandardError ($serverLog + '.err') `
        -WindowStyle Hidden

    # The sampler runs for the whole session. A load-time-only sample cannot see
    # shared memory climbing as a long context is filled, which is the number
    # that decides whether a context level is servable.
    $sampler = Start-Job -ScriptBlock {
        param($telemetryPath, $pid_, $outPath, $interval)
        . $telemetryPath
        while ($true) {
            try {
                $s = Get-V2MemorySample -ProcessId $pid_
                ($s | ConvertTo-Json -Compress -Depth 4) | Add-Content -LiteralPath $outPath -Encoding UTF8
            }
            catch { }
            Start-Sleep -Seconds $interval
        }
    } -ArgumentList (Join-Path $PSScriptRoot 'Telemetry.ps1'), $process.Id, $samplePath, $SampleIntervalSeconds

    $deadline = [DateTime]::UtcNow.AddSeconds($StartupTimeoutSeconds)
    $ready = $false
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($process.HasExited) {
            throw "llama-server exited with code $($process.ExitCode) before becoming ready. See $serverLog.err"
        }
        try {
            $health = Invoke-RestMethod -Uri "http://127.0.0.1:$Port/health" -TimeoutSec 5
            if ($health.status -eq 'ok') { $ready = $true; break }
        }
        catch { Start-Sleep -Seconds 2 }
    }
    $started.Stop()
    if (-not $ready) { throw "llama-server did not become ready within $StartupTimeoutSeconds seconds." }
    $loadSeconds = [Math]::Round($started.Elapsed.TotalSeconds, 1)
    Write-Host ("  loaded in {0}s" -f $loadSeconds)

    & python @pyArgs
    $evalExit = $LASTEXITCODE
}
catch {
    $failure = $_.Exception.Message
    Write-Warning ("  FAILED: {0}" -f $failure)
}
finally {
    if ($null -ne $sampler) {
        Stop-Job -Job $sampler -ErrorAction SilentlyContinue
        Remove-Job -Job $sampler -Force -ErrorAction SilentlyContinue
    }
    if ($null -ne $process -and -not $process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        $process.WaitForExit(30000) | Out-Null
    }
    $env:PATH = $previousPath
    if ($null -eq $previousHip) { Remove-Item Env:\HIP_VISIBLE_DEVICES -ErrorAction SilentlyContinue }
    else { $env:HIP_VISIBLE_DEVICES = $previousHip }
}

# Reduce the sample stream to the peaks the manifest and the abort criteria use.
$peak = $null
$sampleCount = 0
if (Test-Path -LiteralPath $samplePath) {
    $rows = @(Get-Content -LiteralPath $samplePath | ForEach-Object {
            try { $_ | ConvertFrom-Json } catch { $null }
        } | Where-Object { $null -ne $_ })
    $sampleCount = $rows.Count
    if ($sampleCount -gt 0) {
        function Peak([string]$name) {
            $vals = @($rows | ForEach-Object { $_.$name } | Where-Object { $null -ne $_ })
            if ($vals.Count -eq 0) { return $null }
            return ($vals | Measure-Object -Maximum).Maximum
        }
        $peak = [ordered]@{
            vram_dedicated_mib  = Peak 'vram_dedicated_mib'
            vram_shared_mib     = Peak 'vram_shared_mib'
            process_vram_mib    = Peak 'process_vram_mib'
            process_ws_gib      = Peak 'process_ws_gib'
            process_private_gib = Peak 'process_private_gib'
            commit_gib          = Peak 'commit_gib'
            physical_used_gib   = Peak 'physical_used_gib'
        }
    }
}

$pressure = $null
if ($null -ne $peak -and $null -ne $peak.vram_dedicated_mib) {
    $pressure = Test-V2GpuMemoryPressure -TotalMib $DeviceVramMib -Sample ([pscustomobject]@{
            instance      = 'session-peak'
            dedicated_mib = $peak.vram_dedicated_mib
            shared_mib    = $(if ($null -ne $peak.vram_shared_mib) { $peak.vram_shared_mib } else { 0 })
        })
}

$evalReport = $null
if (Test-Path -LiteralPath $evalOut) {
    try { $evalReport = Get-Content -LiteralPath $evalOut -Raw | ConvertFrom-Json } catch { }
}

$report = [ordered]@{
    schema_version = 1
    scenario       = 'profile-qualification'
    label          = $Label
    started_utc    = [DateTime]::UtcNow.ToString('o')
    configuration  = [ordered]@{
        model_path      = $ModelPath
        model_bytes     = (Get-Item -LiteralPath $ModelPath).Length
        runtime_root    = $RuntimeRoot
        context_tokens  = $ContextTokens
        cache_type_k    = $CacheTypeK
        cache_type_v    = $CacheTypeV
        ubatch_size     = $UBatchSize
        batch_size      = $BatchSize
        gpu_layers      = $NGpuLayers
        threads         = $Threads
        parallel        = $Parallel
        tensor_override = $TensorOverride
        n_predict       = $(if ($NPredict -gt 0) { $NPredict } else { $null })
        reasoning_budget = $(if ($ReasoningBudget -ge 0) { $ReasoningBudget } else { $null })
        reasoning_budget_message = $(if (-not [string]::IsNullOrWhiteSpace($ReasoningBudgetMessage)) { $ReasoningBudgetMessage } else { $null })
        cpu_moe         = [bool]$CpuMoe
        n_cpu_moe       = $(if ($NCpuMoe -ge 0) { $NCpuMoe } else { $null })
        device_vram_mib = $DeviceVramMib
        max_tokens      = $(if ($MaxTokens -gt 0) { $MaxTokens } else { $null })
        # What the battery was actually allowed to generate, and why. A future
        # reader must be able to tell a NO_ANSWER produced under the deployed
        # contract from one produced under a benchmark cap the deployment would
        # never impose.
        request_budget  = [ordered]@{
            effective_max_tokens   = $(if ($budget.max_tokens -gt 0) { $budget.max_tokens } else { $null })
            source                 = $budget.source
            reasoning_budget       = $budget.reasoning_budget
            answer_reserve         = $budget.answer_reserve
            minimum_answer_reserve = $budget.minimum_answer_reserve
            reserve_ok             = $budget.reserve_ok
            constrained_diagnostic = $budget.diagnostic
        }
        temperature     = $Temperature
        seed            = $Seed
        system_policy   = $SystemPolicy
        strict_tool_policy = [bool]$StrictToolPolicy
        tool_schema_policy = [bool]$ToolSchemaPolicy
        constrained_request_budget_diagnostic = [bool]$ConstrainedRequestBudgetDiagnostic
        capture_diagnostics = [bool]$CaptureDiagnostics
        command_line    = $commandLine
    }
    load_seconds   = $loadSeconds
    idle           = [ordered]@{
        vram_dedicated_mib = $idle.vram_dedicated_mib
        vram_shared_mib    = $idle.vram_shared_mib
        commit_gib         = $idle.commit_gib
        physical_used_gib  = $idle.physical_used_gib
    }
    session_peak   = $peak
    marginal_vram_mib = $(if ($null -ne $peak -and $null -ne $idle.vram_dedicated_mib -and $null -ne $peak.vram_dedicated_mib) {
            [Math]::Round($peak.vram_dedicated_mib - $idle.vram_dedicated_mib, 1)
        } else { $null })
    memory_samples = $sampleCount
    gpu_pressure   = $pressure
    eval_exit_code = $evalExit
    failure        = $failure
    eval           = $evalReport
}

$report | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $finalOut -Encoding UTF8
Write-Host ("  -> {0}" -f $finalOut)
if ($null -ne $peak) {
    Write-Host ("  peak: dedicated {0:N0} MiB | shared {1:N0} MiB | ws {2:N2} GiB | private {3:N2} GiB | {4}" -f `
            $peak.vram_dedicated_mib, $peak.vram_shared_mib, $peak.process_ws_gib, $peak.process_private_gib, `
        $(if ($null -ne $pressure) { $pressure.state } else { 'unclassified' }))
}
$report
