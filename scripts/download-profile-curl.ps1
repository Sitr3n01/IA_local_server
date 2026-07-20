param(
    [Parameter(Mandatory = $true)]
    [string]$ProfileId,

    [string]$MatrixPath
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $scriptDir
if ([string]::IsNullOrWhiteSpace($MatrixPath)) {
    $MatrixPath = Join-Path $root "model-test-matrix.json"
}

$matrix = Get-Content -Raw -LiteralPath $MatrixPath | ConvertFrom-Json
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
$modelDir = Split-Path -Parent $modelPath
New-Item -ItemType Directory -Force -Path $modelDir | Out-Null

if (Test-Path -LiteralPath $modelPath) {
    Write-Host "Already present: $modelPath"
    exit 0
}

$partialPath = "$modelPath.part"
$hfPartial = Get-ChildItem -LiteralPath (Join-Path $modelDir ".cache\huggingface\download") -File -Filter "*.incomplete" -ErrorAction SilentlyContinue |
    Sort-Object Length -Descending |
    Select-Object -First 1
if ($hfPartial -and -not (Test-Path -LiteralPath $partialPath)) {
    Move-Item -LiteralPath $hfPartial.FullName -Destination $partialPath
}

$escapedFilename = ($profile.filename -replace "\\", "/").Split("/") | ForEach-Object { [uri]::EscapeDataString($_) }
$urlPath = ($escapedFilename -join "/")
$url = "https://huggingface.co/$($profile.repo)/resolve/main/$urlPath"

Write-Host "Downloading $($profile.repo) / $($profile.filename)"
Write-Host "Target: $modelPath"
if (Test-Path -LiteralPath $partialPath) {
    $existing = (Get-Item -LiteralPath $partialPath).Length
    Write-Host "Resuming from $existing bytes"
}

& curl.exe -L --fail --retry 8 --retry-delay 5 --retry-all-errors -C - -o $partialPath $url
if ($LASTEXITCODE -ne 0) {
    throw "curl download failed for $ProfileId"
}

Move-Item -LiteralPath $partialPath -Destination $modelPath -Force
Write-Host $modelPath
