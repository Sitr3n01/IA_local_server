[CmdletBinding()]
param(
	[ValidateSet('Canary', 'Final')]
	[string]$Environment = 'Canary',
	[string]$SourceBinary = 'C:\IA\local-ai-v2\state\staging\cia-tray.exe',
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
	throw "Panel installation root must be exactly '$expectedRoot'."
}
if ($Apply -and -not (Test-V2IsAdministrator)) {
	throw 'Panel installation requires an elevated PowerShell because bin is protected. Preview remains available without elevation.'
}
$source = (Resolve-Path -LiteralPath $SourceBinary -ErrorAction Stop).Path
if (-not (Test-Path -LiteralPath $source -PathType Leaf) -or [IO.Path]::GetExtension($source) -ne '.exe') {
	throw "Panel staging executable is invalid: $SourceBinary"
}
if ($ExpectedSha256 -and $ExpectedSha256 -notmatch '^[A-Fa-f0-9]{64}$') {
	throw 'ExpectedSha256 must contain exactly 64 hexadecimal characters.'
}
$approvedHash = if ($ExpectedSha256) { $ExpectedSha256.ToUpperInvariant() } else { $null }

$environmentName = $Environment.ToLowerInvariant()
$panelConfig = Join-Path $resolvedRoot "config\panel.$environmentName.json"
$trayLauncher = Join-Path $resolvedRoot "launchers\tray-$environmentName.vbs"
$missingPrerequisites = @(@($panelConfig, $trayLauncher) | Where-Object { -not (Test-Path -LiteralPath $_ -PathType Leaf) })

$destination = Join-Path $resolvedRoot 'bin\cia-tray.exe'
$resolvedDestination = [IO.Path]::GetFullPath($destination)
if ([string]::Equals([IO.Path]::GetFullPath($source), $resolvedDestination, [StringComparison]::OrdinalIgnoreCase)) {
	throw 'Panel source and destination must be different files.'
}
$sourceHash = (Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash
if ($approvedHash -and $sourceHash -ne $approvedHash) {
	throw "Panel staging hash does not match ExpectedSha256. Expected $approvedHash; found $sourceHash."
}
$destinationHash = if (Test-Path -LiteralPath $destination -PathType Leaf) {
	(Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash
}
else { $null }
$action = if (-not $destinationHash) { 'create' } elseif ($destinationHash -eq $sourceHash) { 'unchanged' } elseif ($Replace) { 'replace' } else { 'blocked-existing' }

$result = [pscustomobject]@{
	mode = if ($Apply) { 'apply' } else { 'preview' }
	environment = $environmentName
	source = $source
	destination = $destination
	source_sha256 = $sourceHash
	expected_sha256 = $approvedHash
	destination_sha256 = $destinationHash
	action = $action
	prerequisites_ready = $missingPrerequisites.Count -eq 0
	missing_prerequisites = $missingPrerequisites
	started = $false
	startup_changed = $false
}

if (-not $Apply) {
	$result | ConvertTo-Json -Depth 3
	Write-Host 'Preview only. Record source_sha256, then re-run elevated with -Apply -ExpectedSha256 <reviewed hash>.'
	return
}
if (-not $approvedHash) {
	throw 'Apply requires -ExpectedSha256 with the exact hash reviewed during preview.'
}
if ($missingPrerequisites.Count -gt 0) {
	throw "Generate the $environmentName panel configuration before installation: $($missingPrerequisites -join ', ')"
}
[void](Assert-V2DeploymentMarker `
		-Path (Join-Path $resolvedRoot "config\deployment.$environmentName.json") `
		-Environment $Environment `
		-InstallRoot $resolvedRoot)
if ($action -eq 'blocked-existing') {
	throw "Installed panel differs from staging. Inspect both hashes, then use -Apply -Replace if replacement is intended."
}
if ($action -in @('create', 'replace')) {
	$destinationDirectory = Split-Path -Parent $destination
	if (-not (Test-Path -LiteralPath $destinationDirectory -PathType Container)) {
		throw "Protected panel destination directory is missing: $destinationDirectory"
	}
	$currentHash = if (Test-Path -LiteralPath $destination -PathType Leaf) {
		(Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash
	}
	else { $null }
	if ($currentHash -ne $destinationHash) {
		throw 'Installed panel changed after preview within this process; no file was replaced.'
	}

	$nonce = [Guid]::NewGuid().ToString('N')
	$temporary = Join-Path $destinationDirectory ".cia-tray.$nonce.tmp"
	$backup = Join-Path $destinationDirectory ".cia-tray.$nonce.bak"
	$discard = Join-Path $destinationDirectory ".cia-tray.$nonce.failed"
	$created = $false
	$replaced = $false
	try {
		[IO.File]::Copy($source, $temporary, $false)
		$temporaryHash = (Get-FileHash -LiteralPath $temporary -Algorithm SHA256).Hash
		if ($temporaryHash -ne $approvedHash) {
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
		$installedHash = (Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash
		if ($installedHash -ne $approvedHash) {
			throw 'Installed panel hash does not match ExpectedSha256.'
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
	throw 'Installed panel hash does not match ExpectedSha256.'
}
$result | Add-Member -NotePropertyName installed_sha256 -NotePropertyValue $installedHash
$result | ConvertTo-Json -Depth 3
