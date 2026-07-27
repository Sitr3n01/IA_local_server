[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Edge', 'Mcp', 'McpAdmin', 'McpInference', 'Supervisor')]
    [string]$Component,
    [ValidateSet('Canary', 'Final')]
    [string]$Environment = 'Canary',
    [string]$SourceBinary,
    [string]$ExpectedSha256,
    [string]$InstallRoot = 'C:\IA\local-ai-v2',
    [switch]$Apply,
    [switch]$Replace
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Common.ps1')

$expectedRoot = [IO.Path]::GetFullPath('C:\IA\local-ai-v2').TrimEnd('\')
$resolvedRoot = [IO.Path]::GetFullPath($InstallRoot).TrimEnd('\')
if (-not [string]::Equals($resolvedRoot, $expectedRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Binary installation root must be exactly '$expectedRoot'."
}
if ($Apply -and -not (Test-V2IsAdministrator)) {
    throw 'Binary installation requires an elevated PowerShell because bin is protected. Preview remains available without elevation.'
}

$componentName = $Component.ToLowerInvariant()
$binaryName = switch ($Component) {
    'Edge' { 'cia-edge.exe' }
    'Mcp' { 'cia-mcp.exe' }
    'McpAdmin' { 'cia-mcp-admin.exe' }
    'McpInference' { 'cia-mcp-inference.exe' }
    'Supervisor' { 'cia-supervisor.exe' }
}
if ([string]::IsNullOrWhiteSpace($SourceBinary)) {
    $SourceBinary = Join-Path $resolvedRoot "state\staging\$binaryName"
}
$source = (Resolve-Path -LiteralPath $SourceBinary -ErrorAction Stop).Path
if (-not (Test-Path -LiteralPath $source -PathType Leaf) -or [IO.Path]::GetExtension($source) -ne '.exe') {
    throw "Staged $componentName executable is invalid: $SourceBinary"
}
if ($ExpectedSha256 -and $ExpectedSha256 -notmatch '^[A-Fa-f0-9]{64}$') {
    throw 'ExpectedSha256 must contain exactly 64 hexadecimal characters.'
}
$approvedHash = if ($ExpectedSha256) { $ExpectedSha256.ToUpperInvariant() } else { $null }

if ($Component -eq 'Edge') {
    $environmentName = $Environment.ToLowerInvariant()
    $deploymentPath = Join-Path $resolvedRoot "config\deployment.$environmentName.json"
    [void](Assert-V2DeploymentMarker -Path $deploymentPath -Environment $Environment -InstallRoot $resolvedRoot)
}

$destination = Join-Path $resolvedRoot "bin\$binaryName"
if ([string]::Equals([IO.Path]::GetFullPath($source), [IO.Path]::GetFullPath($destination), [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Binary source and destination must be different files.'
}
$sourceHash = (Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash
if ($approvedHash -and $sourceHash -ne $approvedHash) {
    throw "$Component staging hash does not match ExpectedSha256. Expected $approvedHash; found $sourceHash."
}
$destinationHash = if (Test-Path -LiteralPath $destination -PathType Leaf) {
    (Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash
}
else { $null }
$action = if (-not $destinationHash) { 'create' } elseif ($destinationHash -eq $sourceHash) { 'unchanged' } elseif ($Replace) { 'replace' } else { 'blocked-existing' }

$running = @(
    Get-Process -Name ([IO.Path]::GetFileNameWithoutExtension($binaryName)) -ErrorAction SilentlyContinue |
        Where-Object {
            try { [string]::Equals($_.Path, $destination, [StringComparison]::OrdinalIgnoreCase) }
            catch { $false }
        } |
        Select-Object -ExpandProperty Id
)
$result = [pscustomobject]@{
    mode = if ($Apply) { 'apply' } else { 'preview' }
    component = $componentName
    environment = $Environment.ToLowerInvariant()
    source = $source
    destination = $destination
    source_sha256 = $sourceHash
    expected_sha256 = $approvedHash
    destination_sha256 = $destinationHash
    action = $action
    running_pids = $running
    started = $false
    startup_changed = $false
}

if (-not $Apply) {
    $result | ConvertTo-Json -Depth 3
    Write-Host 'Preview only. Record source_sha256, stop the listed process if any, then re-run elevated with -Apply -ExpectedSha256 <reviewed hash>.'
    return
}
if (-not $approvedHash) {
    throw 'Apply requires -ExpectedSha256 with the exact hash reviewed during preview.'
}
if ($running.Count -gt 0) {
    throw "$Component is still running from the protected destination (PID: $($running -join ', ')). Stop its task/client before replacement."
}
if ($action -eq 'blocked-existing') {
    throw "Installed $Component differs from staging. Inspect both hashes, then use -Apply -Replace if replacement is intended."
}

if ($action -in @('create', 'replace')) {
    $destinationDirectory = Split-Path -Parent $destination
    if (-not (Test-Path -LiteralPath $destinationDirectory -PathType Container)) {
        throw "Protected binary destination directory is missing: $destinationDirectory"
    }
    $currentHash = if (Test-Path -LiteralPath $destination -PathType Leaf) {
        (Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash
    }
    else { $null }
    if ($currentHash -ne $destinationHash) {
        throw 'Installed binary changed during preflight; no file was replaced.'
    }

    $nonce = [Guid]::NewGuid().ToString('N')
    $temporary = Join-Path $destinationDirectory ".$binaryName.$nonce.tmp"
    $backup = Join-Path $destinationDirectory ".$binaryName.$nonce.bak"
    $discard = Join-Path $destinationDirectory ".$binaryName.$nonce.failed"
    $created = $false
    $replaced = $false
    try {
        [IO.File]::Copy($source, $temporary, $false)
        if ((Get-FileHash -LiteralPath $temporary -Algorithm SHA256).Hash -ne $approvedHash) {
            throw 'Protected staging copy does not match ExpectedSha256; destination was not changed.'
        }
        if ($action -eq 'create') {
            [IO.File]::Move($temporary, $destination)
            $created = $true
        }
        else {
            [IO.File]::Replace($temporary, $destination, $backup, $true)
            $replaced = $true
        }
        if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash -ne $approvedHash) {
            throw 'Installed binary hash does not match ExpectedSha256.'
        }
    }
    catch {
        $failure = $_
        if ($replaced -and (Test-Path -LiteralPath $backup -PathType Leaf)) {
            try { [IO.File]::Replace($backup, $destination, $discard, $true) } catch { }
        }
        elseif ($created -and (Test-Path -LiteralPath $destination -PathType Leaf)) {
            Remove-Item -LiteralPath $destination -Force -ErrorAction SilentlyContinue
        }
        throw $failure
    }
    finally {
        foreach ($temporaryArtifact in @($temporary, $backup, $discard)) {
            if (Test-Path -LiteralPath $temporaryArtifact -PathType Leaf) {
                Remove-Item -LiteralPath $temporaryArtifact -Force -ErrorAction SilentlyContinue
            }
        }
    }
}

$installedHash = (Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash
if ($installedHash -ne $approvedHash) {
    throw 'Installed binary hash does not match ExpectedSha256.'
}
$result | Add-Member -NotePropertyName installed_sha256 -NotePropertyValue $installedHash
$result | ConvertTo-Json -Depth 3
