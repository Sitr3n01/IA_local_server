Set-StrictMode -Version Latest

<#
Release-level transaction and rollback.

Individual installers are already atomic per file. A deployment is not: it
crosses configuration, several binaries, scheduled tasks, ACLs, and a firewall
policy, and a failure between those steps leaves a hybrid installation that no
single-file rollback can describe.

A release transaction records, before anything is mutated, the bytes and task
definitions the deployment is about to replace. If the cutover fails, the same
record drives a restore back to the previous release. If the restore also fails,
that is reported as DEGRADED rather than hidden, and the release manifest is
left absent so every consumer stays fail-closed.

Nothing here rolls back a candidate model, a benchmark, or a user configuration.
The transaction owns exactly what the deployment replaced.
#>

$script:V2ReleaseSchemaVersion = 1
$script:V2ReleaseStatuses = @('installed', 'rolled-back', 'degraded')

function Get-V2ReleaseRoot {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot
    )

    $root = [IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))
    return (Join-Path (Join-Path $root 'state') 'releases')
}

function Get-V2ReleaseManifestPath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot,

        [Parameter(Mandatory = $true)]
        [ValidateSet('Canary', 'Final')]
        [string]$Environment
    )

    $root = [IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))
    return (Join-Path (Join-Path $root 'config') ("release.{0}.json" -f $Environment.ToLowerInvariant()))
}

# Release identifiers are sortable, unique, and safe as both a directory name
# and a Prometheus label. cia-edge validates the same character set.
function New-V2ReleaseId {
    param(
        [Parameter(Mandatory = $true)]
        [ValidateSet('Canary', 'Final')]
        [string]$Environment,

        [datetime]$Timestamp = [DateTime]::UtcNow
    )

    return '{0}-{1}-{2}' -f $Environment.ToLowerInvariant(),
        $Timestamp.ToUniversalTime().ToString('yyyyMMddTHHmmssZ'),
        ([Guid]::NewGuid().ToString('N').Substring(0, 8))
}

# Source provenance for the release record. A missing or unusable git is not an
# error: the release is still identified by its own id and by component hashes.
function Get-V2SourceProvenance {
    param(
        [Parameter(Mandatory = $true)]
        [string]$RepositoryRoot
    )

    $commit = ''
    $dirty = $false
    try {
        $revision = (& git -C $RepositoryRoot rev-parse HEAD 2>$null | Out-String).Trim()
        if ($LASTEXITCODE -eq 0 -and $revision -match '^[0-9a-f]{40}$') {
            $commit = $revision
            $status = (& git -C $RepositoryRoot status --porcelain 2>$null | Out-String).Trim()
            $dirty = ($LASTEXITCODE -ne 0) -or (-not [string]::IsNullOrWhiteSpace($status))
        }
    }
    catch {
        $commit = ''
        $dirty = $false
    }
    return [pscustomobject]@{ commit = $commit; source_dirty = $dirty }
}

function Get-V2InstalledRelease {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot,

        [Parameter(Mandatory = $true)]
        [ValidateSet('Canary', 'Final')]
        [string]$Environment
    )

    $path = Get-V2ReleaseManifestPath -InstallRoot $InstallRoot -Environment $Environment
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        return $null
    }
    try {
        $release = Get-Content -LiteralPath $path -Raw -Encoding UTF8 | ConvertFrom-Json
    }
    catch {
        throw "Installed release manifest is not valid JSON: $path. $($_.Exception.Message)"
    }
    if ($release.schema_version -ne $script:V2ReleaseSchemaVersion) {
        throw "Installed release manifest has unsupported schema_version '$($release.schema_version)'."
    }
    if ($release.environment -ne $Environment.ToLowerInvariant()) {
        throw "Installed release manifest environment '$($release.environment)' does not match '$($Environment.ToLowerInvariant())'."
    }
    return $release
}

