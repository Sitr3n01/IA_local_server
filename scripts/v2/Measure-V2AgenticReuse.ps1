<#
.SYNOPSIS
Measures whether a running llama-server reuses an accumulated context across an
agentic multi-turn conversation, or re-prefills it every turn.

.DESCRIPTION
This is the acceptance test for context checkpoints on a hybrid/recurrent model,
and it is a regression gate rather than an observation. Two identical prompts are
not sufficient evidence: real harness traffic grows a large context by small
increments interleaved with tool calls, and that is the pattern checkpoints have
to survive.

Per turn it records how many prompt tokens the server actually processed against
how many were genuinely new, taken from the server's own `timings` counters
rather than derived from a rate. A healthy run processes roughly the increment. A
broken one reprocesses the whole conversation, which is the failure llama.cpp
issues #24055 and #22384 describe and the reason this repository will not adopt a
runtime on the strength of a patch being present.

Nothing here is a benchmark of decode speed. Decode throughput is recorded
because it is free to record, but a runtime that decodes quickly while
re-prefilling 200k tokens a turn has failed this test.

The fixture is synthetic and deterministic: a seeded filler drawn from a fixed
vocabulary, so no real prompt is ever sent or stored. The report contains token
counts, timings, memory samples, and hashes only, per docs/BENCHMARKS.md.

.EXAMPLE
./Measure-V2AgenticReuse.ps1 -BaseUrl http://127.0.0.1:19300 -Model qwen38-27b-buun `
    -RuntimeLabel buun -ApiKeyFile C:\IA\local-ai-v2\state\router-api-key.txt
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$BaseUrl,

    [Parameter(Mandatory = $true)]
    [string]$Model,

    # Identifies which side of the A/B this report is. Compare-V2Runtimes.ps1
    # refuses to compare two reports carrying the same label.
    [Parameter(Mandatory = $true)]
    [string]$RuntimeLabel,

    [string]$ApiKeyFile,

    # Approximate size of the standing context before the first increment. Gate B
    # in docs/BENCHMARKS.md starts at 60k; gates C and D repeat at 128k, 192k and
    # ~256k. The achieved size is measured, not assumed.
    [ValidateRange(1024, 400000)]
    [int]$BaseContextTokens = 60000,

    [ValidateRange(128, 32768)]
    [int]$IncrementTokens = 2000,

    [ValidateRange(2, 64)]
    [int]$Turns = 6,

    [ValidateRange(16, 4096)]
    [int]$MaxOutputTokens = 128,

    # A turn after the first fails when the server processes more than this
    # fraction of the conversation it was handed. It is deliberately generous:
    # the failure being caught is a full re-prefill, and a threshold tuned finely
    # enough to argue about would be a magic number rather than a gate.
    [ValidateRange(0.05, 0.95)]
    [double]$ReprefillFraction = 0.5,

    # Process to sample memory from. Without it the memory columns are null, which
    # is the honest result: this script never estimates a measurement.
    [int]$ServerProcessId = 0,

    # The adapter's dedicated budget, so the GPU pressure verdict is computed
    # against the same number the edge admission gate uses
    # (runtimes[].device.vram_mib). Left at 0 the report still carries the raw
    # dedicated and shared samples and simply does not classify them, rather than
    # inventing a budget from a driver-reported total that Windows truncates.
    [ValidateRange(0, 1048576)]
    [int]$DeviceVramMib = 0,

    [int]$Seed = 20260817,

    [int]$TimeoutSeconds = 900,

    [string]$OutputPath,

    [switch]$Quiet
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# Host and GPU sampling lives in Telemetry.ps1 so this script and the memory
# profiler measure the same quantities the same way. It replaced three local
# helpers that read localized Get-Counter paths; see that file's header.
. (Join-Path $PSScriptRoot 'Telemetry.ps1')

# Peaks skip nulls rather than treating them as zero. A turn whose sample failed
# must not lower a maximum, and a run where every sample failed must report null
# rather than 0 - the manifest reads these values, and a zero would look like a
# measured model with no footprint.
function Get-PeakMemoryValue {
    param(
        [Parameter(Mandatory = $true)][AllowEmptyCollection()][object[]]$Turns,
        [Parameter(Mandatory = $true)][string]$Field
    )
    $values = @($Turns | ForEach-Object { $_.memory.$Field } | Where-Object { $null -ne $_ })
    if ($values.Count -eq 0) { return $null }
    return ($values | Measure-Object -Maximum).Maximum
}

