Set-StrictMode -Version Latest

<#
Production artifact boundary.

A candidate artifact lives wherever it was produced or downloaded - typically
`C:\IA\models` or a runtime build directory - and stays there, unmodified,
forever. A production artifact is a verified *copy* of those exact bytes inside
the protected installation root, where the serving user has read/execute only
and Administrators/SYSTEM keep recovery.

The manifest is not rewritten to point at the copy. Identity stays SHA-256 plus
byte size, and the production path is derived from the artifact's own identifier,
so publishing never invalidates a pinned hash, a source snapshot, or a
qualification record. Final generation resolves the derived path and re-verifies
that it holds exactly the manifest's bytes; if it does not, generation fails.

Nothing here deletes, moves, or modifies a source artifact.
#>

$script:V2ArtifactIndexSchemaVersion = 1
$script:V2ArtifactIdPattern = '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'

function Get-V2ArtifactRoot {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot
    )

    return (Join-Path ([IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))) 'artifacts')
}

function Get-V2ArtifactIndexPath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot
    )

    return (Join-Path (Get-V2ArtifactRoot -InstallRoot $InstallRoot) 'published.json')
}

# Canonicalises a path and refuses anything that escapes the approved root or
# traverses a reparse point. Both checks matter: a relative segment is the
# obvious escape, and a junction planted inside the tree is the quiet one.
function Assert-V2SafePath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,

        [Parameter(Mandatory = $true)]
        [string]$Root,

        [switch]$AllowRoot,

        [switch]$MustExist
    )

    if (-not [IO.Path]::IsPathRooted($Path) -or $Path -notmatch '^[A-Za-z]:[\\/]') {
        throw "Artifact path must be an absolute local path: $Path"
    }
    $canonicalPath = [IO.Path]::GetFullPath($Path).TrimEnd([IO.Path]::DirectorySeparatorChar)
    $canonicalRoot = [IO.Path]::GetFullPath($Root).TrimEnd([IO.Path]::DirectorySeparatorChar)
    if ([string]::IsNullOrWhiteSpace($canonicalPath) -or
        $canonicalPath -eq [IO.Path]::GetPathRoot($canonicalPath).TrimEnd([IO.Path]::DirectorySeparatorChar)) {
        throw "Refusing a filesystem root as an artifact target: $Path"
    }

    if ([string]::Equals($canonicalPath, $canonicalRoot, [StringComparison]::OrdinalIgnoreCase)) {
        if (-not $AllowRoot) {
            throw "Artifact target may not be the protected root itself: $canonicalPath"
        }
    }
    else {
        $prefix = $canonicalRoot + [IO.Path]::DirectorySeparatorChar
        if (-not $canonicalPath.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Artifact target escapes the protected root '$canonicalRoot': $canonicalPath"
        }
    }

    # Walk every existing segment from the root down. A reparse point anywhere on
    # the way is a redirection the publisher must not follow.
    $segments = [System.Collections.Generic.List[string]]::new()
    $cursor = $canonicalPath
    while ($cursor -and -not [string]::Equals($cursor, $canonicalRoot, [StringComparison]::OrdinalIgnoreCase)) {
        $segments.Insert(0, $cursor)
        $parent = [IO.Path]::GetDirectoryName($cursor)
        if ([string]::IsNullOrEmpty($parent) -or [string]::Equals($parent, $cursor, [StringComparison]::OrdinalIgnoreCase)) {
            break
        }
        $cursor = $parent.TrimEnd([IO.Path]::DirectorySeparatorChar)
    }
    $segments.Insert(0, $canonicalRoot)
    foreach ($segment in $segments) {
        if (-not (Test-Path -LiteralPath $segment)) {
            continue
        }
        $item = Get-Item -LiteralPath $segment -Force -ErrorAction Stop
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Reparse points are not valid artifact path segments: $($item.FullName)"
        }
    }

    if ($MustExist -and -not (Test-Path -LiteralPath $canonicalPath)) {
        throw "Artifact path does not exist: $canonicalPath"
    }
    return $canonicalPath
}

