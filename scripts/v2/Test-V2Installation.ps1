[CmdletBinding()]
param(
    [ValidateSet('Canary', 'Final')]
    [string]$Environment = 'Canary',
    [string]$InstallRoot = 'C:\IA\local-ai-v2',
    [string]$ManifestPath = 'C:\IA\local-ai-v2\config\models.yaml',
    [string]$SchemaPath = 'C:\IA\local-ai-v2\config\models.schema.json',
    [string]$SchemaValidatorPath = 'C:\IA\local-ai-v2\bin\cia-manifest.exe',
    [switch]$VerifyHashes,
    [switch]$Online
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Common.ps1')
$settings = Get-V2DeploymentSettings -Environment $Environment
Assert-V2ManifestSchema -ManifestPath $ManifestPath -SchemaPath $SchemaPath -ValidatorPath $SchemaValidatorPath
$manifest = Read-V2Manifest -Path $ManifestPath
Assert-V2ManifestSemantics -Manifest $manifest

$checks = [System.Collections.Generic.List[object]]::new()
function Add-Check([string]$Name, [bool]$Pass, [string]$Detail) {
    $checks.Add([pscustomobject]@{ check = $Name; pass = $Pass; detail = $Detail })
}

foreach ($runtime in @($manifest.runtimes)) {
    try {
        Assert-V2Artifact -Artifact $runtime.artifact -Label "Runtime '$($runtime.id)'" -VerifyHash:$VerifyHashes
        Add-Check "runtime:$($runtime.id)" $true $(if ($VerifyHashes) { 'size and SHA-256 match' } else { 'path and size match' })
    }
    catch { Add-Check "runtime:$($runtime.id)" $false $_.Exception.Message }
}
foreach ($model in @($manifest.models)) {
    try {
        Assert-V2Artifact -Artifact $model.artifact -Label "Model '$($model.id)'" -VerifyHash:$VerifyHashes
        Add-Check "model:$($model.id)" $true $(if ($VerifyHashes) { 'size and SHA-256 match' } else { 'path and size match' })
    }
    catch { Add-Check "model:$($model.id)" $false $_.Exception.Message }
}

$expectedFiles = @(
    (Join-Path $InstallRoot 'bin\llama-swap.exe'),
    (Join-Path $InstallRoot 'bin\cia-edge.exe'),
    (Join-Path $InstallRoot 'bin\cia-mcp.exe'),
    (Join-Path $InstallRoot 'bin\cia-mcp-admin.exe'),
    (Join-Path $InstallRoot 'bin\cia-mcp-inference.exe'),
    (Join-Path $InstallRoot 'bin\cia-credential.exe'),
    (Join-Path $InstallRoot 'bin\cia-supervisor.exe'),
    (Join-Path $InstallRoot 'bin\cia-manifest.exe'),
    (Join-Path $InstallRoot ("config\llama-swap.{0}.yaml" -f $settings.Name)),
    (Join-Path $InstallRoot ("config\deployment.{0}.json" -f $settings.Name)),
    (Join-Path $InstallRoot ("launchers\router-{0}.vbs" -f $settings.Name)),
	(Join-Path $InstallRoot ("launchers\edge-{0}.vbs" -f $settings.Name))
)
$panelConfigPath = Join-Path $InstallRoot ("config\panel.{0}.json" -f $settings.Name)
if (Test-Path -LiteralPath $panelConfigPath -PathType Leaf) {
	$expectedFiles += @(
		(Join-Path $InstallRoot 'bin\cia-tray.exe'),
		(Join-Path $InstallRoot ("launchers\tray-{0}.vbs" -f $settings.Name))
	)
}
foreach ($path in $expectedFiles) {
    Add-Check "file:$path" (Test-Path -LiteralPath $path -PathType Leaf) 'required installation artifact'
}

$deploymentPath = Join-Path $InstallRoot ("config\deployment.{0}.json" -f $settings.Name)
if (Test-Path -LiteralPath $deploymentPath -PathType Leaf) {
    try {
        $deployment = Assert-V2DeploymentMarker -Path $deploymentPath -Environment $Environment -InstallRoot $InstallRoot
        Add-Check 'deployment:generated-files-bound' $true 'marker certifies every generated config and launcher file'
        Add-Check 'deployment:schema-version' ($deployment.schema_version -eq 1) 'expected deployment marker schema_version 1'
        Add-Check 'deployment:environment' ($deployment.environment -eq $settings.Name) "expected $($settings.Name)"
        Add-Check 'deployment:manifest-sha256-format' ($deployment.manifest_sha256 -match '^[A-Fa-f0-9]{64}$') 'marker must contain manifest SHA-256'
        Add-Check 'deployment:schema-sha256-format' ($deployment.schema_sha256 -match '^[A-Fa-f0-9]{64}$') 'marker must contain schema SHA-256'

        $installedManifestHash = (Get-FileHash -LiteralPath $ManifestPath -Algorithm SHA256).Hash
        $installedSchemaHash = (Get-FileHash -LiteralPath $SchemaPath -Algorithm SHA256).Hash
        Add-Check 'deployment:manifest-bound' ([string]::Equals($deployment.manifest_sha256, $installedManifestHash, [StringComparison]::OrdinalIgnoreCase)) 'marker must certify installed manifest bytes'
        Add-Check 'deployment:schema-bound' ([string]::Equals($deployment.schema_sha256, $installedSchemaHash, [StringComparison]::OrdinalIgnoreCase)) 'marker must certify installed schema bytes'
        Add-Check 'deployment:manifest-path' ([string]::Equals([IO.Path]::GetFullPath($deployment.manifest_path), [IO.Path]::GetFullPath($ManifestPath), [StringComparison]::OrdinalIgnoreCase)) 'marker must reference installed manifest'
        Add-Check 'deployment:schema-path' ([string]::Equals([IO.Path]::GetFullPath($deployment.manifest_schema_path), [IO.Path]::GetFullPath($SchemaPath), [StringComparison]::OrdinalIgnoreCase)) 'marker must reference installed schema'
    }
    catch {
        Add-Check 'deployment:marker' $false $_.Exception.Message
    }
}

if (Test-Path -LiteralPath $panelConfigPath -PathType Leaf) {
	try {
		$panelConfig = Get-Content -LiteralPath $panelConfigPath -Raw -Encoding UTF8 | ConvertFrom-Json
		Add-Check 'panel:environment' ($panelConfig.environment -eq $settings.Name) "expected $($settings.Name)"
		Add-Check 'panel:control-loopback' ($panelConfig.control_url -eq "http://$($settings.ControlAddress)") 'must use this deployment control plane'
		Add-Check 'panel:manifest-installed' ([string]::Equals($panelConfig.manifest_path, $ManifestPath, [StringComparison]::OrdinalIgnoreCase)) 'must consume installed manifest'
		foreach ($launcher in @($panelConfig.launchers.codex, $panelConfig.launchers.opencode)) {
			Add-Check "panel:launcher:$launcher" (Test-Path -LiteralPath $launcher -PathType Leaf) 'approved launcher must be installed'
		}
		Add-Check 'panel:unsloth-hidden' ($panelConfig.launchers.PSObject.Properties.Match('unsloth').Count -eq 0) 'Unsloth must not be a panel action'
	}
	catch {
		Add-Check 'panel:config' $false $_.Exception.Message
	}
}

$swapPath = $manifest.dependencies.llama_swap.install_path
if (Test-Path -LiteralPath $swapPath -PathType Leaf) {
    $actualSwapHash = (Get-FileHash -LiteralPath $swapPath -Algorithm SHA256).Hash
    Add-Check 'dependency:llama-swap-sha256' ($actualSwapHash -eq $manifest.dependencies.llama_swap.executable_sha256) 'installed executable must match v240 lock'
}

$ports = @(
    [int]($settings.RouterAddress.Split(':')[-1]),
    [int]($settings.DataAddress.Split(':')[-1]),
    [int]($settings.ControlAddress.Split(':')[-1])
)
foreach ($port in $ports) {
    $listeners = @(Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue)
    $nonLoopback = @($listeners | Where-Object { $_.LocalAddress -notin @('127.0.0.1', '::1') })
    Add-Check "listener:${port}:loopback-only" ($nonLoopback.Count -eq 0) $(if ($listeners.Count -eq 0) { 'not listening' } elseif ($nonLoopback.Count -eq 0) { 'loopback only' } else { "unsafe listeners: $($nonLoopback.LocalAddress -join ', ')" })
}

$configFiles = @(Get-ChildItem -LiteralPath (Join-Path $InstallRoot 'config') -File -ErrorAction SilentlyContinue)
foreach ($file in $configFiles) {
    $raw = Get-Content -LiteralPath $file.FullName -Raw -ErrorAction SilentlyContinue
    $looksSecret = $raw -match '(?i)authorization\s*[:=]\s*bearer\s+\S+' -or $raw -match '(?i)"?(token|api[_-]?key)"?\s*[:=]\s*"?(?!\$\{env\.)[A-Za-z0-9+/_=-]{24,}'
    Add-Check "secret-scan:$($file.Name)" (-not $looksSecret) 'no literal bearer/API token pattern'
}

if ($Online) {
    $before = @(Get-Process -Name 'llama-server' -ErrorAction SilentlyContinue).Count
    try {
        $live = Invoke-WebRequest -UseBasicParsing -Uri "http://$($settings.ControlAddress)/livez" -TimeoutSec 5
        Add-Check 'online:livez' ($live.StatusCode -eq 200) "HTTP $($live.StatusCode)"
    }
    catch { Add-Check 'online:livez' $false $_.Exception.Message }

	try {
		$beforeStatus = @(Get-Process -Name 'llama-server' -ErrorAction SilentlyContinue).Count
		$statusResponse = Invoke-WebRequest -UseBasicParsing -Uri "http://$($settings.ControlAddress)/api/v1/status" -TimeoutSec 5
		$statusPayload = $statusResponse.Content | ConvertFrom-Json
		$afterStatus = @(Get-Process -Name 'llama-server' -ErrorAction SilentlyContinue).Count
		$statusSafe = $statusResponse.StatusCode -eq 200 -and $statusPayload.service -eq 'cia-edge' -and $afterStatus -eq $beforeStatus
		Add-Check 'online:status-public-read-only' $statusSafe "HTTP $($statusResponse.StatusCode); llama-server count $beforeStatus -> $afterStatus"
	}
	catch { Add-Check 'online:status-public-read-only' $false $_.Exception.Message }

    $credentialHelper = Join-Path $InstallRoot 'bin\cia-credential.exe'
    $inferenceToken = ''
    try {
        if (-not (Test-Path -LiteralPath $credentialHelper -PathType Leaf)) {
            throw "Credential helper is missing: $credentialHelper"
        }
        $inferenceToken = (& $credentialHelper get inference 2>$null | Out-String).Trim()
        if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($inferenceToken)) {
            throw 'Unable to obtain inference credential.'
        }
        $modelsResponse = Invoke-WebRequest `
            -UseBasicParsing `
            -Uri "http://$($settings.DataAddress)/v1/models" `
            -Headers @{ Authorization = "Bearer $inferenceToken" } `
            -TimeoutSec 5
        $after = @(Get-Process -Name 'llama-server' -ErrorAction SilentlyContinue).Count
        Add-Check 'online:models-read-only' ($modelsResponse.StatusCode -eq 200 -and $after -eq $before) "HTTP $($modelsResponse.StatusCode); llama-server count $before -> $after"
    }
    catch { Add-Check 'online:models-read-only' $false $_.Exception.Message }
    finally { $inferenceToken = $null }
}

$checks | Format-Table -AutoSize
$failed = @($checks | Where-Object { -not $_.pass })
if ($failed.Count -gt 0) {
    throw "$($failed.Count) v2 installation check(s) failed."
}
