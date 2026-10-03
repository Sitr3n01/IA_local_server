[CmdletBinding()]
param(
    [ValidateSet('Canary', 'Final')]
    [string]$Environment = 'Canary',
    [string]$TrayBinary = 'C:\IA\local-ai-v2\bin\cia-tray.exe',
    [string]$PanelConfig,
    [switch]$Apply
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ([string]::IsNullOrWhiteSpace($PanelConfig)) {
    $PanelConfig = Join-Path 'C:\IA\local-ai-v2\config' ("panel.{0}.json" -f $Environment.ToLowerInvariant())
}
foreach ($required in @($TrayBinary, $PanelConfig)) {
    if (-not [IO.Path]::IsPathRooted($required) -or -not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Claude shortcut dependency must be an existing absolute file: $required"
    }
}
$desktopDirectory = [Environment]::GetFolderPath('Desktop')
$programsDirectory = [Environment]::GetFolderPath('Programs')
if ([string]::IsNullOrWhiteSpace($desktopDirectory) -or [string]::IsNullOrWhiteSpace($programsDirectory)) {
    throw 'The current user has no Desktop or Start-menu Programs directory.'
}
$shell = New-Object -ComObject WScript.Shell
$claudePackage = Get-AppxPackage -Name Claude | Sort-Object Version -Descending | Select-Object -First 1
if ($null -eq $claudePackage) { throw 'The native Claude Desktop package is not installed.' }
$assetDirectory = Join-Path $env:LOCALAPPDATA 'IA Local\icons'

# Package the installed app's PNG frames unchanged in a Windows icon file.
# Keep the icon outside the versioned MSIX package and use a content-specific
# filename so the Shell cannot reuse a cached image from an earlier icon.
$frames = @()
foreach ($size in @(16, 24, 32, 48, 64, 256)) {
    $source = Join-Path $claudePackage.InstallLocation ("assets\Square44x44Logo.targetsize-{0}_altform-unplated.png" -f $size)
    $bytes = [IO.File]::ReadAllBytes($source)
    if ($bytes.Length -lt 24 -or [BitConverter]::ToString($bytes, 0, 8) -ne '89-50-4E-47-0D-0A-1A-0A' -or
        [Net.IPAddress]::NetworkToHostOrder([BitConverter]::ToInt32($bytes, 16)) -ne $size -or
        [Net.IPAddress]::NetworkToHostOrder([BitConverter]::ToInt32($bytes, 20)) -ne $size) {
        throw "Unexpected Claude icon frame: $source"
    }
    $frames += [pscustomobject]@{ size = $size; bytes = $bytes }
}
$iconStream = [IO.MemoryStream]::new()
$iconWriter = [IO.BinaryWriter]::new($iconStream)
try {
    $iconWriter.Write([uint16]0)
    $iconWriter.Write([uint16]1)
    $iconWriter.Write([uint16]$frames.Count)
    $offset = 6 + 16 * $frames.Count
    foreach ($frame in $frames) {
        $dimension = if ($frame.size -eq 256) { 0 } else { $frame.size }
        $iconWriter.Write([byte]$dimension)
        $iconWriter.Write([byte]$dimension)
        $iconWriter.Write([uint16]0)
        $iconWriter.Write([uint16]1)
        $iconWriter.Write([uint16]32)
        $iconWriter.Write([uint32]$frame.bytes.Length)
        $iconWriter.Write([uint32]$offset)
        $offset += $frame.bytes.Length
    }
    foreach ($frame in $frames) { $iconWriter.Write([byte[]]$frame.bytes) }
    $iconWriter.Flush()
    $iconBytes = $iconStream.ToArray()
}
finally { $iconWriter.Dispose(); $iconStream.Dispose() }
$iconHasher = [Security.Cryptography.SHA256]::Create()
try { $iconDigest = [BitConverter]::ToString($iconHasher.ComputeHash($iconBytes)).Replace('-', '').ToLowerInvariant() }
finally { $iconHasher.Dispose() }
$iconPath = Join-Path $assetDirectory ('claude-' + $iconDigest.Substring(0, 16) + '.ico')

$plan = @()
$legacyPlan = @()
$arguments = '-config "{0}" -claude-local' -f $PanelConfig
foreach ($directory in @($desktopDirectory, (Join-Path $programsDirectory 'IA Local'))) {
    $path = Join-Path $directory 'Claude Local.lnk'
    $shortcut = $shell.CreateShortcut($path)
    $action = 'create'
    if (Test-Path -LiteralPath $path -PathType Leaf) {
        if ($shortcut.TargetPath -ne $TrayBinary -or $shortcut.Arguments -ne $arguments) {
            $action = 'blocked-existing'
        }
        elseif ($shortcut.IconLocation -ne ($iconPath + ',0')) { $action = 'update-icon' }
        else { $action = 'unchanged' }
    }
    $plan += [pscustomobject]@{ path = $path; target = $TrayBinary; arguments = $arguments; icon = ($iconPath + ',0'); action = $action }
    foreach ($legacy in @(
        @{ name = 'Claude oficial'; action = '-claude-open' },
        @{ name = 'Claude Gateway'; action = '-claude-local' }
    )) {
        $path = Join-Path $directory ($legacy.name + '.lnk')
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { continue }
        $shortcut = $shell.CreateShortcut($path)
        $expectedArguments = '-config "{0}" {1}' -f $PanelConfig, $legacy.action
        $action = if ($shortcut.TargetPath -eq $TrayBinary -and $shortcut.Arguments -eq $expectedArguments) { 'remove-owned-legacy' } else { 'preserve-other-shortcut' }
        $legacyPlan += [pscustomobject]@{ path = $path; action = $action }
    }
}
if (-not $Apply) {
    [pscustomobject]@{ mode = 'preview'; official = 'native Claude app unchanged'; icon = $iconPath; icon_frames = $frames.Count; shortcuts = $plan; legacy = $legacyPlan } | ConvertTo-Json -Depth 4
    return
}
if (@($plan | Where-Object { $_.action -eq 'blocked-existing' }).Count -gt 0) {
    throw 'An existing Claude shortcut has another target or arguments; no shortcut was changed.'
}
if (-not (Test-Path -LiteralPath $assetDirectory -PathType Container)) {
    New-Item -ItemType Directory -Path $assetDirectory -Force | Out-Null
}
if (-not (Test-Path -LiteralPath $iconPath -PathType Leaf) -or
    (Get-FileHash -LiteralPath $iconPath -Algorithm SHA256).Hash -ne $iconDigest) {
    [IO.File]::WriteAllBytes($iconPath, $iconBytes)
}
foreach ($entry in $plan) {
    if ($entry.action -eq 'unchanged') { continue }
    $parentDirectory = Split-Path -Parent $entry.path
    if (-not (Test-Path -LiteralPath $parentDirectory -PathType Container)) {
        New-Item -ItemType Directory -Path $parentDirectory | Out-Null
    }
    $shortcut = $shell.CreateShortcut($entry.path)
    $shortcut.TargetPath = $entry.target
    $shortcut.Arguments = $entry.arguments
    $shortcut.WorkingDirectory = Split-Path -Parent $TrayBinary
    $shortcut.Description = 'Abre o Claude Desktop com os modelos do gateway IA Local.'
    $shortcut.IconLocation = $entry.icon
    $shortcut.Save()
}
# Saving a shortcut does not guarantee that every Shell surface refreshes its
# cached image. Notify the changed items explicitly without resetting the cache.
if (-not ('IALocal.ClaudeShortcutShell' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
namespace IALocal {
    public static class ClaudeShortcutShell {
        [DllImport("shell32.dll", CharSet = CharSet.Unicode)]
        private static extern void SHChangeNotify(int events, uint flags, string path, IntPtr item2);
        public static void Refresh(string path) {
            SHChangeNotify(0x2000, 0x1005, path, IntPtr.Zero);
        }
    }
}
'@
}
[IALocal.ClaudeShortcutShell]::Refresh($iconPath)
foreach ($entry in $plan) { [IALocal.ClaudeShortcutShell]::Refresh($entry.path) }
$ownedLegacy = @($legacyPlan | Where-Object { $_.action -eq 'remove-owned-legacy' })
$backupDirectory = $null
if ($ownedLegacy.Count -gt 0) {
    $backupDirectory = Join-Path $env:LOCALAPPDATA ('IA Local\shortcut-backups\' + [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ'))
    New-Item -ItemType Directory -Path $backupDirectory -Force | Out-Null
    foreach ($entry in $ownedLegacy) {
        $location = if ((Split-Path -Parent $entry.path) -eq $desktopDirectory) { 'desktop' } else { 'start-menu' }
        Copy-Item -LiteralPath $entry.path -Destination (Join-Path $backupDirectory ($location + '-' + [IO.Path]::GetFileName($entry.path)))
        Remove-Item -LiteralPath $entry.path -Force
    }
}
[pscustomobject]@{ mode = 'apply'; official = 'native Claude app unchanged'; icon = $iconPath; icon_frames = $frames.Count; shortcuts = $plan; legacy = $legacyPlan; legacy_backup = $backupDirectory } | ConvertTo-Json -Depth 4
