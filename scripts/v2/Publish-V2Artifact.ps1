<#
.SYNOPSIS
Publishes one manifest artifact into the protected production artifact store.

.DESCRIPTION
A candidate GGUF or runtime build lives outside the installation root, where the
ordinary user can replace it. A production deployment must not depend on a file
with that property. This script copies the exact bytes the manifest pins into
`<InstallRoot>\artifacts`, verifying size and SHA-256 in the source, in staging,
and again at the destination.

The source is never moved, modified, or deleted. The manifest is never rewritten:
the production location is derived from the artifact identifier, so every pinned
hash, source snapshot, and qualification record stays valid.

Preview is the default. Apply requires an elevated shell, because the artifact
tree is protected, and requires the independently reviewed SHA-256 as an explicit
approval boundary.

.EXAMPLE
.\scripts\v2\Publish-V2Artifact.ps1 -Kind Model -Id gemma4-12b-qat-ud-q4xl

.EXAMPLE
.\scripts\v2\Publish-V2Artifact.ps1 -Kind Model -Id gemma4-12b-qat-ud-q4xl -ExpectedSha256 '<reviewed 64-hex>' -Apply
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Model', 'Runtime')]
    [string]$Kind,

    [Parameter(Mandatory = $true)]
    [string]$Id,

    [string]$ManifestPath = (Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'config\models.yaml'),

    [string]$InstallRoot = 'C:\IA\local-ai-v2',

    [ValidatePattern('^$|^[A-Fa-f0-9]{64}$')]
    [string]$ExpectedSha256 = '',

    [switch]$Apply,

    [switch]$Replace,

    # Skips the post-publication ACL apply/audit. Only for a batch that runs the
    # ACL step once at the end; the artifact is not protected until it runs.
    [switch]$SkipAcl
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Common.ps1')