# Opens a transaction. Directories are created eagerly so a later backup cannot
# fail for a missing path in the middle of a cutover.
function New-V2ReleaseTransaction {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot,

        [Parameter(Mandatory = $true)]
        [ValidateSet('Canary', 'Final')]
        [string]$Environment,

        [string]$Version = '',

        [string]$ReleaseId = '',

        [string]$RepositoryRoot = ''
    )

    $resolvedRoot = [IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))
    if ([string]::IsNullOrWhiteSpace($ReleaseId)) {
        $ReleaseId = New-V2ReleaseId -Environment $Environment
    }
    if ($ReleaseId -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$') {
        throw "Release identifier '$ReleaseId' is not a safe identifier."
    }
    if (-not [string]::IsNullOrWhiteSpace($Version) -and $Version -notmatch '^[A-Za-z0-9._-]{1,128}$') {
        throw "Release version '$Version' is not a safe identifier."
    }

    $releaseRoot = Get-V2ReleaseRoot -InstallRoot $resolvedRoot
    $directory = Join-Path $releaseRoot $ReleaseId
    $backupRoot = Join-Path $directory 'backup'
    foreach ($required in @($releaseRoot, $directory, $backupRoot, (Join-Path $backupRoot 'files'))) {
        if (-not (Test-Path -LiteralPath $required -PathType Container)) {
            New-Item -ItemType Directory -Path $required -Force -ErrorAction Stop | Out-Null
        }
    }

    $previous = $null
    try {
        $installed = Get-V2InstalledRelease -InstallRoot $resolvedRoot -Environment $Environment
        if ($null -ne $installed) { $previous = [string]$installed.release_id }
    }
    catch {
        # A previous manifest that no longer parses must not block a repair
        # deployment. It is recorded as unknown rather than silently trusted.
        $previous = ''
    }

    $provenance = [pscustomobject]@{ commit = ''; source_dirty = $false }
    if (-not [string]::IsNullOrWhiteSpace($RepositoryRoot)) {
        $provenance = Get-V2SourceProvenance -RepositoryRoot $RepositoryRoot
    }

    return [pscustomobject]@{
        schema_version = $script:V2ReleaseSchemaVersion
        release_id = $ReleaseId
        environment = $Environment.ToLowerInvariant()
        version = $Version
        commit = $provenance.commit
        source_dirty = $provenance.source_dirty
        previous_release_id = $previous
        install_root = $resolvedRoot
        directory = $directory
        backup_root = $backupRoot
        created_utc = [DateTime]::UtcNow.ToString('o')
        stage = 'opened'
        files = [System.Collections.Generic.List[object]]::new()
        tasks = [System.Collections.Generic.List[object]]::new()
        components = [System.Collections.Generic.List[object]]::new()
        artifacts = [System.Collections.Generic.List[object]]::new()
        events = [System.Collections.Generic.List[object]]::new()
    }
}

function Add-V2ReleaseEvent {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Transaction,

        [Parameter(Mandatory = $true)]
        [string]$Stage,

        [Parameter(Mandatory = $true)]
        [string]$Detail
    )

    $Transaction.stage = $Stage
    $Transaction.events.Add([ordered]@{
            time = [DateTime]::UtcNow.ToString('o')
            stage = $Stage
            detail = $Detail
        })
    Save-V2ReleaseTransaction -Transaction $Transaction
}