# The production location of one artifact, derived from its manifest identifier
# and the source file name. Deriving rather than storing keeps the mapping
# reproducible from the manifest alone and impossible to redirect by editing an
# index file.
function Get-V2ProductionArtifactPath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot,

        [Parameter(Mandatory = $true)]
        [ValidateSet('Model', 'Runtime')]
        [string]$Kind,

        [Parameter(Mandatory = $true)]
        [string]$Id,

        [Parameter(Mandatory = $true)]
        [string]$SourcePath
    )

    if ($Id -notmatch $script:V2ArtifactIdPattern) {
        throw "Artifact identifier '$Id' is not a safe directory name."
    }
    $leaf = [IO.Path]::GetFileName($SourcePath)
    if ([string]::IsNullOrWhiteSpace($leaf) -or $leaf -in @('.', '..') -or $leaf -match '[\\/:*?"<>|]') {
        throw "Artifact file name '$leaf' is not a safe file name."
    }

    $artifactRoot = Get-V2ArtifactRoot -InstallRoot $InstallRoot
    $kindDirectory = if ($Kind -eq 'Model') { 'models' } else { 'runtimes' }
    $destination = Join-Path (Join-Path (Join-Path $artifactRoot $kindDirectory) $Id) $leaf
    return (Assert-V2SafePath -Path $destination -Root $artifactRoot)
}

function Get-V2ArtifactStagingRoot {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot
    )

    # Staging lives under the writable state directory rather than under
    # artifacts, so a half-copied file is never enumerated as a production
    # artifact and never inherits the immutable class.
    $root = [IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))
    return (Join-Path (Join-Path $root 'state') 'artifact-staging')
}

function Get-V2FileSha256 {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToUpperInvariant()
}

# Verifies one file against an expected size and SHA-256. Size is checked first
# because it is free and rules out the common mismatch before hashing gigabytes.
function Assert-V2FileIdentity {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,

        [Parameter(Mandatory = $true)]
        [long]$Bytes,

        [Parameter(Mandatory = $true)]
        [string]$Sha256,

        [Parameter(Mandatory = $true)]
        [string]$Label
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Label is missing: $Path"
    }
    $item = Get-Item -LiteralPath $Path -Force -ErrorAction Stop
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "$Label is a reparse point and cannot be trusted: $Path"
    }
    if ([long]$item.Length -ne $Bytes) {
        throw "$Label size mismatch at '$Path': expected $Bytes, found $($item.Length)."
    }
    $actual = Get-V2FileSha256 -Path $Path
    if (-not [string]::Equals($actual, $Sha256.ToUpperInvariant(), [StringComparison]::OrdinalIgnoreCase)) {
        throw "$Label SHA-256 mismatch at '$Path': expected $($Sha256.ToUpperInvariant()), found $actual."
    }
    return $actual
}

