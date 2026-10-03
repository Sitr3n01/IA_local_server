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
        [Parameter(Mandatory = $true)][bool]$Condition,
        [Parameter(Mandatory = $true)][string]$Message
    )
    if (-not $Condition) { throw $Message }
}

function Invoke-InstallerJson {
    param([Parameter(Mandatory = $true)][hashtable]$Arguments)

    $output = & $script:installer @Arguments | Out-String
    return $output | ConvertFrom-Json
}

$installer = Join-Path $RepoRoot 'scripts\v2\Install-V2McpInferenceIntegrations.ps1'
Assert-True (Test-Path -LiteralPath $installer -PathType Leaf) "Missing installer: $installer"

$testRoot = Join-Path $RepoRoot ('scripts\v2\.test-mcp-inference-' + [Guid]::NewGuid().ToString('N'))
$resolvedTestRoot = [IO.Path]::GetFullPath($testRoot).TrimEnd('\')
[IO.Directory]::CreateDirectory($resolvedTestRoot) | Out-Null
try {
    $binary = Join-Path $resolvedTestRoot 'cia-mcp-inference.exe'
    [IO.File]::WriteAllBytes($binary, [Text.Encoding]::ASCII.GetBytes('test binary'))
    $codex = Join-Path $resolvedTestRoot 'codex\config.toml'
    $desktop = Join-Path $resolvedTestRoot 'claude\claude_desktop_config.json'
    $claudeCode = Join-Path $resolvedTestRoot '.claude.json'
    $openCode = Join-Path $resolvedTestRoot 'opencode\opencode.json'
    $backups = Join-Path $resolvedTestRoot 'backups'
    foreach ($directory in @((Split-Path -Parent $codex), (Split-Path -Parent $desktop), (Split-Path -Parent $openCode))) {
        [IO.Directory]::CreateDirectory($directory) | Out-Null
    }

    @'
model = "sota-cloud-model"
model_provider = "cloud-provider"

[mcp_servers.keep]
command = "keep.exe"
'@ | Set-Content -LiteralPath $codex -Encoding UTF8
    @{
        theme = 'dark'
        mcpServers = @{ keep = @{ command = 'keep.exe'; args = @() } }
    } | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $desktop -Encoding UTF8
    @{
        accountState = @{ privateMarker = 'private-sentinel-never-plaintext-backup' }
        projects = @{ 'C:\work' = @{ allowedTools = @('Read') } }
        mcpServers = @{ keep = @{ command = 'keep.exe'; args = @() } }
    } | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $claudeCode -Encoding UTF8
    @{
        '$schema' = 'https://opencode.ai/config.json'
        model = 'anthropic/sota-cloud-model'
        provider = @{ anthropic = @{ options = @{ apiKey = '{env:ANTHROPIC_API_KEY}' } } }
        mcp = @{ keep = @{ type = 'local'; command = @('keep.exe') } }
        permission = @{ edit = 'allow' }
    } | ConvertTo-Json -Depth 20 | Set-Content -LiteralPath $openCode -Encoding UTF8

    # A fixture manifest, so the pin is resolved and validated against a known
    # roster rather than whatever this machine happens to have installed.
    $manifestFixture = Join-Path $resolvedTestRoot 'models.yaml'
    @{
        provider = @{ public_model = 'pinned-public-model' }
        models = @(
            @{ id = 'pinned-public-model'; state = 'candidate' },
            @{ id = 'pinned-other-model'; state = 'candidate' },
            @{ id = 'retired-model'; state = 'retired' }
        )
    } | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $manifestFixture -Encoding UTF8

    $common = @{
        BinaryPath = $binary
        DataUrl = 'http://127.0.0.1:18090'
        Model = 'pinned-other-model'
        ManifestPath = $manifestFixture
        CodexConfigPath = $codex
        ClaudeDesktopConfigPath = $desktop
        ClaudeCodeConfigPath = $claudeCode
        OpenCodeConfigPath = $openCode
        BackupRoot = $backups
        Clients = @('Codex', 'ClaudeDesktop', 'ClaudeCode', 'OpenCode')
    }
    $preview = Invoke-InstallerJson -Arguments $common
    Assert-True ($preview.mode -eq 'preview') 'Installer did not return preview mode.'
    Assert-True ($preview.provider_or_primary_model_touched -eq $false) 'Preview claims provider/model mutation.'
    Assert-True ($preview.secrets_written -eq $false) 'Preview claims secret persistence.'
    Assert-True (@($preview.items | Where-Object { $_.action -eq 'update' }).Count -eq 4) 'Expected four update actions.'

    $applyArguments = $common.Clone()
    $applyArguments['Apply'] = $true
    $applyArguments['ExpectedPlanSha256'] = $preview.plan_sha256
    $applied = Invoke-InstallerJson -Arguments $applyArguments
    Assert-True ($applied.mode -eq 'apply') 'Installer did not return apply mode.'
    Assert-True (@($applied.backup_files).Count -eq 4) 'Expected one encrypted backup per updated config.'

    $codexText = Get-Content -LiteralPath $codex -Raw -Encoding UTF8
    Assert-True ($codexText -match '(?m)^model = "sota-cloud-model"\r?$') 'Codex primary model changed.'
    Assert-True ($codexText -match '(?m)^model_provider = "cloud-provider"\r?$') 'Codex provider changed.'
    Assert-True ($codexText -match '(?m)^\[mcp_servers\.keep\]\r?$') 'Existing Codex MCP was lost.'
    $managedCodexHeaders = @([regex]::Matches($codexText, '(?m)^\[mcp_servers\.cia-local-inference\]\r?$'))
    Assert-True ($managedCodexHeaders.Count -eq 1) "Managed Codex MCP is missing or duplicated (found $($managedCodexHeaders.Count))."
    Assert-True ($codexText -match '(?m)^enabled_tools = \["local_ai_delegate"\]\r?$') 'Codex tool allowlist is wrong.'
    Assert-True ($codexText -match '(?m)^tool_timeout_sec = 300\r?$') 'Codex tool timeout is wrong.'
    Assert-True ($codexText -match '(?m)^approval_mode = "prompt"\r?$') 'Codex tool approval is not prompt.'
    Assert-True ($codexText -match "(?m)^CIA_MCP_INFERENCE_DATA_URL = 'http://127\.0\.0\.1:18090'\r?$") 'Codex data URL is wrong.'
    Assert-True ($codexText -match '(?m)^CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS = "4096"\r?$') 'Codex output limit is wrong.'
    Assert-True ($codexText -notmatch '(?i)token\s*=|api[_-]?key\s*=') 'Managed Codex block contains a secret field.'

    $desktopJson = Get-Content -LiteralPath $desktop -Raw -Encoding UTF8 | ConvertFrom-Json
    Assert-True ($desktopJson.theme -eq 'dark') 'Claude Desktop unrelated setting changed.'
    Assert-True ($desktopJson.mcpServers.keep.command -eq 'keep.exe') 'Claude Desktop existing MCP was lost.'
    Assert-True ($desktopJson.mcpServers.'cia-local-inference'.env.CIA_MCP_INFERENCE_MODEL -eq 'pinned-other-model') 'Claude Desktop managed MCP is wrong.'
    Assert-True ($desktopJson.mcpServers.'cia-local-inference'.env.CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS -eq '4096') 'Claude Desktop output limit is wrong.'
    Assert-True (@($desktopJson.mcpServers.'cia-local-inference'.args).Count -eq 0) 'Claude Desktop MCP args must be empty.'

    $claudeJson = Get-Content -LiteralPath $claudeCode -Raw -Encoding UTF8 | ConvertFrom-Json
    Assert-True ($claudeJson.accountState.privateMarker -eq 'private-sentinel-never-plaintext-backup') 'Claude Code unrelated state changed.'
    Assert-True ($claudeJson.projects.'C:\work'.allowedTools[0] -eq 'Read') 'Claude Code nested project state changed.'
    Assert-True ($claudeJson.mcpServers.keep.command -eq 'keep.exe') 'Claude Code existing MCP was lost.'
    Assert-True ($claudeJson.mcpServers.'cia-local-inference'.env.CIA_MCP_INFERENCE_DATA_URL -eq 'http://127.0.0.1:18090') 'Claude Code managed MCP is wrong.'
    Assert-True ($claudeJson.mcpServers.'cia-local-inference'.env.CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS -eq '4096') 'Claude Code output limit is wrong.'

    $openCodeJson = Get-Content -LiteralPath $openCode -Raw -Encoding UTF8 | ConvertFrom-Json
    Assert-True ($openCodeJson.model -eq 'anthropic/sota-cloud-model') 'OpenCode primary model changed.'
    Assert-True ($null -ne $openCodeJson.provider.anthropic) 'OpenCode provider was lost.'
    Assert-True ($openCodeJson.mcp.keep.command[0] -eq 'keep.exe') 'OpenCode existing MCP was lost.'
    Assert-True ($openCodeJson.mcp.'cia-local-inference'.type -eq 'local') 'OpenCode managed MCP type is wrong.'
    Assert-True ($openCodeJson.permission.edit -eq 'allow') 'OpenCode existing permission changed.'
    Assert-True ($openCodeJson.permission.'cia-local-inference_*' -eq 'ask') 'OpenCode managed tool approval is not ask.'

    Add-Type -AssemblyName System.Security
    $backupFiles = @(Get-ChildItem -LiteralPath $backups -Filter '*.dpapi' -File)
    Assert-True ($backupFiles.Count -eq 4) 'Persistent backup count is wrong.'
    foreach ($backup in $backupFiles) {
        $encryptedText = [Text.Encoding]::UTF8.GetString([IO.File]::ReadAllBytes($backup.FullName))
        Assert-True (-not $encryptedText.Contains('private-sentinel-never-plaintext-backup')) 'A persistent backup contains plaintext private material.'
        $plain = [Security.Cryptography.ProtectedData]::Unprotect(
            [IO.File]::ReadAllBytes($backup.FullName),
            $null,
            [Security.Cryptography.DataProtectionScope]::CurrentUser
        )
        Assert-True ($plain.Length -gt 0) 'A DPAPI backup cannot be decrypted by the current user.'
    }

    $idempotent = Invoke-InstallerJson -Arguments $common
    Assert-True (@($idempotent.items | Where-Object { $_.action -eq 'unchanged' }).Count -eq 4) 'Second preview is not idempotent.'

    $driftPreview = $idempotent
    Add-Content -LiteralPath $desktop -Value "`r`n" -Encoding UTF8
    $driftArguments = $common.Clone()
    $driftArguments['Apply'] = $true
    $driftArguments['ExpectedPlanSha256'] = $driftPreview.plan_sha256
    $driftRejected = $false
    try {
        Invoke-InstallerJson -Arguments $driftArguments | Out-Null
    }
    catch {
        $driftRejected = $_.Exception.Message -match 'plan does not match'
    }
    Assert-True $driftRejected 'Apply accepted config drift after preview.'

    foreach ($unsafeUrl in @(
        'http://user@127.0.0.1:18090',
        'http://127.0.0.1:18090?route=external',
        'http://127.0.0.1:18090#fragment',
        'http://127.0.0.1:18090/v1',
        'http://127.0.0.1'
    )) {
        $unsafeArguments = $common.Clone()
        $unsafeArguments['DataUrl'] = $unsafeUrl
        $rejected = $false
        $unsafeError = $null
        try {
            Invoke-InstallerJson -Arguments $unsafeArguments | Out-Null
        }
        catch {
            $unsafeError = $_.Exception.Message
            $rejected = $_.Exception.Message -match 'HTTP loopback origin'
        }
        Assert-True $rejected "Installer accepted unsafe DataUrl '$unsafeUrl'. Error: $unsafeError"
    }

    $invalidJson = Join-Path $resolvedTestRoot 'invalid-desktop.json'
    [IO.File]::WriteAllText($invalidJson, '{not-json', [Text.UTF8Encoding]::new($false))
    $invalidArguments = $common.Clone()
    $invalidArguments['ClaudeDesktopConfigPath'] = $invalidJson
    $invalidArguments['Clients'] = @('ClaudeDesktop')
    $invalidRejected = $false
    try {
        Invoke-InstallerJson -Arguments $invalidArguments | Out-Null
    }
    catch {
        $invalidRejected = $_.Exception.Message -match 'not valid JSON'
    }
    Assert-True $invalidRejected 'Installer accepted an invalid existing JSON config.'

    # With no -Model the pin is the manifest's public model.
    $publicArguments = $common.Clone()
    $publicArguments.Remove('Model')
    $publicPreview = Invoke-InstallerJson -Arguments $publicArguments
    Assert-True ($publicPreview.model -eq 'pinned-public-model') "Installer did not pin provider.public_model (got '$($publicPreview.model)')."
    Assert-True ($publicPreview.model_source -eq 'provider.public_model') 'Installer did not report where the pin came from.'

    # A model the manifest does not list as active would be refused by the edge
    # on every call, so the installer refuses to pin it.
    foreach ($inactiveModel in @('retired-model', 'removed-model')) {
        $inactiveArguments = $common.Clone()
        $inactiveArguments['Model'] = $inactiveModel
        $inactiveRejected = $false
        try {
            Invoke-InstallerJson -Arguments $inactiveArguments | Out-Null
        }
        catch {
            $inactiveRejected = $_.Exception.Message -match 'not an active model'
        }
        Assert-True $inactiveRejected "Installer pinned '$inactiveModel', which is not an active model in the manifest."
    }

    # With neither -Model nor a readable manifest there is nothing to pin.
    $unresolvedArguments = $common.Clone()
    $unresolvedArguments.Remove('Model')
    $unresolvedArguments['ManifestPath'] = Join-Path $resolvedTestRoot 'missing-models.yaml'
    $unresolvedRejected = $false
    try {
        Invoke-InstallerJson -Arguments $unresolvedArguments | Out-Null
    }
    catch {
        $unresolvedRejected = $_.Exception.Message -match 'Pass -Model explicitly'
    }
    Assert-True $unresolvedRejected 'Installer invented a model with no -Model and no manifest.'

    # What an installed canary client actually looks like: Codex rewrote its
    # config and dropped the END marker while BEGIN survived, a table was
    # written after the managed block, and the registration carries tuned
    # limits that a re-pin must restate rather than reset.
    $rewrittenRoot = Join-Path $resolvedTestRoot 'rewritten'
    [IO.Directory]::CreateDirectory($rewrittenRoot) | Out-Null
    $rewrittenCodex = Join-Path $rewrittenRoot 'config.toml'
    @'
model = "sota-cloud-model"

# BEGIN CIA LOCAL INFERENCE MCP (managed)
[mcp_servers.cia-local-inference]
command = 'C:\old\cia-mcp-inference.exe'
enabled = true

[mcp_servers.cia-local-inference.env]
CIA_MCP_INFERENCE_MODEL = 'removed-model'
CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS = "65536"

[mcp_servers.cia-local-inference.tools.local_ai_delegate]
approval_mode = "prompt"

[mcp_servers.after]
command = "after.exe"
'@ | Set-Content -LiteralPath $rewrittenCodex -Encoding UTF8
    $rewrittenClaude = Join-Path $rewrittenRoot '.claude.json'
    @{
        accountState = @{ privateMarker = 'private-sentinel-never-plaintext-backup' }
        mcpServers = @{ 'cia-local-inference' = @{ command = 'C:\old\cia-mcp-inference.exe'; env = @{ CIA_MCP_INFERENCE_MODEL = 'removed-model' } } }
    } | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $rewrittenClaude -Encoding UTF8

    $tunedArguments = $common.Clone()
    $tunedArguments['CodexConfigPath'] = $rewrittenCodex
    $tunedArguments['ClaudeCodeConfigPath'] = $rewrittenClaude
    $tunedArguments['Clients'] = @('Codex', 'ClaudeCode')
    $tunedArguments['MaxOutputTokens'] = 65536
    $tunedArguments['Timeout'] = '30m'
    $tunedArguments['Temperature'] = '0.2'
    $tunedPreview = Invoke-InstallerJson -Arguments $tunedArguments
    Assert-True (@($tunedPreview.items | Where-Object { $_.action -eq 'update' }).Count -eq 2) 'Expected both rewritten configs to be updated.'
    $tunedApply = $tunedArguments.Clone()
    $tunedApply['Apply'] = $true
    $tunedApply['ExpectedPlanSha256'] = $tunedPreview.plan_sha256
    Invoke-InstallerJson -Arguments $tunedApply | Out-Null

    $rewrittenText = Get-Content -LiteralPath $rewrittenCodex -Raw -Encoding UTF8
    Assert-True (@([regex]::Matches($rewrittenText, '(?m)^\[mcp_servers\.cia-local-inference\]\r?$')).Count -eq 1) 'The orphan-marker Codex config did not end with exactly one managed table.'
    Assert-True (@([regex]::Matches($rewrittenText, '(?m)^# BEGIN CIA LOCAL INFERENCE MCP \(managed\)\r?$')).Count -eq 1) 'The BEGIN marker was not restored exactly once.'
    Assert-True (@([regex]::Matches($rewrittenText, '(?m)^# END CIA LOCAL INFERENCE MCP \(managed\)\r?$')).Count -eq 1) 'The END marker was not restored exactly once.'
    Assert-True ($rewrittenText -match '(?m)^\[mcp_servers\.after\]\r?$' -and $rewrittenText -match 'after\.exe') 'A table written after the managed block was lost.'
    Assert-True ($rewrittenText -match '(?m)^model = "sota-cloud-model"\r?$') 'The Codex primary model changed.'
    Assert-True ($rewrittenText -notmatch 'removed-model') 'The stale pin survived the re-pin.'
    Assert-True ($rewrittenText -match "(?m)^CIA_MCP_INFERENCE_MODEL = 'pinned-other-model'\r?$") 'The re-pin did not write the new model.'
    Assert-True ($rewrittenText -match '(?m)^CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS = "65536"\r?$') 'The output limit was not restated.'
    Assert-True ($rewrittenText -match '(?m)^CIA_MCP_INFERENCE_TIMEOUT = "30m"\r?$') 'The timeout was not restated.'
    Assert-True ($rewrittenText -match '(?m)^CIA_MCP_INFERENCE_TEMPERATURE = "0\.2"\r?$') 'The temperature was not restated.'

    $rewrittenClaudeJson = Get-Content -LiteralPath $rewrittenClaude -Raw -Encoding UTF8 | ConvertFrom-Json
    $rewrittenEnv = $rewrittenClaudeJson.mcpServers.'cia-local-inference'.env
    Assert-True ($rewrittenClaudeJson.accountState.privateMarker -eq 'private-sentinel-never-plaintext-backup') 'Claude Code unrelated state changed during the re-pin.'
    Assert-True ($rewrittenEnv.CIA_MCP_INFERENCE_MODEL -eq 'pinned-other-model') 'Claude Code was not re-pinned.'
    Assert-True ($rewrittenEnv.CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS -eq '65536' -and $rewrittenEnv.CIA_MCP_INFERENCE_TIMEOUT -eq '30m' -and $rewrittenEnv.CIA_MCP_INFERENCE_TEMPERATURE -eq '0.2') 'Claude Code limits were not restated.'

    foreach ($badTuning in @(@{ Timeout = '41m' }, @{ Timeout = '30' }, @{ Temperature = '2.5' }, @{ Temperature = '0,2' })) {
        $badArguments = $common.Clone()
        foreach ($key in $badTuning.Keys) { $badArguments[$key] = $badTuning[$key] }
        $badRejected = $false
        try {
            Invoke-InstallerJson -Arguments $badArguments | Out-Null
        }
        catch {
            $badRejected = $_.Exception.Message -match 'Timeout must|Temperature must'
        }
        Assert-True $badRejected "Installer accepted an invalid delegate limit: $(($badTuning.GetEnumerator() | ForEach-Object { "$($_.Key)=$($_.Value)" }) -join ', ')."
    }

    [pscustomobject]@{
        status = 'ok'
        clients = @('Codex', 'ClaudeDesktop', 'ClaudeCode', 'OpenCode')
        server = 'cia-local-inference'
        tool = 'local_ai_delegate'
        backups = 'DPAPI CurrentUser'
        provider_and_model_preserved = $true
    } | ConvertTo-Json -Depth 4
}
finally {
    $full = [IO.Path]::GetFullPath($resolvedTestRoot).TrimEnd('\')
    $scriptsRoot = [IO.Path]::GetFullPath((Join-Path $RepoRoot 'scripts\v2')).TrimEnd('\')
    if ($full.StartsWith($scriptsRoot + '\', [StringComparison]::OrdinalIgnoreCase) -and
        (Split-Path -Leaf $full).StartsWith('.test-mcp-inference-', [StringComparison]::Ordinal)) {
        Remove-Item -LiteralPath $full -Recurse -Force -ErrorAction SilentlyContinue
    }
}
