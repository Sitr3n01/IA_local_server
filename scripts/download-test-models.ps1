param(
    [string]$MatrixPath,
    [string]$ProfileId
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $scriptDir
if ([string]::IsNullOrWhiteSpace($MatrixPath)) {
    $MatrixPath = Join-Path $root "model-test-matrix.json"
}

$hf = "C:\Users\Sitr3n\.unsloth\studio\unsloth_studio\Scripts\hf.exe"
if (-not (Test-Path -LiteralPath $hf)) {
    throw "hf.exe not found at $hf"
}

$iaRoot = "C:\IA"
$hfHome = Join-Path $iaRoot "hf-home"
New-Item -ItemType Directory -Force -Path $hfHome | Out-Null
$env:HF_HOME = $hfHome
$env:HF_HUB_CACHE = Join-Path $hfHome "hub"
$env:HF_XET_CACHE = Join-Path $hfHome "xet"
if ([string]::IsNullOrWhiteSpace($env:HF_HUB_DISABLE_XET)) {
    $env:HF_HUB_DISABLE_XET = "1"
}

$matrix = Get-Content -Raw -LiteralPath $MatrixPath | ConvertFrom-Json
$profiles = $matrix.profiles
if (-not [string]::IsNullOrWhiteSpace($ProfileId)) {
    $profiles = $profiles | Where-Object { $_.id -eq $ProfileId }
    if (-not $profiles) {
        throw "ProfileId not found: $ProfileId"
    }
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

foreach ($profile in $profiles) {
    $localPath = Resolve-ProfileModelPath -BaseRoot $root -LocalPath $profile.local_path
    $localDir = Split-Path -Parent $localPath
    New-Item -ItemType Directory -Force -Path $localDir | Out-Null

    if (Test-Path -LiteralPath $localPath) {
        Write-Host "Already present: $localPath"
        continue
    }

    Write-Host "Downloading $($profile.repo) / $($profile.filename)"
    & $hf download $profile.repo $profile.filename --local-dir $localDir --max-workers 4
    if ($LASTEXITCODE -ne 0) {
        throw "hf download failed for $($profile.id)"
    }

    if (-not (Test-Path -LiteralPath $localPath)) {
        $downloaded = Get-ChildItem -LiteralPath $localDir -Recurse -File -Filter $profile.filename -ErrorAction SilentlyContinue |
            Select-Object -First 1
        if (-not $downloaded) {
            throw "Downloaded file not found for $($profile.id)"
        }
    }
}
