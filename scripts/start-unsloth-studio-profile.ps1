param(
    [Parameter(Mandatory = $true)]
    [string]$ProfileId,

    [int]$Port = 8888,
    [string]$HostName = "127.0.0.1",
    [switch]$ApiOnly,
    [switch]$StudioVerbose,
    [int]$Parallel = 1,
    [string[]]$ExtraArgs = @()
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $scriptDir
$matrixPath = Join-Path $root "model-test-matrix.json"
$unsloth = "C:\Users\Sitr3n\.unsloth\studio\bin\unsloth.exe"
$iaRoot = "C:\IA"
$hfHome = Join-Path $iaRoot "hf-home"

if (-not (Test-Path -LiteralPath $unsloth)) {
    throw "Unsloth CLI not found: $unsloth"
}

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
$resolvedModelPath = (Resolve-Path -LiteralPath $modelPath -ErrorAction Stop).Path

if ([IO.Path]::GetExtension($resolvedModelPath).ToLowerInvariant() -ne ".gguf") {
    throw "Unsloth Studio profile launcher expects a .gguf file: $resolvedModelPath"
}

New-Item -ItemType Directory -Force -Path $hfHome | Out-Null
$env:HF_HOME = $hfHome
$env:HF_HUB_CACHE = Join-Path $hfHome "hub"
$env:HF_XET_CACHE = Join-Path $hfHome "xet"

$argsList = @(
    "studio",
    "run",
    "--model", $resolvedModelPath,
    "--host", $HostName,
    "--port", "$Port",
    "--parallel", "$Parallel",
    "--max-seq-length", "$($profile.context_size)",
    "--no-cloudflare",
    "--no-secure",
    "--disable-tools",
    "--ctx-size", "$($profile.context_size)",
    "--cache-type-k", "$($profile.cache_type_k)",
    "--cache-type-v", "$($profile.cache_type_v)",
    "--flash-attn", "on",
    "--reasoning", "off",
    "--reasoning-budget", "0"
)

if ($ApiOnly) {
    $argsList += "--api-only"
}

if ($StudioVerbose) {
    $argsList += "--verbose"
}

$argsList += $ExtraArgs

Write-Host "Unsloth Studio profile: $ProfileId"
Write-Host "Model: $resolvedModelPath"
Write-Host "HF cache: $env:HF_HUB_CACHE"
Write-Host "URL: http://$HostName`:$Port"
Write-Host "Command: $unsloth $($argsList -join ' ')"

& $unsloth @argsList
exit $LASTEXITCODE