function Get-PeakMemoryGiBFromMib {
    param(
        [Parameter(Mandatory = $true)][AllowEmptyCollection()][object[]]$Turns,
        [Parameter(Mandatory = $true)][string]$Field
    )
    $peak = Get-PeakMemoryValue -Turns $Turns -Field $Field
    if ($null -eq $peak) { return $null }
    return [Math]::Round($peak / 1024, 3)
}

# Fixed vocabulary. Synthetic filler only: never a real prompt, never repository
# content, so a stored fixture hash discloses nothing.
$script:FillerVocabulary = @(
    'alpha', 'beta', 'gamma', 'delta', 'epsilon', 'zeta', 'eta', 'theta',
    'iota', 'kappa', 'lambda', 'mu', 'nu', 'xi', 'omicron', 'pi',
    'rho', 'sigma', 'tau', 'upsilon', 'phi', 'chi', 'psi', 'omega',
    'north', 'south', 'east', 'west', 'ridge', 'valley', 'harbour', 'meadow'
)

# Words per token for this vocabulary, used only to size the synthetic filler.
# Every number that appears in the report is measured from the server's counters,
# so an imprecise ratio changes the fixture size and nothing else.
$script:WordsPerToken = 0.75

function New-DeterministicFiller {
    param(
        [Parameter(Mandatory = $true)][int]$ApproximateTokens,
        [Parameter(Mandatory = $true)][int]$Seed
    )

    # A linear congruential generator rather than Get-Random: the fixture has to
    # be identical between the upstream and fork runs or the comparison is not a
    # comparison. These are the Numerical Recipes constants.
    $state = [uint32]$Seed
    $wordCount = [int][Math]::Max(1, [Math]::Round($ApproximateTokens * $script:WordsPerToken))
    $builder = [System.Text.StringBuilder]::new($wordCount * 6)
    for ($index = 0; $index -lt $wordCount; $index++) {
        $state = [uint32](($state * 1664525 + 1013904223) % 4294967296)
        [void]$builder.Append($script:FillerVocabulary[$state % $script:FillerVocabulary.Count])
        [void]$builder.Append($(if (($index + 1) % 16 -eq 0) { "`n" } else { ' ' }))
    }
    return $builder.ToString()
}

function Get-V2Sha256Hex {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string]$Value)

    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $bytes = $sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($Value))
    }
    finally {
        $sha.Dispose()
    }
    return -join ($bytes | ForEach-Object { $_.ToString('X2') })
}

# Memory samples. Each returns $null rather than a plausible number when the
# counter is unavailable, because docs/MODEL_PROMOTION.md forbids recording an
# estimate as a measurement.
function Invoke-V2Chat {
    param(
        [Parameter(Mandatory = $true)][string]$Endpoint,
        [Parameter(Mandatory = $true)][hashtable]$Headers,
        [Parameter(Mandatory = $true)][object]$Body,
        [Parameter(Mandatory = $true)][int]$TimeoutSeconds
    )

    $json = $Body | ConvertTo-Json -Depth 12 -Compress
    $started = [Diagnostics.Stopwatch]::StartNew()
    $response = Invoke-RestMethod -Method Post -Uri $Endpoint -Headers $Headers `
        -ContentType 'application/json' -Body ([Text.Encoding]::UTF8.GetBytes($json)) `
        -TimeoutSec $TimeoutSeconds
    $started.Stop()
    return [pscustomobject]@{
        Response   = $response
        ElapsedMs  = [Math]::Round($started.Elapsed.TotalMilliseconds, 1)
    }
}

function Get-V2TimingValue {
    param(
        [Parameter(Mandatory = $true)][object]$Timings,
        [Parameter(Mandatory = $true)][string]$Name
    )

    $property = $Timings.PSObject.Properties[$Name]
    if ($null -eq $property -or $null -eq $property.Value) { return $null }
    return $property.Value
}

