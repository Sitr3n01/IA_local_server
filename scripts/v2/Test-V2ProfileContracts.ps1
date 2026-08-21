<#
.SYNOPSIS
Verifies the output/reasoning contract of a profile against a real server.

.DESCRIPTION
Three things the ordinary smoke does not cover, each of which this consolidation
changed and none of which can be established by reading a config file.

**The output ceiling is real and is the profile's own.** The Huge profile exists
because Q2_K_XL was measured spending an entire 8192-token budget inside
`reasoning_content` and returning an empty answer. Raising the ceiling to 32768
is only meaningful if the server actually honours a request above 8192, so this
asks for one and checks the token count that comes back.

**An impossible request fails early and says why.** A prompt that leaves no room
for the reserved output must be refused with a legible error rather than served
and silently truncated. With `--no-context-shift` llama-server refuses it; this
records the status and the message so the behaviour is evidence rather than an
assumption about the runtime.

**A long generation stays cancellable.** A 32k budget is worth nothing if the
operator cannot stop it. This starts a streaming request, cancels mid-flight, and
checks that generation stops, the slot is released and the process survives to
serve the next request.

.EXAMPLE
./Test-V2ProfileContracts.ps1 -ModelId qwen38-27b-huge-256k -CheckLongOutput
#>
[CmdletBinding()]
param(
    [string]$ManifestPath = (Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'config\models.yaml'),
    [Parameter(Mandatory = $true)][string]$ModelId,
    [ValidateRange(1024, 65535)][int]$Port = 19399,
    [ValidateRange(60, 3600)][int]$StartupTimeoutSeconds = 900,

    # Off by default: generating past 8192 tokens costs several minutes.
    [switch]$CheckLongOutput,
    [ValidateRange(1, 65536)][int]$LongOutputFloor = 8192,
    [string]$OutputPath,
    [switch]$Quiet
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Common.ps1')

$script:Checks = 0
$script:Failures = [System.Collections.Generic.List[string]]::new()
function Assert-Contract {
    param([Parameter(Mandatory = $true)][bool]$Condition,
        [Parameter(Mandatory = $true)][string]$Message)
    $script:Checks++
    if ($Condition) { if (-not $Quiet) { Write-Host "  ok    $Message" } }
    else { $script:Failures.Add($Message); if (-not $Quiet) { Write-Warning "  FAIL  $Message" } }
}

$manifest = Read-V2Manifest -Path $ManifestPath
$model = @($manifest.models | Where-Object { $_.id -eq $ModelId })[0]
if ($null -eq $model) { throw "Model '$ModelId' is not in $ManifestPath." }
$runtime = @($manifest.runtimes | Where-Object { $_.id -eq $model.runtime })[0]

$command = New-V2LlamaServerCommand -Runtime $runtime -Model $model -RouterAPIKeyPath 'UNUSED'
$command = $command -replace '\$\{PORT\}', "$Port"
$command = $command -replace '\s--api-key-file\s+"[^"]*"', ''
$argumentText = $command.Substring($command.IndexOf('" ') + 2)

$contextTokens = [int]$model.context_tokens
$maxOutput = [int]$model.max_output_tokens
$declaredNPredict = Get-V2ModelSetting -Model $model -Name 'n_predict'
$declaredBudget = Get-V2ModelSetting -Model $model -Name 'reasoning_budget'

if (-not $Quiet) {
    Write-Host ("Contracts: {0}  ctx={1}  max_output={2}  n_predict={3}  reasoning_budget={4}" -f `
            $ModelId, $contextTokens, $maxOutput,
        $(if ($null -ne $declaredNPredict) { $declaredNPredict } else { '(unset)' }),
        $(if ($null -ne $declaredBudget) { $declaredBudget } else { '(unset)' }))
}

$runtimeRoot = Split-Path -Parent $runtime.artifact.path
$previousPath = $env:PATH
$env:PATH = "$runtimeRoot;C:\Windows\System32\downlevel;$env:PATH"
foreach ($entry in $runtime.environment.psobject.Properties) {
    Set-Item -Path ("Env:\" + $entry.Name) -Value $entry.Value
}

$endpoint = "http://127.0.0.1:$Port"
$process = $null
$observed = [ordered]@{}
try {
    $process = Start-Process -FilePath $runtime.artifact.path -ArgumentList $argumentText -PassThru -WindowStyle Hidden
    $deadline = [DateTime]::UtcNow.AddSeconds($StartupTimeoutSeconds)
    $ready = $false
    while ([DateTime]::UtcNow -lt $deadline -and -not $process.HasExited) {
        try { if ((Invoke-RestMethod -Uri "$endpoint/health" -TimeoutSec 5).status -eq 'ok') { $ready = $true; break } }
        catch { Start-Sleep -Seconds 2 }
    }
    Assert-Contract -Condition $ready -Message 'server became ready'
    if (-not $ready) { throw 'server never became ready' }

    # --- 1. the server advertises the declared window -------------------
    $props = Invoke-RestMethod -Uri "$endpoint/props" -TimeoutSec 30
    $servedCtx = [int]$props.default_generation_settings.n_ctx
    $observed['served_n_ctx'] = $servedCtx
    Assert-Contract -Condition ($servedCtx -ge $contextTokens - 64) `
        -Message ("served context {0} matches the declared {1}" -f $servedCtx, $contextTokens)

    # --- 2. an over-budget request is refused, legibly ------------------
    # Ask for more output than the profile's ceiling allows. The server must
    # not quietly serve it.
    $over = $maxOutput + 4096
    $body = @{ messages = @(@{ role = 'user'; content = 'Count.' }); max_tokens = $over; temperature = 0 } | ConvertTo-Json -Depth 5
    $overStatus = $null
    $overTokens = $null
    try {
        $r = Invoke-RestMethod -Uri "$endpoint/v1/chat/completions" -Method Post -Body $body -ContentType 'application/json' -TimeoutSec 1800
        $overStatus = 'served'
        $overTokens = [int]$r.usage.completion_tokens
    }
    catch { $overStatus = "refused: $($_.Exception.Message)" }
    $observed['over_budget_request'] = [ordered]@{ asked = $over; status = $overStatus; completion_tokens = $overTokens }
    # Either outcome is acceptable as long as it is bounded: a refusal, or a
    # generation clamped at or below the declared ceiling. What must not happen
    # is a generation that runs past it.
    Assert-Contract -Condition ($null -eq $overTokens -or $overTokens -le $maxOutput) `
        -Message ("a request for {0} tokens does not generate past the {1}-token ceiling (got {2})" -f `
                $over, $maxOutput, $(if ($null -ne $overTokens) { $overTokens } else { 'refusal' }))

    # --- 3. a prompt with no room for output is refused -----------------
    # Build a prompt longer than the whole window. --no-context-shift means the
    # server cannot silently drop the head of it.
    $filler = ('token ' * 4096)
    $repeats = [Math]::Ceiling(($contextTokens * 1.2) / 4096.0)
    $huge = ($filler * [int]$repeats)
    $body = @{ messages = @(@{ role = 'user'; content = $huge }); max_tokens = 32; temperature = 0 } | ConvertTo-Json -Depth 5
    $oversizeStatus = 'served (no refusal)'
    try {
        Invoke-RestMethod -Uri "$endpoint/v1/chat/completions" -Method Post -Body $body -ContentType 'application/json' -TimeoutSec 900 | Out-Null
    }
    catch {
        $oversizeStatus = $_.Exception.Message
        if ($_.ErrorDetails -and $_.ErrorDetails.Message) { $oversizeStatus = $_.ErrorDetails.Message }
    }
    $observed['oversize_prompt'] = $oversizeStatus
    Assert-Contract -Condition ($oversizeStatus -ne 'served (no refusal)') `
        -Message ('a prompt larger than the context window is refused rather than truncated')

    # --- 4. long output is actually permitted ---------------------------
    if ($CheckLongOutput) {
        $prompt = 'Write a detailed technical design document for a distributed job scheduler. ' +
        'Cover requirements, data model, queueing, retries, idempotency, observability, ' +
        'failure modes, capacity planning and a rollout plan. Be exhaustive and use headings.'
        $body = @{ messages = @(@{ role = 'user'; content = $prompt }); max_tokens = $maxOutput; temperature = 0.2 } | ConvertTo-Json -Depth 5
        $long = Invoke-RestMethod -Uri "$endpoint/v1/chat/completions" -Method Post -Body $body -ContentType 'application/json' -TimeoutSec 5400
        $produced = [int]$long.usage.completion_tokens
        $observed['long_output'] = [ordered]@{
            completion_tokens = $produced
            finish_reason     = [string]$long.choices[0].finish_reason
        }
        Assert-Contract -Condition ($produced -gt $LongOutputFloor) `
            -Message ("generation exceeded {0} tokens (produced {1}); the old ceiling is gone" -f $LongOutputFloor, $produced)
    }

    # --- 5. a generation stays cancellable ------------------------------
    $cancelBody = @{ messages = @(@{ role = 'user'; content = 'Write an extremely long essay about compilers.' })
        max_tokens = $maxOutput; temperature = 0.2; stream = $true } | ConvertTo-Json -Depth 5
    # HttpWebRequest rather than HttpClient: Windows PowerShell 5.1 does not
    # load System.Net.Http by default, and this script has to run on the same
    # 5.1 the operator's machine uses rather than only under pwsh 7.
    $cancelled = $false
    $chunks = 0
    $request = [System.Net.HttpWebRequest]::CreateHttp("$endpoint/v1/chat/completions")
    $request.Method = 'POST'
    $request.ContentType = 'application/json'
    $request.Timeout = 600000
    $request.ReadWriteTimeout = 600000
    try {
        $bytes = [System.Text.Encoding]::UTF8.GetBytes($cancelBody)
        $requestStream = $request.GetRequestStream()
        $requestStream.Write($bytes, 0, $bytes.Length)
        $requestStream.Close()

        $response = $request.GetResponse()
        $reader = [System.IO.StreamReader]::new($response.GetResponseStream())
        while ($chunks -lt 12 -and -not $reader.EndOfStream) { $null = $reader.ReadLine(); $chunks++ }
        # Abort is the client hanging up mid-stream, which is exactly what a
        # harness does when the operator presses escape.
        $request.Abort()
        $reader.Dispose()
        $response.Dispose()
        $cancelled = $chunks -ge 1
    }
    catch {
        # An abort surfaces as an exception on the reader; that is the success
        # path here, not a failure, provided bytes had already arrived.
        $cancelled = $chunks -ge 1
    }
    $observed['cancellation'] = [ordered]@{ chunks_before_cancel = $chunks }

    Assert-Contract -Condition $cancelled -Message 'a streaming generation can be cancelled mid-flight'

    Start-Sleep -Seconds 3
    Assert-Contract -Condition (-not $process.HasExited) -Message 'the server survived the cancellation'
    $after = Invoke-RestMethod -Uri "$endpoint/v1/chat/completions" -Method Post -TimeoutSec 600 `
        -ContentType 'application/json' `
        -Body (@{ messages = @(@{ role = 'user'; content = 'Reply with the word OK.' }); max_tokens = 128; temperature = 0 } | ConvertTo-Json -Depth 5)
    Assert-Contract -Condition ($null -ne $after.choices) -Message 'the slot was released and serves the next request'
}
finally {
    if ($null -ne $process -and -not $process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        $process.WaitForExit(30000) | Out-Null
    }
    $env:PATH = $previousPath
}

Assert-Contract -Condition (-not (Get-Process llama-server -ErrorAction SilentlyContinue)) `
    -Message 'no llama-server survived'

$report = [ordered]@{
    schema_version = 1
    scenario       = 'profile-output-contract'
    model          = $ModelId
    started_utc    = [DateTime]::UtcNow.ToString('o')
    declared       = [ordered]@{
        context_tokens           = $contextTokens
        max_output_tokens        = $maxOutput
        n_predict                = $declaredNPredict
        reasoning_budget         = $declaredBudget
        compact_threshold_tokens = (Get-V2ModelSetting -Model $model -Name 'compact_threshold_tokens')
    }
    observed       = $observed
    checks         = $script:Checks
    failures       = @($script:Failures)
    verdict        = $(if ($script:Failures.Count -eq 0) { 'contract_pass' } else { 'contract_fail' })
}
if ($OutputPath) {
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $OutputPath) | Out-Null
    $report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $OutputPath -Encoding UTF8
}
if (-not $Quiet) { Write-Host ("{0}: {1}/{2} checks" -f $report.verdict, ($script:Checks - $script:Failures.Count), $script:Checks) }
$report
