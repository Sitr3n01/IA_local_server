param(
    [Parameter(Mandatory = $true)]
    [string]$ProfileId,

    [ValidateSet("amd", "unsloth")]
    [string]$Runtime = "amd",

    [int]$Port = 18180,
    [int]$StartupTimeoutSeconds = 300,
    [int]$MaxTokens = 128,
    [int]$PromptWords = 650
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $scriptDir
$matrixPath = Join-Path $root "model-test-matrix.json"
$matrix = Get-Content -Raw -LiteralPath $matrixPath | ConvertFrom-Json
$profile = $matrix.profiles | Where-Object { $_.id -eq $ProfileId } | Select-Object -First 1
if (-not $profile) {
    throw "ProfileId not found: $ProfileId"
}

function Resolve-ProfileModelPath {
    param(
        [string]$BaseRoot,
        [string]$LocalPath
    )

    if ([IO.Path]::IsPathRooted($LocalPath)) {
        return $LocalPath
    }

    return (Join-Path $BaseRoot $LocalPath)
}

$modelPath = Resolve-ProfileModelPath -BaseRoot $root -LocalPath $profile.local_path
if (-not (Test-Path -LiteralPath $modelPath)) {
    throw "Model is not downloaded: $modelPath"
}

$startScript = Join-Path $scriptDir "start-llama-server.ps1"
$job = Start-Job -ScriptBlock {
    param($startScript, $modelPath, $runtime, $port, $ctx, $ctk, $ctv, $alias)
    powershell -NoProfile -ExecutionPolicy Bypass -File $startScript `
        -Runtime $runtime `
        -ModelPath $modelPath `
        -Port $port `
        -ContextSize $ctx `
        -CacheTypeK $ctk `
        -CacheTypeV $ctv `
        -Alias $alias `
        -Reasoning off `
        -ReasoningBudget 0 `
        -NoWebUI
} -ArgumentList $startScript,$modelPath,$Runtime,$Port,$profile.context_size,$profile.cache_type_k,$profile.cache_type_v,$profile.alias

$baseUrl = "http://127.0.0.1:$Port/v1"
$ready = $false
for ($i = 0; $i -lt $StartupTimeoutSeconds; $i++) {
    Start-Sleep -Seconds 1
    if ($job.State -ne "Running") {
        break
    }
    try {
        Invoke-RestMethod -Method Get -Uri "$baseUrl/models" -TimeoutSec 2 | Out-Null
        $ready = $true
        break
    }
    catch {}
}

$requestOk = $false
$requestError = $null
if ($ready) {
    $prompt = (1..$PromptWords | ForEach-Object { "contexto" }) -join " "
    $payload = @{
        model = $profile.alias
        messages = @(
            @{ role = "system"; content = "Responda de forma objetiva." },
            @{ role = "user"; content = "Leia o texto a seguir e responda apenas com uma frase curta: $prompt" }
        )
        max_tokens = $MaxTokens
        temperature = 0
    } | ConvertTo-Json -Depth 8

    try {
        Invoke-RestMethod -Method Post -Uri "$baseUrl/chat/completions" -Body $payload -ContentType "application/json" -TimeoutSec 180 | Out-Null
        $requestOk = $true
    }
    catch {
        $requestError = $_.Exception.Message
    }
}

if ($job.State -eq "Running") {
    Stop-Job $job
}
$oldErrorActionPreference = $ErrorActionPreference
$ErrorActionPreference = "Continue"
$jobOutput = Receive-Job $job -Keep *>&1 | ForEach-Object { $_.ToString() }
$ErrorActionPreference = $oldErrorActionPreference
Remove-Job $job -Force

$promptTps = $null
$genTps = $null
$promptTokens = $null
$genTokens = $null
foreach ($line in $jobOutput) {
    if ($line -match "prompt eval time =\s+[\d.]+ ms /\s+(\d+) tokens .*?([\d.]+) tokens per second") {
        $promptTokens = [int]$Matches[1]
        $promptTps = [double]$Matches[2]
    }
    if ($line -match "\beval time =\s+[\d.]+ ms /\s+(\d+) tokens .*?([\d.]+) tokens per second") {
        $genTokens = [int]$Matches[1]
        $genTps = [double]$Matches[2]
    }
}

$result = [PSCustomObject]@{
    profile = $ProfileId
    runtime = $Runtime
    context_size = $profile.context_size
    cache_type_k = $profile.cache_type_k
    cache_type_v = $profile.cache_type_v
    ready = $ready
    request_ok = $requestOk
    request_error = $requestError
    prompt_tokens = $promptTokens
    prompt_tps = $promptTps
    gen_tokens = $genTokens
    gen_tps = $genTps
}

$resultPath = Join-Path $root ("benchmarks\chat-bench-{0}-{1:yyyyMMdd-HHmmss}-{2}.json" -f $ProfileId, (Get-Date), $PID)
$result | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $resultPath -Encoding UTF8
Write-Host "Result: $resultPath"
$result | Format-List