$endpoint = ($BaseUrl.TrimEnd('/')) + '/v1/chat/completions'
$headers = @{ 'Accept' = 'application/json' }
if ($ApiKeyFile) {
    if (-not (Test-Path -LiteralPath $ApiKeyFile -PathType Leaf)) {
        throw "API key file is missing: $ApiKeyFile"
    }
    $headers['Authorization'] = 'Bearer ' + (Get-Content -LiteralPath $ApiKeyFile -Raw -Encoding UTF8).Trim()
}

# One tool, declared on every request so the template stays byte-stable across
# turns. Any change to the prefix invalidates every checkpoint, which would make
# the measurement meaningless rather than merely worse.
$tools = @(
    @{
        type     = 'function'
        function = @{
            name        = 'read_workspace_file'
            description = 'Return the contents of a file in the synthetic workspace.'
            parameters  = @{
                type       = 'object'
                properties = @{ path = @{ type = 'string' } }
                required   = @('path')
            }
        }
    }
)

$systemPrompt = 'You are a local coding assistant. Answer briefly. Use the provided tool when a file is mentioned.'
$baseContext = New-DeterministicFiller -ApproximateTokens $BaseContextTokens -Seed $Seed
$fixtureSha256 = Get-V2Sha256Hex -Value ($systemPrompt + "`n" + $baseContext)

$messages = [System.Collections.Generic.List[object]]::new()
$messages.Add(@{ role = 'system'; content = $systemPrompt })
$messages.Add(@{ role = 'user'; content = "Here is the working context.`n$baseContext`nAcknowledge and wait." })

$turnReports = [System.Collections.Generic.List[object]]::new()
$previousCarriedTokens = 0
$verdictFailures = [System.Collections.Generic.List[string]]::new()