# Records the current bytes of a file the deployment is about to replace. A file
# that does not exist yet is recorded as absent, so rollback removes it instead
# of leaving a binary the previous release never had.
function Backup-V2ReleaseFile {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Transaction,

        [Parameter(Mandatory = $true)]
        [string]$Path,

        [string]$Label = ''
    )

    $resolved = [IO.Path]::GetFullPath($Path)
    if (@($Transaction.files | Where-Object { [string]::Equals([string]$_.path, $resolved, [StringComparison]::OrdinalIgnoreCase) }).Count -gt 0) {
        return
    }

    $entry = [ordered]@{
        path = $resolved
        label = $Label
        existed = $false
        sha256 = ''
        bytes = 0
        backup = ''
    }
    if (Test-Path -LiteralPath $resolved -PathType Leaf) {
        $item = Get-Item -LiteralPath $resolved -Force -ErrorAction Stop
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Refusing to back up a reparse point: $resolved"
        }
        $backupDirectory = Join-Path $Transaction.backup_root 'files'
        $backupName = '{0:d3}-{1}' -f $Transaction.files.Count, ([IO.Path]::GetFileName($resolved))
        $backupPath = Join-Path $backupDirectory $backupName
        [IO.File]::Copy($resolved, $backupPath, $true)
        $entry.existed = $true
        $entry.sha256 = (Get-FileHash -LiteralPath $backupPath -Algorithm SHA256).Hash.ToUpperInvariant()
        $entry.bytes = [long]$item.Length
        $entry.backup = $backupPath
        $sourceHash = (Get-FileHash -LiteralPath $resolved -Algorithm SHA256).Hash.ToUpperInvariant()
        if (-not [string]::Equals($sourceHash, [string]$entry.sha256, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Backup copy of '$resolved' does not match the file it was taken from."
        }
    }
    $Transaction.files.Add($entry)
}

# Records a scheduled task definition. The exporter is injectable so the
# transaction can be exercised without registering real tasks.
function Backup-V2ReleaseTask {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Transaction,

        [Parameter(Mandatory = $true)]
        [string]$TaskName,

        [scriptblock]$TaskExporter = $null
    )

    if ($null -eq $TaskExporter) {
        $TaskExporter = {
            param($name)
            if ($null -eq (Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue)) {
                return $null
            }
            return (Export-ScheduledTask -TaskName $name | Out-String)
        }
    }

    $definition = & $TaskExporter $TaskName
    $entry = [ordered]@{
        name = $TaskName
        existed = $false
        definition_sha256 = ''
        definition = ''
    }
    if ($null -ne $definition -and -not [string]::IsNullOrWhiteSpace([string]$definition)) {
        $text = [string]$definition
        $entry.existed = $true
        $entry.definition = $text
        $stream = [IO.MemoryStream]::new([Text.Encoding]::UTF8.GetBytes($text))
        try {
            $entry.definition_sha256 = (Get-FileHash -InputStream $stream -Algorithm SHA256).Hash.ToUpperInvariant()
        }
        finally {
            $stream.Dispose()
        }
    }
    $Transaction.tasks.Add($entry)
}

function Add-V2ReleaseComponent {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Transaction,

        [Parameter(Mandatory = $true)]
        [string]$Name,

        [Parameter(Mandatory = $true)]
        [string]$Path,

        [string]$Sha256 = ''
    )

    $resolved = [IO.Path]::GetFullPath($Path)
    $hash = $Sha256
    if ([string]::IsNullOrWhiteSpace($hash) -and (Test-Path -LiteralPath $resolved -PathType Leaf)) {
        $hash = (Get-FileHash -LiteralPath $resolved -Algorithm SHA256).Hash
    }
    $Transaction.components.Add([ordered]@{
            name = $Name
            path = $resolved
            sha256 = $hash.ToUpperInvariant()
        })
}

function Add-V2ReleaseArtifact {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Transaction,

        [Parameter(Mandatory = $true)]
        [ValidateSet('model', 'runtime')]
        [string]$Kind,

        [Parameter(Mandatory = $true)]
        [string]$Id,

        [Parameter(Mandatory = $true)]
        [string]$Path,

        [Parameter(Mandatory = $true)]
        [string]$Sha256,

        [Parameter(Mandatory = $true)]
        [long]$Bytes
    )

    $Transaction.artifacts.Add([ordered]@{
            kind = $Kind
            id = $Id
            path = [IO.Path]::GetFullPath($Path)
            sha256 = $Sha256.ToUpperInvariant()
            bytes = $Bytes
        })
}

