param(
    [string]$RepoRoot = (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)),
    [ValidateSet('Canary', 'Final')]
    [string]$Environment = 'Canary'
)

$ErrorActionPreference = 'Stop'

$manifestPath = Join-Path $RepoRoot 'config\models.yaml'
$manifest = Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
$deployment = $Environment.ToLowerInvariant()
$models = @($manifest.models | Where-Object {
    $_.state -ne 'retired' -and @($_.deployments) -contains $deployment
})
if ($models.Count -eq 0) {
    throw "No models are available for deployment '$deployment'."
}

function Write-Utf8Json {
    param(
        [Parameter(Mandatory = $true)]$Value,
        [Parameter(Mandatory = $true)][string]$Path
    )
    $json = $Value | ConvertTo-Json -Depth 100
    $utf8NoBom = [System.Text.UTF8Encoding]::new($false)
    [System.IO.File]::WriteAllText($Path, $json + [Environment]::NewLine, $utf8NoBom)
}

$instructions = 'You are Codex using an explicitly selected local model. Keep tool arguments exact, use shell_command for file edits and verification, and report capability limitations instead of inventing a cloud fallback. The apply_patch custom tool is intentionally unavailable because this local runtime accepts standard function tools only.'
$priority = 1
$codexModels = foreach ($model in $models) {
    [ordered]@{
        slug = [string]$model.id
        display_name = "CIA Local - $($model.display_name)"
        description = 'Explicit local-only model served from this machine. No cloud fallback.'
        default_reasoning_level = 'medium'
        supported_reasoning_levels = @([ordered]@{ effort = 'medium'; description = 'Local model default' })
        shell_type = 'shell_command'
        visibility = 'list'
        supported_in_api = $true
        priority = $priority++
        additional_speed_tiers = @()
        service_tiers = @()
        availability_nux = $null
        upgrade = $null
        base_instructions = $instructions
        model_messages = [ordered]@{
            instructions_template = $instructions
            instructions_variables = $null
            approvals = $null
            auto_review = $null
            permissions = $null
        }
        include_skills_usage_instructions = $false
        default_reasoning_summary = 'none'
        support_verbosity = $true
        default_verbosity = 'low'
        web_search_tool_type = 'text_and_image'
        truncation_policy = [ordered]@{ mode = 'tokens'; limit = 10000 }
        supports_parallel_tool_calls = [bool]$model.capabilities.function_calling
        supports_image_detail_original = $false
        context_window = [int]$model.context_tokens
        max_context_window = [int]$model.context_tokens
        comp_hash = "cia-local-v2-$($model.id)"
        effective_context_window_percent = 85
        experimental_supported_tools = @()
        input_modalities = @('text')
        supports_search_tool = $false
        use_responses_lite = $false
    }
}

$codexPath = Join-Path $RepoRoot 'integrations\codex\codex-model-catalog.json'
Write-Utf8Json -Value ([ordered]@{ models = @($codexModels) }) -Path $codexPath

function New-OpenCodeConfig {
    param(
        [Parameter(Mandatory = $true)][string]$Provider,
        [Parameter(Mandatory = $true)][string]$ProviderName,
        [Parameter(Mandatory = $true)][int]$DataPort,
        [Parameter(Mandatory = $true)][int]$ControlPort
    )
    $modelMap = [ordered]@{}
    foreach ($model in $models) {
        $modelMap[[string]$model.id] = [ordered]@{
            name = [string]$model.display_name
            limit = [ordered]@{
                context = [int]$model.context_tokens
                output = [int]$model.max_output_tokens
            }
        }
    }
    [ordered]@{
        '$schema' = 'https://opencode.ai/config.json'
        model = "$Provider/$($manifest.provider.public_model)"
        enabled_providers = @($Provider)
        share = 'disabled'
        provider = [ordered]@{
            $Provider = [ordered]@{
                npm = '@ai-sdk/openai-compatible'
                name = $ProviderName
                options = [ordered]@{
                    baseURL = "http://127.0.0.1:$DataPort/v1"
                    apiKey = '{env:CIA_LOCAL_API_KEY}'
                }
                models = $modelMap
            }
        }
        mcp = [ordered]@{
            $Provider = [ordered]@{
                type = 'local'
                command = @('C:\IA\local-ai-v2\bin\cia-mcp.exe')
                enabled = $true
                timeout = 10000
                environment = [ordered]@{
                    CIA_CONTROL_URL = "http://127.0.0.1:$ControlPort"
                }
            }
        }
    }
}

$localConfig = New-OpenCodeConfig -Provider 'cia-local' -ProviderName 'CIA Local AI' -DataPort 8090 -ControlPort 8091
$canaryConfig = New-OpenCodeConfig -Provider 'cia-local-canary' -ProviderName 'CIA Local AI (canary)' -DataPort 18090 -ControlPort 18091
Write-Utf8Json -Value $localConfig -Path (Join-Path $RepoRoot 'integrations\opencode\opencode.local-provider.jsonc')
Write-Utf8Json -Value $canaryConfig -Path (Join-Path $RepoRoot 'integrations\opencode\opencode.canary-provider.jsonc')

[pscustomobject]@{
    Environment = $Environment
    Models = @($models.id)
    CodexCatalog = $codexPath
    OpenCodeLocal = Join-Path $RepoRoot 'integrations\opencode\opencode.local-provider.jsonc'
    OpenCodeCanary = Join-Path $RepoRoot 'integrations\opencode\opencode.canary-provider.jsonc'
}
