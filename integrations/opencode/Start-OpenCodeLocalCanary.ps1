[CmdletBinding()]
param(
	[string]$OpenCode = (Join-Path $env:LOCALAPPDATA 'Programs\@opencode-aidesktop\OpenCode.exe'),
	[ValidatePattern('^[a-z0-9][a-z0-9._-]{0,127}$')]
	[string]$Model = 'local-coding',
	[string[]]$Arguments = @()
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$helperPath = 'C:\IA\local-ai-v2\bin\cia-credential.exe'
$configPath = 'C:\IA\local-ai-v2\config\opencode.cia-local-canary.jsonc'
$provider = 'cia-local-canary'

foreach ($required in @($helperPath, $configPath, $OpenCode)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Required canary artifact not found: $required. Run Install-V2Harness.ps1 -Environment Canary -Apply first."
    }
}

$providerConfig = Get-Content -LiteralPath $configPath -Raw -Encoding UTF8 | ConvertFrom-Json
$configuredModels = @($providerConfig.provider.$provider.models.PSObject.Properties.Name)
if ($configuredModels -notcontains $Model) {
	throw "Model '$Model' is not present in the installed OpenCode local-only provider."
}
$providerModel = "$provider/$Model"

foreach ($argument in $Arguments) {
    if ($argument -in @('--model', '-m') -or $argument -match '^--model=') {
        throw "Argument '$argument' is not allowed by the canary launcher. Start OpenCode directly for another model."
    }
}

$previousConfig = $env:OPENCODE_CONFIG
$previousInlineConfig = $env:OPENCODE_CONFIG_CONTENT
$openCodeExitCode = 1
try {
    # OPENCODE_CONFIG is an explicit, process-scoped config. The inline copy has
    # higher precedence than project config, preventing a repository-level file
    # from redirecting the canary provider. Neither value contains a secret.
    $env:OPENCODE_CONFIG = $configPath
    $env:OPENCODE_CONFIG_CONTENT = Get-Content -LiteralPath $configPath -Raw -Encoding UTF8

	& $helperPath run-opencode -- $OpenCode --model $providerModel @Arguments
    $openCodeExitCode = $LASTEXITCODE
}
finally {
    if ($null -eq $previousConfig) {
        Remove-Item Env:OPENCODE_CONFIG -ErrorAction SilentlyContinue
    }
    else {
        $env:OPENCODE_CONFIG = $previousConfig
    }

    if ($null -eq $previousInlineConfig) {
        Remove-Item Env:OPENCODE_CONFIG_CONTENT -ErrorAction SilentlyContinue
    }
    else {
        $env:OPENCODE_CONFIG_CONTENT = $previousInlineConfig
    }
}

exit $openCodeExitCode