function ConvertTo-V2ReleaseRecord {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Transaction,

        [Parameter(Mandatory = $true)]
        [string]$Status
    )

    if ($Status -notin $script:V2ReleaseStatuses) {
        throw "Release status '$Status' is not one of: $($script:V2ReleaseStatuses -join ', ')."
    }
    return [ordered]@{
        schema_version = $script:V2ReleaseSchemaVersion
        environment = $Transaction.environment
        release_id = $Transaction.release_id
        version = $Transaction.version
        commit = $Transaction.commit
        source_dirty = [bool]$Transaction.source_dirty
        previous_release_id = $Transaction.previous_release_id
        status = $Status
        created_utc = $Transaction.created_utc
        completed_utc = [DateTime]::UtcNow.ToString('o')
        install_root = $Transaction.install_root
        stage = $Transaction.stage
        components = @($Transaction.components)
        artifacts = @($Transaction.artifacts)
        replaced_files = @(@($Transaction.files) | ForEach-Object {
                [ordered]@{ path = $_.path; label = $_.label; previous_sha256 = $_.sha256; previously_existed = $_.existed }
            })
        tasks = @(@($Transaction.tasks) | ForEach-Object {
                [ordered]@{ name = $_.name; previously_existed = $_.existed; previous_definition_sha256 = $_.definition_sha256 }
            })
        events = @($Transaction.events)
    }
}

# The journal lives beside the backups and is rewritten on every stage change,
# so an interrupted deployment leaves a readable account of how far it got.
function Save-V2ReleaseTransaction {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Transaction
    )

    $path = Join-Path $Transaction.directory 'transaction.json'
    $payload = [ordered]@{
        schema_version = $script:V2ReleaseSchemaVersion
        release_id = $Transaction.release_id
        environment = $Transaction.environment
        version = $Transaction.version
        commit = $Transaction.commit
        previous_release_id = $Transaction.previous_release_id
        install_root = $Transaction.install_root
        created_utc = $Transaction.created_utc
        stage = $Transaction.stage
        files = @($Transaction.files)
        # Task XML is retained in the backup directory only, never in the
        # journal, so the journal stays small and quotable in a report.
        tasks = @(@($Transaction.tasks) | ForEach-Object {
                [ordered]@{ name = $_.name; existed = $_.existed; definition_sha256 = $_.definition_sha256 }
            })
        components = @($Transaction.components)
        artifacts = @($Transaction.artifacts)
        events = @($Transaction.events)
    }
    Write-V2JsonAtomic -Path $path -Value $payload
}

# Publishes the release manifest. The environment copy under config is what
# cia-edge reads, and it is written last so a half-finished deployment never
# advertises itself as installed.
function Complete-V2ReleaseTransaction {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Transaction,

        [Parameter(Mandatory = $true)]
        [ValidateSet('installed', 'rolled-back', 'degraded')]
        [string]$Status
    )

    $record = ConvertTo-V2ReleaseRecord -Transaction $Transaction -Status $Status
    Write-V2JsonAtomic -Path (Join-Path $Transaction.directory 'release.json') -Value $record
    Save-V2ReleaseTransaction -Transaction $Transaction

    $manifestPath = Get-V2ReleaseManifestPath -InstallRoot $Transaction.install_root -Environment $Transaction.environment
    if ($Status -eq 'installed') {
        Write-V2JsonAtomic -Path $manifestPath -Value $record
    }
    elseif (Test-Path -LiteralPath $manifestPath -PathType Leaf) {
        # A rolled-back or degraded deployment must not leave a manifest naming a
        # release that is not the one serving. Only this release's own manifest is
        # removed: a successful restore has already put the previous one back, and
        # deleting that would erase the identity of what is actually installed.
        $naming = $false
        try {
            $installed = Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
            $naming = [string]::Equals([string]$installed.release_id, [string]$Transaction.release_id, [StringComparison]::OrdinalIgnoreCase)
        }
        catch {
            # An unreadable manifest cannot be trusted to describe the previous
            # release either, so it is removed and the edge reports no identity.
            $naming = $true
        }
        if ($naming) {
            Remove-Item -LiteralPath $manifestPath -Force -ErrorAction SilentlyContinue
        }
    }
    Update-V2ReleaseHistory -InstallRoot $Transaction.install_root -Record $record
    return $record
}

