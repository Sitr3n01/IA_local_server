param(
    [ValidateSet("amd", "unsloth")]
    [string]$Runtime = "amd"
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

$serverExe = Resolve-LlamaExe -RuntimeName $Runtime -ExeName "llama-server.exe"
$serverDir = Split-Path -Parent $serverExe
$torchLib = "C:\Users\Sitr3n\.unsloth\studio\unsloth_studio\Lib\site-packages\torch\lib"
$ucrtDownlevel = "C:\Windows\System32\downlevel"
$env:PATH = "$serverDir;$torchLib;$ucrtDownlevel;$env:PATH"

if ($Runtime -eq "unsloth") {
    $env:HIP_VISIBLE_DEVICES = "1"
}
else {
    Remove-Item Env:\HIP_VISIBLE_DEVICES -ErrorAction SilentlyContinue
    $env:ROCBLAS_USE_HIPBLASLT = "0"
}

Push-Location $serverDir
try {
    & $serverExe --list-devices
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
