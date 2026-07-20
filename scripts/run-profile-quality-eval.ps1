param(
    [Parameter(Mandatory = $true)]
    [string]$ProfileId,

    [ValidateSet("amd", "unsloth")]
    [string]$Runtime = "amd",

    [int]$Port = 18380,
    [int]$StartupTimeoutSeconds = 300,
    [int]$MaxTokens = 96
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

function Normalize-Answer {
    param([string]$Text)

    if ($null -eq $Text) {
        return ""
    }

    $normalized = $Text -replace "(?is)<think>.*?</think>", ""
    $normalized = $normalized -replace "(?is)</think>", ""
    return $normalized.Trim()
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

$tests = @(
    @{
        id = "instruction_exact"
        prompt = "Responda exatamente, sem aspas e sem pontuacao: ALFA-739"
        expected = "ALFA-739"
        mode = "exact"
    },
    @{
        id = "arithmetic"
        prompt = "Quanto e (137 * 19) - 48? Responda apenas o numero."
        expected = "2555"
        mode = "contains"
    },
    @{
        id = "list_reasoning"
        prompt = "Em Python, qual e o resultado de sorted(set([5,2,5,4,2,6]))? Responda apenas a lista."
        expected = "[2, 4, 5, 6]"
        mode = "contains"
    },
    @{
        id = "local_config_recall"
        prompt = "Fatos: porta do painel=8090; alias do servidor=local-model; compactacao automatica=85%. Responda apenas: porta, alias, porcentagem."
        expected = "8090 local-model 85%"
        mode = "all_terms"
        terms = @("8090", "local-model", "85")
    }
)

$results = @()
if ($ready) {
    foreach ($test in $tests) {
        $payload = @{
            model = $profile.alias
            messages = @(
                @{ role = "system"; content = "Siga a instrucao do usuario com resposta curta e direta." },
                @{ role = "user"; content = $test.prompt }
            )
            max_tokens = $MaxTokens
            temperature = 0
        } | ConvertTo-Json -Depth 8

        $raw = $null
        $answer = ""
        $passed = $false
        $errorMessage = $null
        try {
            $response = Invoke-RestMethod -Method Post -Uri "$baseUrl/chat/completions" -Body $payload -ContentType "application/json" -TimeoutSec 180
            $raw = $response.choices[0].message.content
            $answer = Normalize-Answer -Text $raw
            if ($test.mode -eq "exact") {
                $passed = $answer -eq $test.expected
            }
            elseif ($test.mode -eq "contains") {
                $passed = $answer.Contains($test.expected)
            }
            elseif ($test.mode -eq "all_terms") {
                $passed = $true
                foreach ($term in $test.terms) {
                    if (-not $answer.Contains($term)) {
                        $passed = $false
                    }
                }
            }
        }
        catch {
            $errorMessage = $_.Exception.Message
        }

        $results += [PSCustomObject]@{
            id = $test.id
            passed = $passed
            expected = $test.expected
            answer = $answer
            raw = $raw
            error = $errorMessage
        }
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
foreach ($line in $jobOutput) {
    if ($line -match "prompt eval time =\s+[\d.]+ ms /\s+\d+ tokens .*?([\d.]+) tokens per second") {
        $promptTps = [double]$Matches[1]
    }
    if ($line -match "\beval time =\s+[\d.]+ ms /\s+\d+ tokens .*?([\d.]+) tokens per second") {
        $genTps = [double]$Matches[1]
    }
}

$passedCount = @($results | Where-Object { $_.passed }).Count
$result = [PSCustomObject]@{
    profile = $ProfileId
    runtime = $Runtime
    context_size = $profile.context_size
    cache_type_k = $profile.cache_type_k
    cache_type_v = $profile.cache_type_v
    ready = $ready
    passed = $passedCount
    total = @($tests).Count
    prompt_tps_last = $promptTps
    gen_tps_last = $genTps
    tests = $results
}

$resultPath = Join-Path $root ("benchmarks\quality-{0}-{1:yyyyMMdd-HHmmss}-{2}.json" -f $ProfileId, (Get-Date), $PID)
$result | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $resultPath -Encoding UTF8
Write-Host "Result: $resultPath"
$result | ConvertTo-Json -Depth 10
