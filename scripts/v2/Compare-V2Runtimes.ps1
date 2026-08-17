<#
.SYNOPSIS
Compares two Measure-V2AgenticReuse.ps1 reports, upstream llama.cpp against the
pinned buun-llama-cpp build.

.DESCRIPTION
The comparison is only meaningful if both runs saw the same inputs, so this
refuses to compare reports whose control variables differ rather than printing a
table that looks authoritative and is not. Fixture hash, seed, base context,
increment size, turn count, output cap and the re-prefill threshold must all
match; the runtime label must not.

The headline is not decode throughput. It is processed prompt tokens against new
prompt tokens, because that is the difference the fork is being adopted for, and
a runtime that decodes faster while re-prefilling the conversation has lost.

.EXAMPLE
./Compare-V2Runtimes.ps1 -BaselineReport upstream-192k.json -CandidateReport buun-192k.json
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$BaselineReport,

    [Parameter(Mandatory = $true)]
    [string]$CandidateReport,

    [string]$OutputPath,

    [switch]$Quiet
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Read-V2ReuseReport {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Report is missing: $Path"
    }
    $report = Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json
    foreach ($required in @('schema_version', 'scenario', 'runtime_label', 'controls', 'turns', 'verdict')) {
        if ($null -eq $report.PSObject.Properties[$required]) {
            throw "Report '$Path' is missing required property '$required'."
        }
    }
    if ($report.schema_version -ne 1) {
        throw "Report '$Path' has unsupported schema_version '$($report.schema_version)'."
    }
    if ($report.scenario -ne 'agentic-incremental-reuse') {
        throw "Report '$Path' is scenario '$($report.scenario)'; only agentic-incremental-reuse reports are comparable."
    }
    return $report
}

function Get-V2Mean {
    param([object[]]$Values)

    $numeric = @($Values | Where-Object { $null -ne $_ })
    if ($numeric.Count -eq 0) { return $null }
    return [Math]::Round(($numeric | Measure-Object -Average).Average, 4)
}

function Get-V2Summary {
    param([Parameter(Mandatory = $true)][object]$Report)

    # Turn one is the cold prefill on both sides and says nothing about reuse.
    $measured = @($Report.turns | Where-Object { $_.turn -gt 1 })
    return [ordered]@{
        runtime_label            = [string]$Report.runtime_label
        model                    = [string]$Report.model
        verdict                  = [string]$Report.verdict
        full_reprefill_turns     = @($measured | Where-Object { $_.full_reprefill }).Count
        peak_context_tokens      = $Report.peak_context_tokens
        new_prompt_tokens        = (@($measured | ForEach-Object { $_.new_prompt_tokens }) | Measure-Object -Sum).Sum
        processed_prompt_tokens  = (@($measured | ForEach-Object { $_.processed_prompt_tokens }) | Measure-Object -Sum).Sum
        mean_reuse_ratio         = Get-V2Mean -Values @($measured | ForEach-Object { $_.reuse_ratio })
        mean_turn_efficiency     = Get-V2Mean -Values @($measured | ForEach-Object { $_.agentic_turn_efficiency })
        mean_prompt_tokens_per_second = Get-V2Mean -Values @($measured | ForEach-Object { $_.prompt_tokens_per_second })
        mean_decode_tokens_per_second = Get-V2Mean -Values @($measured | ForEach-Object { $_.decode_tokens_per_second })
        mean_prefill_ms          = Get-V2Mean -Values @($measured | ForEach-Object { $_.prompt_ms })
        mean_turn_latency_ms     = Get-V2Mean -Values @($measured | ForEach-Object { $_.turn_latency_ms })
        mean_mtp_acceptance      = Get-V2Mean -Values @($measured | ForEach-Object { $_.mtp_acceptance })
        peak_vram_gib            = $Report.peak_vram_gib
        peak_ram_gib             = $Report.peak_ram_gib
        peak_commit_gib          = $Report.peak_commit_gib
    }
}

$baseline = Read-V2ReuseReport -Path $BaselineReport
$candidate = Read-V2ReuseReport -Path $CandidateReport

if ($baseline.runtime_label -eq $candidate.runtime_label) {
    throw "Both reports are labelled '$($baseline.runtime_label)'. An A/B needs two distinct runtimes."
}
if ($baseline.model -ne $candidate.model) {
    throw "The reports use different models ('$($baseline.model)' and '$($candidate.model)'). The runtime must be the only variable."
}

$controlMismatches = @()
foreach ($control in @('fixture_sha256', 'seed', 'base_context_tokens', 'increment_tokens', 'turns', 'max_output_tokens', 'reprefill_fraction')) {
    $left = $baseline.controls.PSObject.Properties[$control]
    $right = $candidate.controls.PSObject.Properties[$control]
    if ($null -eq $left -or $null -eq $right) {
        $controlMismatches += "$control (absent from one report)"
        continue
    }
    if ([string]$left.Value -ne [string]$right.Value) {
        $controlMismatches += "$control ('$($left.Value)' vs '$($right.Value)')"
    }
}
if ($controlMismatches.Count -gt 0) {
    throw "The two runs did not share their inputs, so they cannot be compared: $($controlMismatches -join ', '). Re-run both sides with identical parameters."
}

$comparison = [ordered]@{
    schema_version = 1
    scenario       = 'agentic-incremental-reuse-ab'
    generated_utc  = [DateTime]::UtcNow.ToString('o')
    controls       = $baseline.controls
    baseline       = Get-V2Summary -Report $baseline
    candidate      = Get-V2Summary -Report $candidate
}

# The one conclusion this script is willing to draw. Everything else in the table
# is context for a human decision, not an automated verdict.
$comparison['candidate_eliminates_reprefill'] = (
    $comparison.candidate.full_reprefill_turns -eq 0 -and
    $comparison.baseline.full_reprefill_turns -gt 0
)

if ($OutputPath) {
    $directory = Split-Path -Parent $OutputPath
    if ($directory) { New-Item -ItemType Directory -Force -Path $directory | Out-Null }
    Set-Content -LiteralPath $OutputPath -Value ($comparison | ConvertTo-Json -Depth 8) -Encoding UTF8
}

if (-not $Quiet) {
    $comparison | ConvertTo-Json -Depth 8
}
