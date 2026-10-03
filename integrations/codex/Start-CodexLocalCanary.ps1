[CmdletBinding()]
param(
	[ValidatePattern('^[a-z0-9][a-z0-9._-]{0,127}$')]
	[string]$Model,
	[string[]]$Arguments = @()
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$profileName = 'cia-local-canary'
$codexHome = if ([string]::IsNullOrWhiteSpace($env:CODEX_HOME)) {
    Join-Path $env:USERPROFILE '.codex'
}
else {
    $env:CODEX_HOME
}
$profilePath = Join-Path $codexHome "$profileName.config.toml"
$catalogPath = 'C:\IA\local-ai-v2\config\codex-model-catalog.json'
$helperPath = 'C:\IA\local-ai-v2\bin\cia-credential.exe'
$mcpPath = 'C:\IA\local-ai-v2\bin\cia-mcp.exe'
$manifestPath = 'C:\IA\local-ai-v2\config\models.yaml'

function Assert-CodexModelCapabilities {
	param([Parameter(Mandatory = $true)][object]$Capabilities)
	foreach ($name in @('responses', 'streaming', 'function_calling')) {
		$property = $Capabilities.PSObject.Properties[$name]
		if ($null -eq $property -or $property.Value -isnot [bool] -or -not $property.Value) {
			throw "The selected model is not qualified for Codex: '$name' is required. Use the inference MCP for local delegation."
		}
	}
}

function Assert-SafeCodexSessionArguments {
	param(
		[AllowEmptyCollection()]
		[string[]]$Values
	)

	# These options can replace the pinned local provider/model/configuration,
	# connect the TUI to another server, or re-enable network-backed features.
	# Comparisons are ordinal so the legitimate -C working-directory option is
	# not confused with the security-sensitive lowercase -c config override.
	$blockedExact = @(
		'-c', '--config',
		'-m', '--model',
		'-p', '--profile',
		'--oss', '--local-provider',
		'--remote', '--remote-auth-token-env',
		'--search', '--enable',
		'cloud', 'remote-control', 'app-server', 'exec-server',
		'login', 'logout', 'update', 'app', 'plugin', 'mcp', 'mcp-server'
	)
	$blockedLongValuePrefixes = @(
		'--config=', '--model=', '--profile=',
		'--oss=', '--local-provider=',
		'--remote=', '--remote-auth-token-env=',
		'--search=', '--enable='
	)

	foreach ($argument in @($Values)) {
		if ($null -eq $argument) {
			throw 'Null arguments are not allowed by the canary launcher.'
		}
		if ($blockedExact -ccontains $argument) {
			throw "Argument '$argument' is not allowed by the canary launcher. Start Codex directly for provider, remote, cloud, or feature overrides."
		}

		$blockedShortValue = $argument.Length -gt 2 -and (
			$argument.StartsWith('-c', [StringComparison]::Ordinal) -or
			$argument.StartsWith('-m', [StringComparison]::Ordinal) -or
			$argument.StartsWith('-p', [StringComparison]::Ordinal)
		)
		$blockedLongValue = $false
		foreach ($prefix in $blockedLongValuePrefixes) {
			if ($argument.StartsWith($prefix, [StringComparison]::Ordinal)) {
				$blockedLongValue = $true
				break
			}
		}
		if ($blockedShortValue -or $blockedLongValue) {
			throw "Argument '$argument' is not allowed by the canary launcher. Start Codex directly for provider, remote, cloud, or feature overrides."
		}
	}
}

foreach ($required in @($profilePath, $catalogPath, $helperPath, $mcpPath, $manifestPath)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Required canary artifact not found: $required. Run Install-V2Harness.ps1 -Environment Canary -Apply first."
    }
}

$catalog = Get-Content -LiteralPath $catalogPath -Raw -Encoding UTF8 | ConvertFrom-Json
$manifest = Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
if ([string]::IsNullOrWhiteSpace($Model)) {
	$defaultModel = [regex]::Match((Get-Content -LiteralPath $profilePath -Raw), '(?m)^model\s*=\s*"([a-z0-9][a-z0-9._-]{0,127})"\s*$')
	if (-not $defaultModel.Success) { throw 'The installed Codex profile must declare its local default model.' }
	$Model = $defaultModel.Groups[1].Value
}
$allowedModels = @($catalog.models | ForEach-Object { [string]$_.slug })
if ($allowedModels -notcontains $Model) {
	throw "Model '$Model' is not present in the installed Codex local-only catalog."
}
$selected = @($manifest.models | Where-Object { $_.id -eq $Model -and $_.state -ne 'retired' -and @($_.deployments) -contains 'canary' })
if ($selected.Count -ne 1) { throw "Model '$Model' is not deployed to canary in the installed manifest." }
Assert-CodexModelCapabilities -Capabilities $selected[0].capabilities

# Reject every known CLI escape hatch from this explicit local-only session.
# Ordinary prompts, workspace, sandbox, approval, image, and display arguments
# remain available.
Assert-SafeCodexSessionArguments -Values $Arguments

$codexPath = Get-ChildItem -LiteralPath "$env:LOCALAPPDATA\OpenAI\Codex\bin" -Recurse -Filter codex.exe -File -ErrorAction SilentlyContinue |
    Sort-Object LastWriteTime -Descending |
    Select-Object -First 1 -ExpandProperty FullName
if (-not $codexPath) {
    $codex = Get-Command codex -ErrorAction SilentlyContinue
    if ($codex) { $codexPath = $codex.Source }
}
if (-not $codexPath) {
    throw 'Codex CLI was not found.'
}

# CLI overrides have higher precedence than repository-local .codex config.
# Pinning these values prevents a project config from redirecting this explicit
# canary session to a cloud provider or to the final/v1 ports.
$pinnedOverrides = @(
	'-c', ('model="{0}"' -f $Model),
    '-c', 'model_provider="cia-local-canary"',
    '-c', 'model_providers.cia-local-canary.base_url="http://127.0.0.1:18090/v1"',
    '-c', 'model_providers.cia-local-canary.wire_api="responses"',
    '-c', 'features.enable_request_compression=false',
    '-c', 'web_search="disabled"'
)

$previousErrorActionPreference = $ErrorActionPreference
try {
	# Windows PowerShell 5 surfaces native stderr as ErrorRecord objects. Codex
	# uses stderr for normal progress messages, so process success must follow
	# its exit code instead of promoting those messages to terminating errors.
	$ErrorActionPreference = 'Continue'
	& $codexPath --profile $profileName @pinnedOverrides @Arguments
	$codexExitCode = $LASTEXITCODE
}
finally {
	$ErrorActionPreference = $previousErrorActionPreference
}
exit $codexExitCode
