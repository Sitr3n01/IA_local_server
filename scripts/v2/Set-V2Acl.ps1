[CmdletBinding(DefaultParameterSetName = 'Preview')]
param(
    [string]$InstallRoot = 'C:\IA\local-ai-v2',
    [string]$ServingUser = "$env:COMPUTERNAME\Sitr3n",

    [Parameter(ParameterSetName = 'Audit')]
    [switch]$Audit,

    [Parameter(ParameterSetName = 'Apply')]
    [switch]$Apply
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$approvedInstallRoot = 'C:\IA\local-ai-v2'
$writableDirectoryNames = @('logs', 'state')
# Writable roots receive and retain an exact protected ACL. Their runtime/build
# contents are deliberately not normalized one file at a time: new logs and
# state\staging artifacts inherit the root's three-principal DACL and may be
# owned by the serving user. Installed binaries/configuration remain in strict
# immutable subtrees, and installers pin every staging copy by approved SHA-256.
$requiredDirectoryNames = @('bin', 'config', 'logs', 'state')

function Test-IsAdministrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Get-CanonicalPath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    # Windows PowerShell 5.1 runs on .NET Framework, which does not expose
    # Path.IsPathFullyQualified. Require a drive-qualified local path instead;
    # rooted paths such as \temp and all UNC paths are deliberately rejected.
    if (-not [IO.Path]::IsPathRooted($Path) -or $Path -notmatch '^[A-Za-z]:[\\/]') {
        throw "Path must be absolute: $Path"
    }

    $fullPath = [IO.Path]::GetFullPath($Path).TrimEnd([IO.Path]::DirectorySeparatorChar)
    if ([string]::IsNullOrWhiteSpace($fullPath) -or $fullPath -eq [IO.Path]::GetPathRoot($fullPath).TrimEnd([IO.Path]::DirectorySeparatorChar)) {
        throw "Refusing a filesystem root as an ACL target: $Path"
    }
    return $fullPath
}

function Assert-PathUnderRoot {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,

        [Parameter(Mandatory = $true)]
        [string]$Root,

        [switch]$AllowRoot
    )

    $canonicalPath = Get-CanonicalPath -Path $Path
    $canonicalRoot = Get-CanonicalPath -Path $Root
    if ($AllowRoot -and [string]::Equals($canonicalPath, $canonicalRoot, [StringComparison]::OrdinalIgnoreCase)) {
        return $canonicalPath
    }

    $prefix = $canonicalRoot + [IO.Path]::DirectorySeparatorChar
    if (-not $canonicalPath.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "ACL target escapes the approved installation root: $canonicalPath"
    }
    return $canonicalPath
}

function Assert-NotReparsePoint {
    param(
        [Parameter(Mandatory = $true)]
        [System.IO.FileSystemInfo]$Item
    )

    if (($Item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Reparse points are not valid ACL targets: $($Item.FullName)"
    }
}

function Get-SafeTreeItems {
    param(
        [Parameter(Mandatory = $true)]
        [System.IO.DirectoryInfo]$Directory,

        [Parameter(Mandatory = $true)]
        [string]$Root
    )

    $result = [System.Collections.Generic.List[System.IO.FileSystemInfo]]::new()
    $pending = [System.Collections.Generic.Stack[System.IO.DirectoryInfo]]::new()
    $pending.Push($Directory)

    while ($pending.Count -gt 0) {
        $current = $pending.Pop()
        foreach ($child in @(Get-ChildItem -LiteralPath $current.FullName -Force -ErrorAction Stop)) {
            Assert-NotReparsePoint -Item $child
            [void](Assert-PathUnderRoot -Path $child.FullName -Root $Root)
            $result.Add($child)
            if ($child -is [System.IO.DirectoryInfo]) {
                $pending.Push($child)
            }
        }
    }

    return @($result)
}

function Resolve-PrincipalSid {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Account
    )

    try {
        return [Security.Principal.NTAccount]::new($Account).Translate([Security.Principal.SecurityIdentifier])
    }
    catch {
        throw "Unable to resolve serving account '$Account' to a SID. $($_.Exception.Message)"
    }
}

function New-Target {
    param(
        [Parameter(Mandatory = $true)]
        [System.IO.FileSystemInfo]$Item,

        [Parameter(Mandatory = $true)]
        [ValidateSet('immutable', 'runtime-writable')]
        [string]$Class
    )

    return [pscustomobject]@{
        path = $Item.FullName
        class = $Class
        is_directory = $Item -is [System.IO.DirectoryInfo]
        depth = $Item.FullName.Split([IO.Path]::DirectorySeparatorChar).Count
    }
}

