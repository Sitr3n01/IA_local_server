param(
    [Parameter(Mandatory = $true)]
    [string]$ProfileId,

    [ValidateSet("amd", "unsloth")]
    [string]$Runtime = "amd",

    [int]$Port = 18080,
    [int]$StartupTimeoutSeconds = 180
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

$modelsOk = $false
$chatOk = $false
$chatPreview = $null
$baseUrl = "http://127.0.0.1:$Port/v1"

for ($i = 0; $i -lt $StartupTimeoutSeconds; $i++) {
    Start-Sleep -Seconds 1
    if ($job.State -ne "Running") {
        break
    }
    try {
        Invoke-RestMethod -Method Get -Uri "$baseUrl/models" -TimeoutSec 2 | Out-Null
        $modelsOk = $true
        break
    }
    catch {}
}

if ($modelsOk) {
    try {
        $payload = @{
            model = $profile.alias
            messages = @(@{ role = "user"; content = "Responda apenas: ok" })
            max_tokens = 8
            temperature = 0
        } | ConvertTo-Json -Depth 8
        $chat = Invoke-RestMethod -Method Post -Uri "$baseUrl/chat/completions" -Body $payload -ContentType "application/json" -TimeoutSec 60
        $chatOk = $true
        $chatPreview = $chat.choices[0].message.content
    }
    catch {
        $chatPreview = $_.Exception.Message
    }
}

if ($job.State -eq "Running") {
    Stop-Job $job
}
$oldErrorActionPreference = $ErrorActionPreference
$ErrorActionPreference = "Continue"
$jobOutput = Receive-Job $job -Keep *>&1
$ErrorActionPreference = $oldErrorActionPreference
Remove-Job $job -Force

$result = [PSCustomObject]@{
    profile = $ProfileId
    runtime = $Runtime
    context_size = $profile.context_size
    cache_type_k = $profile.cache_type_k
    cache_type_v = $profile.cache_type_v
    models_endpoint = $modelsOk
    chat_endpoint = $chatOk
    chat_preview = $chatPreview
}

$resultPath = Join-Path $root ("benchmarks\smoke-{0}-{1:yyyyMMdd-HHmmss}.json" -f $ProfileId, (Get-Date))
$result | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $resultPath -Encoding UTF8
Write-Host "Result: $resultPath"
$result | Format-List
Write-Host "--- server output tail ---"
$jobOutput | Select-Object -Last 120
