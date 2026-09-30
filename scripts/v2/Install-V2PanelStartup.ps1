[CmdletBinding()]
param(
    [ValidateSet('Canary', 'Final')]
    [string]$Environment = 'Canary',
    [string]$InstallRoot = 'C:\IA\local-ai-v2',
    [switch]$SelfTest,
    [switch]$Apply,
    [switch]$Replace
)

# Registers IA Local (cia-tray.exe) to start at logon for the current user and
# adds it to the Start menu. The tray starts the router and edge tasks when it
# opens, so this one entry decides whether the whole system starts: Task
# Manager lists it on its Startup apps tab by the executable's own name and
# icon, and disabling it there disables IA Local at logon.
#
# It also removes the startup shortcut earlier versions installed, which ran
# the tray through wscript.exe and a VBS launcher and therefore appeared in
# Task Manager as "Windows Script Host".
#
# Preview by default. Nothing is started, and a choice made in Task Manager
# (StartupApproved) is reported but never changed.

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Get-V2StartupCanonicalPath {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not [IO.Path]::IsPathRooted($Path) -or $Path -notmatch '^[A-Za-z]:[\\/]') {
        throw "Path must be a drive-qualified absolute path: $Path"
    }
    return [IO.Path]::GetFullPath($Path).TrimEnd([char[]]@('\', '/'))
}

function Assert-V2StartupLeaf {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Label
    )

    $item = Get-Item -LiteralPath $Path -Force -ErrorAction Stop
    if ($item -isnot [IO.FileInfo]) {
        throw "$Label is not a file: $Path"
    }
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "$Label cannot be a reparse point: $Path"
    }
    return $item
}

function Assert-V2StartupDirectory {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Expected,
        [Parameter(Mandatory = $true)][string]$Label
    )

    $resolved = Get-V2StartupCanonicalPath -Path $Path
    if (-not [string]::Equals($resolved, (Get-V2StartupCanonicalPath -Path $Expected), [StringComparison]::OrdinalIgnoreCase)) {
        throw "$Label is redirected outside its expected profile location: $resolved"
    }
    $item = Get-Item -LiteralPath $resolved -Force -ErrorAction Stop
    if ($item -isnot [IO.DirectoryInfo] -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "$Label is invalid: $resolved"
    }
    return $resolved
}

function Get-V2ShortcutMetadata {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return $null
    }
    [void](Assert-V2StartupLeaf -Path $Path -Label 'Shortcut')
    $shell = $null
    $shortcut = $null
    try {
        $shell = New-Object -ComObject WScript.Shell
        $shortcut = $shell.CreateShortcut($Path)
        return [pscustomobject]@{
            target = [string]$shortcut.TargetPath
            arguments = [string]$shortcut.Arguments
            working_directory = [string]$shortcut.WorkingDirectory
            icon_location = [string]$shortcut.IconLocation
            description = [string]$shortcut.Description
        }
    }
    finally {
        if ($null -ne $shortcut) { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($shortcut) }
        if ($null -ne $shell) { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($shell) }
    }
}

function Write-V2Shortcut {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][object]$Desired
    )

    $shell = $null
    $shortcut = $null
    try {
        $shell = New-Object -ComObject WScript.Shell
        $shortcut = $shell.CreateShortcut($Path)
        $shortcut.TargetPath = $Desired.target
        $shortcut.Arguments = $Desired.arguments
        $shortcut.WorkingDirectory = $Desired.working_directory
        $shortcut.IconLocation = $Desired.icon_location
        $shortcut.Description = $Desired.description
        $shortcut.Save()
    }
    finally {
        if ($null -ne $shortcut) { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($shortcut) }
        if ($null -ne $shell) { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($shell) }
    }
}

function Test-V2ShortcutMatches {
    param(
        [AllowNull()][object]$Actual,
        [Parameter(Mandatory = $true)][object]$Desired
    )

    if ($null -eq $Actual) { return $false }
    $pathsMatch = try {
        [string]::Equals((Get-V2StartupCanonicalPath -Path $Actual.target), (Get-V2StartupCanonicalPath -Path $Desired.target), [StringComparison]::OrdinalIgnoreCase) -and
        [string]::Equals((Get-V2StartupCanonicalPath -Path $Actual.working_directory), (Get-V2StartupCanonicalPath -Path $Desired.working_directory), [StringComparison]::OrdinalIgnoreCase)
    }
    catch { $false }
    return $pathsMatch -and
        [string]::Equals($Actual.arguments, $Desired.arguments, [StringComparison]::Ordinal) -and
        [string]::Equals($Actual.icon_location, $Desired.icon_location, [StringComparison]::OrdinalIgnoreCase) -and
        [string]::Equals($Actual.description, $Desired.description, [StringComparison]::Ordinal)
}

function Get-V2RegistryValue {
    param(
        [Parameter(Mandatory = $true)][string]$Key,
        [Parameter(Mandatory = $true)][string]$Name
    )

    $item = Get-ItemProperty -LiteralPath $Key -ErrorAction SilentlyContinue
    if ($null -eq $item -or $null -eq $item.PSObject.Properties[$Name]) { return $null }
    return $item.$Name
}