function Update-V2ReleaseHistory {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot,

        [Parameter(Mandatory = $true)]
        [object]$Record
    )

    $path = Join-Path (Get-V2ReleaseRoot -InstallRoot $InstallRoot) 'history.json'
    $entries = @()
    if (Test-Path -LiteralPath $path -PathType Leaf) {
        try {
            $existing = Get-Content -LiteralPath $path -Raw -Encoding UTF8 | ConvertFrom-Json
            $entries = @(@($existing.releases) | Where-Object { $null -ne $_ -and $_.release_id -ne $Record.release_id })
        }
        catch {
            $entries = @()
        }
    }
    $entries += [ordered]@{
        release_id = $Record.release_id
        environment = $Record.environment
        version = $Record.version
        commit = $Record.commit
        previous_release_id = $Record.previous_release_id
        status = $Record.status
        completed_utc = $Record.completed_utc
    }
    Write-V2JsonAtomic -Path $path -Value ([ordered]@{
            schema_version = $script:V2ReleaseSchemaVersion
            updated_utc = [DateTime]::UtcNow.ToString('o')
            releases = @($entries)
        })
}

function Get-V2ReleaseHistory {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot
    )

    $path = Join-Path (Get-V2ReleaseRoot -InstallRoot $InstallRoot) 'history.json'
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        return @()
    }
    try {
        $history = Get-Content -LiteralPath $path -Raw -Encoding UTF8 | ConvertFrom-Json
    }
    catch {
        throw "Release history is not valid JSON: $path. $($_.Exception.Message)"
    }
    return @($history.releases)
}