for ($turn = 1; $turn -le $Turns; $turn++) {
    # Odd turns are user increments, even turns carry a tool result. The first
    # turn is the cold prefill and is excluded from the gate by construction.
    $isToolTurn = ($turn % 2 -eq 0)
    $increment = New-DeterministicFiller -ApproximateTokens $IncrementTokens -Seed ($Seed + $turn)

    if ($turn -gt 1 -and -not $isToolTurn) {
        $messages.Add(@{ role = 'user'; content = "Continue. New material:`n$increment" })
    }

    $body = [ordered]@{
        model             = $Model
        messages          = @($messages)
        max_tokens        = $MaxOutputTokens
        temperature       = 0
        top_p             = 1
        seed              = $Seed
        stream            = $false
        cache_prompt      = $true
        tools             = $tools
    }
    if ($isToolTurn) {
        $body['tool_choice'] = 'required'
    }

    $call = Invoke-V2Chat -Endpoint $endpoint -Headers $headers -Body $body -TimeoutSeconds $TimeoutSeconds
    $response = $call.Response
    $timingsProperty = $response.PSObject.Properties['timings']
    if ($null -eq $timingsProperty -or $null -eq $timingsProperty.Value) {
        throw "Turn ${turn}: the server returned no timings block. Without the native prompt_n/cache_n counters this measurement cannot be made, and deriving it from a rate is forbidden by docs/BENCHMARKS.md."
    }
    $timings = $timingsProperty.Value

    $cached = [int](Get-V2TimingValue -Timings $timings -Name 'cache_n')
    $processed = [int](Get-V2TimingValue -Timings $timings -Name 'prompt_n')
    $generated = [int](Get-V2TimingValue -Timings $timings -Name 'predicted_n')
    $totalPrompt = $cached + $processed

    # Genuinely new material is whatever the conversation grew by since the last
    # request: the previous prompt plus what the model generated into it.
    $newTokens = if ($turn -eq 1) { $totalPrompt } else { $totalPrompt - $previousCarriedTokens }
    if ($newTokens -lt 0) { $newTokens = 0 }

    $efficiency = $null
    if ($processed -gt 0) {
        $efficiency = [Math]::Round($newTokens / [double]$processed, 4)
    }
    $reuse = $null
    if ($totalPrompt -gt 0) {
        $reuse = [Math]::Round($cached / [double]$totalPrompt, 4)
    }

    $draftTotal = Get-V2TimingValue -Timings $timings -Name 'draft_n'
    $draftAccepted = Get-V2TimingValue -Timings $timings -Name 'draft_n_accepted'
    $acceptance = $null
    if ($null -ne $draftTotal -and [int]$draftTotal -gt 0) {
        $acceptance = [Math]::Round([int]$draftAccepted / [double][int]$draftTotal, 4)
    }

    $choice = @($response.choices)[0]
    $toolCallsProperty = $choice.message.PSObject.Properties['tool_calls']
    $toolCallObserved = ($null -ne $toolCallsProperty -and $null -ne $toolCallsProperty.Value -and @($toolCallsProperty.Value).Count -gt 0)

    # A full re-prefill is the failure this exists to catch. It is stated against
    # the conversation the server was handed, so the gate scales with the context
    # instead of resting on an absolute token count.
    $reprefill = ($turn -gt 1 -and $totalPrompt -gt 0 -and $processed -gt ($totalPrompt * $ReprefillFraction))
    if ($reprefill) {
        $verdictFailures.Add("turn ${turn}: processed $processed of $totalPrompt prompt tokens for $newTokens new ones")
    }

    $turnReports.Add([ordered]@{
        turn                     = $turn
        kind                     = $(if ($turn -eq 1) { 'cold-prefill' } elseif ($isToolTurn) { 'tool-result' } else { 'user-increment' })
        context_tokens           = $totalPrompt
        new_prompt_tokens        = $newTokens
        processed_prompt_tokens  = $processed
        cached_prompt_tokens     = $cached
        reuse_ratio              = $reuse
        agentic_turn_efficiency  = $efficiency
        full_reprefill           = $reprefill
        prompt_ms                = Get-V2TimingValue -Timings $timings -Name 'prompt_ms'
        prompt_tokens_per_second = Get-V2TimingValue -Timings $timings -Name 'prompt_per_second'
        generated_tokens         = $generated
        decode_tokens_per_second = Get-V2TimingValue -Timings $timings -Name 'predicted_per_second'
        turn_latency_ms          = $call.ElapsedMs
        mtp_draft_tokens         = $draftTotal
        mtp_draft_accepted       = $draftAccepted
        mtp_acceptance           = $acceptance
        tool_call_observed       = $toolCallObserved
        # One combined sample so every memory column in this row describes the
        # same instant. vram_dedicated/shared are adapter-level: a process-level
        # figure cannot show the driver paging the adapter's working set to
        # system memory, which is the degradation that has no other symptom.
        memory                   = Get-V2MemorySample -ProcessId $ServerProcessId
    })

    # Grow the conversation exactly as a harness would, so the next request sees
    # the previous one as a byte-stable prefix.
    if ($toolCallObserved) {
        $call0 = @($toolCallsProperty.Value)[0]
        $messages.Add(@{ role = 'assistant'; content = ''; tool_calls = @($toolCallsProperty.Value) })
        $messages.Add(@{ role = 'tool'; tool_call_id = [string]$call0.id; content = "File contents:`n$increment" })
    }
    else {
        $assistantContent = [string]$choice.message.content
        $messages.Add(@{ role = 'assistant'; content = $assistantContent })
        if ($isToolTurn) {
            # No tool call was produced, so the tool-result increment cannot be
            # appended honestly. Grow the context as a user increment instead and
            # record that the turn did not exercise tool calling.
            $messages.Add(@{ role = 'user'; content = "Tool unavailable. New material:`n$increment" })
        }
    }

    $previousCarriedTokens = $totalPrompt + $generated
}

$verdict = if ($verdictFailures.Count -eq 0) { 'incremental_reuse_pass' } else { 'incremental_reuse_fail' }
$measuredTurns = @($turnReports | Where-Object { $_.turn -gt 1 })
$efficiencies = @($measuredTurns | Where-Object { $null -ne $_.agentic_turn_efficiency } | ForEach-Object { $_.agentic_turn_efficiency })

# Classified from the run's worst instant, not from a final sample: paging that
# happened at peak context is the finding, and it does not persist after the
# conversation is released.
$gpuPressure = $null
if ($DeviceVramMib -gt 0) {
    $peakDedicated = Get-PeakMemoryValue -Turns $turnReports -Field 'vram_dedicated_mib'
    $peakShared = Get-PeakMemoryValue -Turns $turnReports -Field 'vram_shared_mib'
    if ($null -ne $peakDedicated) {
        $worst = [pscustomobject]@{
            instance      = 'run-peak'
            dedicated_mib = $peakDedicated
            shared_mib    = $(if ($null -ne $peakShared) { $peakShared } else { 0 })
        }
        $gpuPressure = Test-V2GpuMemoryPressure -Sample $worst -TotalMib $DeviceVramMib
    }
}