function Get-DesiredRules {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Target,

        [Parameter(Mandatory = $true)]
        [Security.Principal.SecurityIdentifier]$UserSid,

        [Parameter(Mandatory = $true)]
        [Security.Principal.SecurityIdentifier]$AdministratorsSid,

        [Parameter(Mandatory = $true)]
        [Security.Principal.SecurityIdentifier]$SystemSid
    )

    $inheritance = if ($Target.is_directory) {
        [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
    }
    else {
        [Security.AccessControl.InheritanceFlags]::None
    }

    $userRights = if ($Target.class -eq 'runtime-writable') {
        [Security.AccessControl.FileSystemRights]::Modify
    }
    else {
        [Security.AccessControl.FileSystemRights]::ReadAndExecute
    }

    return @(
        [Security.AccessControl.FileSystemAccessRule]::new(
            $UserSid,
            $userRights,
            $inheritance,
            [Security.AccessControl.PropagationFlags]::None,
            [Security.AccessControl.AccessControlType]::Allow
        ),
        [Security.AccessControl.FileSystemAccessRule]::new(
            $AdministratorsSid,
            [Security.AccessControl.FileSystemRights]::FullControl,
            $inheritance,
            [Security.AccessControl.PropagationFlags]::None,
            [Security.AccessControl.AccessControlType]::Allow
        ),
        [Security.AccessControl.FileSystemAccessRule]::new(
            $SystemSid,
            [Security.AccessControl.FileSystemRights]::FullControl,
            $inheritance,
            [Security.AccessControl.PropagationFlags]::None,
            [Security.AccessControl.AccessControlType]::Allow
        )
    )
}

function Get-RuleKey {
    param(
        [Parameter(Mandatory = $true)]
        [Security.AccessControl.FileSystemAccessRule]$Rule
    )

    $sid = $Rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
    return '{0}|{1}|{2}|{3}|{4}|{5}' -f @(
        $sid,
        [int]$Rule.FileSystemRights,
        [int]$Rule.InheritanceFlags,
        [int]$Rule.PropagationFlags,
        [int]$Rule.AccessControlType,
        [bool]$Rule.IsInherited
    )
}

function Test-TargetAcl {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Target,

        [Parameter(Mandatory = $true)]
        [Security.Principal.SecurityIdentifier]$UserSid,

        [Parameter(Mandatory = $true)]
        [Security.Principal.SecurityIdentifier]$AdministratorsSid,

        [Parameter(Mandatory = $true)]
        [Security.Principal.SecurityIdentifier]$SystemSid
    )

    $acl = Get-Acl -LiteralPath $Target.path -ErrorAction Stop
    $issues = [System.Collections.Generic.List[string]]::new()
    $ownerSid = try {
        [Security.Principal.NTAccount]::new($acl.Owner).Translate([Security.Principal.SecurityIdentifier]).Value
    }
    catch {
        $acl.Owner
    }
    if ($ownerSid -ne $AdministratorsSid.Value) {
        $issues.Add('owner_not_administrators')
    }
    if (-not $acl.AreAccessRulesProtected) {
        $issues.Add('inheritance_enabled')
    }

    $expectedKeys = @(Get-DesiredRules `
            -Target $Target `
            -UserSid $UserSid `
            -AdministratorsSid $AdministratorsSid `
            -SystemSid $SystemSid | ForEach-Object { Get-RuleKey -Rule $_ } | Sort-Object)
    $actualKeys = @($acl.Access | ForEach-Object { Get-RuleKey -Rule $_ } | Sort-Object)
    if (($expectedKeys -join "`n") -ne ($actualKeys -join "`n")) {
        $issues.Add('dacl_differs')
    }

    return [pscustomobject]@{
        path = $Target.path
        class = $Target.class
        compliant = $issues.Count -eq 0
        issues = @($issues)
    }
}

function Set-TargetAcl {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Target,

        [Parameter(Mandatory = $true)]
        [Security.Principal.SecurityIdentifier]$UserSid,

        [Parameter(Mandatory = $true)]
        [Security.Principal.SecurityIdentifier]$AdministratorsSid,

        [Parameter(Mandatory = $true)]
        [Security.Principal.SecurityIdentifier]$SystemSid
    )

    $acl = Get-Acl -LiteralPath $Target.path -ErrorAction Stop
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($existingRule in @($acl.Access)) {
        [void]$acl.RemoveAccessRuleSpecific($existingRule)
    }
    $acl.SetOwner($AdministratorsSid)
    foreach ($desiredRule in @(Get-DesiredRules `
            -Target $Target `
            -UserSid $UserSid `
            -AdministratorsSid $AdministratorsSid `
            -SystemSid $SystemSid)) {
        [void]$acl.AddAccessRule($desiredRule)
    }
    Set-Acl -LiteralPath $Target.path -AclObject $acl -ErrorAction Stop
}

