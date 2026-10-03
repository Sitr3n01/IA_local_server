[CmdletBinding()]
param(
    [string]$RepoRoot
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ([string]::IsNullOrWhiteSpace($RepoRoot)) {
    $RepoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
}

function Assert-True {
    param(
        [Parameter(Mandatory = $true)]
        [bool]$Condition,
        [Parameter(Mandatory = $true)]
        [string]$Message
    )
    if (-not $Condition) {
        throw $Message
    }
}

function Get-ScriptFunctionBody {
	param(
		[Parameter(Mandatory = $true)]
		[string]$Path,
		[Parameter(Mandatory = $true)]
		[string]$Name
	)

	$tokens = $null
	$errors = $null
	$ast = [System.Management.Automation.Language.Parser]::ParseFile(
		(Resolve-Path -LiteralPath $Path -ErrorAction Stop).Path,
		[ref]$tokens,
		[ref]$errors
	)
	if ($errors.Count -gt 0) {
		throw "PowerShell parse failed for '$Path': $($errors[0].Message)"
	}
	$matches = @($ast.FindAll({
		param($node)
		$node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $Name
	}, $true))
	if ($matches.Count -ne 1) {
		throw "Expected exactly one function '$Name' in '$Path'; found $($matches.Count)."
	}
	return $matches[0].Body.GetScriptBlock()
}

$readOnlyTools = @(
    'local_ai_health',
    'local_ai_models',
    'local_ai_active_model',
    'local_ai_capacity',
    'local_ai_recent_events'
)

$blockedCodexArguments = @(
	'-c', '--config', '-m', '--model', '-p', '--profile',
	'--oss', '--local-provider', '--remote', '--remote-auth-token-env',
	'--search', '--enable', 'cloud', 'remote-control', 'app-server',
	'exec-server', 'login', 'logout', 'update', 'app', 'plugin', 'mcp',
	'mcp-server'
)
$blockedCodexValuePrefixes = @(
	'--config=', '--model=', '--profile=', '--oss=', '--local-provider=',
	'--remote=', '--remote-auth-token-env=', '--search=', '--enable='
)

$manifestPath = Join-Path $RepoRoot 'config\models.yaml'
$manifest = Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
$publicModel = [string]$manifest.provider.public_model
Assert-True (-not [string]::IsNullOrWhiteSpace($publicModel)) 'provider.public_model is required.'

$profiles = @(
    [pscustomobject]@{
        Provider = 'cia-local'
        DataPort = 8090
        ControlPort = 8091
        Codex = Join-Path $RepoRoot 'integrations\codex\cia-local.config.toml'
        OpenCode = Join-Path $RepoRoot 'integrations\opencode\opencode.local-provider.jsonc'
		CodexLauncher = Join-Path $RepoRoot 'integrations\codex\Start-CodexLocal.ps1'
		OpenCodeLauncher = Join-Path $RepoRoot 'integrations\opencode\Start-OpenCodeLocal.ps1'
		UnslothLauncher = Join-Path $RepoRoot 'integrations\unsloth\Start-UnslothLocal.ps1'
    },
    [pscustomobject]@{
        Provider = 'cia-local-canary'
        DataPort = 18090
        ControlPort = 18091
        Codex = Join-Path $RepoRoot 'integrations\codex\cia-local-canary.config.toml'
        OpenCode = Join-Path $RepoRoot 'integrations\opencode\opencode.canary-provider.jsonc'
		CodexLauncher = Join-Path $RepoRoot 'integrations\codex\Start-CodexLocalCanary.ps1'
		OpenCodeLauncher = Join-Path $RepoRoot 'integrations\opencode\Start-OpenCodeLocalCanary.ps1'
		UnslothLauncher = Join-Path $RepoRoot 'integrations\unsloth\Start-UnslothLocalCanary.ps1'
    }
)

foreach ($profile in $profiles) {
	foreach ($path in @($profile.Codex, $profile.OpenCode, $profile.CodexLauncher, $profile.OpenCodeLauncher, $profile.UnslothLauncher)) {
        Assert-True (Test-Path -LiteralPath $path -PathType Leaf) "Missing harness artifact: $path"
    }

    $codex = Get-Content -LiteralPath $profile.Codex -Raw -Encoding UTF8
    $providerHeaders = @([regex]::Matches($codex, '(?m)^\[model_providers\.([^\.\]]+)\]\r?$'))
    $mcpHeaders = @([regex]::Matches($codex, '(?m)^\[mcp_servers\.([^\.\]]+)\]\r?$'))
    Assert-True ($providerHeaders.Count -eq 1 -and $providerHeaders[0].Groups[1].Value -eq $profile.Provider) "Codex profile has an unexpected provider table: $($profile.Codex)"
    Assert-True ($mcpHeaders.Count -eq 1 -and $mcpHeaders[0].Groups[1].Value -eq $profile.Provider) "Codex profile has an unexpected MCP table: $($profile.Codex)"
    $codexDefault = [regex]::Match($codex, '(?m)^model\s*=\s*"([a-z0-9][a-z0-9._-]{0,127})"\s*$')
    Assert-True $codexDefault.Success "Codex profile must pin a local model: $($profile.Codex)"
    $codexDefaultModel = @($manifest.models | Where-Object { $_.id -eq $codexDefault.Groups[1].Value -and $_.state -ne 'retired' -and @($_.deployments) -contains 'canary' })
    Assert-True ($codexDefaultModel.Count -eq 1) "Codex default must be in the active roster: $($profile.Codex)"
    $codexDefaultCaps = $codexDefaultModel[0].capabilities
    Assert-True ([bool]($codexDefaultCaps.responses -and $codexDefaultCaps.streaming -and $codexDefaultCaps.function_calling)) "Codex default must qualify Responses, streaming and tools: $($profile.Codex)"
    Assert-True ($codex -match "(?m)^model_provider\s*=\s*`"$([regex]::Escape($profile.Provider))`"\s*$") "Codex provider is not pinned: $($profile.Codex)"
    Assert-True ($codex -match "base_url\s*=\s*`"http://127\.0\.0\.1:$($profile.DataPort)/v1`"") "Codex data URL mismatch: $($profile.Codex)"
    Assert-True ($codex -match '(?m)^wire_api\s*=\s*"responses"\s*$') "Codex wire API mismatch: $($profile.Codex)"
    Assert-True ($codex -match '(?m)^enable_request_compression\s*=\s*false\s*$') "Codex compression defense is missing: $($profile.Codex)"
    Assert-True ($codex -match '(?m)^web_search\s*=\s*"disabled"\s*$') "Codex web search must be disabled for the local-only provider: $($profile.Codex)"
    Assert-True ($codex -match 'cia-credential\.exe') "Codex command-backed auth is missing: $($profile.Codex)"
    Assert-True ($codex -match "CIA_CONTROL_URL\s*=\s*`"http://127\.0\.0\.1:$($profile.ControlPort)`"") "Codex control URL mismatch: $($profile.Codex)"
	Assert-True ($codex -notmatch '(?m)^CIA_(?:ADMIN_TOKEN|CREDENTIAL_HELPER)\s*=') "Codex read-only MCP must not receive an administrative credential: $($profile.Codex)"
    foreach ($tool in $readOnlyTools) {
        Assert-True ($codex -match "`"$tool`"") "Codex MCP allowlist is missing '$tool': $($profile.Codex)"
    }
    Assert-True ($codex -notmatch '(?i)cia-mcp-admin|api\.openai\.com|\bBearer\s+\S+|\bsk-[A-Za-z0-9]') "Codex profile contains an administrative, cloud, or secret marker: $($profile.Codex)"

    $openCode = Get-Content -LiteralPath $profile.OpenCode -Raw -Encoding UTF8 | ConvertFrom-Json
    $providerNames = @($openCode.provider.psobject.Properties.Name)
    $mcpNames = @($openCode.mcp.psobject.Properties.Name)
    Assert-True ($openCode.model -eq "$($profile.Provider)/$publicModel") "OpenCode model mismatch: $($profile.OpenCode)"
    Assert-True (@($openCode.enabled_providers).Count -eq 1 -and $openCode.enabled_providers[0] -eq $profile.Provider) "OpenCode provider allowlist mismatch: $($profile.OpenCode)"
    Assert-True ($openCode.share -eq 'disabled') "OpenCode sharing must be disabled in the local launcher config: $($profile.OpenCode)"
    Assert-True ($providerNames.Count -eq 1 -and $providerNames[0] -eq $profile.Provider) "OpenCode has an unexpected provider: $($profile.OpenCode)"
    Assert-True ($mcpNames.Count -eq 1 -and $mcpNames[0] -eq $profile.Provider) "OpenCode has an unexpected MCP registration: $($profile.OpenCode)"
    $provider = $openCode.provider.psobject.Properties[$profile.Provider].Value
    $mcp = $openCode.mcp.psobject.Properties[$profile.Provider].Value
    Assert-True ($provider.npm -eq '@ai-sdk/openai-compatible') "OpenCode adapter mismatch: $($profile.OpenCode)"
    Assert-True ($provider.options.baseURL -eq "http://127.0.0.1:$($profile.DataPort)/v1") "OpenCode data URL mismatch: $($profile.OpenCode)"
    Assert-True ($provider.options.apiKey -eq '{env:CIA_LOCAL_API_KEY}') "OpenCode credential placeholder mismatch: $($profile.OpenCode)"
    Assert-True (@($mcp.command).Count -eq 1 -and $mcp.command[0] -match 'cia-mcp\.exe$') "OpenCode read-only MCP command mismatch: $($profile.OpenCode)"
    Assert-True ($mcp.command[0] -notmatch '(?i)admin') "OpenCode registered the administrative MCP: $($profile.OpenCode)"
    Assert-True ($mcp.environment.CIA_CONTROL_URL -eq "http://127.0.0.1:$($profile.ControlPort)") "OpenCode control URL mismatch: $($profile.OpenCode)"
	Assert-True ($null -eq $mcp.environment.PSObject.Properties['CIA_ADMIN_TOKEN'] -and $null -eq $mcp.environment.PSObject.Properties['CIA_CREDENTIAL_HELPER']) "OpenCode read-only MCP must not receive an administrative credential: $($profile.OpenCode)"

	$codexLauncher = Get-Content -LiteralPath $profile.CodexLauncher -Raw -Encoding UTF8
	$openCodeLauncher = Get-Content -LiteralPath $profile.OpenCodeLauncher -Raw -Encoding UTF8
	$unslothLauncher = Get-Content -LiteralPath $profile.UnslothLauncher -Raw -Encoding UTF8
	Assert-True ($codexLauncher -match [regex]::Escape("model_provider=`"$($profile.Provider)`"")) "Codex launcher does not pin the provider: $($profile.CodexLauncher)"
    Assert-True ($codexLauncher -match [regex]::Escape("http://127.0.0.1:$($profile.DataPort)/v1")) "Codex launcher does not pin the endpoint: $($profile.CodexLauncher)"
	Assert-True ($codexLauncher -match 'web_search=\\?"disabled\\?"') "Codex launcher does not pin web search off: $($profile.CodexLauncher)"
	Assert-True ($codexLauncher -match '\[string\]\$Model' -and $codexLauncher -match 'installed Codex local-only catalog') "Codex launcher does not validate the panel-selected model: $($profile.CodexLauncher)"
	Assert-True ($codexLauncher -match 'Assert-SafeCodexSessionArguments' -and $codexLauncher -match '\-ccontains') "Codex launcher lacks its ordinal local-session argument gate: $($profile.CodexLauncher)"
	Assert-True ($codexLauncher -match "StartsWith\('\-c', \[StringComparison\]::Ordinal\)" -and $codexLauncher -match "StartsWith\('\-m', \[StringComparison\]::Ordinal\)" -and $codexLauncher -match "StartsWith\('\-p', \[StringComparison\]::Ordinal\)") "Codex launcher does not reject attached short override values without blocking uppercase -C: $($profile.CodexLauncher)"
	foreach ($blockedArgument in $blockedCodexArguments) {
		Assert-True ($codexLauncher.Contains("'$blockedArgument'")) "Codex launcher does not explicitly block '$blockedArgument': $($profile.CodexLauncher)"
	}
	foreach ($blockedPrefix in $blockedCodexValuePrefixes) {
		Assert-True ($codexLauncher.Contains("'$blockedPrefix'")) "Codex launcher does not block the value form '$blockedPrefix': $($profile.CodexLauncher)"
	}
	$codexArgumentGate = Get-ScriptFunctionBody -Path $profile.CodexLauncher -Name 'Assert-SafeCodexSessionArguments'
	$allowedCodexArguments = @(
		'-C', 'C:\IA', '--cd', 'C:\IA', '--add-dir', 'C:\IA\models',
		'--sandbox', 'read-only', '--ask-for-approval', 'untrusted',
		'--image', 'image.png', '--no-alt-screen', 'normal local prompt'
	)
	& $codexArgumentGate -Values $allowedCodexArguments
	$blockedCodexCases = @($blockedCodexArguments) + @($blockedCodexValuePrefixes | ForEach-Object { "${_}unsafe" }) + @('-cunsafe=true', '-mlocal-fast', '-pcloud')
	foreach ($blockedCase in $blockedCodexCases) {
		$rejected = $false
		try {
			& $codexArgumentGate -Values @($blockedCase)
		}
		catch {
			$rejected = $_.Exception.Message -match 'not allowed'
		}
		Assert-True $rejected "Codex argument gate accepted unsafe input '$blockedCase': $($profile.CodexLauncher)"
	}
	Assert-True ($openCodeLauncher -match 'OPENCODE_CONFIG_CONTENT') "OpenCode launcher lacks a project-config precedence override: $($profile.OpenCodeLauncher)"
	Assert-True ($openCodeLauncher -match '\[string\]\$Model' -and $openCodeLauncher -match 'installed OpenCode local-only provider') "OpenCode launcher does not validate the panel-selected model: $($profile.OpenCodeLauncher)"
	Assert-True ($openCodeLauncher -notmatch '(?i)C:\\Users\\[^\\]+') "OpenCode launcher contains a user-specific path: $($profile.OpenCodeLauncher)"
	Assert-True ($unslothLauncher -match '\[string\]\$Model' -and $unslothLauncher -match 'Get-NetTCPConnection') "Unsloth launcher does not validate and reuse the local Studio: $($profile.UnslothLauncher)"
	Assert-True ($unslothLauncher -match 'Test-UnslothListenerOwner' -and $unslothLauncher -match 'OwningProcess' -and $unslothLauncher -match 'Get-CimInstance Win32_Process') "Unsloth launcher does not authenticate an existing listener through its process ancestry: $($profile.UnslothLauncher)"
	Assert-True ($unslothLauncher -match 'Wait-UnslothStudioReady' -and $unslothLauncher -match 'StartupTimeoutSeconds' -and $unslothLauncher -match 'Invoke-WebRequest') "Unsloth launcher lacks a bounded HTTP readiness check: $($profile.UnslothLauncher)"
	Assert-True ($unslothLauncher -notmatch 'Get-Command\s+unsloth') "Unsloth launcher may resolve an executable outside the expected Studio root: $($profile.UnslothLauncher)"
	$unslothStartIndex = $unslothLauncher.IndexOf('Start-Process -FilePath $unslothPath')
	$unslothReadyIndex = $unslothLauncher.LastIndexOf('Wait-UnslothStudioReady')
	$unslothBrowserIndex = $unslothLauncher.IndexOf('Start-Process -FilePath $studioURL')
	$unslothHFHomeIndex = $unslothLauncher.IndexOf('$env:HF_HOME = $hfHome')
	Assert-True ($unslothStartIndex -gt $unslothHFHomeIndex -and $unslothReadyIndex -gt $unslothStartIndex -and $unslothBrowserIndex -gt $unslothReadyIndex) "Unsloth must set its environment, start Studio, wait for readiness, and only then open the browser: $($profile.UnslothLauncher)"
	Assert-True ($unslothLauncher -notmatch '(?i)8090|local-model|studio\.db|sqlite') "Unsloth launcher contains a legacy endpoint, alias, or private-state integration: $($profile.UnslothLauncher)"
	Assert-True ($unslothLauncher -notmatch '(?i)C:\\Users\\[^\\]+') "Unsloth launcher contains a user-specific path: $($profile.UnslothLauncher)"
}

$catalogPath = Join-Path $RepoRoot 'integrations\codex\codex-model-catalog.json'
$catalog = Get-Content -LiteralPath $catalogPath -Raw -Encoding UTF8 | ConvertFrom-Json
$expectedModelIds = @($manifest.models | Where-Object { $_.state -ne 'retired' -and @($_.deployments) -contains 'canary' } | ForEach-Object { [string]$_.id })
Assert-True ($expectedModelIds -contains $publicModel) "provider.public_model '$publicModel' is not deployed to canary."
$catalogModelIds = @($catalog.models | ForEach-Object { [string]$_.slug })
Assert-True ($catalogModelIds.Count -eq $expectedModelIds.Count) 'Codex model catalog count must match the canary manifest.'
foreach ($modelId in $expectedModelIds) {
	Assert-True ($catalogModelIds -contains $modelId) "Codex model catalog is missing $modelId."
}
foreach ($catalogModel in @($catalog.models)) {
	$applyPatchProperty = $catalogModel.PSObject.Properties['apply_patch_tool_type']
	Assert-True ($null -eq $applyPatchProperty -or $null -eq $applyPatchProperty.Value) "Codex apply_patch must remain disabled for $($catalogModel.slug)."
}

# New-V2ClientCatalogs.ps1 maps function_calling onto the Codex catalog's
# supports_parallel_tool_calls, which is what a harness reads before deciding it
# may issue parallel tool calls against this model. The mapping has to stay
# exact in both directions.
#
# It is asserted here because the flag's meaning was questioned after the
# 2026-08-23 campaign scored gemma4-12b-qat-ud-q4xl at 6/7 on the tool suite
# while its manifest declares function_calling: false. That score was measured
# against llama-server's own endpoint; docs/MODEL_PROMOTION.md defines the flag
# as a forced tool call demonstrated through internal/edge/namespace.go for this
# exact quantization. Different question, so the flag stayed false -- and this
# assertion makes the consequence of ever flipping it visible in a fast test
# rather than in a client's behaviour.
$manifestCapabilityById = @{}
$codexEligibilityById = @{}
foreach ($manifestModel in @($manifest.models)) {
	$manifestCapabilityById[[string]$manifestModel.id] = [bool]$manifestModel.capabilities.function_calling
	$codexEligibilityById[[string]$manifestModel.id] = [bool]($manifestModel.capabilities.responses -and $manifestModel.capabilities.streaming -and $manifestModel.capabilities.function_calling)
}
foreach ($catalogModel in @($catalog.models)) {
	$slug = [string]$catalogModel.slug
	Assert-True ($manifestCapabilityById.ContainsKey($slug)) "Codex catalog advertises '$slug', which is not in the manifest."
	if (-not $manifestCapabilityById.ContainsKey($slug)) { continue }
	$declared = $manifestCapabilityById[$slug]
	$advertised = [bool]$catalogModel.supports_parallel_tool_calls
	Assert-True ($advertised -eq $declared) "Codex supports_parallel_tool_calls=$advertised for '$slug' but the manifest declares function_calling=$declared; regenerate the catalogs with New-V2ClientCatalogs.ps1 instead of editing them."
	Assert-True ([bool]$catalogModel.supported_in_api -eq $codexEligibilityById[$slug]) "Codex eligibility for '$slug' must require qualified Responses, streaming and function calling."
}

foreach ($profile in $profiles) {
	$capabilityGate = Get-ScriptFunctionBody -Path $profile.CodexLauncher -Name 'Assert-CodexModelCapabilities'
	$qualified = [pscustomobject]@{ responses = $true; streaming = $true; function_calling = $true }
	& $capabilityGate -Capabilities $qualified
	foreach ($missing in @('responses', 'streaming', 'function_calling')) {
		foreach ($value in @($false, 'true', $null)) {
			$unqualified = [pscustomobject]@{ responses = $true; streaming = $true; function_calling = $true }
			$unqualified.$missing = $value
			$rejected = $false
			try { & $capabilityGate -Capabilities $unqualified }
			catch { $rejected = $_.Exception.Message -match 'not qualified for Codex' }
			Assert-True $rejected "Codex launcher accepted an unqualified '$missing' capability."
		}
	}
}

foreach ($openCodePath in @(
	(Join-Path $RepoRoot 'integrations\opencode\opencode.local-provider.jsonc'),
	(Join-Path $RepoRoot 'integrations\opencode\opencode.canary-provider.jsonc')
)) {
	$openCodeConfig = Get-Content -LiteralPath $openCodePath -Raw -Encoding UTF8 | ConvertFrom-Json
	$providerName = @($openCodeConfig.provider.PSObject.Properties.Name)[0]
	$openCodeModelIds = @($openCodeConfig.provider.$providerName.models.PSObject.Properties.Name)
	Assert-True ($openCodeModelIds.Count -eq $expectedModelIds.Count) "OpenCode model count must match the manifest: $openCodePath"
	foreach ($modelId in $expectedModelIds) {
		Assert-True ($openCodeModelIds -contains $modelId) "OpenCode config is missing ${modelId}: $openCodePath"
		Assert-True ([bool]$openCodeConfig.provider.$providerName.models.$modelId.tool_call -eq $manifestCapabilityById[$modelId]) "OpenCode tool_call must match manifest qualifications for ${modelId}: $openCodePath"
	}
}

[pscustomobject]@{
    status = 'ok'
    profiles = @($profiles.Provider)
    public_model = $publicModel
    mcp_tools = $readOnlyTools
} | ConvertTo-Json -Depth 4
