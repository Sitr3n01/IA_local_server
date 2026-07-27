[CmdletBinding()]
param(
    [string]$InstallRoot = 'C:\IA\local-ai-v2',
    [switch]$SelfTest,
    [switch]$Apply,
    [switch]$Replace
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Get-V2PanelStartupCanonicalPath {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not [IO.Path]::IsPathRooted($Path) -or $Path -notmatch '^[A-Za-z]:[\\/]') {
        throw "Path must be a drive-qualified absolute path: $Path"
    }
    return [IO.Path]::GetFullPath($Path).TrimEnd([char[]]@('\', '/'))
}

function Assert-V2PanelStartupLeaf {
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

function Get-V2PanelShortcutMetadata {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return $null
    }
    [void](Assert-V2PanelStartupLeaf -Path $Path -Label 'Startup shortcut')

    $shell = $null
    $shortcut = $null
    try {
        $shell = New-Object -ComObject WScript.Shell
        $shortcut = $shell.CreateShortcut($Path)
        return [pscustomobject]@{
            target = [string]$shortcut.TargetPath
            arguments = [string]$shortcut.Arguments
            working_directory = [string]$shortcut.WorkingDirectory
            window_style = [int]$shortcut.WindowStyle
            icon_location = [string]$shortcut.IconLocation
            description = [string]$shortcut.Description
        }
    }
    finally {
        if ($null -ne $shortcut) { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($shortcut) }
        if ($null -ne $shell) { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($shell) }
    }
}

function Write-V2PanelShortcutFile {
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
        $shortcut.WindowStyle = $Desired.window_style
        $shortcut.IconLocation = $Desired.icon_location
        $shortcut.Description = $Desired.description
        $shortcut.Save()
    }
    finally {
        if ($null -ne $shortcut) { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($shortcut) }
        if ($null -ne $shell) { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($shell) }
    }
}

function Test-V2PanelShortcutMatches {
    param(
        [AllowNull()][object]$Actual,
        [Parameter(Mandatory = $true)][object]$Desired
    )

    if ($null -eq $Actual) { return $false }
    $pathMatches = try {
        [string]::Equals(
            (Get-V2PanelStartupCanonicalPath -Path $Actual.target),
            (Get-V2PanelStartupCanonicalPath -Path $Desired.target),
            [StringComparison]::OrdinalIgnoreCase
        ) -and [string]::Equals(
            (Get-V2PanelStartupCanonicalPath -Path $Actual.working_directory),
            (Get-V2PanelStartupCanonicalPath -Path $Desired.working_directory),
            [StringComparison]::OrdinalIgnoreCase
        )
    }
    catch { $false }

    return $pathMatches -and
        [string]::Equals($Actual.arguments, $Desired.arguments, [StringComparison]::Ordinal) -and
        $Actual.window_style -eq $Desired.window_style -and
        [string]::Equals($Actual.icon_location, $Desired.icon_location, [StringComparison]::OrdinalIgnoreCase) -and
        [string]::Equals($Actual.description, $Desired.description, [StringComparison]::Ordinal)
}

$approvedRoot = Get-V2PanelStartupCanonicalPath -Path 'C:\IA\local-ai-v2'
$resolvedRoot = Get-V2PanelStartupCanonicalPath -Path $InstallRoot
if (-not [string]::Equals($resolvedRoot, $approvedRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Panel startup installation is restricted to '$approvedRoot'."
}
$rootItem = Get-Item -LiteralPath $resolvedRoot -Force -ErrorAction Stop
if ($rootItem -isnot [IO.DirectoryInfo] -or ($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Protected installation root is invalid: $resolvedRoot"
}

$wscriptPath = Get-V2PanelStartupCanonicalPath -Path 'C:\Windows\System32\wscript.exe'
$launcherPath = Get-V2PanelStartupCanonicalPath -Path (Join-Path $resolvedRoot 'launchers\tray-canary.vbs')
$panelPath = Get-V2PanelStartupCanonicalPath -Path (Join-Path $resolvedRoot 'bin\cia-tray.exe')
[void](Assert-V2PanelStartupLeaf -Path $wscriptPath -Label 'Windows Script Host')
[void](Assert-V2PanelStartupLeaf -Path $launcherPath -Label 'Protected canary tray launcher')
[void](Assert-V2PanelStartupLeaf -Path $panelPath -Label 'Protected tray executable')

$startupDirectory = Get-V2PanelStartupCanonicalPath -Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::Startup))
$expectedStartupDirectory = Get-V2PanelStartupCanonicalPath -Path (Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\Startup')
if (-not [string]::Equals($startupDirectory, $expectedStartupDirectory, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Current-user Startup folder is redirected outside its expected profile location: $startupDirectory"
}
$startupItem = Get-Item -LiteralPath $startupDirectory -Force -ErrorAction Stop
if ($startupItem -isnot [IO.DirectoryInfo] -or ($startupItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Current-user Startup folder is invalid: $startupDirectory"
}

$shortcutPath = Join-Path $startupDirectory 'CIA Local AI v2 Canary Panel.lnk'
$desired = [pscustomobject]@{
    target = $wscriptPath
    arguments = ('"{0}"' -f $launcherPath)
    working_directory = $resolvedRoot
    window_style = 7
    icon_location = "$panelPath,0"
    description = 'CIA Local AI v2 Canary operator panel'
}

if ($SelfTest) {
    if ($Apply -or $Replace) {
        throw 'SelfTest cannot be combined with Apply or Replace.'
    }
    $temporaryRoot = Get-V2PanelStartupCanonicalPath -Path ([IO.Path]::GetTempPath())
    $temporaryRootItem = Get-Item -LiteralPath $temporaryRoot -Force -ErrorAction Stop
    if ($temporaryRootItem -isnot [IO.DirectoryInfo] -or ($temporaryRootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Temporary directory is invalid: $temporaryRoot"
    }
    $nonce = [Guid]::NewGuid().ToString('N')
    $originalPath = Join-Path $temporaryRoot "cia-panel-startup-original-$nonce.lnk"
    $replacementPath = Join-Path $temporaryRoot "cia-panel-startup-replacement-$nonce.lnk"
    $backupPath = Join-Path $temporaryRoot "cia-panel-startup-backup-$nonce.bak"
    $discardPath = Join-Path $temporaryRoot "cia-panel-startup-discard-$nonce.tmp"
    $originalDesired = [pscustomobject]@{
        target = $desired.target
        arguments = $desired.arguments
        working_directory = $desired.working_directory
        window_style = $desired.window_style
        icon_location = $desired.icon_location
        description = 'CIA Local AI v2 Canary startup self-test original'
    }
    try {
        Write-V2PanelShortcutFile -Path $originalPath -Desired $originalDesired
        Write-V2PanelShortcutFile -Path $replacementPath -Desired $desired
        if (-not (Test-V2PanelShortcutMatches -Actual (Get-V2PanelShortcutMetadata -Path $originalPath) -Desired $originalDesired)) {
            throw 'Temporary shortcut round-trip failed.'
        }
        [IO.File]::Replace($replacementPath, $originalPath, $backupPath, $true)
        if (-not (Test-V2PanelShortcutMatches -Actual (Get-V2PanelShortcutMetadata -Path $originalPath) -Desired $desired)) {
            throw 'Temporary atomic replacement failed.'
        }
        [IO.File]::Replace($backupPath, $originalPath, $discardPath, $true)
        if (-not (Test-V2PanelShortcutMatches -Actual (Get-V2PanelShortcutMetadata -Path $originalPath) -Desired $originalDesired)) {
            throw 'Temporary rollback failed.'
        }
        [pscustomobject]@{
            mode = 'self-test'
            passed = $true
            mechanism = 'WScript shortcut round-trip, NTFS atomic replace, rollback'
            temporary_directory = $temporaryRoot
            startup_changed = $false
            scheduled_tasks_changed = $false
            processes_started = $false
        } | ConvertTo-Json -Depth 3
        return
    }
    finally {
        foreach ($artifact in @($originalPath, $replacementPath, $backupPath, $discardPath)) {
            if (Test-Path -LiteralPath $artifact -PathType Leaf) {
                Remove-Item -LiteralPath $artifact -Force -ErrorAction SilentlyContinue
            }
        }
    }
}

$before = Get-V2PanelShortcutMetadata -Path $shortcutPath
$beforeHash = if ($null -ne $before) { (Get-FileHash -LiteralPath $shortcutPath -Algorithm SHA256).Hash } else { $null }
$matches = Test-V2PanelShortcutMatches -Actual $before -Desired $desired
$action = if ($matches) { 'unchanged' } elseif ($null -eq $before) { 'create' } elseif ($Replace) { 'replace' } else { 'blocked-existing' }

$result = [ordered]@{
    mode = if ($Apply) { 'apply' } else { 'preview' }
    environment = 'canary'
    scope = 'current-user-startup'
    shortcut = $shortcutPath
    target = $desired.target
    arguments = $desired.arguments
    launcher = $launcherPath
    working_directory = $desired.working_directory
    icon = $desired.icon_location
    action = $action
    before_sha256 = $beforeHash
    after_sha256 = $beforeHash
    scheduled_tasks_changed = $false
    processes_started = $false
    model_loaded = $false
}

if (-not $Apply) {
    [pscustomobject]$result | ConvertTo-Json -Depth 4
    return
}
if ($action -eq 'blocked-existing') {
    throw "Startup shortcut exists with another definition: $shortcutPath. Re-run with -Replace only after reviewing the preview."
}

if ($action -in @('create', 'replace')) {
    $currentHash = if (Test-Path -LiteralPath $shortcutPath -PathType Leaf) {
        (Get-FileHash -LiteralPath $shortcutPath -Algorithm SHA256).Hash
    }
    else { $null }
    if ($currentHash -ne $beforeHash) {
        throw 'Startup shortcut changed after preflight; no file was replaced.'
    }

    $nonce = [Guid]::NewGuid().ToString('N')
    $temporary = Join-Path $startupDirectory ".cia-panel-startup-$nonce.lnk"
    $backup = Join-Path $startupDirectory ".cia-panel-startup-$nonce.bak"
    $discard = Join-Path $startupDirectory ".cia-panel-startup-$nonce.failed"
    $created = $false
    $replaced = $false
    try {
        Write-V2PanelShortcutFile -Path $temporary -Desired $desired
        $staged = Get-V2PanelShortcutMetadata -Path $temporary
        if (-not (Test-V2PanelShortcutMatches -Actual $staged -Desired $desired)) {
            throw 'Staged startup shortcut failed semantic verification.'
        }
        if ($action -eq 'create') {
            [IO.File]::Move($temporary, $shortcutPath)
            $created = $true
        }
        else {
            [IO.File]::Replace($temporary, $shortcutPath, $backup, $true)
            $replaced = $true
        }
        $installed = Get-V2PanelShortcutMetadata -Path $shortcutPath
        if (-not (Test-V2PanelShortcutMatches -Actual $installed -Desired $desired)) {
            throw 'Installed startup shortcut failed semantic verification.'
        }
    }
    catch {
        $failure = $_
        $rollbackErrors = [System.Collections.Generic.List[string]]::new()
        if ($replaced -and (Test-Path -LiteralPath $backup -PathType Leaf)) {
            try { [IO.File]::Replace($backup, $shortcutPath, $discard, $true) }
            catch { $rollbackErrors.Add($_.Exception.Message) }
        }
        elseif ($created -and (Test-Path -LiteralPath $shortcutPath -PathType Leaf)) {
            try { [IO.File]::Delete($shortcutPath) }
            catch { $rollbackErrors.Add($_.Exception.Message) }
        }
        if ($rollbackErrors.Count -gt 0) {
            throw "Startup shortcut installation failed and rollback was incomplete. Install error: $($failure.Exception.Message). Rollback errors: $($rollbackErrors -join ' | ')"
        }
        throw $failure
    }
    finally {
        foreach ($artifact in @($temporary, $backup, $discard)) {
            if (Test-Path -LiteralPath $artifact -PathType Leaf) {
                Remove-Item -LiteralPath $artifact -Force -ErrorAction SilentlyContinue
            }
        }
    }
}

$verified = Get-V2PanelShortcutMetadata -Path $shortcutPath
if (-not (Test-V2PanelShortcutMatches -Actual $verified -Desired $desired)) {
    throw 'Startup shortcut verification failed after Apply.'
}
$result['after_sha256'] = (Get-FileHash -LiteralPath $shortcutPath -Algorithm SHA256).Hash
[pscustomobject]$result | ConvertTo-Json -Depth 4