# Restores every file and task the transaction recorded. It never stops on the
# first failure: a partial restore that reports exactly what could not be put
# back is more useful than one that abandons the rest of the installation.
function Restore-V2Release {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Transaction,

        [scriptblock]$TaskRestorer = $null,

        [scriptblock]$TaskRemover = $null
    )

    if ($null -eq $TaskRestorer) {
        $TaskRestorer = {
            param($name, $definition)
            Register-ScheduledTask -TaskName $name -Xml $definition -Force -ErrorAction Stop | Out-Null
        }
    }
    if ($null -eq $TaskRemover) {
        $TaskRemover = {
            param($name)
            if ($null -ne (Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue)) {
                Unregister-ScheduledTask -TaskName $name -Confirm:$false -ErrorAction Stop
            }
        }
    }

    # PowerShell resolves variables in a caller-supplied scriptblock against
    # this function's scope, so these locals carry names no caller would choose.
    $v2RestoreFailures = [System.Collections.Generic.List[string]]::new()
    $v2RestoredItems = [System.Collections.Generic.List[string]]::new()

    # Files are restored in reverse order so a dependency replaced late is put
    # back before the thing that referenced it.
    $files = @($Transaction.files)
    for ($index = $files.Count - 1; $index -ge 0; $index--) {
        $entry = $files[$index]
        try {
            if ($entry.existed) {
                # A pre-cutover failure often leaves every recorded file
                # unchanged. Do not rewrite an already-identical executable:
                # it may be held by the still-running service, and no byte
                # restoration is needed.
                if (Test-Path -LiteralPath $entry.path -PathType Leaf) {
                    $currentHash = (Get-FileHash -LiteralPath $entry.path -Algorithm SHA256).Hash.ToUpperInvariant()
                    if ([string]::Equals($currentHash, [string]$entry.sha256, [StringComparison]::OrdinalIgnoreCase)) {
                        $v2RestoredItems.Add("unchanged $($entry.path)")
                        continue
                    }
                }
                if (-not (Test-Path -LiteralPath $entry.backup -PathType Leaf)) {
                    throw "backup copy is missing: $($entry.backup)"
                }
                $directory = Split-Path -Parent $entry.path
                if (-not (Test-Path -LiteralPath $directory -PathType Container)) {
                    New-Item -ItemType Directory -Path $directory -Force -ErrorAction Stop | Out-Null
                }
                Copy-Item -LiteralPath $entry.backup -Destination $entry.path -Force -ErrorAction Stop
                $actual = (Get-FileHash -LiteralPath $entry.path -Algorithm SHA256).Hash.ToUpperInvariant()
                if (-not [string]::Equals($actual, [string]$entry.sha256, [StringComparison]::OrdinalIgnoreCase)) {
                    throw "restored bytes do not match the recorded SHA-256"
                }
                $v2RestoredItems.Add([string]$entry.path)
            }
            elseif (Test-Path -LiteralPath $entry.path -PathType Leaf) {
                Remove-Item -LiteralPath $entry.path -Force -ErrorAction Stop
                $v2RestoredItems.Add("removed $($entry.path)")
            }
        }
        catch {
            $v2RestoreFailures.Add("$($entry.path): $($_.Exception.Message)")
        }
    }

    foreach ($task in @($Transaction.tasks)) {
        try {
            if ($task.existed) {
                & $TaskRestorer $task.name $task.definition
                $v2RestoredItems.Add("task $($task.name)")
            }
            else {
                & $TaskRemover $task.name
                $v2RestoredItems.Add("removed task $($task.name)")
            }
        }
        catch {
            $v2RestoreFailures.Add("task $($task.name): $($_.Exception.Message)")
        }
    }

    $Transaction.stage = 'rolled-back'
    if ($v2RestoreFailures.Count -gt 0) {
        $Transaction.stage = 'rollback-failed'
    }
    $Transaction.events.Add([ordered]@{
            time = [DateTime]::UtcNow.ToString('o')
            stage = $Transaction.stage
            detail = "restored $($v2RestoredItems.Count) item(s); $($v2RestoreFailures.Count) failure(s)"
        })
    Save-V2ReleaseTransaction -Transaction $Transaction

    return [pscustomobject]@{
        succeeded = ($v2RestoreFailures.Count -eq 0)
        restored = @($v2RestoredItems)
        failures = @($v2RestoreFailures)
    }
}

