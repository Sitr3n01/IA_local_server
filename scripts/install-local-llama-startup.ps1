param(
    [string]$DefaultProfileId = "ornith10-9b-q4km-kv-q4-128k",
    [string]$IconPath = "",
    [switch]$SkipUnslothStartup,
    [switch]$StartDefaultModel
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$trayScript = Join-Path $scriptDir "local-llama-tray.ps1"
$unslothScript = Join-Path $scriptDir "start-unsloth-studio-ui.ps1"
$registerMcpScript = Join-Path $scriptDir "register-unsloth-mcp.ps1"
$assetsDir = Join-Path (Split-Path -Parent $scriptDir) "assets"
if (-not (Test-Path -LiteralPath $trayScript)) {
    throw "Tray script not found: $trayScript"
}

function Resolve-IconPath {
    param([string]$Candidate)

    if (-not [string]::IsNullOrWhiteSpace($Candidate) -and (Test-Path -LiteralPath $Candidate)) {
        return (Resolve-Path -LiteralPath $Candidate).Path
    }

    $downloadMatches = Get-ChildItem -Path (Join-Path $env:USERPROFILE "Downloads") -Recurse -File -Include *.png,*.ico -ErrorAction SilentlyContinue |
        Where-Object { $_.BaseName -match "ineffa" } |
        Sort-Object LastWriteTime -Descending

    if ($downloadMatches.Count -gt 0) {
        return $downloadMatches[0].FullName
    }

    return ""
}

function Convert-ToShortcutIcon {
    param([string]$Source)

    if ([string]::IsNullOrWhiteSpace($Source) -or -not (Test-Path -LiteralPath $Source)) {
        return ""
    }

    $resolved = (Resolve-Path -LiteralPath $Source).Path
    if ([System.IO.Path]::GetExtension($resolved).ToLowerInvariant() -eq ".ico") {
        return $resolved
    }

    Add-Type -AssemblyName System.Drawing
    New-Item -ItemType Directory -Force -Path $assetsDir | Out-Null
    $dest = Join-Path $assetsDir "ineffa-tray.ico"
    $bitmap = [System.Drawing.Bitmap]::FromFile($resolved)
    try {
        $handle = $bitmap.GetHicon()
        $icon = [System.Drawing.Icon]::FromHandle($handle)
        $stream = [System.IO.File]::Create($dest)
        try {
            $icon.Save($stream)
        }
        finally {
            $stream.Dispose()
            $icon.Dispose()
        }
    }
    finally {
        $bitmap.Dispose()
    }
    return $dest
}

function New-Shortcut {
    param(
        [string]$Path,
        [string]$TargetPath,
        [string]$Arguments,
        [string]$WorkingDirectory,
        [string]$IconLocation,
        [string]$Description
    )

    $parent = Split-Path -Parent $Path
    New-Item -ItemType Directory -Force -Path $parent | Out-Null
    $shell = New-Object -ComObject WScript.Shell
    $shortcut = $shell.CreateShortcut($Path)
    $shortcut.TargetPath = $TargetPath
    $shortcut.Arguments = $Arguments
    $shortcut.WorkingDirectory = $WorkingDirectory
    if (-not [string]::IsNullOrWhiteSpace($IconLocation)) {
        $shortcut.IconLocation = $IconLocation
    }
    $shortcut.Description = $Description
    $shortcut.Save()
}

$startupDir = [Environment]::GetFolderPath("Startup")
$documentsDir = [Environment]::GetFolderPath("MyDocuments")
New-Item -ItemType Directory -Force -Path $startupDir | Out-Null

$powershell = Join-Path $env:WINDIR "System32\WindowsPowerShell\v1.0\powershell.exe"
$resolvedIcon = Resolve-IconPath -Candidate $IconPath
$shortcutIcon = Convert-ToShortcutIcon -Source $resolvedIcon
$effectiveIcon = if ([string]::IsNullOrWhiteSpace($shortcutIcon)) { "$powershell,0" } else { $shortcutIcon }

$trayArgs = @(
    "-NoProfile",
    "-ExecutionPolicy", "Bypass",
    "-STA",
    "-WindowStyle", "Hidden",
    "-File", "`"$trayScript`"",
    "-DefaultProfileId", "`"$DefaultProfileId`""
)
if ($StartDefaultModel) {
    $trayArgs += "-StartDefault"
}
if (-not [string]::IsNullOrWhiteSpace($shortcutIcon)) {
    $trayArgs += @("-IconPath", "`"$shortcutIcon`"")
}
$trayArguments = $trayArgs -join " "
$trayStartupShortcut = Join-Path $startupDir "Local Llama Model Tray.lnk"
$trayDocumentsShortcut = Join-Path $documentsDir "Local Llama Model Tray.lnk"

New-Shortcut `
    -Path $trayStartupShortcut `
    -TargetPath $powershell `
    -Arguments $trayArguments `
    -WorkingDirectory (Split-Path -Parent $scriptDir) `
    -IconLocation $effectiveIcon `
    -Description "Start local llama model tray and default executor profile"

New-Shortcut `
    -Path $trayDocumentsShortcut `
    -TargetPath $powershell `
    -Arguments $trayArguments `
    -WorkingDirectory (Split-Path -Parent $scriptDir) `
    -IconLocation $effectiveIcon `
    -Description "Quick launcher for local llama model tray"

$unslothStartupShortcut = $null
if (-not $SkipUnslothStartup) {
    if (-not (Test-Path -LiteralPath $unslothScript)) {
        throw "Unsloth startup script not found: $unslothScript"
    }
    $unslothStartupShortcut = Join-Path $startupDir "Unsloth Studio Local.lnk"
    $unslothArguments = "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$unslothScript`""
    New-Shortcut `
        -Path $unslothStartupShortcut `
        -TargetPath $powershell `
        -Arguments $unslothArguments `
        -WorkingDirectory (Split-Path -Parent $scriptDir) `
        -IconLocation $effectiveIcon `
        -Description "Start Unsloth Studio local web UI without loading a duplicate model"
}

if (Test-Path -LiteralPath $registerMcpScript) {
    & powershell -NoProfile -ExecutionPolicy Bypass -File $registerMcpScript -DefaultProfileId $DefaultProfileId | Out-Null
}

[PSCustomObject]@{
    tray_startup_shortcut = $trayStartupShortcut
    tray_documents_shortcut = $trayDocumentsShortcut
    unsloth_startup_shortcut = $unslothStartupShortcut
    target = $powershell
    tray_arguments = $trayArguments
    default_profile = $DefaultProfileId
    icon = $shortcutIcon
}