# Task Manager keeps its enable/disable choice in StartupApproved as a binary
# value whose first byte is even when the entry is enabled (02, 06) and odd
# when it is disabled (03, 07). Absent means enabled.
function Get-V2StartupApproval {
    param(
        [Parameter(Mandatory = $true)][string]$Key,
        [Parameter(Mandatory = $true)][string]$Name
    )

    $value = Get-V2RegistryValue -Key $Key -Name $Name
    if ($null -eq $value) { return 'enabled-by-default' }
    $bytes = [byte[]]$value
    if ($bytes.Length -gt 0 -and ($bytes[0] -band 1) -eq 1) { return 'disabled-in-task-manager' }
    return 'enabled'
}

$environmentName = $Environment.ToLowerInvariant()
$approvedRoot = Get-V2StartupCanonicalPath -Path 'C:\IA\local-ai-v2'
$resolvedRoot = Get-V2StartupCanonicalPath -Path $InstallRoot
if (-not [string]::Equals($resolvedRoot, $approvedRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw "IA Local startup installation is restricted to '$approvedRoot'."
}
$rootItem = Get-Item -LiteralPath $resolvedRoot -Force -ErrorAction Stop
if ($rootItem -isnot [IO.DirectoryInfo] -or ($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Protected installation root is invalid: $resolvedRoot"
}

$trayPath = Get-V2StartupCanonicalPath -Path (Join-Path $resolvedRoot 'bin\cia-tray.exe')
$panelConfig = Get-V2StartupCanonicalPath -Path (Join-Path $resolvedRoot "config\panel.$environmentName.json")
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$runApprovalKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run'
$folderApprovalKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\StartupFolder'
$valueName = "CIA Local AI v2 $Environment"
$command = '"{0}" -config "{1}"' -f $trayPath, $panelConfig
$shortcutName = if ($Environment -eq 'Canary') { 'IA Local.lnk' } else { 'IA Local (final).lnk' }
$legacyName = "CIA Local AI v2 $Environment Panel.lnk"

if ($SelfTest) {
    if ($Apply -or $Replace) {
        throw 'SelfTest cannot be combined with Apply or Replace.'
    }
    # Exercise the registry and shortcut mechanics away from the real Run key
    # and Start menu: a scratch key under HKCU and files in the temp folder.
    $nonce = [Guid]::NewGuid().ToString('N')
    $scratchKey = "HKCU:\Software\CIA-LocalAI-StartupSelfTest-$nonce"
    $temporaryRoot = Get-V2StartupCanonicalPath -Path ([IO.Path]::GetTempPath())
    $shortcutPath = Join-Path $temporaryRoot "cia-startup-selftest-$nonce.lnk"
    $desired = [pscustomobject]@{
        target = Join-Path $env:SystemRoot 'System32\notepad.exe'
        arguments = '-config "C:\example path\panel.json"'
        working_directory = $env:SystemRoot
        icon_location = (Join-Path $env:SystemRoot 'System32\notepad.exe') + ',0'
        description = 'IA Local startup self-test'
    }
    try {
        New-Item -Path $scratchKey -Force | Out-Null
        New-ItemProperty -LiteralPath $scratchKey -Name $valueName -Value $command -PropertyType String -Force | Out-Null
        if ((Get-V2RegistryValue -Key $scratchKey -Name $valueName) -ne $command) {
            throw 'Registry value round-trip failed.'
        }
        New-ItemProperty -LiteralPath $scratchKey -Name 'enabled' -Value ([byte[]](2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)) -PropertyType Binary -Force | Out-Null
        New-ItemProperty -LiteralPath $scratchKey -Name 'disabled' -Value ([byte[]](3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)) -PropertyType Binary -Force | Out-Null
        if ((Get-V2StartupApproval -Key $scratchKey -Name 'enabled') -ne 'enabled' -or
            (Get-V2StartupApproval -Key $scratchKey -Name 'disabled') -ne 'disabled-in-task-manager' -or
            (Get-V2StartupApproval -Key $scratchKey -Name 'absent') -ne 'enabled-by-default') {
            throw 'StartupApproved decoding failed.'
        }
        Write-V2Shortcut -Path $shortcutPath -Desired $desired
        if (-not (Test-V2ShortcutMatches -Actual (Get-V2ShortcutMetadata -Path $shortcutPath) -Desired $desired)) {
            throw 'Shortcut round-trip failed.'
        }
        [pscustomobject]@{
            mode = 'self-test'
            passed = $true
            mechanism = 'HKCU value round-trip, StartupApproved decoding, WScript shortcut round-trip'
            startup_changed = $false
            processes_started = $false
        } | ConvertTo-Json -Depth 3
        return
    }
    finally {
        Remove-Item -LiteralPath $scratchKey -Recurse -Force -ErrorAction SilentlyContinue
        Remove-Item -LiteralPath $shortcutPath -Force -ErrorAction SilentlyContinue
    }
}

[void](Assert-V2StartupLeaf -Path $trayPath -Label 'IA Local executable')
[void](Assert-V2StartupLeaf -Path $panelConfig -Label "$Environment panel configuration")
$startMenu = Assert-V2StartupDirectory -Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::Programs)) `
    -Expected (Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs') -Label 'Current-user Start menu folder'
$startupFolder = Assert-V2StartupDirectory -Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::Startup)) `
    -Expected (Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\Startup') -Label 'Current-user Startup folder'

$currentCommand = Get-V2RegistryValue -Key $runKey -Name $valueName
$runAction = if ($null -eq $currentCommand) { 'create' }
elseif ([string]::Equals($currentCommand, $command, [StringComparison]::Ordinal)) { 'unchanged' }
elseif ($Replace) { 'replace' }
else { 'blocked-existing' }

$shortcutPath = Join-Path $startMenu $shortcutName
$desiredShortcut = [pscustomobject]@{
    target = $trayPath
    arguments = '-config "{0}"' -f $panelConfig
    working_directory = Split-Path -Parent $trayPath
    icon_location = "$trayPath,0"
    description = 'IA Local: servidor local de IA'
}
$currentShortcut = Get-V2ShortcutMetadata -Path $shortcutPath
$shortcutAction = if ($null -eq $currentShortcut) { 'create' }
elseif (Test-V2ShortcutMatches -Actual $currentShortcut -Desired $desiredShortcut) { 'unchanged' }
elseif ($Replace) { 'replace' }
else { 'blocked-existing' }

# The legacy shortcut is removed only when it is recognisably ours: wscript.exe
# running this installation's tray launcher.
$legacyPath = Join-Path $startupFolder $legacyName
$legacy = Get-V2ShortcutMetadata -Path $legacyPath
$legacyLauncher = Get-V2StartupCanonicalPath -Path (Join-Path $resolvedRoot "launchers\tray-$environmentName.vbs")
$legacyAction = 'absent'
if ($null -ne $legacy) {
    $recognised = $legacy.target -match '\\wscript\.exe$' -and
        $legacy.arguments.Trim('"', ' ').Equals($legacyLauncher, [StringComparison]::OrdinalIgnoreCase)
    $legacyAction = if ($recognised) { 'remove' } else { 'kept-unrecognised' }
}
$legacyApproval = $null -ne (Get-V2RegistryValue -Key $folderApprovalKey -Name $legacyName)

$result = [ordered]@{
    mode = if ($Apply) { 'apply' } else { 'preview' }
    environment = $environmentName
    run_value = "$runKey\$valueName"
    command = $command
    run_action = $runAction
    task_manager = Get-V2StartupApproval -Key $runApprovalKey -Name $valueName
    start_menu_shortcut = $shortcutPath
    start_menu_action = $shortcutAction
    legacy_startup_shortcut = $legacyPath
    legacy_action = $legacyAction
    legacy_task_manager_entry = if ($legacyApproval -and $legacyAction -in @('remove', 'absent')) { 'remove' } elseif ($legacyApproval) { 'kept' } else { 'absent' }
    processes_started = $false
}

if (-not $Apply) {
    [pscustomobject]$result | ConvertTo-Json -Depth 4
    return
}
foreach ($blocked in @(@{ Action = $runAction; What = "Run value $valueName" }, @{ Action = $shortcutAction; What = "Start menu shortcut $shortcutPath" })) {
    if ($blocked.Action -eq 'blocked-existing') {
        throw "$($blocked.What) exists with another definition. Re-run with -Replace only after reviewing the preview."
    }
}

if ($shortcutAction -in @('create', 'replace')) {
    $nonce = [Guid]::NewGuid().ToString('N')
    $staged = Join-Path $startMenu ".ia-local-$nonce.lnk"
    try {
        Write-V2Shortcut -Path $staged -Desired $desiredShortcut
        if (-not (Test-V2ShortcutMatches -Actual (Get-V2ShortcutMetadata -Path $staged) -Desired $desiredShortcut)) {
            throw 'Staged Start menu shortcut failed verification.'
        }
        Move-Item -LiteralPath $staged -Destination $shortcutPath -Force
    }
    finally {
        Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue
    }
}
if ($runAction -in @('create', 'replace')) {
    New-ItemProperty -LiteralPath $runKey -Name $valueName -Value $command -PropertyType String -Force | Out-Null
}
if ($legacyAction -eq 'remove') {
    Remove-Item -LiteralPath $legacyPath -Force
}
if ($result.legacy_task_manager_entry -eq 'remove') {
    Remove-ItemProperty -LiteralPath $folderApprovalKey -Name $legacyName -ErrorAction Stop
}

if ((Get-V2RegistryValue -Key $runKey -Name $valueName) -ne $command) {
    throw 'Run value verification failed after Apply.'
}
if (-not (Test-V2ShortcutMatches -Actual (Get-V2ShortcutMetadata -Path $shortcutPath) -Desired $desiredShortcut)) {
    throw 'Start menu shortcut verification failed after Apply.'
}
if ($legacyAction -eq 'remove' -and (Test-Path -LiteralPath $legacyPath)) {
    throw 'Legacy startup shortcut is still present after Apply.'
}
[pscustomobject]$result | ConvertTo-Json -Depth 4