$report = [ordered]@{
    schema_version           = 1
    scenario                 = 'agentic-incremental-reuse'
    runtime_label            = $RuntimeLabel
    model                    = $Model
    started_utc              = [DateTime]::UtcNow.ToString('o')
    # Control variables. Compare-V2Runtimes.ps1 refuses to compare two reports
    # whose fixture, turn shape, or sampling differ, because a comparison across
    # different inputs is not a comparison of the runtime.
    controls                 = [ordered]@{
        fixture_sha256        = $fixtureSha256
        seed                  = $Seed
        base_context_tokens   = $BaseContextTokens
        increment_tokens      = $IncrementTokens
        turns                 = $Turns
        max_output_tokens     = $MaxOutputTokens
        reprefill_fraction    = $ReprefillFraction
    }
    turns                    = @($turnReports)
    peak_context_tokens      = ($turnReports | ForEach-Object { $_.context_tokens } | Measure-Object -Maximum).Maximum
    mean_turn_efficiency     = $(if ($efficiencies.Count -gt 0) { [Math]::Round(($efficiencies | Measure-Object -Average).Average, 4) } else { $null })
    # peak_vram_gib feeds resources.peak_vram_gib in the model manifest, which
    # the edge admission gate compares against runtimes[].device.vram_mib. It is
    # taken from the adapter, not from the process. The process counter is
    # accurate for what llama-server was charged, but the budget applies to the
    # whole device, and on this workstation the desktop already holds ~3.0 GiB of
    # it. Gating on the process figure therefore compared the model's cost alone
    # against a budget it does not have to itself.
    # peak_process_vram_gib keeps the per-process quantity for attribution, and
    # peak_vram_shared_gib is the paging signal itself.
    # peak_commit_gib feeds resources.peak_commit_gib, which the edge compares
    # against the host's *available* commit headroom. It therefore has to be this
    # model's own commit demand, exactly as peak_ram_gib is this model's own
    # resident footprint. It previously carried system-wide committed bytes,
    # which is an absolute level rather than a demand: on this workstation that
    # is around 40 GiB against 36 GiB of available headroom, so a correct
    # manifest would have been refused by its own admission gate.
    # peak_system_commit_gib keeps the system-wide observation, which is useful
    # context for a reader and is not what the gate consumes.
    peak_commit_gib          = (Get-PeakMemoryValue -Turns $turnReports -Field 'process_private_gib')
    peak_system_commit_gib   = (Get-PeakMemoryValue -Turns $turnReports -Field 'commit_gib')
    peak_ram_gib             = (Get-PeakMemoryValue -Turns $turnReports -Field 'process_ws_gib')
    peak_vram_gib            = (Get-PeakMemoryGiBFromMib -Turns $turnReports -Field 'vram_dedicated_mib')
    peak_process_vram_gib    = (Get-PeakMemoryGiBFromMib -Turns $turnReports -Field 'process_vram_mib')
    peak_vram_shared_gib     = (Get-PeakMemoryGiBFromMib -Turns $turnReports -Field 'vram_shared_mib')
    peak_physical_used_gib   = (Get-PeakMemoryValue -Turns $turnReports -Field 'physical_used_gib')
    gpu_memory_pressure      = $gpuPressure
    verdict                  = $verdict
    failures                 = @($verdictFailures)
}

if ($OutputPath) {
    $directory = Split-Path -Parent $OutputPath
    if ($directory) { New-Item -ItemType Directory -Force -Path $directory | Out-Null }
    Set-Content -LiteralPath $OutputPath -Value ($report | ConvertTo-Json -Depth 8) -Encoding UTF8
}

if (-not $Quiet) {
    $report | ConvertTo-Json -Depth 8
}

if ($verdict -ne 'incremental_reuse_pass') {
    Write-Error "Incremental context reuse failed on $($verdictFailures.Count) turn(s): $($verdictFailures -join '; ')"
    exit 1
}