# Copies one verified file into the protected tree through a staging copy and an
# atomic replacement. The bytes are verified in the source, again in staging, and
# again at the destination; the destination is re-checked immediately before the
# swap so a file that changed during the transaction aborts it.
function Publish-V2ProtectedFile {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Source,

        [Parameter(Mandatory = $true)]
        [string]$Destination,

        [Parameter(Mandatory = $true)]
        [long]$Bytes,

        [Parameter(Mandatory = $true)]
        [string]$Sha256,

        [Parameter(Mandatory = $true)]
        [string]$StagingRoot,

        [string]$ObservedDestinationSha256,

        [switch]$Replace
    )

    $expected = $Sha256.ToUpperInvariant()
    [void](Assert-V2FileIdentity -Path $Source -Bytes $Bytes -Sha256 $expected -Label 'Source artifact')

    $destinationDirectory = Split-Path -Parent $Destination
    if (-not (Test-Path -LiteralPath $destinationDirectory -PathType Container)) {
        New-Item -ItemType Directory -Path $destinationDirectory -Force -ErrorAction Stop | Out-Null
    }

    $existed = Test-Path -LiteralPath $Destination -PathType Leaf
    if ($existed) {
        $current = Get-V2FileSha256 -Path $Destination
        if ($PSBoundParameters.ContainsKey('ObservedDestinationSha256') -and
            -not [string]::IsNullOrWhiteSpace($ObservedDestinationSha256) -and
            -not [string]::Equals($current, $ObservedDestinationSha256.ToUpperInvariant(), [StringComparison]::OrdinalIgnoreCase)) {
            throw "Destination artifact changed during the transaction; nothing was published: $Destination"
        }
        if ([string]::Equals($current, $expected, [StringComparison]::OrdinalIgnoreCase)) {
            return [pscustomobject]@{ action = 'unchanged'; path = $Destination; sha256 = $expected }
        }
        if (-not $Replace) {
            throw "A different artifact is already published at '$Destination'. Re-run with -Replace only after reviewing both hashes."
        }
    }

    if (-not (Test-Path -LiteralPath $StagingRoot -PathType Container)) {
        New-Item -ItemType Directory -Path $StagingRoot -Force -ErrorAction Stop | Out-Null
    }
    $nonce = [Guid]::NewGuid().ToString('N')
    $staged = Join-Path $StagingRoot ("{0}.{1}.staged" -f ([IO.Path]::GetFileName($Destination)), $nonce)
    $backup = Join-Path $StagingRoot ("{0}.{1}.previous" -f ([IO.Path]::GetFileName($Destination)), $nonce)
    $discard = Join-Path $StagingRoot ("{0}.{1}.discarded" -f ([IO.Path]::GetFileName($Destination)), $nonce)

    $created = $false
    $replaced = $false
    try {
        [IO.File]::Copy($Source, $staged, $false)
        [void](Assert-V2FileIdentity -Path $staged -Bytes $Bytes -Sha256 $expected -Label 'Staged artifact')

        if ($existed) {
            [IO.File]::Replace($staged, $Destination, $backup, $true)
            $replaced = $true
        }
        else {
            [IO.File]::Move($staged, $Destination)
            $created = $true
        }
        [void](Assert-V2FileIdentity -Path $Destination -Bytes $Bytes -Sha256 $expected -Label 'Published artifact')
    }
    catch {
        $failure = $_
        if ($replaced -and (Test-Path -LiteralPath $backup -PathType Leaf)) {
            try { [IO.File]::Replace($backup, $Destination, $discard, $true) } catch { }
        }
        elseif ($created -and (Test-Path -LiteralPath $Destination -PathType Leaf)) {
            Remove-Item -LiteralPath $Destination -Force -ErrorAction SilentlyContinue
        }
        throw $failure
    }
    finally {
        foreach ($residue in @($staged, $backup, $discard)) {
            if (Test-Path -LiteralPath $residue -PathType Leaf) {
                Remove-Item -LiteralPath $residue -Force -ErrorAction SilentlyContinue
            }
        }
    }

    $action = 'created'
    if ($replaced) { $action = 'replaced' }
    return [pscustomobject]@{ action = $action; path = $Destination; sha256 = $expected }
}

