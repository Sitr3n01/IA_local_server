param(
    [string]$ModelPath,

    [ValidateSet("amd", "unsloth")]
    [string]$Runtime = "amd",

    [string]$HostName = "127.0.0.1",
    [int]$Port = 8080,
    [int]$ContextSize = 4096,
    [int]$BatchSize = 2048,
    [int]$UBatchSize = 512,
    [int]$Parallel = 1,
    [string]$CacheTypeK = "f16",
    [string]$CacheTypeV = "f16",
    [string]$Alias = "local-model",
    [switch]$Metrics,
    [switch]$ContextShift,
    [ValidateSet("on", "off", "auto")]
    [string]$Reasoning = "auto",
    [int]$ReasoningBudget = -1,
    [string]$ChatTemplateKwargs = "",
    [switch]$NoWebUI,
    [string[]]$ExtraArgs = @()
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $scriptDir

$runtimeRoots = @{
    amd     = Join-Path $root "amd\llama_cpp_b8407_rocm_7.2.1"
    unsloth = "C:\Users\Sitr3n\.unsloth\llama.cpp\build\bin\Release"
}

function Resolve-LlamaExe {
    param(
        [string]$RuntimeName,
        [string]$ExeName
    )

    $runtimeRoot = $runtimeRoots[$RuntimeName]
    if (-not (Test-Path -LiteralPath $runtimeRoot)) {
        throw "Runtime '$RuntimeName' not found at $runtimeRoot"
    }

    $match = Get-ChildItem -LiteralPath $runtimeRoot -Recurse -Filter $ExeName -File -ErrorAction SilentlyContinue |
        Sort-Object FullName |
        Select-Object -First 1

    if (-not $match) {
        throw "$ExeName not found under $runtimeRoot"
    }

    return $match.FullName
}

if ([string]::IsNullOrWhiteSpace($ModelPath)) {
    throw "ModelPath is required. Example: .\work\local-llama\scripts\start-llama-server.ps1 -ModelPath .\work\local-llama\models\model.gguf"
}

$resolvedModelPath = (Resolve-Path -LiteralPath $ModelPath -ErrorAction Stop).Path
if ([IO.Path]::GetExtension($resolvedModelPath).ToLowerInvariant() -ne ".gguf") {
    throw "ModelPath must point to a .gguf file: $resolvedModelPath"
}

$serverExe = Resolve-LlamaExe -RuntimeName $Runtime -ExeName "llama-server.exe"
$serverDir = Split-Path -Parent $serverExe
$logDir = Join-Path $root "logs"
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
$logFile = Join-Path $logDir ("llama-server-{0:yyyyMMdd-HHmmss}.log" -f (Get-Date))

$torchLib = "C:\Users\Sitr3n\.unsloth\studio\unsloth_studio\Lib\site-packages\torch\lib"
$ucrtDownlevel = "C:\Windows\System32\downlevel"
$env:PATH = "$serverDir;$torchLib;$ucrtDownlevel;$env:PATH"

if ($Runtime -eq "unsloth") {
    # Hide the integrated GPU from HIP so the RX 9070 XT becomes ROCm0.
    $env:HIP_VISIBLE_DEVICES = "1"
    $deviceNote = "HIP_VISIBLE_DEVICES=1, llama.cpp device ROCm0"
}
else {
    # The AMD ROCm 7.2.1 package detects the gfx1201 dGPU and ignores the gfx1036 iGPU.
    Remove-Item Env:\HIP_VISIBLE_DEVICES -ErrorAction SilentlyContinue
    $env:ROCBLAS_USE_HIPBLASLT = "0"
    $deviceNote = "AMD runtime native selection, ROCBLAS_USE_HIPBLASLT=0, llama.cpp device ROCm0"
}

$argsList = @(
    "--model", $resolvedModelPath,
    "--host", $HostName,
    "--port", "$Port",
    "--alias", $Alias,
    "--device", "ROCm0",
    "--split-mode", "none",
    "--gpu-layers", "99",
    "--flash-attn", "on",
    "--ctx-size", "$ContextSize",
    "--batch-size", "$BatchSize",
    "--ubatch-size", "$UBatchSize",
    "--cache-type-k", $CacheTypeK,
    "--cache-type-v", $CacheTypeV,
    "--parallel", "$Parallel",
    "--cont-batching",
    "--warmup",
    "--log-file", $logFile,
    "--log-timestamps"
)

if ($NoWebUI) {
    $argsList += "--no-webui"
}

if ($Metrics) {
    $argsList += "--metrics"
}

if ($ContextShift) {
    $argsList += "--context-shift"
}

if ($Reasoning -ne "auto") {
    $argsList += @("--reasoning", $Reasoning)
}

if ($ReasoningBudget -ge 0) {
    $argsList += @("--reasoning-budget", "$ReasoningBudget")
}

if (-not [string]::IsNullOrWhiteSpace($ChatTemplateKwargs)) {
    $env:LLAMA_CHAT_TEMPLATE_KWARGS = $ChatTemplateKwargs
}

$argsList += $ExtraArgs

Write-Host "Runtime: $Runtime"
Write-Host "llama-server: $serverExe"
Write-Host "Model: $resolvedModelPath"
Write-Host "Device isolation: $deviceNote"
Write-Host "API: http://$HostName`:$Port/v1"
Write-Host "Log: $logFile"

Push-Location $serverDir
try {
    & $serverExe @argsList
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
