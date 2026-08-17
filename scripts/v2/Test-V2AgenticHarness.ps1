<#
.SYNOPSIS
Exercises Measure-V2AgenticReuse.ps1 and Compare-V2Runtimes.ps1 against a
scripted llama-server, with no GPU and no model.

.DESCRIPTION
The agentic reuse measurement is a regression gate, so the gate itself needs one.
A harness that reported "pass" because of an arithmetic slip would be worse than
no harness: it would certify a runtime that re-prefills the whole conversation
every turn, which is the exact failure the fork is being adopted to escape.

Two scripted servers stand in for the two outcomes. One reports the counters a
runtime with working context checkpoints produces - a large cached prefix and a
prompt evaluation the size of the increment. The other reports what a runtime
without them produces: nothing cached, and the entire conversation evaluated
again. The harness must call the first a pass and the second a failure, and must
compute the same per-turn numbers a real run would.
#>
[CmdletBinding()]
param(
    [switch]$Quiet
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-True {
    param(
        [Parameter(Mandatory = $true)][bool]$Condition,
        [Parameter(Mandatory = $true)][string]$Message
    )
    if (-not $Condition) { throw $Message }
}

function Get-FreeLoopbackPort {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try {
        return $listener.LocalEndpoint.Port
    }
    finally {
        $listener.Stop()
    }
}

# The scripted server. It never inspects the prompt: it produces the timings a
# runtime in the named mode would produce, so the harness is measured against a
# known answer rather than against a model.
$serverScript = {
    param([int]$Port, [string]$Mode, [int]$Increment, [int]$BaseTokens, [int]$Generated)

    $listener = [Net.HttpListener]::new()
    $listener.Prefixes.Add("http://127.0.0.1:$Port/")
    $listener.Start()

    $previousTotal = 0
    $previousGenerated = 0
    try {
        while ($listener.IsListening) {
            $context = $listener.GetContext()
            $reader = [IO.StreamReader]::new($context.Request.InputStream, [Text.Encoding]::UTF8)
            $requestBody = $reader.ReadToEnd()
            $reader.Dispose()

            if ($context.Request.Url.AbsolutePath -eq '/shutdown') {
                $context.Response.StatusCode = 200
                $context.Response.Close()
                break
            }

            $total = if ($previousTotal -eq 0) { $BaseTokens } else { $previousTotal + $previousGenerated + $Increment }
            if ($Mode -eq 'reuse' -and $previousTotal -gt 0) {
                $cached = $previousTotal + $previousGenerated
                $processed = $total - $cached
            }
            else {
                $cached = 0
                $processed = $total
            }

            $wantsTool = $requestBody -match '"tool_choice"\s*:\s*"required"'
            $message = if ($wantsTool) {
                @{
                    role       = 'assistant'
                    content    = ''
                    tool_calls = @(@{
                            id       = 'call_1'
                            type     = 'function'
                            function = @{ name = 'read_workspace_file'; arguments = '{"path":"a.txt"}' }
                        })
                }
            }
            else {
                @{ role = 'assistant'; content = 'acknowledged' }
            }

            $payload = @{
                id      = 'chatcmpl-scripted'
                object  = 'chat.completion'
                choices = @(@{ index = 0; message = $message; finish_reason = 'stop' })
                timings = @{
                    cache_n               = $cached
                    prompt_n              = $processed
                    prompt_ms             = 100.0
                    prompt_per_second     = 500.0
                    predicted_n           = $Generated
                    predicted_ms          = 200.0
                    predicted_per_second  = 25.0
                    draft_n               = 40
                    draft_n_accepted      = 30
                }
            } | ConvertTo-Json -Depth 8 -Compress

            $bytes = [Text.Encoding]::UTF8.GetBytes($payload)
            $context.Response.ContentType = 'application/json'
            $context.Response.ContentLength64 = $bytes.Length
            $context.Response.OutputStream.Write($bytes, 0, $bytes.Length)
            $context.Response.Close()

            $previousTotal = $total
            $previousGenerated = $Generated
        }
    }
    finally {
        $listener.Stop()
        $listener.Close()
    }
}

$host_ = [Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
$measureScript = Join-Path $PSScriptRoot 'Measure-V2AgenticReuse.ps1'
$compareScript = Join-Path $PSScriptRoot 'Compare-V2Runtimes.ps1'
$workRoot = Join-Path ([IO.Path]::GetTempPath()) ("cia-agentic-harness-{0}" -f [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $workRoot | Out-Null

$baseTokens = 60000
$increment = 2000
$generated = 32
$turns = 6

function Invoke-ScriptedRun {
    param(
        [Parameter(Mandatory = $true)][string]$Mode,
        [Parameter(Mandatory = $true)][string]$Label
    )

    $port = Get-FreeLoopbackPort
    $job = Start-Job -ScriptBlock $serverScript -ArgumentList $port, $Mode, $increment, $baseTokens, $generated
    try {
        $deadline = [DateTime]::UtcNow.AddSeconds(30)
        $ready = $false
        while ([DateTime]::UtcNow -lt $deadline -and -not $ready) {
            try {
                $probe = [Net.Sockets.TcpClient]::new()
                $probe.Connect('127.0.0.1', $port)
                $probe.Close()
                $ready = $true
            }
            catch {
                Start-Sleep -Milliseconds 200
            }
        }
        Assert-True $ready "The scripted server did not start on port $port."

        # The failing scenario is expected to write to stderr and exit non-zero;
        # that is the behaviour under test, so its diagnostics are discarded here
        # rather than mixed into this script's own output.
        $reportPath = Join-Path $workRoot "$Label.json"
        & $host_ -NoProfile -File $measureScript `
            -BaseUrl "http://127.0.0.1:$port" -Model 'scripted' -RuntimeLabel $Label `
            -BaseContextTokens 2048 -IncrementTokens 256 -Turns $turns -MaxOutputTokens 32 `
            -OutputPath $reportPath -Quiet 2>&1 | Out-Null
        $exitCode = $LASTEXITCODE

        Assert-True (Test-Path -LiteralPath $reportPath -PathType Leaf) "No report was written for the '$Mode' server."
        return [pscustomobject]@{
            Report   = Get-Content -LiteralPath $reportPath -Raw -Encoding UTF8 | ConvertFrom-Json
            Path     = $reportPath
            ExitCode = $exitCode
        }
    }
    finally {
        try { Invoke-WebRequest -Uri "http://127.0.0.1:$port/shutdown" -TimeoutSec 5 -UseBasicParsing | Out-Null } catch { }
        Remove-Job -Job $job -Force -ErrorAction SilentlyContinue
    }
}

try {
    # A runtime that restores checkpoints: after the cold prefill it evaluates the
    # increment and nothing else.
    $reuse = Invoke-ScriptedRun -Mode 'reuse' -Label 'buun'
    Assert-True ($reuse.Report.verdict -eq 'incremental_reuse_pass') "A reusing runtime was reported as '$($reuse.Report.verdict)'."
    Assert-True ($reuse.ExitCode -eq 0) "A passing run exited with code $($reuse.ExitCode)."
    Assert-True (@($reuse.Report.turns).Count -eq $turns) "The run recorded $(@($reuse.Report.turns).Count) turns, expected $turns."

    $coldTurn = @($reuse.Report.turns)[0]
    Assert-True ($coldTurn.processed_prompt_tokens -eq $baseTokens) "The cold prefill processed $($coldTurn.processed_prompt_tokens) tokens, expected $baseTokens."
    Assert-True (-not $coldTurn.full_reprefill) 'The cold prefill was counted as a re-prefill; turn one is the baseline, not a failure.'

    foreach ($turn in @($reuse.Report.turns | Where-Object { $_.turn -gt 1 })) {
        Assert-True ($turn.processed_prompt_tokens -eq $increment) "Turn $($turn.turn) processed $($turn.processed_prompt_tokens) tokens, expected the $increment-token increment."
        Assert-True ($turn.new_prompt_tokens -eq $increment) "Turn $($turn.turn) counted $($turn.new_prompt_tokens) new tokens, expected $increment."
        Assert-True ($turn.agentic_turn_efficiency -eq 1) "Turn $($turn.turn) efficiency was $($turn.agentic_turn_efficiency), expected 1 when only new tokens are processed."
        Assert-True ($turn.reuse_ratio -gt 0.9) "Turn $($turn.turn) reuse ratio was $($turn.reuse_ratio), expected most of the context to be cached."
        Assert-True ($turn.mtp_acceptance -eq 0.75) "Turn $($turn.turn) reported MTP acceptance $($turn.mtp_acceptance), expected 30 of 40 drafted tokens."
    }
    Assert-True (@($reuse.Report.turns | Where-Object { $_.tool_call_observed }).Count -ge 2) 'The scenario never exercised a tool call.'
    Assert-True ($reuse.Report.peak_context_tokens -gt $baseTokens) 'The conversation never grew beyond its base context.'

    # A runtime without working checkpoints: every turn re-evaluates everything.
    # This is the case that must never be reported as a pass.
    $reprefill = Invoke-ScriptedRun -Mode 'reprefill' -Label 'upstream'
    Assert-True ($reprefill.Report.verdict -eq 'incremental_reuse_fail') "A re-prefilling runtime was reported as '$($reprefill.Report.verdict)'."
    Assert-True ($reprefill.ExitCode -ne 0) 'A failing run exited successfully; the gate has to be usable from a script.'
    Assert-True (@($reprefill.Report.failures).Count -eq ($turns - 1)) "The failing run named $(@($reprefill.Report.failures).Count) bad turns, expected $($turns - 1)."
    foreach ($turn in @($reprefill.Report.turns | Where-Object { $_.turn -gt 1 })) {
        Assert-True $turn.full_reprefill "Turn $($turn.turn) reprocessed the whole context and was not flagged."
        Assert-True ($turn.agentic_turn_efficiency -lt 0.1) "Turn $($turn.turn) efficiency was $($turn.agentic_turn_efficiency), expected a value near zero."
    }

    # The comparison refuses anything that is not actually a comparison.
    $comparisonPath = Join-Path $workRoot 'comparison.json'
    & $compareScript -BaselineReport $reprefill.Path -CandidateReport $reuse.Path -OutputPath $comparisonPath -Quiet
    $comparison = Get-Content -LiteralPath $comparisonPath -Raw -Encoding UTF8 | ConvertFrom-Json
    Assert-True $comparison.candidate_eliminates_reprefill 'The comparison did not record that the candidate eliminated the re-prefill.'
    Assert-True ($comparison.candidate.processed_prompt_tokens -lt $comparison.baseline.processed_prompt_tokens) 'The candidate did not process fewer prompt tokens than the baseline.'

    $selfComparisonRejected = $false
    try {
        & $compareScript -BaselineReport $reuse.Path -CandidateReport $reuse.Path -Quiet | Out-Null
    }
    catch {
        $selfComparisonRejected = $_.Exception.Message -match 'An A/B needs two distinct runtimes'
    }
    Assert-True $selfComparisonRejected 'A report was compared against itself.'

    $divergentPath = Join-Path $workRoot 'divergent.json'
    $divergent = Get-Content -LiteralPath $reuse.Path -Raw -Encoding UTF8 | ConvertFrom-Json
    $divergent.runtime_label = 'buun-other'
    $divergent.controls.fixture_sha256 = ('9' * 64)
    Set-Content -LiteralPath $divergentPath -Value ($divergent | ConvertTo-Json -Depth 8) -Encoding UTF8
    $divergentRejected = $false
    try {
        & $compareScript -BaselineReport $reuse.Path -CandidateReport $divergentPath -Quiet | Out-Null
    }
    catch {
        $divergentRejected = $_.Exception.Message -match 'did not share their inputs'
    }
    Assert-True $divergentRejected 'Two runs over different fixtures were compared as if they were an A/B.'

    if (-not $Quiet) {
        [pscustomobject]@{
            harness_tests            = 6
            reuse_verdict            = $reuse.Report.verdict
            reprefill_verdict        = $reprefill.Report.verdict
            reuse_mean_efficiency    = $reuse.Report.mean_turn_efficiency
            reprefill_mean_efficiency = $reprefill.Report.mean_turn_efficiency
            valid                    = $true
        } | ConvertTo-Json -Depth 3
    }
}
finally {
    Remove-Item -LiteralPath $workRoot -Recurse -Force -ErrorAction SilentlyContinue
}