# Publishes a whole runtime directory. A runtime is not one executable: the
# pinned llama-server.exe cannot load without the backend DLLs beside it, so
# copying only the pinned file would produce a production tree that silently
# falls back to CPU or fails to start.
function Publish-V2ProtectedRuntime {
    param(
        [Parameter(Mandatory = $true)]
        [string]$SourceExecutable,

        [Parameter(Mandatory = $true)]
        [string]$Destination,

        [Parameter(Mandatory = $true)]
        [long]$Bytes,

        [Parameter(Mandatory = $true)]
        [string]$Sha256,

        [Parameter(Mandatory = $true)]
        [string]$StagingRoot,

        [switch]$Replace
    )

    $expected = $Sha256.ToUpperInvariant()
    [void](Assert-V2FileIdentity -Path $SourceExecutable -Bytes $Bytes -Sha256 $expected -Label 'Source runtime')

    $sourceDirectory = Split-Path -Parent $SourceExecutable
    $destinationDirectory = Split-Path -Parent $Destination
    $existed = Test-Path -LiteralPath $destinationDirectory -PathType Container
    if ($existed -and -not $Replace) {
        $publishedExecutable = Test-Path -LiteralPath $Destination -PathType Leaf
        if ($publishedExecutable -and [string]::Equals((Get-V2FileSha256 -Path $Destination), $expected, [StringComparison]::OrdinalIgnoreCase)) {
            return [pscustomobject]@{ action = 'unchanged'; path = $Destination; sha256 = $expected; file_count = @(Get-ChildItem -LiteralPath $destinationDirectory -Recurse -File -Force).Count }
        }
        throw "A different runtime is already published at '$destinationDirectory'. Re-run with -Replace only after reviewing both hashes."
    }

    if (-not (Test-Path -LiteralPath $StagingRoot -PathType Container)) {
        New-Item -ItemType Directory -Path $StagingRoot -Force -ErrorAction Stop | Out-Null
    }
    $nonce = [Guid]::NewGuid().ToString('N')
    $staged = Join-Path $StagingRoot ("runtime.{0}.staged" -f $nonce)
    $retired = Join-Path $StagingRoot ("runtime.{0}.previous" -f $nonce)

    $movedAside = $false
    $moved = $false
    try {
        New-Item -ItemType Directory -Path $staged -ErrorAction Stop | Out-Null
        foreach ($file in @(Get-ChildItem -LiteralPath $sourceDirectory -Recurse -Force -ErrorAction Stop)) {
            if (($file.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Runtime source contains a reparse point and cannot be published: $($file.FullName)"
            }
            $relative = $file.FullName.Substring($sourceDirectory.Length).TrimStart([char[]]@('\', '/'))
            $target = Join-Path $staged $relative
            if ($file -is [IO.DirectoryInfo]) {
                New-Item -ItemType Directory -Path $target -Force -ErrorAction Stop | Out-Null
                continue
            }
            $targetParent = Split-Path -Parent $target
            if (-not (Test-Path -LiteralPath $targetParent -PathType Container)) {
                New-Item -ItemType Directory -Path $targetParent -Force -ErrorAction Stop | Out-Null
            }
            [IO.File]::Copy($file.FullName, $target, $false)
        }

        $stagedExecutable = Join-Path $staged ([IO.Path]::GetFileName($SourceExecutable))
        [void](Assert-V2FileIdentity -Path $stagedExecutable -Bytes $Bytes -Sha256 $expected -Label 'Staged runtime')

        $parent = Split-Path -Parent $destinationDirectory
        if (-not (Test-Path -LiteralPath $parent -PathType Container)) {
            New-Item -ItemType Directory -Path $parent -Force -ErrorAction Stop | Out-Null
        }
        if ($existed) {
            [IO.Directory]::Move($destinationDirectory, $retired)
            $movedAside = $true
        }
        [IO.Directory]::Move($staged, $destinationDirectory)
        $moved = $true
        [void](Assert-V2FileIdentity -Path $Destination -Bytes $Bytes -Sha256 $expected -Label 'Published runtime')
    }
    catch {
        $failure = $_
        if (-not $moved -and $movedAside -and (Test-Path -LiteralPath $retired -PathType Container)) {
            try { [IO.Directory]::Move($retired, $destinationDirectory) } catch { }
        }
        elseif ($moved -and $movedAside -and (Test-Path -LiteralPath $retired -PathType Container)) {
            try {
                Remove-Item -LiteralPath $destinationDirectory -Recurse -Force -ErrorAction Stop
                [IO.Directory]::Move($retired, $destinationDirectory)
            }
            catch { }
        }
        throw $failure
    }
    finally {
        foreach ($residue in @($staged, $retired)) {
            if (Test-Path -LiteralPath $residue -PathType Container) {
                Remove-Item -LiteralPath $residue -Recurse -Force -ErrorAction SilentlyContinue
            }
        }
    }

    $action = 'created'
    if ($existed) { $action = 'replaced' }
    return [pscustomobject]@{
        action = $action
        path = $Destination
        sha256 = $expected
        file_count = @(Get-ChildItem -LiteralPath $destinationDirectory -Recurse -File -Force).Count
    }
}

function Read-V2ArtifactIndex {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot
    )

    $path = Get-V2ArtifactIndexPath -InstallRoot $InstallRoot
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        return [pscustomobject]@{
            schema_version = $script:V2ArtifactIndexSchemaVersion
            install_root = [IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))
            updated_utc = $null
            artifacts = @()
        }
    }
    try {
        $index = Get-Content -LiteralPath $path -Raw -Encoding UTF8 | ConvertFrom-Json
    }
    catch {
        throw "Production artifact index is not valid JSON: $path. $($_.Exception.Message)"
    }
    if ($index.schema_version -ne $script:V2ArtifactIndexSchemaVersion) {
        throw "Production artifact index has unsupported schema_version '$($index.schema_version)'."
    }
    if ($null -eq $index.PSObject.Properties['artifacts']) {
        throw "Production artifact index is missing its 'artifacts' array: $path"
    }
    return $index
}

