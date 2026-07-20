param(
    [string]$MatrixPath,
    [string]$CatalogRoot = "C:\IA\unsloth-catalog",
    [string]$HostName = "127.0.0.1",
    [int]$Port = 8888,
    [string]$ApiKey = "",
    [switch]$Register,
    [switch]$ReplaceScanFolders,
    [switch]$Prune
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $scriptDir
if ([string]::IsNullOrWhiteSpace($MatrixPath)) {
    $MatrixPath = Join-Path $root "model-test-matrix.json"
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

function Get-StudioApiKey {
    param([string]$CurrentKey)

    if (-not [string]::IsNullOrWhiteSpace($CurrentKey)) {
        return $CurrentKey
    }

    $latestLog = Get-ChildItem -LiteralPath (Join-Path $root "logs") -Filter "unsloth-studio-*-stdout*.log" -ErrorAction SilentlyContinue |
        Sort-Object LastWriteTime -Descending |
        Select-Object -First 1
    if (-not $latestLog) {
        return ""
    }

    $line = Select-String -Path $latestLog.FullName -Pattern "API Key:" | Select-Object -Last 1
    if (-not $line) {
        return ""
    }

    return ($line.Line -replace "^.*API Key:\s*", "").Trim()
}

function New-CatalogHardLink {
    param(
        [string]$SourcePath,
        [string]$TargetPath
    )

    $targetDir = Split-Path -Parent $TargetPath
    New-Item -ItemType Directory -Force -Path $targetDir | Out-Null

    if (Test-Path -LiteralPath $TargetPath) {
        $sourceSize = (Get-Item -LiteralPath $SourcePath).Length
        $targetSize = (Get-Item -LiteralPath $TargetPath).Length
        if ($sourceSize -eq $targetSize) {
            return
        }

        $resolvedCatalog = [IO.Path]::GetFullPath($CatalogRoot)
        $resolvedTarget = [IO.Path]::GetFullPath($TargetPath)
        if (-not $resolvedTarget.StartsWith($resolvedCatalog, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to replace target outside catalog: $TargetPath"
        }
        Remove-Item -LiteralPath $TargetPath -Force
    }

    New-Item -ItemType HardLink -Path $TargetPath -Target $SourcePath | Out-Null
}

$matrix = Get-Content -Raw -LiteralPath $MatrixPath | ConvertFrom-Json
New-Item -ItemType Directory -Force -Path $CatalogRoot | Out-Null

$entries = @()
$desiredCatalogPaths = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
foreach ($profile in $matrix.profiles) {
    $sourcePath = Resolve-ProfileModelPath -BaseRoot $root -LocalPath $profile.local_path
    $exists = Test-Path -LiteralPath $sourcePath -PathType Leaf
    if ($exists) {
        $folderName = if ($profile.catalog_folder) { $profile.catalog_folder } else { $profile.family }
        $fileName = if ($profile.catalog_filename) { $profile.catalog_filename } else { $profile.filename }
        $targetPath = Join-Path (Join-Path $CatalogRoot $folderName) $fileName
        [void]$desiredCatalogPaths.Add([IO.Path]::GetFullPath($targetPath))
        New-CatalogHardLink -SourcePath $sourcePath -TargetPath $targetPath
    }
    else {
        $targetPath = $null
    }

    $entries += [PSCustomObject]@{
        id = $profile.id
        display_name = $profile.display_name
        source_path = $sourcePath
        catalog_path = $targetPath
        exists = $exists
    }
}

if ($Prune) {
    $resolvedCatalog = [IO.Path]::GetFullPath($CatalogRoot).TrimEnd("\")
    foreach ($file in Get-ChildItem -LiteralPath $CatalogRoot -Recurse -File -Filter "*.gguf" -ErrorAction SilentlyContinue) {
        $resolvedFile = [IO.Path]::GetFullPath($file.FullName)
        if (-not $resolvedFile.StartsWith($resolvedCatalog, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to prune file outside catalog: $($file.FullName)"
        }
        if (-not $desiredCatalogPaths.Contains($resolvedFile)) {
            Remove-Item -LiteralPath $file.FullName -Force
        }
    }

    Get-ChildItem -LiteralPath $CatalogRoot -Recurse -Directory -ErrorAction SilentlyContinue |
        Sort-Object FullName -Descending |
        Where-Object { -not (Get-ChildItem -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue | Select-Object -First 1) } |
        ForEach-Object {
            $resolvedDir = [IO.Path]::GetFullPath($_.FullName)
            if ($resolvedDir.StartsWith($resolvedCatalog, [StringComparison]::OrdinalIgnoreCase)) {
                Remove-Item -LiteralPath $_.FullName -Force
            }
        }
}

$scanFolders = @()
if ($Register -or $ReplaceScanFolders) {
    $ApiKey = Get-StudioApiKey -CurrentKey $ApiKey
    if ([string]::IsNullOrWhiteSpace($ApiKey)) {
        throw "ApiKey not provided and no Unsloth Studio API key was found in local logs."
    }

    $baseUrl = "http://$HostName`:$Port"
    $headers = @{ Authorization = "Bearer $ApiKey" }

    $current = Invoke-RestMethod -Method Get -Uri "$baseUrl/api/hub/scan-folders" -Headers $headers -TimeoutSec 20
    $resolvedCatalog = [IO.Path]::GetFullPath($CatalogRoot).TrimEnd("\")

    if ($ReplaceScanFolders) {
        foreach ($folder in @($current.folders)) {
            $resolvedFolder = [IO.Path]::GetFullPath([string]$folder.path).TrimEnd("\")
            if (-not $resolvedFolder.Equals($resolvedCatalog, [StringComparison]::OrdinalIgnoreCase)) {
                Invoke-RestMethod -Method Delete -Uri "$baseUrl/api/hub/scan-folders/$($folder.id)" -Headers $headers -TimeoutSec 20 | Out-Null
            }
        }
    }

    $current = Invoke-RestMethod -Method Get -Uri "$baseUrl/api/hub/scan-folders" -Headers $headers -TimeoutSec 20
    $alreadyRegistered = $false
    foreach ($folder in @($current.folders)) {
        $resolvedFolder = [IO.Path]::GetFullPath([string]$folder.path).TrimEnd("\")
        if ($resolvedFolder.Equals($resolvedCatalog, [StringComparison]::OrdinalIgnoreCase)) {
            $alreadyRegistered = $true
        }
    }

    if (-not $alreadyRegistered) {
        $body = @{ path = $CatalogRoot } | ConvertTo-Json
        Invoke-RestMethod -Method Post -Uri "$baseUrl/api/hub/scan-folders" -Headers $headers -ContentType "application/json" -Body $body -TimeoutSec 20 | Out-Null
    }

    $scanFolders = (Invoke-RestMethod -Method Get -Uri "$baseUrl/api/hub/scan-folders" -Headers $headers -TimeoutSec 20).folders
}

[PSCustomObject]@{
    catalog_root = $CatalogRoot
    linked_models = @($entries | Where-Object { $_.exists }).Count
    missing_models = @($entries | Where-Object { -not $_.exists }).Count
    entries = $entries
    scan_folders = $scanFolders
}
