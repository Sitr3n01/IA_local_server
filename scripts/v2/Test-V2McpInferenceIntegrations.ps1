[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent (Split-Path -Parent $PSScriptRoot))
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

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

    $common = @{
        BinaryPath = $binary
        DataUrl = 'http://127.0.0.1:18090'
        Model = 'local-coding'
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
    Assert-True ($codexText -match '(?m)^model = "sota-cloud-model"$') 'Codex primary model changed.'
    Assert-True ($codexText -match '(?m)^model_provider = "cloud-provider"$') 'Codex provider changed.'
    Assert-True ($codexText -match '(?m)^\[mcp_servers\.keep\]$') 'Existing Codex MCP was lost.'
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
    Assert-True ($desktopJson.mcpServers.'cia-local-inference'.env.CIA_MCP_INFERENCE_MODEL -eq 'local-coding') 'Claude Desktop managed MCP is wrong.'
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