# Rewrites the index atomically with the entry for one artifact replaced. The
# index is the last thing made visible, so a failed publication can never leave
# a record claiming bytes that were not installed.
function Update-V2ArtifactIndex {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot,

        [Parameter(Mandatory = $true)]
        [object]$Entry
    )

    $index = Read-V2ArtifactIndex -InstallRoot $InstallRoot
    $retained = @(@($index.artifacts) | Where-Object {
            $null -ne $_ -and -not ($_.kind -eq $Entry.kind -and $_.id -eq $Entry.id)
        })
    $updated = [ordered]@{
        schema_version = $script:V2ArtifactIndexSchemaVersion
        install_root = [IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))
        updated_utc = [DateTime]::UtcNow.ToString('o')
        artifacts = @(@($retained + @($Entry)) | Sort-Object kind, id)
    }

    $path = Get-V2ArtifactIndexPath -InstallRoot $InstallRoot
    $directory = Split-Path -Parent $path
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) {
        New-Item -ItemType Directory -Path $directory -Force -ErrorAction Stop | Out-Null
    }
    $temporary = Join-Path $directory (".published.{0}.tmp" -f [Guid]::NewGuid().ToString('N'))
    $backup = Join-Path $directory (".published.{0}.previous" -f [Guid]::NewGuid().ToString('N'))
    try {
        Set-Content -LiteralPath $temporary -Value ($updated | ConvertTo-Json -Depth 6) -Encoding UTF8 -ErrorAction Stop
        Get-Content -LiteralPath $temporary -Raw -Encoding UTF8 | ConvertFrom-Json | Out-Null
        if ([IO.File]::Exists($path)) {
            [IO.File]::Replace($temporary, $path, $backup, $true)
        }
        else {
            [IO.File]::Move($temporary, $path)
        }
    }
    finally {
        foreach ($residue in @($temporary, $backup)) {
            if ([IO.File]::Exists($residue)) {
                try { [IO.File]::Delete($residue) } catch { }
            }
        }
    }
    return $updated
}

# Resolves the production copy of one manifest artifact and proves it holds the
# manifest's exact bytes. This is what makes a Final deployment independent of a
# file the ordinary user can still edit.
function Assert-V2ProductionArtifact {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot,

        [Parameter(Mandatory = $true)]
        [ValidateSet('Model', 'Runtime')]
        [string]$Kind,

        [Parameter(Mandatory = $true)]
        [string]$Id,

        [Parameter(Mandatory = $true)]
        [object]$Artifact,

        [switch]$VerifyHash
    )

    $destination = Get-V2ProductionArtifactPath -InstallRoot $InstallRoot -Kind $Kind -Id $Id -SourcePath $Artifact.path
    $index = Read-V2ArtifactIndex -InstallRoot $InstallRoot
    $entry = @(@($index.artifacts) | Where-Object { $_.kind -eq $Kind.ToLowerInvariant() -and $_.id -eq $Id })
    if ($entry.Count -ne 1) {
        throw "$Kind '$Id' has no production artifact record. Publish it with Publish-V2Artifact.ps1 before generating a production deployment."
    }
    $record = $entry[0]
    if (-not [string]::Equals([IO.Path]::GetFullPath([string]$record.path), $destination, [StringComparison]::OrdinalIgnoreCase)) {
        throw "$Kind '$Id' production record points at '$($record.path)', not the derived location '$destination'."
    }
    if (-not [string]::Equals([string]$record.sha256, [string]$Artifact.sha256, [StringComparison]::OrdinalIgnoreCase)) {
        throw "$Kind '$Id' production record certifies a different SHA-256 than the manifest."
    }
    if ([long]$record.bytes -ne [long]$Artifact.bytes) {
        throw "$Kind '$Id' production record certifies a different byte size than the manifest."
    }

    if ($VerifyHash) {
        [void](Assert-V2FileIdentity -Path $destination -Bytes ([long]$Artifact.bytes) -Sha256 ([string]$Artifact.sha256) -Label "Production $($Kind.ToLowerInvariant()) '$Id'")
    }
    else {
        if (-not (Test-Path -LiteralPath $destination -PathType Leaf)) {
            throw "Production $($Kind.ToLowerInvariant()) '$Id' is missing: $destination"
        }
        $item = Get-Item -LiteralPath $destination -Force -ErrorAction Stop
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Production $($Kind.ToLowerInvariant()) '$Id' is a reparse point: $destination"
        }
        if ([long]$item.Length -ne [long]$Artifact.bytes) {
            throw "Production $($Kind.ToLowerInvariant()) '$Id' size mismatch at '$destination'."
        }
    }
    return $destination
}

# Returns a copy of a manifest entry whose artifact path points at the verified
# production copy. The manifest on disk is never rewritten; only the object used
# for this one generation is redirected.
function ConvertTo-V2ProductionEntry {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Entry,

        [Parameter(Mandatory = $true)]
        [string]$ProductionPath
    )

    $clone = $Entry | ConvertTo-Json -Depth 20 | ConvertFrom-Json
    $clone.artifact.path = $ProductionPath
    return $clone
}