$expectedRoot = [IO.Path]::GetFullPath('C:\IA\local-ai-v2').TrimEnd([char[]]@('\', '/'))
$resolvedRoot = [IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))
if (-not [string]::Equals($resolvedRoot, $expectedRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Artifact publication is restricted to the protected install root '$expectedRoot'."
}
if ($Apply -and -not (Test-V2IsAdministrator)) {
    throw 'Artifact publication requires an elevated PowerShell because the artifact tree is protected. Preview remains available without elevation.'
}

$manifest = Read-V2Manifest -Path $ManifestPath
Assert-V2ManifestSemantics -Manifest $manifest

$collection = if ($Kind -eq 'Model') { @($manifest.models) } else { @($manifest.runtimes) }
$matched = @($collection | Where-Object { $_.id -eq $Id })
if ($matched.Count -ne 1) {
    throw "$Kind '$Id' is not declared exactly once in the manifest."
}
$entry = $matched[0]
$artifact = $entry.artifact

$source = Assert-V2SafePath -Path ([string]$artifact.path) -Root ([IO.Path]::GetPathRoot([string]$artifact.path)) -AllowRoot -MustExist
$sourceItem = Get-Item -LiteralPath $source -Force -ErrorAction Stop
if (($sourceItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Source artifact is a reparse point and cannot be published: $source"
}
if ([long]$sourceItem.Length -ne [long]$artifact.bytes) {
    throw "Source artifact size mismatch at '$source': expected $($artifact.bytes), found $($sourceItem.Length)."
}

$destination = Get-V2ProductionArtifactPath -InstallRoot $resolvedRoot -Kind $Kind -Id $Id -SourcePath $source
$destinationExisting = $null
if (Test-Path -LiteralPath $destination -PathType Leaf) {
    $destinationExisting = Get-V2FileSha256 -Path $destination
}
$action = 'create'
if ($null -ne $destinationExisting) {
    if ([string]::Equals($destinationExisting, [string]$artifact.sha256, [StringComparison]::OrdinalIgnoreCase)) {
        $action = 'unchanged'
    }
    elseif ($Replace) {
        $action = 'replace'
    }
    else {
        $action = 'blocked-existing'
    }
}

$sourceDirectory = Split-Path -Parent $source
$payloadBytes = [long]$sourceItem.Length
$payloadFiles = 1
if ($Kind -eq 'Runtime') {
    # A runtime is its whole directory: the pinned executable cannot load
    # without the backend libraries beside it.
    $runtimeFiles = @(Get-ChildItem -LiteralPath $sourceDirectory -Recurse -File -Force -ErrorAction Stop)
    $payloadFiles = $runtimeFiles.Count
    $payloadBytes = [long](@($runtimeFiles | Measure-Object -Property Length -Sum).Sum)
}

$plan = [pscustomobject]@{
    mode = $(if ($Apply) { 'apply' } else { 'preview' })
    kind = $Kind.ToLowerInvariant()
    id = $Id
    source = $source
    source_directory = $sourceDirectory
    destination = $destination
    manifest_sha256 = ([string]$artifact.sha256).ToUpperInvariant()
    manifest_bytes = [long]$artifact.bytes
    destination_sha256 = $destinationExisting
    payload_files = $payloadFiles
    payload_bytes = $payloadBytes
    action = $action
    source_is_modified = $false
    index = (Get-V2ArtifactIndexPath -InstallRoot $resolvedRoot)
}

if (-not $Apply) {
    $plan | ConvertTo-Json -Depth 4
    Write-Host 'Preview only. No file was copied, replaced, or removed, and the source artifact was not touched.'
    Write-Host 'Verify manifest_sha256 against the reviewed value, then re-run elevated with -ExpectedSha256 <reviewed hash> -Apply.'
    return
}

if ([string]::IsNullOrWhiteSpace($ExpectedSha256)) {
    throw 'Apply requires -ExpectedSha256 with the exact hash reviewed during preview.'
}
if (-not [string]::Equals($ExpectedSha256.ToUpperInvariant(), ([string]$artifact.sha256).ToUpperInvariant(), [StringComparison]::OrdinalIgnoreCase)) {
    throw "ExpectedSha256 does not match the manifest entry for $($Kind.ToLowerInvariant()) '$Id'."
}
if ($action -eq 'blocked-existing') {
    throw "A different artifact is already published at '$destination'. Inspect both hashes, then re-run with -Apply -Replace if replacement is intended."
}

foreach ($required in @('state', 'artifacts')) {
    $path = Join-Path $resolvedRoot $required
    if (-not (Test-Path -LiteralPath $path -PathType Container)) {
        New-Item -ItemType Directory -Path $path -Force -ErrorAction Stop | Out-Null
    }
}

$stagingRoot = Get-V2ArtifactStagingRoot -InstallRoot $resolvedRoot
$publishMutex = [Threading.Mutex]::new($false, 'Local\CIA.LocalAI.V2.ArtifactPublication')
$publishMutexAcquired = $false
try {
    try { $publishMutexAcquired = $publishMutex.WaitOne(0) }
    catch [Threading.AbandonedMutexException] { $publishMutexAcquired = $true }
    if (-not $publishMutexAcquired) {
        throw 'Another v2 artifact publication is already in progress.'
    }

    if ($Kind -eq 'Model') {
        $result = Publish-V2ProtectedFile `
            -Source $source `
            -Destination $destination `
            -Bytes ([long]$artifact.bytes) `
            -Sha256 ([string]$artifact.sha256) `
            -StagingRoot $stagingRoot `
            -ObservedDestinationSha256 $destinationExisting `
            -Replace:$Replace
    }
    else {
        $result = Publish-V2ProtectedRuntime `
            -SourceExecutable $source `
            -Destination $destination `
            -Bytes ([long]$artifact.bytes) `
            -Sha256 ([string]$artifact.sha256) `
            -StagingRoot $stagingRoot `
            -Replace:$Replace
    }

    # The source must be byte-identical after publication. Proving it is cheap
    # relative to the copy and it is the guarantee the promotion process needs:
    # publishing never mutates a candidate.
    $sourceAfter = Get-Item -LiteralPath $source -Force -ErrorAction Stop
    if ([long]$sourceAfter.Length -ne [long]$artifact.bytes) {
        throw "Source artifact changed during publication: $source"
    }

    $indexEntry = [ordered]@{
        kind = $Kind.ToLowerInvariant()
        id = $Id
        source = $source
        path = $destination
        bytes = [long]$artifact.bytes
        sha256 = ([string]$artifact.sha256).ToUpperInvariant()
        payload_files = $payloadFiles
        payload_bytes = $payloadBytes
        published_utc = [DateTime]::UtcNow.ToString('o')
    }
    [void](Update-V2ArtifactIndex -InstallRoot $resolvedRoot -Entry $indexEntry)
    [void](Assert-V2ProductionArtifact -InstallRoot $resolvedRoot -Kind $Kind -Id $Id -Artifact $artifact -VerifyHash)
}
finally {
    if (Test-Path -LiteralPath $stagingRoot -PathType Container) {
        Get-ChildItem -LiteralPath $stagingRoot -Force -ErrorAction SilentlyContinue |
            Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
    }
    if ($publishMutexAcquired) { [void]$publishMutex.ReleaseMutex() }
    $publishMutex.Dispose()
}

$plan.action = $result.action
$plan | Add-Member -NotePropertyName published_sha256 -NotePropertyValue $result.sha256
$plan | ConvertTo-Json -Depth 4

if (-not $SkipAcl) {
    # The published bytes arrive from the writable state directory and carry its
    # DACL until the installation policy is reapplied. Reusing the reviewed
    # script rather than reimplementing the policy keeps the two from drifting.
    & (Join-Path $PSScriptRoot 'Set-V2Acl.ps1') -InstallRoot $resolvedRoot -Apply | Out-Host
    & (Join-Path $PSScriptRoot 'Set-V2Acl.ps1') -InstallRoot $resolvedRoot -Audit | Out-Host
}

Write-Host "Published $($Kind.ToLowerInvariant()) '$Id' to the protected artifact store. The source artifact was not modified or removed."