$canonicalApprovedRoot = Get-CanonicalPath -Path $approvedInstallRoot
$canonicalRequestedRoot = Get-CanonicalPath -Path $InstallRoot
if (-not [string]::Equals($canonicalRequestedRoot, $canonicalApprovedRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw "InstallRoot must be the approved v2 root '$canonicalApprovedRoot'; received '$canonicalRequestedRoot'."
}

$resolvedRoot = (Resolve-Path -LiteralPath $canonicalRequestedRoot -ErrorAction Stop).Path
$resolvedRoot = Assert-PathUnderRoot -Path $resolvedRoot -Root $canonicalApprovedRoot -AllowRoot
$rootItem = Get-Item -LiteralPath $resolvedRoot -Force -ErrorAction Stop
if ($rootItem -isnot [System.IO.DirectoryInfo]) {
    throw "InstallRoot is not a directory: $resolvedRoot"
}
Assert-NotReparsePoint -Item $rootItem

foreach ($requiredName in $requiredDirectoryNames) {
    $requiredPath = Join-Path $resolvedRoot $requiredName
    [void](Assert-PathUnderRoot -Path $requiredPath -Root $resolvedRoot)
    if (-not (Test-Path -LiteralPath $requiredPath -PathType Container)) {
        throw "Required v2 directory is missing: $requiredPath"
    }
}

$servingUserSid = Resolve-PrincipalSid -Account $ServingUser
$expectedServingUser = "$env:COMPUTERNAME\Sitr3n"
$expectedServingUserSid = Resolve-PrincipalSid -Account $expectedServingUser
if ($servingUserSid.Value -ne $expectedServingUserSid.Value) {
    throw "ServingUser must resolve to the dedicated local account '$expectedServingUser'."
}
$administratorsSid = [Security.Principal.SecurityIdentifier]::new('S-1-5-32-544')
$systemSid = [Security.Principal.SecurityIdentifier]::new('S-1-5-18')
if ($servingUserSid.Value -in @($administratorsSid.Value, $systemSid.Value)) {
    throw "ServingUser must be a dedicated non-system user: $ServingUser"
}

$targets = [System.Collections.Generic.List[object]]::new()
$targets.Add((New-Target -Item $rootItem -Class 'immutable'))
foreach ($topLevelItem in @(Get-ChildItem -LiteralPath $resolvedRoot -Force -ErrorAction Stop)) {
    Assert-NotReparsePoint -Item $topLevelItem
    [void](Assert-PathUnderRoot -Path $topLevelItem.FullName -Root $resolvedRoot)
    $class = if ($topLevelItem -is [System.IO.DirectoryInfo] -and $topLevelItem.Name -in $writableDirectoryNames) {
        'runtime-writable'
    }
    else {
        'immutable'
    }
    $targets.Add((New-Target -Item $topLevelItem -Class $class))
    if ($topLevelItem -is [System.IO.DirectoryInfo]) {
        $descendants = @(Get-SafeTreeItems -Directory $topLevelItem -Root $resolvedRoot)
        if ($class -eq 'immutable') {
            foreach ($descendant in $descendants) {
                $targets.Add((New-Target -Item $descendant -Class $class))
            }
        }
    }
}
$targets = @($targets | Sort-Object path -Unique)

$before = @($targets | ForEach-Object {
        Test-TargetAcl `
            -Target $_ `
            -UserSid $servingUserSid `
            -AdministratorsSid $administratorsSid `
            -SystemSid $systemSid
    })
$beforeDrift = @($before | Where-Object { -not $_.compliant })
$mode = if ($Apply) { 'apply' } elseif ($Audit) { 'audit' } else { 'preview' }

[pscustomobject]@{
    mode = $mode
    install_root = $resolvedRoot
    serving_user = $ServingUser
    serving_user_sid = $servingUserSid.Value
    owner_sid = $administratorsSid.Value
    immutable_user_rights = 'ReadAndExecute'
    runtime_writable_user_rights = 'Modify'
    writable_directories = @($writableDirectoryNames | ForEach-Object { Join-Path $resolvedRoot $_ })
    target_count = $targets.Count
    immutable_target_count = @($targets | Where-Object { $_.class -eq 'immutable' }).Count
    runtime_writable_target_count = @($targets | Where-Object { $_.class -eq 'runtime-writable' }).Count
    targets = @($targets | Select-Object path, class, is_directory)
    non_compliant_count = $beforeDrift.Count
    non_compliant = $beforeDrift
} | ConvertTo-Json -Depth 6

if ($Audit) {
    if ($beforeDrift.Count -gt 0) {
        throw "ACL audit failed for $($beforeDrift.Count) of $($targets.Count) exact v2 target(s)."
    }
    Write-Host "ACL audit passed for all $($targets.Count) exact v2 target(s)."
    return
}
if (-not $Apply) {
    Write-Host 'Preview only. No ACL or ownership changes were made. Re-run from an elevated PowerShell with -Apply.'
    return
}
if (-not (Test-IsAdministrator)) {
    throw 'ACL and ownership changes require an elevated PowerShell session.'
}

$backupDirectory = Join-Path $resolvedRoot 'state\acl-backups'
[void](Assert-PathUnderRoot -Path $backupDirectory -Root $resolvedRoot)
if (-not (Test-Path -LiteralPath $backupDirectory -PathType Container)) {
    New-Item -ItemType Directory -Path $backupDirectory -Force -ErrorAction Stop | Out-Null
}
$backupDirectoryItem = Get-Item -LiteralPath $backupDirectory -Force -ErrorAction Stop
Assert-NotReparsePoint -Item $backupDirectoryItem
$backupTimestamp = Get-Date -Format 'yyyyMMdd-HHmmss-fffffff'
$backupPath = Join-Path $backupDirectory "acl-before-$backupTimestamp.json"
[void](Assert-PathUnderRoot -Path $backupPath -Root $resolvedRoot)

$backupRecords = @($targets | ForEach-Object {
        $acl = Get-Acl -LiteralPath $_.path -ErrorAction Stop
        [pscustomobject]@{
            path = $_.path
            owner = $acl.Owner
            sddl = $acl.GetSecurityDescriptorSddlForm([Security.AccessControl.AccessControlSections]::Access -bor [Security.AccessControl.AccessControlSections]::Owner)
        }
    })
[pscustomobject]@{
    schema_version = 1
    created_at = (Get-Date).ToUniversalTime().ToString('o')
    install_root = $resolvedRoot
    records = $backupRecords
} | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $backupPath -Encoding UTF8 -NoNewline -ErrorAction Stop

# Re-enumerate after writing the recovery record so the new state items receive
# the runtime-writable policy too. Deepest paths are changed first and the root
# last, which avoids relying on a partially changed parent inheritance state.
$targets = [System.Collections.Generic.List[object]]::new()
$targets.Add((New-Target -Item $rootItem -Class 'immutable'))
foreach ($topLevelItem in @(Get-ChildItem -LiteralPath $resolvedRoot -Force -ErrorAction Stop)) {
    Assert-NotReparsePoint -Item $topLevelItem
    [void](Assert-PathUnderRoot -Path $topLevelItem.FullName -Root $resolvedRoot)
    $class = if ($topLevelItem -is [System.IO.DirectoryInfo] -and $topLevelItem.Name -in $writableDirectoryNames) {
        'runtime-writable'
    }
    else {
        'immutable'
    }
    $targets.Add((New-Target -Item $topLevelItem -Class $class))
    if ($topLevelItem -is [System.IO.DirectoryInfo]) {
        $descendants = @(Get-SafeTreeItems -Directory $topLevelItem -Root $resolvedRoot)
        if ($class -eq 'immutable') {
            foreach ($descendant in $descendants) {
                $targets.Add((New-Target -Item $descendant -Class $class))
            }
        }
    }
}
$targets = @($targets | Sort-Object @{ Expression = 'depth'; Descending = $true }, @{ Expression = 'path'; Descending = $true } -Unique)

try {
    foreach ($target in $targets) {
        Set-TargetAcl `
            -Target $target `
            -UserSid $servingUserSid `
            -AdministratorsSid $administratorsSid `
            -SystemSid $systemSid
    }
}
catch {
    throw "ACL apply failed. The installation may be partially hardened. Recovery metadata: '$backupPath'. $($_.Exception.Message)"
}

$after = @($targets | ForEach-Object {
        Test-TargetAcl `
            -Target $_ `
            -UserSid $servingUserSid `
            -AdministratorsSid $administratorsSid `
            -SystemSid $systemSid
    })
$afterDrift = @($after | Where-Object { -not $_.compliant })
if ($afterDrift.Count -gt 0) {
    throw "ACL verification failed for $($afterDrift.Count) target(s). Recovery metadata: '$backupPath'."
}

Write-Host "Applied and verified ACL hardening for $($targets.Count) exact v2 target(s). Recovery metadata: $backupPath"
