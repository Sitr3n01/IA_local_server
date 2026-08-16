[CmdletBinding()]
param(
    [ValidateSet('Canary', 'Final')]
    [string]$Environment = 'Canary',
    [string]$ManifestPath = (Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'config\models.yaml'),
    [string]$SchemaPath = (Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'config\models.schema.json'),
    [string]$SchemaValidatorPath = 'C:\IA\local-ai-v2\bin\cia-manifest.exe',
    [string]$OutputRoot = 'C:\IA\local-ai-v2',
    [switch]$Apply
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Common.ps1')

$expectedOutputRoot = [IO.Path]::GetFullPath('C:\IA\local-ai-v2').TrimEnd([char[]]@('\', '/'))
try {
    $normalizedOutputRoot = [IO.Path]::GetFullPath($OutputRoot).TrimEnd([char[]]@('\', '/'))
}
catch {
    throw "OutputRoot is not a valid absolute path: '$OutputRoot'."
}
if (-not [string]::Equals($normalizedOutputRoot, $expectedOutputRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw "V2 configuration generation is restricted to the protected install root '$expectedOutputRoot'; received '$OutputRoot'."
}
$OutputRoot = $expectedOutputRoot
if ($Apply -and -not (Test-V2IsAdministrator)) {
    throw "Configuration apply requires an elevated PowerShell because '$OutputRoot\config' and '$OutputRoot\launchers' are protected. Preview remains available without elevation."
}

$repoRoot = Get-V2RepoRoot
$resolvedManifestPath = (Resolve-Path -LiteralPath $ManifestPath -ErrorAction Stop).Path
$resolvedSchemaPath = (Resolve-Path -LiteralPath $SchemaPath -ErrorAction Stop).Path
$manifestSha256BeforeValidation = (Get-FileHash -LiteralPath $resolvedManifestPath -Algorithm SHA256).Hash
$schemaSha256BeforeValidation = (Get-FileHash -LiteralPath $resolvedSchemaPath -Algorithm SHA256).Hash
Assert-V2ManifestSchema -ManifestPath $ManifestPath -SchemaPath $SchemaPath -ValidatorPath $SchemaValidatorPath
$manifest = Read-V2Manifest -Path $ManifestPath
$manifestSha256 = (Get-FileHash -LiteralPath $resolvedManifestPath -Algorithm SHA256).Hash
$schemaSha256 = (Get-FileHash -LiteralPath $resolvedSchemaPath -Algorithm SHA256).Hash
if ($manifestSha256 -ne $manifestSha256BeforeValidation -or $schemaSha256 -ne $schemaSha256BeforeValidation) {
    throw 'Manifest or schema changed while it was being validated. Retry generation from a stable source tree.'
}
Assert-V2ManifestSemantics -Manifest $manifest
$settings = Get-V2DeploymentSettings -Environment $Environment
$models = @($manifest.models | Where-Object { $_.deployments -contains $settings.Name })

if ($models.Count -lt 1) {
    throw "Deployment '$($settings.Name)' must contain at least one model."
}
if (@($models | Where-Object { $_.id -eq $manifest.provider.public_model }).Count -ne 1) {
    throw "Deployment '$($settings.Name)' must include provider.public_model '$($manifest.provider.public_model)'."
}

if ($Environment -eq 'Final') {
    foreach ($model in $models) {
        $runtime = @($manifest.runtimes | Where-Object { $_.id -eq $model.runtime })[0]
        if ($model.state -notin @('qualified', 'enabled')) {
            throw "Final generation is blocked because model '$($model.id)' is '$($model.state)'."
        }
        if ($runtime.state -notin @('qualified', 'enabled')) {
            throw "Final generation is blocked because runtime '$($runtime.id)' is '$($runtime.state)'."
        }
    }
}

$runtimeIds = @($models | ForEach-Object { $_.runtime } | Select-Object -Unique)
foreach ($runtimeId in $runtimeIds) {
    $runtime = @($manifest.runtimes | Where-Object { $_.id -eq $runtimeId })[0]
    Assert-V2Artifact -Artifact $runtime.artifact -Label "Runtime '$runtimeId'" -VerifyHash:$Apply
}
foreach ($model in $models) {
    Assert-V2Artifact -Artifact $model.artifact -Label "Model '$($model.id)'" -VerifyHash:$Apply
}

$configRoot = Join-Path $OutputRoot 'config'
$launcherRoot = Join-Path $OutputRoot 'launchers'
$installedManifestPath = Join-Path $configRoot 'models.yaml'
$installedSchemaPath = Join-Path $configRoot 'models.schema.json'
$routerAPIKeyPath = Join-Path $OutputRoot 'state\router-api-key.txt'
$swapConfigPath = Join-Path $configRoot ("llama-swap.{0}.yaml" -f $settings.Name)
$deploymentPath = Join-Path $configRoot ("deployment.{0}.json" -f $settings.Name)
$panelConfigPath = Join-Path $configRoot ("panel.{0}.json" -f $settings.Name)
$routerVbsPath = Join-Path $launcherRoot ("router-{0}.vbs" -f $settings.Name)
$edgeVbsPath = Join-Path $launcherRoot ("edge-{0}.vbs" -f $settings.Name)
$trayVbsPath = Join-Path $launcherRoot ("tray-{0}.vbs" -f $settings.Name)

$modelBlocks = [System.Collections.Generic.List[string]]::new()
foreach ($model in $models) {
    $runtime = @($manifest.runtimes | Where-Object { $_.id -eq $model.runtime })[0]
    $envLines = @()
    foreach ($entry in $runtime.environment.psobject.Properties) {
        $envLines += "      - " + (ConvertTo-V2YamlSingleQuoted "$($entry.Name)=$($entry.Value)")
    }
    $command = New-V2LlamaServerCommand -Runtime $runtime -Model $model -RouterAPIKeyPath $routerAPIKeyPath
    $block = @(
        "  $($model.id):",
        "    name: $(ConvertTo-V2YamlSingleQuoted $model.display_name)",
        "    description: 'Explicit local-only model; no fallback or aliases.'",
        '    cmd: >-',
        "      $command",
        '    checkEndpoint: /health',
        '    ttl: 900',
        '    concurrencyLimit: 1',
        '    sendLoadingState: false',
        '    env:'
    ) + $envLines + @(
        '    metadata:',
        "      lifecycle: $(ConvertTo-V2YamlSingleQuoted $model.state)",
        "      runtime: $(ConvertTo-V2YamlSingleQuoted $runtime.id)",
        "      context_tokens: $($model.context_tokens)",
        "      manifest_sha256: $(ConvertTo-V2YamlSingleQuoted $manifestSha256)"
    )
    foreach ($line in $block) { $modelBlocks.Add($line) }
}

$templatePath = Join-Path $repoRoot 'config\llama-swap.template.yaml'
$swapConfig = Get-Content -LiteralPath $templatePath -Raw -Encoding UTF8
$swapConfig = $swapConfig.Replace('@@MODEL_START_PORT@@', [string]$settings.ModelStartPort)
$swapConfig = $swapConfig.Replace('@@MODEL_BLOCKS@@', ($modelBlocks -join [Environment]::NewLine))

$deployment = [ordered]@{
    schema_version = 1
    environment = $settings.Name
    source_manifest_path = $resolvedManifestPath
    source_schema_path = $resolvedSchemaPath
    manifest_path = $installedManifestPath
    manifest_schema_path = $installedSchemaPath
    manifest_sha256 = $manifestSha256
    schema_sha256 = $schemaSha256
    models = @($models | ForEach-Object { $_.id })
    router = "http://$($settings.RouterAddress)"
    data = "http://$($settings.DataAddress)"
    control = "http://$($settings.ControlAddress)"
    generated_utc = [DateTime]::UtcNow.ToString('o')
    generated_files = @()
}

if ($Environment -eq 'Canary') {
	$codexLauncherName = 'Start-CodexLocalCanary.ps1'
	$openCodeLauncherName = 'Start-OpenCodeLocalCanary.ps1'
}
else {
	$codexLauncherName = 'Start-CodexLocal.ps1'
	$openCodeLauncherName = 'Start-OpenCodeLocal.ps1'
}
$panelConfig = [ordered]@{
	schema_version = 2
	environment = $settings.Name
	data_url = "http://$($settings.DataAddress)"
	control_url = "http://$($settings.ControlAddress)"
	manifest_path = $installedManifestPath
	selection_path = (Join-Path $OutputRoot "state\panel-selection.$($settings.Name).json")
	model_roots_path = (Join-Path $OutputRoot "state\model-roots.$($settings.Name).json")
	validation_path = (Join-Path $OutputRoot "state\model-validation.$($settings.Name).json")
	logs_path = (Join-Path $OutputRoot 'logs')
	launchers = [ordered]@{
		codex = (Join-Path $OutputRoot "integrations\codex\$codexLauncherName")
		opencode = (Join-Path $OutputRoot "integrations\opencode\$openCodeLauncherName")
	}
	refresh_seconds = 10
	operation_timeout_seconds = 180
}

$powershell = "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe"
$routerScript = Join-Path $PSScriptRoot 'Start-V2Router.ps1'
$edgeScript = Join-Path $PSScriptRoot 'Start-V2Edge.ps1'
$routerArgs = "-NoProfile -NonInteractive -ExecutionPolicy RemoteSigned -File `"$routerScript`" -Environment $Environment -InstallRoot `"$OutputRoot`" -Run"
$edgeArgs = "-NoProfile -NonInteractive -ExecutionPolicy RemoteSigned -File `"$edgeScript`" -Environment $Environment -InstallRoot `"$OutputRoot`" -ManifestPath `"$installedManifestPath`" -SchemaPath `"$installedSchemaPath`" -Run"

function New-HiddenLauncherContent {
	param([string]$Executable, [string]$Arguments, [bool]$Wait = $true)
	$commandLine = ('"{0}" {1}' -f $Executable, $Arguments).Replace('"', '""')
	$waitValue = if ($Wait) { 'True' } else { 'False' }
	return @"
Option Explicit
Dim shell
Dim exitCode
Set shell = CreateObject("WScript.Shell")
exitCode = shell.Run("$commandLine", 0, $waitValue)
WScript.Quit exitCode
"@
}

$trayExecutable = Join-Path $OutputRoot 'bin\cia-tray.exe'
$trayArgs = "--config `"$panelConfigPath`""

$plan = [pscustomobject]@{
    mode = $(if ($Apply) { 'apply' } else { 'preview' })
    environment = $settings.Name
    models = @($models | ForEach-Object { $_.id })
    manifest_sha256 = $manifestSha256
    schema_sha256 = $schemaSha256
    router_address = $settings.RouterAddress
    data_address = $settings.DataAddress
    control_address = $settings.ControlAddress
	files = @($installedManifestPath, $installedSchemaPath, $swapConfigPath, $deploymentPath, $panelConfigPath, $routerVbsPath, $edgeVbsPath, $trayVbsPath)
	commit_marker = $deploymentPath
}

if (-not $Apply) {
    $plan | ConvertTo-Json -Depth 4
    Write-Host 'Preview only. Re-run with -Apply to write generated files outside the repository.'
    return
}

function Publish-V2StagedFile {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Source,

        [Parameter(Mandatory = $true)]
        [string]$Destination
    )

    $destinationDirectory = Split-Path -Parent $Destination
    New-Item -ItemType Directory -Force -Path $destinationDirectory | Out-Null
    $temporaryDestination = Join-Path $destinationDirectory ('.{0}.{1}.tmp' -f ([IO.Path]::GetFileName($Destination)), [Guid]::NewGuid().ToString('N'))
    $replacementBackup = Join-Path $destinationDirectory ('.{0}.{1}.replace-backup' -f ([IO.Path]::GetFileName($Destination)), [Guid]::NewGuid().ToString('N'))

    try {
        [IO.File]::Copy($Source, $temporaryDestination, $false)
        if ([IO.File]::Exists($Destination)) {
            # File.Replace maps to a same-volume atomic replacement on NTFS.
            # Windows PowerShell 5.1/.NET Framework rejects a null backup path,
            # so use a unique same-directory backup and remove it after the
            # replacement. The outer transaction owns the durable rollback.
            [IO.File]::Replace($temporaryDestination, $Destination, $replacementBackup, $true)
        }
        else {
            [IO.File]::Move($temporaryDestination, $Destination)
        }
    }
    finally {
        if ([IO.File]::Exists($temporaryDestination)) {
            try { [IO.File]::Delete($temporaryDestination) } catch { }
        }
        if ([IO.File]::Exists($replacementBackup)) {
            # Cleanup cannot be allowed to turn a successful atomic publish
            # into a false transaction failure. Any residue remains protected
            # by the destination directory ACL and visible to inventory checks.
            try { [IO.File]::Delete($replacementBackup) } catch { }
        }
    }
}

$generationMutex = [Threading.Mutex]::new($false, 'Local\CIA.LocalAI.V2.ConfigGeneration')
$generationMutexAcquired = $false
try {
    $generationMutexAcquired = $generationMutex.WaitOne(0)
}
catch [Threading.AbandonedMutexException] {
    $generationMutexAcquired = $true
}
if (-not $generationMutexAcquired) {
    $generationMutex.Dispose()
    throw 'Another v2 configuration generation is already in progress.'
}

$stagingParent = $configRoot
$stagingRoot = Join-Path $stagingParent ('config-generation-{0}' -f [Guid]::NewGuid().ToString('N'))
$stagingConfigRoot = Join-Path $stagingRoot 'config'
$stagingLauncherRoot = Join-Path $stagingRoot 'launchers'

try {
    New-Item -ItemType Directory -Force -Path $stagingConfigRoot, $stagingLauncherRoot | Out-Null

    $stagedManifestPath = Join-Path $stagingConfigRoot 'models.yaml'
    $stagedSchemaPath = Join-Path $stagingConfigRoot 'models.schema.json'
    $stagedSwapConfigPath = Join-Path $stagingConfigRoot ([IO.Path]::GetFileName($swapConfigPath))
    $stagedDeploymentPath = Join-Path $stagingConfigRoot ([IO.Path]::GetFileName($deploymentPath))
    $stagedPanelConfigPath = Join-Path $stagingConfigRoot ([IO.Path]::GetFileName($panelConfigPath))
    $stagedRouterVbsPath = Join-Path $stagingLauncherRoot ([IO.Path]::GetFileName($routerVbsPath))
    $stagedEdgeVbsPath = Join-Path $stagingLauncherRoot ([IO.Path]::GetFileName($edgeVbsPath))
    $stagedTrayVbsPath = Join-Path $stagingLauncherRoot ([IO.Path]::GetFileName($trayVbsPath))

    Copy-Item -LiteralPath $resolvedManifestPath -Destination $stagedManifestPath
    Copy-Item -LiteralPath $resolvedSchemaPath -Destination $stagedSchemaPath
    Set-Content -LiteralPath $stagedSwapConfigPath -Value $swapConfig -Encoding UTF8
    Set-Content -LiteralPath $stagedPanelConfigPath -Value ($panelConfig | ConvertTo-Json -Depth 5) -Encoding UTF8
    Set-Content -LiteralPath $stagedRouterVbsPath -Value (New-HiddenLauncherContent -Executable $powershell -Arguments $routerArgs) -Encoding ASCII
    Set-Content -LiteralPath $stagedEdgeVbsPath -Value (New-HiddenLauncherContent -Executable $powershell -Arguments $edgeArgs) -Encoding ASCII
    Set-Content -LiteralPath $stagedTrayVbsPath -Value (New-HiddenLauncherContent -Executable $trayExecutable -Arguments $trayArgs -Wait:$false) -Encoding ASCII

    $deployment.generated_files = @(
        [ordered]@{ path = $swapConfigPath; sha256 = (Get-FileHash -LiteralPath $stagedSwapConfigPath -Algorithm SHA256).Hash },
        [ordered]@{ path = $panelConfigPath; sha256 = (Get-FileHash -LiteralPath $stagedPanelConfigPath -Algorithm SHA256).Hash },
        [ordered]@{ path = $routerVbsPath; sha256 = (Get-FileHash -LiteralPath $stagedRouterVbsPath -Algorithm SHA256).Hash },
        [ordered]@{ path = $edgeVbsPath; sha256 = (Get-FileHash -LiteralPath $stagedEdgeVbsPath -Algorithm SHA256).Hash },
        [ordered]@{ path = $trayVbsPath; sha256 = (Get-FileHash -LiteralPath $stagedTrayVbsPath -Algorithm SHA256).Hash }
    )
    Set-Content -LiteralPath $stagedDeploymentPath -Value ($deployment | ConvertTo-Json -Depth 6) -Encoding UTF8

    if ((Get-FileHash -LiteralPath $stagedManifestPath -Algorithm SHA256).Hash -ne $manifestSha256) {
        throw 'Staged manifest hash changed before publication.'
    }
    if ((Get-FileHash -LiteralPath $stagedSchemaPath -Algorithm SHA256).Hash -ne $schemaSha256) {
        throw 'Staged schema hash changed before publication.'
    }
    # Parse both generated JSON files before invalidating a previous marker.
    Get-Content -LiteralPath $stagedDeploymentPath -Raw -Encoding UTF8 | ConvertFrom-Json | Out-Null
    Get-Content -LiteralPath $stagedPanelConfigPath -Raw -Encoding UTF8 | ConvertFrom-Json | Out-Null

    # Invalidate the previous commit marker before changing any of the files it
    # certifies. A failed publication is therefore fail-closed and cannot leave
    # a stale marker authorizing a partially updated configuration set.
    $previousMarkerPath = $null
    if ([IO.File]::Exists($deploymentPath)) {
        $previousMarkerPath = Join-Path $stagingRoot 'previous-deployment-marker.json'
        [IO.File]::Move($deploymentPath, $previousMarkerPath)
    }

    $publication = @(
        [pscustomobject]@{ Source = $stagedManifestPath; Destination = $installedManifestPath },
        [pscustomobject]@{ Source = $stagedSchemaPath; Destination = $installedSchemaPath },
        [pscustomobject]@{ Source = $stagedSwapConfigPath; Destination = $swapConfigPath },
        [pscustomobject]@{ Source = $stagedPanelConfigPath; Destination = $panelConfigPath },
        [pscustomobject]@{ Source = $stagedRouterVbsPath; Destination = $routerVbsPath },
        [pscustomobject]@{ Source = $stagedEdgeVbsPath; Destination = $edgeVbsPath },
        [pscustomobject]@{ Source = $stagedTrayVbsPath; Destination = $trayVbsPath }
    )
    $published = [System.Collections.Generic.List[object]]::new()
    try {
        foreach ($item in $publication) {
            $backupPath = $null
            $existed = [IO.File]::Exists($item.Destination)
            if ($existed) {
                $backupPath = Join-Path $stagingRoot ('backup-{0}-{1}' -f ([IO.Path]::GetFileName($item.Destination)), [Guid]::NewGuid().ToString('N'))
                [IO.File]::Copy($item.Destination, $backupPath, $false)
            }
            Publish-V2StagedFile -Source $item.Source -Destination $item.Destination
            $published.Add([pscustomobject]@{
                    Destination = $item.Destination
                    Existed = $existed
                    Backup = $backupPath
                })
        }

        # The deployment marker is the commit record and is intentionally the
        # last file made visible.
        Publish-V2StagedFile -Source $stagedDeploymentPath -Destination $deploymentPath
        [void](Assert-V2DeploymentMarker -Path $deploymentPath -Environment $Environment -InstallRoot $OutputRoot)
    }
    catch {
        $publicationFailure = $_
        $rollbackFailures = [System.Collections.Generic.List[string]]::new()
        for ($publishedIndex = $published.Count - 1; $publishedIndex -ge 0; $publishedIndex--) {
            $item = $published[$publishedIndex]
            try {
                if ($item.Existed) {
                    Publish-V2StagedFile -Source $item.Backup -Destination $item.Destination
                }
                elseif ([IO.File]::Exists($item.Destination)) {
                    [IO.File]::Delete($item.Destination)
                }
            }
            catch {
                $rollbackFailures.Add("$($item.Destination): $($_.Exception.Message)")
            }
        }

        # A marker is restored only after every certified file was restored.
        # If rollback is incomplete, leaving it absent keeps all consumers
        # fail-closed instead of authorizing a mixed configuration generation.
        if ($rollbackFailures.Count -eq 0 -and $previousMarkerPath -and [IO.File]::Exists($previousMarkerPath)) {
            try {
                Publish-V2StagedFile -Source $previousMarkerPath -Destination $deploymentPath
            }
            catch {
                $rollbackFailures.Add("${deploymentPath}: $($_.Exception.Message)")
            }
        }
        elseif ([IO.File]::Exists($deploymentPath)) {
            [IO.File]::Delete($deploymentPath)
        }

        if ($rollbackFailures.Count -gt 0) {
            throw "Configuration publication failed and rollback was incomplete. The deployment marker remains absent. Publication error: $($publicationFailure.Exception.Message). Rollback errors: $($rollbackFailures -join ' | ')"
        }
        throw $publicationFailure
    }
}
finally {
    try {
        $normalizedStagingRoot = [IO.Path]::GetFullPath($stagingRoot)
        $normalizedStagingParent = [IO.Path]::GetFullPath($stagingParent).TrimEnd([char[]]@('\', '/')) + [IO.Path]::DirectorySeparatorChar
        if ((Test-Path -LiteralPath $normalizedStagingRoot) -and $normalizedStagingRoot.StartsWith($normalizedStagingParent, [StringComparison]::OrdinalIgnoreCase)) {
            Remove-Item -LiteralPath $normalizedStagingRoot -Recurse -Force
        }
    }
    finally {
        if ($generationMutexAcquired) {
            [void]$generationMutex.ReleaseMutex()
        }
        $generationMutex.Dispose()
    }
}

$plan | ConvertTo-Json -Depth 4
