[CmdletBinding()]
param(
    [ValidateSet('Canary', 'Final')]
    [string]$Environment = 'Canary',
    [string]$ManifestPath = (Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'config\models.yaml'),
    [string]$SchemaPath = (Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'config\models.schema.json'),
    [string]$SchemaValidatorPath = 'C:\IA\local-ai-v2\bin\cia-manifest.exe',
    [string]$InstallRoot = 'C:\IA\local-ai-v2',
	[string]$TargetCodexHome,
	[string]$ExpectedPlanSha256,
    [switch]$Apply,
    [switch]$Replace
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Common.ps1')

$expectedInstallRoot = [IO.Path]::GetFullPath('C:\IA\local-ai-v2').TrimEnd([char[]]@('\', '/'))
$resolvedInstallRoot = [IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))
if (-not [string]::Equals($resolvedInstallRoot, $expectedInstallRoot, [StringComparison]::OrdinalIgnoreCase)) {
	throw "Harness installation root must be exactly '$expectedInstallRoot'."
}
$InstallRoot = $expectedInstallRoot
if ($Apply -and -not (Test-V2IsAdministrator)) {
	throw 'Harness installation requires an elevated PowerShell because the v2 installation root is protected. Preview remains available without elevation.'
}
if ($ExpectedPlanSha256 -and $ExpectedPlanSha256 -notmatch '^[A-Fa-f0-9]{64}$') {
	throw 'ExpectedPlanSha256 must contain exactly 64 hexadecimal characters.'
}
$approvedPlanHash = if ($ExpectedPlanSha256) { $ExpectedPlanSha256.ToUpperInvariant() } else { $null }

$repoRoot = Get-V2RepoRoot
$resolvedManifest = (Resolve-Path -LiteralPath $ManifestPath -ErrorAction Stop).Path
$resolvedSchema = (Resolve-Path -LiteralPath $SchemaPath -ErrorAction Stop).Path
$sourceManifestHashBeforeValidation = (Get-FileHash -LiteralPath $resolvedManifest -Algorithm SHA256).Hash
$sourceSchemaHashBeforeValidation = (Get-FileHash -LiteralPath $resolvedSchema -Algorithm SHA256).Hash
Assert-V2ManifestSchema -ManifestPath $ManifestPath -SchemaPath $SchemaPath -ValidatorPath $SchemaValidatorPath
$manifest = Read-V2Manifest -Path $ManifestPath
$sourceManifestHash = (Get-FileHash -LiteralPath $resolvedManifest -Algorithm SHA256).Hash
$sourceSchemaHash = (Get-FileHash -LiteralPath $resolvedSchema -Algorithm SHA256).Hash
if ($sourceManifestHash -ne $sourceManifestHashBeforeValidation -or $sourceSchemaHash -ne $sourceSchemaHashBeforeValidation) {
    throw 'Manifest or schema changed while it was being validated. Retry harness installation from a stable source tree.'
}
Assert-V2ManifestSemantics -Manifest $manifest
$codexHome = if (-not [string]::IsNullOrWhiteSpace($TargetCodexHome)) {
	[IO.Path]::GetFullPath($TargetCodexHome)
}
elseif ([string]::IsNullOrWhiteSpace($env:CODEX_HOME)) {
    Join-Path $env:USERPROFILE '.codex'
}
else {
    $env:CODEX_HOME
}

if ($Environment -eq 'Canary') {
	$profileName = 'cia-local-canary'
	$codexSourceName = 'cia-local-canary.config.toml'
	$codexLauncherName = 'Start-CodexLocalCanary.ps1'
	$openCodeSourceName = 'opencode.canary-provider.jsonc'
	$openCodeLauncherName = 'Start-OpenCodeLocalCanary.ps1'
	$unslothLauncherName = 'Start-UnslothLocalCanary.ps1'
}
else {
	$profileName = 'cia-local'
	$codexSourceName = 'cia-local.config.toml'
	$codexLauncherName = 'Start-CodexLocal.ps1'
	$openCodeSourceName = 'opencode.local-provider.jsonc'
	$openCodeLauncherName = 'Start-OpenCodeLocal.ps1'
	$unslothLauncherName = 'Start-UnslothLocal.ps1'
}

# Require a deployment generated from this manifest. This makes a profile
# install follow the same promotion gate as Router/Edge configuration and keeps
# a stale marker from bypassing a later model demotion.
$deploymentReady = $true
$deploymentError = $null
try {
$deploymentName = $Environment.ToLowerInvariant()
$deploymentPath = Join-Path $InstallRoot "config\deployment.$deploymentName.json"
if (-not (Test-Path -LiteralPath $deploymentPath -PathType Leaf)) {
    throw "$Environment harness installation is blocked until its generated deployment exists: $deploymentPath"
}
$deployment = Assert-V2DeploymentMarker -Path $deploymentPath -Environment $Environment -InstallRoot $InstallRoot
if ($deployment.schema_version -ne 1) {
    throw "$Environment deployment marker has unsupported schema_version '$($deployment.schema_version)'."
}
if ($deployment.environment -ne $deploymentName) {
    throw "$Environment deployment marker has an unexpected environment: $($deployment.environment)"
}

foreach ($requiredProperty in @(
    'source_manifest_path',
    'source_schema_path',
    'manifest_path',
    'manifest_schema_path',
    'manifest_sha256',
    'schema_sha256'
)) {
    $property = $deployment.PSObject.Properties[$requiredProperty]
    if ($null -eq $property -or [string]::IsNullOrWhiteSpace([string]$property.Value)) {
        throw "$Environment deployment marker is missing required property '$requiredProperty'. Regenerate it with New-V2Config.ps1."
    }
}
if ($deployment.manifest_sha256 -notmatch '^[A-Fa-f0-9]{64}$' -or $deployment.schema_sha256 -notmatch '^[A-Fa-f0-9]{64}$') {
    throw "$Environment deployment marker contains an invalid manifest or schema SHA-256."
}

$expectedInstalledManifest = [IO.Path]::GetFullPath((Join-Path $InstallRoot 'config\models.yaml'))
$expectedInstalledSchema = [IO.Path]::GetFullPath((Join-Path $InstallRoot 'config\models.schema.json'))

try {
    $markerSourceManifest = [IO.Path]::GetFullPath([string]$deployment.source_manifest_path)
    $markerSourceSchema = [IO.Path]::GetFullPath([string]$deployment.source_schema_path)
    $markerInstalledManifest = [IO.Path]::GetFullPath([string]$deployment.manifest_path)
    $markerInstalledSchema = [IO.Path]::GetFullPath([string]$deployment.manifest_schema_path)
}
catch {
    throw "$Environment deployment marker contains an invalid manifest or schema path."
}

if (-not [string]::Equals($markerSourceManifest, $resolvedManifest, [StringComparison]::OrdinalIgnoreCase)) {
    throw "$Environment deployment marker was generated from another manifest: $markerSourceManifest"
}
if (-not [string]::Equals($markerSourceSchema, $resolvedSchema, [StringComparison]::OrdinalIgnoreCase)) {
    throw "$Environment deployment marker was generated from another schema: $markerSourceSchema"
}
if (-not [string]::Equals($markerInstalledManifest, $expectedInstalledManifest, [StringComparison]::OrdinalIgnoreCase)) {
    throw "$Environment deployment marker references an unexpected installed manifest: $markerInstalledManifest"
}
if (-not [string]::Equals($markerInstalledSchema, $expectedInstalledSchema, [StringComparison]::OrdinalIgnoreCase)) {
    throw "$Environment deployment marker references an unexpected installed schema: $markerInstalledSchema"
}

foreach ($installedCopy in @($expectedInstalledManifest, $expectedInstalledSchema)) {
    if (-not (Test-Path -LiteralPath $installedCopy -PathType Leaf)) {
        throw "$Environment harness installation is blocked because a certified installed copy is missing: $installedCopy"
    }
}

if ((Get-FileHash -LiteralPath $resolvedManifest -Algorithm SHA256).Hash -ne $sourceManifestHash -or
    (Get-FileHash -LiteralPath $resolvedSchema -Algorithm SHA256).Hash -ne $sourceSchemaHash) {
    throw 'Manifest or schema changed after validation. Retry harness installation from a stable source tree.'
}
$installedManifestHash = (Get-FileHash -LiteralPath $expectedInstalledManifest -Algorithm SHA256).Hash
$installedSchemaHash = (Get-FileHash -LiteralPath $expectedInstalledSchema -Algorithm SHA256).Hash
if (-not [string]::Equals($deployment.manifest_sha256, $sourceManifestHash, [StringComparison]::OrdinalIgnoreCase) -or
    -not [string]::Equals($deployment.manifest_sha256, $installedManifestHash, [StringComparison]::OrdinalIgnoreCase)) {
    throw "$Environment deployment marker does not certify both the source and installed manifest copies. Regenerate the deployment."
}
if (-not [string]::Equals($deployment.schema_sha256, $sourceSchemaHash, [StringComparison]::OrdinalIgnoreCase) -or
    -not [string]::Equals($deployment.schema_sha256, $installedSchemaHash, [StringComparison]::OrdinalIgnoreCase)) {
    throw "$Environment deployment marker does not certify both the source and installed schema copies. Regenerate the deployment."
}

$markerModels = if ($deployment.PSObject.Properties.Match('models').Count -gt 0) {
    @($deployment.models)
}
else {
    @()
}
if ($markerModels.Count -eq 0 -and $deployment.PSObject.Properties.Match('model').Count -gt 0 -and
    -not [string]::IsNullOrWhiteSpace([string]$deployment.model)) {
    $markerModels = @([string]$deployment.model)
}
if ($markerModels.Count -eq 0) {
    throw "$Environment deployment marker contains no model IDs."
}
$deployedIds = @($manifest.models | Where-Object { $_.deployments -contains $deploymentName } | ForEach-Object { [string]$_.id })
if ($markerModels.Count -ne $deployedIds.Count -or @($markerModels | Where-Object { $_ -notin $deployedIds }).Count -gt 0) {
    throw "$Environment deployment marker models do not match the current manifest."
}
}
catch {
	if ($Apply) {
		throw
	}
	$deploymentReady = $false
	$deploymentError = $_.Exception.Message
}

$artifacts = @(
    [pscustomobject]@{
        Name = "Codex $($Environment.ToLowerInvariant()) profile"
        Source = Join-Path $repoRoot "integrations\codex\$codexSourceName"
        Destination = Join-Path $codexHome "$profileName.config.toml"
    },
    [pscustomobject]@{
        Name = 'Codex local-only model catalog'
        Source = Join-Path $repoRoot 'integrations\codex\codex-model-catalog.json'
        Destination = Join-Path $InstallRoot 'config\codex-model-catalog.json'
    },
	[pscustomobject]@{
		Name = "OpenCode $($Environment.ToLowerInvariant()) provider"
		Source = Join-Path $repoRoot "integrations\opencode\$openCodeSourceName"
		Destination = Join-Path $InstallRoot "config\opencode.$profileName.jsonc"
	},
	[pscustomobject]@{
		Name = "Codex $($Environment.ToLowerInvariant()) launcher"
		Source = Join-Path $repoRoot "integrations\codex\$codexLauncherName"
		Destination = Join-Path $InstallRoot "integrations\codex\$codexLauncherName"
	},
	[pscustomobject]@{
		Name = "OpenCode $($Environment.ToLowerInvariant()) launcher"
		Source = Join-Path $repoRoot "integrations\opencode\$openCodeLauncherName"
		Destination = Join-Path $InstallRoot "integrations\opencode\$openCodeLauncherName"
	},
	[pscustomobject]@{
		Name = "Unsloth $($Environment.ToLowerInvariant()) launcher"
		Source = Join-Path $repoRoot "integrations\unsloth\$unslothLauncherName"
		Destination = Join-Path $InstallRoot "integrations\unsloth\$unslothLauncherName"
	}
)

$plan = foreach ($artifact in $artifacts) {
    if (-not (Test-Path -LiteralPath $artifact.Source -PathType Leaf)) {
        throw "$($artifact.Name) source is missing: $($artifact.Source)"
    }

    $destinationExists = Test-Path -LiteralPath $artifact.Destination -PathType Leaf
    $sourceHash = (Get-FileHash -LiteralPath $artifact.Source -Algorithm SHA256).Hash
    $destinationHash = if ($destinationExists) {
        (Get-FileHash -LiteralPath $artifact.Destination -Algorithm SHA256).Hash
    }
    else {
        $null
    }

    [pscustomobject]@{
        name = $artifact.Name
        source = $artifact.Source
        destination = $artifact.Destination
        source_sha256 = $sourceHash
        destination_sha256 = $destinationHash
        action = if (-not $destinationExists) { 'create' } elseif ($sourceHash -eq $destinationHash) { 'unchanged' } elseif ($Replace) { 'replace' } else { 'blocked-existing' }
    }
}

$planMaterial = (@($plan) | ForEach-Object {
	'{0}|{1}|{2}|{3}|{4}' -f $_.name, ([IO.Path]::GetFullPath($_.destination)), $_.source_sha256, $_.destination_sha256, $_.action
}) -join "`n"
$sha256 = [Security.Cryptography.SHA256]::Create()
try {
	$planHash = [BitConverter]::ToString($sha256.ComputeHash([Text.Encoding]::UTF8.GetBytes($planMaterial))).Replace('-', '')
}
finally {
	$sha256.Dispose()
}

$result = [pscustomobject]@{
    mode = if ($Apply) { 'apply' } else { 'preview' }
    environment = $Environment.ToLowerInvariant()
	codex_home = $codexHome
	plan_sha256 = $planHash
    expected_plan_sha256 = $approvedPlanHash
	deployment_ready = $deploymentReady
	deployment_error = $deploymentError
    base_config_touched = $false
    cloud_credentials_touched = $false
    artifacts = @($plan)
}

if (-not $Apply) {
    $result | ConvertTo-Json -Depth 5
	Write-Host 'Preview only. Record plan_sha256, then re-run elevated with -Apply -TargetCodexHome <path> -ExpectedPlanSha256 <reviewed hash>.'
    return
}

if ([string]::IsNullOrWhiteSpace($TargetCodexHome)) {
	throw 'Apply requires an explicit -TargetCodexHome so elevation cannot redirect the Codex profile to another account.'
}
if (-not $approvedPlanHash) {
	throw 'Apply requires -ExpectedPlanSha256 with the exact plan_sha256 reviewed during preview.'
}
if ($approvedPlanHash -ne $planHash) {
	throw "Harness plan does not match ExpectedPlanSha256. Expected $approvedPlanHash; found $planHash."
}
$blocked = @($plan | Where-Object { $_.action -eq 'blocked-existing' })
if ($blocked.Count -gt 0) {
    $targets = ($blocked.destination -join ', ')
    throw "Existing harness files differ from the tracked templates: $targets. Inspect them, then use -Apply -Replace if replacement is intended."
}

$stagedItems = @()
$committedItems = [Collections.Generic.List[object]]::new()
try {
	foreach ($item in $plan | Where-Object { $_.action -in @('create', 'replace') }) {
		$parent = Split-Path -Parent $item.destination
		New-Item -ItemType Directory -Path $parent -Force | Out-Null
		$nonce = [Guid]::NewGuid().ToString('N')
		$temporary = Join-Path $parent ('.cia-harness.{0}.tmp' -f $nonce)
		$backup = Join-Path $parent ('.cia-harness.{0}.bak' -f $nonce)
		$discard = Join-Path $parent ('.cia-harness.{0}.failed' -f $nonce)
		[IO.File]::Copy($item.source, $temporary, $false)
		$temporaryHash = (Get-FileHash -LiteralPath $temporary -Algorithm SHA256).Hash
		if ($temporaryHash -ne $item.source_sha256) {
			throw "Staged harness artifact changed while copying: $($item.name)."
		}
		$stagedItems += [pscustomobject]@{
			item = $item
			temporary = $temporary
			backup = $backup
			discard = $discard
			committed = $false
		}
	}

	foreach ($staged in $stagedItems) {
		$item = $staged.item
		if ((Get-FileHash -LiteralPath $item.source -Algorithm SHA256).Hash -ne $item.source_sha256) {
			throw "Harness source changed after preview: $($item.name)."
		}
		$currentDestinationHash = if (Test-Path -LiteralPath $item.destination -PathType Leaf) {
			(Get-FileHash -LiteralPath $item.destination -Algorithm SHA256).Hash
		}
		else { $null }
		if ($currentDestinationHash -ne $item.destination_sha256) {
			throw "Harness destination changed after preview: $($item.name)."
		}
	}

	foreach ($staged in $stagedItems) {
		$item = $staged.item
		if ($item.action -eq 'create') {
			[IO.File]::Move($staged.temporary, $item.destination)
		}
		else {
			[IO.File]::Replace($staged.temporary, $item.destination, $staged.backup, $true)
		}
		$staged.committed = $true
		$committedItems.Add($staged)
		if ((Get-FileHash -LiteralPath $item.destination -Algorithm SHA256).Hash -ne $item.source_sha256) {
			throw "Installed harness artifact failed hash verification: $($item.name)."
		}
	}
}
catch {
	$failure = $_
	for ($index = $committedItems.Count - 1; $index -ge 0; $index--) {
		$staged = $committedItems[$index]
		$item = $staged.item
		if ($item.action -eq 'replace' -and (Test-Path -LiteralPath $staged.backup -PathType Leaf)) {
			try { [IO.File]::Replace($staged.backup, $item.destination, $staged.discard, $true) } catch { }
		}
		elseif ($item.action -eq 'create' -and (Test-Path -LiteralPath $item.destination -PathType Leaf)) {
			Remove-Item -LiteralPath $item.destination -Force -ErrorAction SilentlyContinue
		}
	}
	throw $failure
}
finally {
	foreach ($staged in $stagedItems) {
		foreach ($temporaryArtifact in @($staged.temporary, $staged.backup, $staged.discard)) {
			if (Test-Path -LiteralPath $temporaryArtifact -PathType Leaf) {
				Remove-Item -LiteralPath $temporaryArtifact -Force -ErrorAction SilentlyContinue
			}
		}
	}
}

$result | ConvertTo-Json -Depth 5
