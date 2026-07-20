param(
    [string]$CodexHome = "C:\Users\Sitr3n\.codex",
    [string]$MatrixPath = "C:\IA\local-llama\model-test-matrix.json"
)

$ErrorActionPreference = "Stop"

$cachePath = Join-Path $CodexHome "models_cache.json"
$outDir = Join-Path $CodexHome "model-catalogs"
$outPath = Join-Path $outDir "cia-local-plus-openai.json"

New-Item -ItemType Directory -Force -Path $outDir | Out-Null

$cache = Get-Content -Raw -LiteralPath $cachePath | ConvertFrom-Json
$matrix = Get-Content -Raw -LiteralPath $MatrixPath | ConvertFrom-Json
$localSlugs = @("local-model", "local-small-model") + @($matrix.profiles | ForEach-Object { $_.id })
$models = @($cache.models | Where-Object { $localSlugs -notcontains $_.slug })

function New-LocalCodexModel($slug, $name, $desc, $ctx, $priority) {
    $instructions = "You are Codex running through the user's local C:\IA model executor. Be concise and pragmatic. Local model quality and tool-calling can be weaker than hosted OpenAI models; when a task needs high reliability, say so explicitly."
    [pscustomobject]@{
        slug = $slug
        display_name = $name
        description = $desc
        default_reasoning_level = "medium"
        supported_reasoning_levels = @([pscustomobject]@{ effort = "medium"; description = "Local model default" })
        shell_type = "shell_command"
        visibility = "list"
        supported_in_api = $true
        priority = $priority
        additional_speed_tiers = @()
        service_tiers = @()
        availability_nux = $null
        upgrade = $null
        base_instructions = $instructions
        model_messages = [pscustomobject]@{
            instructions_template = $instructions
            instructions_variables = $null
            approvals = $null
            auto_review = $null
            permissions = $null
        }
        include_skills_usage_instructions = $false
        default_reasoning_summary = "none"
        support_verbosity = $true
        default_verbosity = "low"
        apply_patch_tool_type = "freeform"
        web_search_tool_type = "text_and_image"
        truncation_policy = [pscustomobject]@{ mode = "tokens"; limit = 10000 }
        supports_parallel_tool_calls = $true
        supports_image_detail_original = $false
        context_window = [int]$ctx
        max_context_window = [int]$ctx
        comp_hash = "local-cia"
        effective_context_window_percent = 95
        experimental_supported_tools = @()
        input_modalities = @("text")
        supports_search_tool = $false
        use_responses_lite = $false
    }
}

$localModels = @()
$localModels += New-LocalCodexModel "local-model" "Local C:\IA Current Profile" "Uses the selected C:\IA local executor through http://127.0.0.1:8090/v1." 131072 80
$localModels += New-LocalCodexModel "local-small-model" "Local C:\IA Qwen 4B Small" "Uses the small parallel C:\IA executor on demand through the panel proxy." 65536 81

$priority = 82
foreach ($profile in $matrix.profiles) {
    $localModels += New-LocalCodexModel $profile.id ("Local C:\IA " + $profile.display_name) ("Switches the C:\IA panel to profile " + $profile.id + " before generation.") $profile.context_size $priority
    $priority += 1
}

$out = [pscustomobject]@{ models = @($models + $localModels) }
$json = $out | ConvertTo-Json -Depth 100
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText($outPath, $json, $utf8NoBom)

Get-Item -LiteralPath $outPath