# Rehydrates a stored transaction so a later, separate rollback run can use the
# same restore path as an in-flight failure.
function Import-V2ReleaseTransaction {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot,

        [Parameter(Mandatory = $true)]
        [string]$ReleaseId
    )

    if ($ReleaseId -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$') {
        throw "Release identifier '$ReleaseId' is not a safe identifier."
    }
    $directory = Join-Path (Get-V2ReleaseRoot -InstallRoot $InstallRoot) $ReleaseId
    $journalPath = Join-Path $directory 'transaction.json'
    if (-not (Test-Path -LiteralPath $journalPath -PathType Leaf)) {
        throw "Release transaction journal is missing: $journalPath"
    }
    try {
        $journal = Get-Content -LiteralPath $journalPath -Raw -Encoding UTF8 | ConvertFrom-Json
    }
    catch {
        throw "Release transaction journal is not valid JSON: $journalPath. $($_.Exception.Message)"
    }
    if ($journal.schema_version -ne $script:V2ReleaseSchemaVersion) {
        throw "Release transaction journal has unsupported schema_version '$($journal.schema_version)'."
    }

    $files = [System.Collections.Generic.List[object]]::new()
    foreach ($entry in @($journal.files)) { $files.Add($entry) }
    $tasks = [System.Collections.Generic.List[object]]::new()
    foreach ($entry in @($journal.tasks)) {
        # The journal stores only the definition hash; the XML itself is read
        # back from the backup directory so a hand-edited journal cannot inject
        # a task definition.
        $definitionPath = Join-Path (Join-Path $directory 'backup') ("tasks\{0}.xml" -f ($entry.name -replace '[\\/:*?"<>|]', '_'))
        $definition = ''
        if (Test-Path -LiteralPath $definitionPath -PathType Leaf) {
            $definition = Get-Content -LiteralPath $definitionPath -Raw -Encoding UTF8
        }
        $tasks.Add([pscustomobject]@{
                name = $entry.name
                existed = [bool]$entry.existed
                definition_sha256 = [string]$entry.definition_sha256
                definition = $definition
            })
    }
    $components = [System.Collections.Generic.List[object]]::new()
    foreach ($entry in @($journal.components)) { $components.Add($entry) }
    $artifacts = [System.Collections.Generic.List[object]]::new()
    foreach ($entry in @($journal.artifacts)) { $artifacts.Add($entry) }
    $events = [System.Collections.Generic.List[object]]::new()
    foreach ($entry in @($journal.events)) { $events.Add($entry) }

    return [pscustomobject]@{
        schema_version = $script:V2ReleaseSchemaVersion
        release_id = [string]$journal.release_id
        environment = [string]$journal.environment
        version = [string]$journal.version
        commit = [string]$journal.commit
        source_dirty = $false
        previous_release_id = [string]$journal.previous_release_id
        install_root = [string]$journal.install_root
        directory = $directory
        backup_root = (Join-Path $directory 'backup')
        created_utc = [string]$journal.created_utc
        stage = [string]$journal.stage
        files = $files
        tasks = $tasks
        components = $components
        artifacts = $artifacts
        events = $events
    }
}

# Persists the exported task XML next to the backups so Import-V2ReleaseTransaction
# can restore it later.
function Save-V2ReleaseTaskDefinitions {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Transaction
    )

    $directory = Join-Path $Transaction.backup_root 'tasks'
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) {
        New-Item -ItemType Directory -Path $directory -Force -ErrorAction Stop | Out-Null
    }
    foreach ($task in @($Transaction.tasks)) {
        if (-not $task.existed) { continue }
        $safeName = ($task.name -replace '[\\/:*?"<>|]', '_')
        Set-Content -LiteralPath (Join-Path $directory ("{0}.xml" -f $safeName)) -Value ([string]$task.definition) -Encoding UTF8 -ErrorAction Stop
    }
}

function Write-V2JsonAtomic {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,

        [Parameter(Mandatory = $true)]
        [object]$Value
    )

    $directory = Split-Path -Parent $Path
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) {
        New-Item -ItemType Directory -Path $directory -Force -ErrorAction Stop | Out-Null
    }
    $temporary = Join-Path $directory ('.{0}.{1}.tmp' -f ([IO.Path]::GetFileName($Path)), [Guid]::NewGuid().ToString('N'))
    $backup = Join-Path $directory ('.{0}.{1}.previous' -f ([IO.Path]::GetFileName($Path)), [Guid]::NewGuid().ToString('N'))
    try {
        # Windows PowerShell 5.1's -Encoding UTF8 emits a BOM. Go's strict JSON
        # decoder correctly rejects those extra leading bytes, so write an
        # explicitly BOM-less UTF-8 document on every PowerShell edition.
        $json = ($Value | ConvertTo-Json -Depth 12) + [Environment]::NewLine
        [IO.File]::WriteAllText($temporary, $json, [Text.UTF8Encoding]::new($false))
        Get-Content -LiteralPath $temporary -Raw -Encoding UTF8 | ConvertFrom-Json | Out-Null
        if ([IO.File]::Exists($Path)) {
            [IO.File]::Replace($temporary, $Path, $backup, $true)
        }
        else {
            [IO.File]::Move($temporary, $Path)
        }
    }
    finally {
        foreach ($residue in @($temporary, $backup)) {
            if ([IO.File]::Exists($residue)) {
                try { [IO.File]::Delete($residue) } catch { }
            }
        }
    }
}
