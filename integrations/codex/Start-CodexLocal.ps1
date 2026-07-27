[CmdletBinding()]
param(
	[ValidatePattern('^[a-z0-9][a-z0-9._-]{0,127}$')]
	[string]$Model = 'local-coding',
	[string[]]$Arguments = @()
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$profileName = 'cia-local'
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
			throw 'Null arguments are not allowed by the local launcher.'
		}
		if ($blockedExact -ccontains $argument) {
			throw "Argument '$argument' is not allowed by the local launcher. Start Codex directly for provider, remote, cloud, or feature overrides."
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
			throw "Argument '$argument' is not allowed by the local launcher. Start Codex directly for provider, remote, cloud, or feature overrides."
		}
	}
}

foreach ($required in @($profilePath, $catalogPath, $helperPath, $mcpPath)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Required final artifact not found: $required. Run Install-V2Harness.ps1 -Environment Final -Apply after promotion."
    }
}

$catalog = Get-Content -LiteralPath $catalogPath -Raw -Encoding UTF8 | ConvertFrom-Json
$allowedModels = @($catalog.models | ForEach-Object { [string]$_.slug })
if ($allowedModels -notcontains $Model) {
	throw "Model '$Model' is not present in the installed Codex local-only catalog."
}

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

$pinnedOverrides = @(
	'-c', ('model="{0}"' -f $Model),
    '-c', 'model_provider="cia-local"',
    '-c', 'model_providers.cia-local.base_url="http://127.0.0.1:8090/v1"',
    '-c', 'model_providers.cia-local.wire_api="responses"',
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
