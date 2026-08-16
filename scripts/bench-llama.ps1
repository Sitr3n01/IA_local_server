param(
    [string]$ModelPath,

    [ValidateSet("amd", "unsloth")]
    [string]$Runtime = "amd",

    # Points the sweep at a llama.cpp build that is not one of the two pinned
    # runtimes. Required for architectures the pinned b8407 predates, such as the
    # Gated DeltaNet hybrids, which need b10419 or newer.
    [string]$RuntimeRoot,

    [int]$PromptTokens = 512,
    [int]$GenTokens = 128,
    [int]$BatchSize = 2048,
    [int]$UBatchSize = 512,
    [int]$NGpuLayers = 99,
    [int]$Threads = 16,
    [int]$Repetitions = 5,
    [string]$CacheTypeK = "f16",
    [string]$CacheTypeV = "f16",
    [string]$Label,

    # The hipBLASLt path is the closest local analogue to a serving-kernel swap.
    # It has never been A/B'd on gfx1201 in this repository; "0" preserves the
    # pinned runtime's environment so existing baselines stay comparable.
    [ValidateSet("0", "1")]
    [string]$RocBlasUseHipBlasLt = "0",

    [string[]]$ExtraArgs = @()
)

$ErrorActionPreference = "Stop"

# 65..256 collapses throughput by up to 40x on hybrid Gated DeltaNet models. The
# manifest schema refuses the band outright; a bench sweep must not wander into
# it either and then report the result as a tuning data point.
if ($UBatchSize -ge 65 -and $UBatchSize -le 256) {
    throw "UBatchSize $UBatchSize falls in the 65..256 throughput dead band for hybrid models. Pick a value below 65 or above 256."
}

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

    $runtimeRoot = if ([string]::IsNullOrWhiteSpace($RuntimeRoot)) { $runtimeRoots[$RuntimeName] } else { $RuntimeRoot }
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
    throw "ModelPath is required. Example: .\work\local-llama\scripts\bench-llama.ps1 -ModelPath .\work\local-llama\models\model.gguf"
}

$resolvedModelPath = (Resolve-Path -LiteralPath $ModelPath -ErrorAction Stop).Path
if ([IO.Path]::GetExtension($resolvedModelPath).ToLowerInvariant() -ne ".gguf") {
    throw "ModelPath must point to a .gguf file: $resolvedModelPath"
}

$benchExe = Resolve-LlamaExe -RuntimeName $Runtime -ExeName "llama-bench.exe"
$benchDir = Split-Path -Parent $benchExe
$benchDirOut = Join-Path $root "benchmarks"
New-Item -ItemType Directory -Force -Path $benchDirOut | Out-Null
$runLabel = if ([string]::IsNullOrWhiteSpace($Label)) { $Runtime } else { "$Runtime-$Label" }
$outFile = Join-Path $benchDirOut ("llama-bench-{0}-{1:yyyyMMdd-HHmmss}-{2}.txt" -f $runLabel, (Get-Date), $PID)

$torchLib = "C:\Users\Sitr3n\.unsloth\studio\unsloth_studio\Lib\site-packages\torch\lib"
$ucrtDownlevel = "C:\Windows\System32\downlevel"
$env:PATH = "$benchDir;$torchLib;$ucrtDownlevel;$env:PATH"

if ($Runtime -eq "unsloth") {
    # Hide the integrated GPU from HIP so the RX 9070 XT becomes ROCm0.
    $env:HIP_VISIBLE_DEVICES = "1"
    $deviceNote = "HIP_VISIBLE_DEVICES=1, llama.cpp device ROCm0"
}
else {
    # The AMD ROCm 7.2.1 package detects the gfx1201 dGPU and ignores the gfx1036 iGPU.
    Remove-Item Env:\HIP_VISIBLE_DEVICES -ErrorAction SilentlyContinue
    $env:ROCBLAS_USE_HIPBLASLT = $RocBlasUseHipBlasLt
    $deviceNote = "AMD runtime native selection, ROCBLAS_USE_HIPBLASLT=$RocBlasUseHipBlasLt, llama.cpp device ROCm0"
}

$argsList = @(
    "--model", $resolvedModelPath,
    "--device", "ROCm0",
    "--split-mode", "none",
    "--n-gpu-layers", "$NGpuLayers",
    "--flash-attn", "1",
    "--n-prompt", "$PromptTokens",
    "--n-gen", "$GenTokens",
    "--batch-size", "$BatchSize",
    "--ubatch-size", "$UBatchSize",
    "--cache-type-k", $CacheTypeK,
    "--cache-type-v", $CacheTypeV,
    "--threads", "$Threads",
    "--repetitions", "$Repetitions"
)

$argsList += $ExtraArgs

Write-Host "Runtime: $Runtime"
Write-Host "llama-bench: $benchExe"
Write-Host "Model: $resolvedModelPath"
Write-Host "Device isolation: $deviceNote"
Write-Host "Benchmark output: $outFile"

Push-Location $benchDir
try {
    $oldErrorActionPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    & $benchExe @argsList *>&1 | ForEach-Object { $_.ToString() } | Tee-Object -FilePath $outFile
    $exitCode = $LASTEXITCODE
    $ErrorActionPreference = $oldErrorActionPreference
    exit $exitCode
}
finally {
    if ($oldErrorActionPreference) {
        $ErrorActionPreference = $oldErrorActionPreference
    }
    Pop-Location
}
