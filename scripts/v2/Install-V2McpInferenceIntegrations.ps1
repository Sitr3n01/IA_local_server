[CmdletBinding()]
param(
    [string]$BinaryPath = 'C:\IA\local-ai-v2\bin\cia-mcp-inference.exe',
    [string]$DataUrl = 'http://127.0.0.1:18090',
    [string]$Model = 'local-coding',
    [string]$CodexConfigPath = (Join-Path $env:USERPROFILE '.codex\config.toml'),
    [string]$ClaudeDesktopConfigPath = (Join-Path $env:APPDATA 'Claude\claude_desktop_config.json'),
    [string]$ClaudeCodeConfigPath = (Join-Path $env:USERPROFILE '.claude.json'),
    [string]$OpenCodeConfigPath = (Join-Path $env:USERPROFILE '.config\opencode\opencode.json'),
    [string]$BackupRoot = 'C:\IA\local-ai-v2\state\integration-backups',
    [ValidateSet('Codex', 'ClaudeDesktop', 'ClaudeCode', 'OpenCode')]
    [string[]]$Clients = @(),
    [string]$ExpectedPlanSha256,
    [switch]$Apply
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$serverName = 'cia-local-inference'
$toolName = 'local_ai_delegate'
$utf8Strict = [Text.UTF8Encoding]::new($false, $true)
$utf8NoBom = [Text.UTF8Encoding]::new($false)

function Get-BytesSha256 {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)

    $sha256 = [Security.Cryptography.SHA256]::Create()
    try {
        return [BitConverter]::ToString($sha256.ComputeHash($Bytes)).Replace('-', '')
    }
    finally {
        $sha256.Dispose()
    }
}

function Read-Utf8File {
    param([Parameter(Mandatory = $true)][string]$Path)

    $bytes = [IO.File]::ReadAllBytes($Path)
    $hasBom = $bytes.Length -ge 3 -and $bytes[0] -eq 0xEF -and $bytes[1] -eq 0xBB -and $bytes[2] -eq 0xBF
    $offset = if ($hasBom) { 3 } else { 0 }
    try {
        $text = $utf8Strict.GetString($bytes, $offset, $bytes.Length - $offset)
    }
    catch {
        throw "Configuration is not valid UTF-8: $Path"
    }

    return [pscustomobject]@{
        Bytes = $bytes
        Text = $text
        HasBom = $hasBom
        NewLine = if ($text.Contains("`r`n")) { "`r`n" } else { "`n" }
    }
}

function ConvertTo-Utf8Bytes {
    param(
        [Parameter(Mandatory = $true)][AllowEmptyString()][string]$Text,
        [bool]$WithBom = $false
    )

    $body = $utf8NoBom.GetBytes($Text)
    if (-not $WithBom) {
        return $body
    }

    $result = [byte[]]::new($body.Length + 3)
    $result[0] = 0xEF
    $result[1] = 0xBB
    $result[2] = 0xBF
    [Array]::Copy($body, 0, $result, 3, $body.Length)
    return $result
}

function ConvertTo-TomlLiteral {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string]$Value)

    if ($Value.IndexOfAny([char[]]@("'", "`r", "`n")) -ge 0 -or $Value -match '[\x00-\x1F\x7F]') {
        throw 'A managed TOML value contains a character that cannot be represented safely.'
    }
    return "'$Value'"
}

function Remove-ManagedCodexTables {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string]$Text)

    $begin = '# BEGIN CIA LOCAL INFERENCE MCP (managed)'
    $end = '# END CIA LOCAL INFERENCE MCP (managed)'
    $beginMatches = @([regex]::Matches($Text, '(?m)^' + [regex]::Escape($begin) + '[ \t]*\r?$'))
    $endMatches = @([regex]::Matches($Text, '(?m)^' + [regex]::Escape($end) + '[ \t]*\r?$'))
    if ($beginMatches.Count -ne $endMatches.Count -or $beginMatches.Count -gt 1) {
        throw 'Codex config contains malformed or duplicate CIA managed-block markers.'
    }
    if ($beginMatches.Count -eq 1) {
        if ($endMatches[0].Index -lt $beginMatches[0].Index) {
            throw 'Codex config contains CIA managed-block markers in the wrong order.'
        }
        $blockStart = $beginMatches[0].Index
        $blockEnd = $endMatches[0].Index + $endMatches[0].Length
        if ($blockEnd -lt $Text.Length -and $Text[$blockEnd] -eq "`n") { $blockEnd++ }
        elseif ($blockEnd + 1 -lt $Text.Length -and $Text.Substring($blockEnd, 2) -eq "`r`n") { $blockEnd += 2 }
        $Text = $Text.Remove($blockStart, $blockEnd - $blockStart)
    }

    # Support an older/unmarked installation of the same exact server name.
    # Each table body ends at the next TOML table header. Other MCP entries and
    # every non-managed setting remain byte-for-byte unchanged.
    $serverKey = '(?:cia-local-inference|"cia-local-inference"|''cia-local-inference'')'
    $toolKey = '(?:local_ai_delegate|"local_ai_delegate"|''local_ai_delegate'')'
    $managedName = '^mcp_servers\.' + $serverKey + '(?:\.env|\.tools\.' + $toolKey + ')?$'

    while ($true) {
        $headers = @([regex]::Matches($Text, '(?m)^[ \t]*\[(?<name>[^\]\r\n]+)\][ \t]*(?:#.*)?\r?$'))
        $managed = @($headers | Where-Object { $_.Groups['name'].Value.Trim() -match $managedName })
        if ($managed.Count -eq 0) { break }

        $target = $managed[$managed.Count - 1]
        $next = @($headers | Where-Object { $_.Index -gt $target.Index } | Select-Object -First 1)
        $endIndex = if ($next.Count -eq 1) { $next[0].Index } else { $Text.Length }
        $Text = $Text.Remove($target.Index, $endIndex - $target.Index)
    }

    return $Text.TrimEnd("`r", "`n", ' ', "`t")
}

function Merge-CodexConfig {
    param(
        [Parameter(Mandatory = $true)][AllowEmptyString()][string]$Current,
        [Parameter(Mandatory = $true)][string]$NewLine
    )

    $clean = Remove-ManagedCodexTables -Text $Current
    $binary = ConvertTo-TomlLiteral -Value $script:resolvedBinaryPath
    $url = ConvertTo-TomlLiteral -Value $script:normalizedDataUrl
    $modelValue = ConvertTo-TomlLiteral -Value $script:normalizedModel
    $lines = @(
        '# BEGIN CIA LOCAL INFERENCE MCP (managed)',
        '[mcp_servers.cia-local-inference]',
        "command = $binary",
        'enabled = true',
        'required = false',
        'startup_timeout_sec = 10',
        'tool_timeout_sec = 300',
        'enabled_tools = ["local_ai_delegate"]',
        '',
        '[mcp_servers.cia-local-inference.env]',
        "CIA_MCP_INFERENCE_DATA_URL = $url",
        "CIA_MCP_INFERENCE_MODEL = $modelValue",
        'CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS = "4096"',
        '',
        '[mcp_servers.cia-local-inference.tools.local_ai_delegate]',
        'approval_mode = "prompt"',
        '# END CIA LOCAL INFERENCE MCP (managed)'
    )
    $block = $lines -join $NewLine
    $merged = if ([string]::IsNullOrEmpty($clean)) {
        $block + $NewLine
    }
    else {
        $clean + $NewLine + $NewLine + $block + $NewLine
    }

    $expectedHeaders = @(
        'mcp_servers.cia-local-inference',
        'mcp_servers.cia-local-inference.env',
        'mcp_servers.cia-local-inference.tools.local_ai_delegate'
    )
    $headers = @([regex]::Matches($merged, '(?m)^[ \t]*\[(?<name>[^\]\r\n]+)\][ \t]*(?:#.*)?\r?$'))
    foreach ($expectedHeader in $expectedHeaders) {
        $count = @($headers | Where-Object { $_.Groups['name'].Value.Trim() -ceq $expectedHeader }).Count
        if ($count -ne 1) {
            throw "Managed Codex TOML validation failed for table '$expectedHeader'."
        }
    }
    foreach ($requiredLine in @(
        'enabled_tools = ["local_ai_delegate"]',
        'CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS = "4096"',
        'approval_mode = "prompt"'
    )) {
        if ($merged.IndexOf($requiredLine, [StringComparison]::Ordinal) -lt 0) {
            throw "Managed Codex TOML validation failed for '$requiredLine'."
        }
    }
    return $merged
}

function Assert-JsonObject {
    param(
        [Parameter(Mandatory = $true)][AllowNull()][object]$Value,
        [Parameter(Mandatory = $true)][string]$Label
    )

    if ($null -eq $Value -or -not ($Value -is [Management.Automation.PSCustomObject])) {
        throw "$Label must be a JSON object."
    }
}

function Set-JsonProperty {
    param(
        [Parameter(Mandatory = $true)][object]$Object,
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][AllowNull()][object]$Value
    )

    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) {
        $Object | Add-Member -MemberType NoteProperty -Name $Name -Value $Value
    }
    else {
        $property.Value = $Value
    }
}

function Read-JsonRoot {
    param(
        [Parameter(Mandatory = $true)][AllowEmptyString()][string]$Text,
        [Parameter(Mandatory = $true)][string]$Label
    )

    if ([string]::IsNullOrWhiteSpace($Text)) {
        return [pscustomobject][ordered]@{}
    }
    try {
        $root = $Text | ConvertFrom-Json
    }
    catch {
        throw "$Label is not valid JSON: $($_.Exception.Message)"
    }
    Assert-JsonObject -Value $root -Label $Label
    return $root
}

function New-ClaudeServerConfig {
    return [pscustomobject][ordered]@{
        command = $script:resolvedBinaryPath
        args = @()
        env = [pscustomobject][ordered]@{
            CIA_MCP_INFERENCE_DATA_URL = $script:normalizedDataUrl
            CIA_MCP_INFERENCE_MODEL = $script:normalizedModel
            CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS = '4096'
        }
    }
}

function Merge-ClaudeConfig {
    param(
        [Parameter(Mandatory = $true)][AllowEmptyString()][string]$Current,
        [Parameter(Mandatory = $true)][string]$Label
    )

    $root = Read-JsonRoot -Text $Current -Label $Label
    $mcpProperty = $root.PSObject.Properties['mcpServers']
    if ($null -eq $mcpProperty) {
        $mcpServers = [pscustomobject][ordered]@{}
        Set-JsonProperty -Object $root -Name 'mcpServers' -Value $mcpServers
    }
    else {
        $mcpServers = $mcpProperty.Value
        Assert-JsonObject -Value $mcpServers -Label "$Label.mcpServers"
    }
    Set-JsonProperty -Object $mcpServers -Name $script:serverName -Value (New-ClaudeServerConfig)
    return ($root | ConvertTo-Json -Depth 100) + "`r`n"
}

function Merge-OpenCodeConfig {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string]$Current)

    $root = Read-JsonRoot -Text $Current -Label 'OpenCode config'
    if ($null -eq $root.PSObject.Properties['$schema']) {
        Set-JsonProperty -Object $root -Name '$schema' -Value 'https://opencode.ai/config.json'
    }

    $mcpProperty = $root.PSObject.Properties['mcp']
    if ($null -eq $mcpProperty) {
        $mcp = [pscustomobject][ordered]@{}
        Set-JsonProperty -Object $root -Name 'mcp' -Value $mcp
    }
    else {
        $mcp = $mcpProperty.Value
        Assert-JsonObject -Value $mcp -Label 'OpenCode config.mcp'
    }
    $server = [pscustomobject][ordered]@{
        type = 'local'
        command = @($script:resolvedBinaryPath)
        enabled = $true
        timeout = 10000
        environment = [pscustomobject][ordered]@{
            CIA_MCP_INFERENCE_DATA_URL = $script:normalizedDataUrl
            CIA_MCP_INFERENCE_MODEL = $script:normalizedModel
            CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS = '4096'
        }
    }
    Set-JsonProperty -Object $mcp -Name $script:serverName -Value $server

    $permissionProperty = $root.PSObject.Properties['permission']
    if ($null -eq $permissionProperty) {
        $permission = [pscustomobject][ordered]@{}
        Set-JsonProperty -Object $root -Name 'permission' -Value $permission
    }
    else {
        $permission = $permissionProperty.Value
        Assert-JsonObject -Value $permission -Label 'OpenCode config.permission'
    }
    Set-JsonProperty -Object $permission -Name ($script:serverName + '_*') -Value 'ask'
    return ($root | ConvertTo-Json -Depth 100) + "`r`n"
}

function Test-ClientDetected {
    param(
        [Parameter(Mandatory = $true)][string]$Client,
        [Parameter(Mandatory = $true)][string]$Path
    )

    if ($script:explicitClients) {
        return $script:Clients -contains $Client
    }
    if (Test-Path -LiteralPath $Path -PathType Leaf) {
        return $true
    }
    switch ($Client) {
        'Codex' { return $null -ne (Get-Command codex -ErrorAction SilentlyContinue) }
        'ClaudeDesktop' {
            if (Test-Path -LiteralPath (Split-Path -Parent $Path) -PathType Container) { return $true }
            return $null -ne (Get-AppxPackage -Name 'Claude' -ErrorAction SilentlyContinue | Select-Object -First 1)
        }
        'ClaudeCode' { return $null -ne (Get-Command claude -ErrorAction SilentlyContinue) }
        'OpenCode' { return $null -ne (Get-Command opencode -ErrorAction SilentlyContinue) }
    }
    return $false
}

function New-EncryptedBackup {
    param(
        [Parameter(Mandatory = $true)][string]$Client,
        [Parameter(Mandatory = $true)][byte[]]$Plaintext,
        [Parameter(Mandatory = $true)][string]$OriginalHash
    )

    Add-Type -AssemblyName System.Security
    $encrypted = [Security.Cryptography.ProtectedData]::Protect(
        $Plaintext,
        $null,
        [Security.Cryptography.DataProtectionScope]::CurrentUser
    )
    [IO.Directory]::CreateDirectory($script:resolvedBackupRoot) | Out-Null
    $timestamp = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssfffffffZ')
    $safeClient = $Client.ToLowerInvariant()
    $name = '{0}-{1}-{2}.dpapi' -f $timestamp, $safeClient, $OriginalHash.Substring(0, 12)
    $destination = Join-Path $script:resolvedBackupRoot $name
    $temporary = $destination + '.tmp-' + [Guid]::NewGuid().ToString('N')
    try {
        [IO.File]::WriteAllBytes($temporary, $encrypted)
        $roundTrip = [Security.Cryptography.ProtectedData]::Unprotect(
            [IO.File]::ReadAllBytes($temporary),
            $null,
            [Security.Cryptography.DataProtectionScope]::CurrentUser
        )
        if ((Get-BytesSha256 -Bytes $roundTrip) -ne $OriginalHash) {
            throw "Encrypted backup verification failed for $Client."
        }
        [IO.File]::Move($temporary, $destination)
    }
    finally {
        if (Test-Path -LiteralPath $temporary -PathType Leaf) {
            Remove-Item -LiteralPath $temporary -Force -ErrorAction SilentlyContinue
        }
    }
    return $destination
}

$resolvedBinaryPath = [IO.Path]::GetFullPath($BinaryPath)
$resolvedBackupRoot = [IO.Path]::GetFullPath($BackupRoot)
$candidateDataUrl = $DataUrl.Trim()
if ($candidateDataUrl -notmatch '^http://127\.0\.0\.1:[0-9]{1,5}/?$') {
    throw 'DataUrl must be an HTTP loopback origin with an explicit port, such as http://127.0.0.1:18090.'
}
$normalizedDataUrl = $candidateDataUrl.TrimEnd('/')
$normalizedModel = $Model.Trim()
$explicitClients = $Clients.Count -gt 0

if (-not [IO.Path]::IsPathRooted($BinaryPath) -or [IO.Path]::GetExtension($resolvedBinaryPath) -ne '.exe' -or $resolvedBinaryPath.IndexOfAny([char[]]@("'", "`r", "`n")) -ge 0) {
    throw 'BinaryPath must be an absolute Windows .exe path without quotes or line breaks.'
}
$uri = $null
$validAbsoluteUri = [Uri]::TryCreate($normalizedDataUrl, [UriKind]::Absolute, [ref]$uri)
$invalidDataUrl = -not $validAbsoluteUri
if ($validAbsoluteUri) {
    $invalidDataUrl = (
        ($uri.Scheme -cne 'http') -or
        ($uri.Host -cne '127.0.0.1') -or
        (-not [string]::IsNullOrEmpty($uri.UserInfo)) -or
        (-not [string]::IsNullOrEmpty($uri.Query)) -or
        (-not [string]::IsNullOrEmpty($uri.Fragment)) -or
        ($uri.Port -lt 1) -or
        ($uri.Port -gt 65535) -or
        ($uri.AbsolutePath -cne '/')
    )
}
if ($invalidDataUrl) {
    throw 'DataUrl must be an HTTP loopback origin such as http://127.0.0.1:18090.'
}
if ($normalizedModel -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$') {
    throw 'Model must be a stable local model identifier.'
}
if ($ExpectedPlanSha256 -and $ExpectedPlanSha256 -notmatch '^[A-Fa-f0-9]{64}$') {
    throw 'ExpectedPlanSha256 must contain exactly 64 hexadecimal characters.'
}

$binaryExists = Test-Path -LiteralPath $resolvedBinaryPath -PathType Leaf
$binaryHash = if ($binaryExists) { (Get-FileHash -LiteralPath $resolvedBinaryPath -Algorithm SHA256).Hash } else { $null }
$targets = @(
    [pscustomobject]@{ Client = 'Codex'; Path = [IO.Path]::GetFullPath($CodexConfigPath); Kind = 'toml' },
    [pscustomobject]@{ Client = 'ClaudeDesktop'; Path = [IO.Path]::GetFullPath($ClaudeDesktopConfigPath); Kind = 'claude-json' },
    [pscustomobject]@{ Client = 'ClaudeCode'; Path = [IO.Path]::GetFullPath($ClaudeCodeConfigPath); Kind = 'claude-json' },
    [pscustomobject]@{ Client = 'OpenCode'; Path = [IO.Path]::GetFullPath($OpenCodeConfigPath); Kind = 'opencode-json' }
)
$duplicateTargets = @($targets | Group-Object { $_.Path.ToUpperInvariant() } | Where-Object { $_.Count -gt 1 })
if ($duplicateTargets.Count -gt 0) {
    throw 'Two or more client configurations resolve to the same destination path.'
}

$items = @()
foreach ($target in $targets) {
    $detected = Test-ClientDetected -Client $target.Client -Path $target.Path
    if (-not $detected) {
        $items += [pscustomobject]@{
            client = $target.Client
            path = $target.Path
            detected = $false
            action = 'skip-not-detected'
            before_sha256 = $null
            after_sha256 = $null
            before_bytes = $null
            after_bytes = $null
        }
        continue
    }

    $exists = Test-Path -LiteralPath $target.Path -PathType Leaf
    $currentFile = if ($exists) { Read-Utf8File -Path $target.Path } else {
        [pscustomobject]@{ Bytes = [byte[]]@(); Text = ''; HasBom = $false; NewLine = "`r`n" }
    }
    $merged = switch ($target.Kind) {
        'toml' { Merge-CodexConfig -Current $currentFile.Text -NewLine $currentFile.NewLine }
        'claude-json' { Merge-ClaudeConfig -Current $currentFile.Text -Label ($target.Client + ' config') }
        'opencode-json' { Merge-OpenCodeConfig -Current $currentFile.Text }
    }
    if ($target.Kind -ne 'toml') {
        Read-JsonRoot -Text $merged -Label ($target.Client + ' merged config') | Out-Null
    }
    $desiredBytes = ConvertTo-Utf8Bytes -Text $merged -WithBom $currentFile.HasBom
    $beforeHash = if ($exists) { Get-BytesSha256 -Bytes $currentFile.Bytes } else { $null }
    $afterHash = Get-BytesSha256 -Bytes $desiredBytes
    $action = if (-not $exists) { 'create' } elseif ($beforeHash -eq $afterHash) { 'unchanged' } else { 'update' }
    $items += [pscustomobject]@{
        client = $target.Client
        path = $target.Path
        detected = $true
        action = $action
        before_sha256 = $beforeHash
        after_sha256 = $afterHash
        before_bytes = if ($exists) { [int64]$currentFile.Bytes.Length } else { $null }
        after_bytes = [int64]$desiredBytes.Length
        _before = $currentFile.Bytes
        _after = $desiredBytes
    }
}

$planLines = @(
    "server=$serverName",
    "tool=$toolName",
    "binary=$resolvedBinaryPath",
    "binary_sha256=$binaryHash",
    "backup_root=$resolvedBackupRoot",
    "data_url=$normalizedDataUrl",
    "model=$normalizedModel"
)
$planLines += @($items | ForEach-Object {
    '{0}|{1}|{2}|{3}|{4}|{5}' -f $_.client, $_.path, $_.detected, $_.action, $_.before_sha256, $_.after_sha256
})
$planHash = Get-BytesSha256 -Bytes ($utf8NoBom.GetBytes($planLines -join "`n"))
$approvedPlan = if ($ExpectedPlanSha256) { $ExpectedPlanSha256.ToUpperInvariant() } else { $null }

$publicItems = @($items | ForEach-Object {
    [pscustomobject]@{
        client = $_.client
        path = $_.path
        detected = $_.detected
        action = $_.action
        before_sha256 = $_.before_sha256
        after_sha256 = $_.after_sha256
        before_bytes = $_.before_bytes
        after_bytes = $_.after_bytes
    }
})
$result = [ordered]@{
    mode = if ($Apply) { 'apply' } else { 'preview' }
    server = $serverName
    tool = $toolName
    binary_path = $resolvedBinaryPath
    binary_exists = $binaryExists
    binary_sha256 = $binaryHash
    data_url = $normalizedDataUrl
    model = $normalizedModel
    provider_or_primary_model_touched = $false
    secrets_written = $false
    backups = 'DPAPI CurrentUser'
    backup_root = $resolvedBackupRoot
    plan_sha256 = $planHash
    expected_plan_sha256 = $approvedPlan
    items = $publicItems
}

if (-not $Apply) {
    [pscustomobject]$result | ConvertTo-Json -Depth 6
    exit 0
}
if (-not $approvedPlan) {
    throw 'Apply requires -ExpectedPlanSha256 with the exact plan_sha256 reviewed during preview.'
}
if ($approvedPlan -ne $planHash) {
    throw "Integration plan does not match ExpectedPlanSha256. Expected $approvedPlan; found $planHash."
}
if (-not $binaryExists) {
    throw "MCP inference binary is missing: $resolvedBinaryPath"
}

$mutex = [Threading.Mutex]::new($false, 'Local\CIA.LocalAI.V2.McpInferenceIntegrations')
$lockTaken = $false
$staged = [Collections.Generic.List[object]]::new()
$committed = [Collections.Generic.List[object]]::new()
$backupPaths = [Collections.Generic.List[string]]::new()
try {
    $lockTaken = $mutex.WaitOne([TimeSpan]::FromSeconds(30))
    if (-not $lockTaken) {
        throw 'Another MCP inference integration install is already running.'
    }
    if ((Get-FileHash -LiteralPath $resolvedBinaryPath -Algorithm SHA256).Hash -ne $binaryHash) {
        throw 'MCP inference binary changed after the reviewed preview.'
    }

    foreach ($item in @($items | Where-Object { $_.action -in @('create', 'update') })) {
        $directory = Split-Path -Parent $item.path
        [IO.Directory]::CreateDirectory($directory) | Out-Null
        $currentHash = if (Test-Path -LiteralPath $item.path -PathType Leaf) {
            (Get-FileHash -LiteralPath $item.path -Algorithm SHA256).Hash
        }
        else { $null }
        if ($currentHash -ne $item.before_sha256) {
            throw "Configuration changed after preview: $($item.client)."
        }

        if ($item.action -eq 'update') {
            $backupPaths.Add((New-EncryptedBackup -Client $item.client -Plaintext $item._before -OriginalHash $item.before_sha256))
        }
        $temporary = Join-Path $directory ('.cia-stage-' + [Guid]::NewGuid().ToString('N'))
        $rollback = Join-Path $directory ('.cia-rollback-' + [Guid]::NewGuid().ToString('N'))
        $discard = Join-Path $directory ('.cia-discard-' + [Guid]::NewGuid().ToString('N'))
        $stage = [pscustomobject]@{
            item = $item
            temporary = $temporary
            rollback = $rollback
            discard = $discard
            committed = $false
        }
        $staged.Add($stage)
        [IO.File]::WriteAllBytes($temporary, $item._after)
        if ((Get-FileHash -LiteralPath $temporary -Algorithm SHA256).Hash -ne $item.after_sha256) {
            throw "Staged configuration failed hash verification: $($item.client)."
        }
    }

    foreach ($stage in $staged) {
        $item = $stage.item
        $currentHash = if (Test-Path -LiteralPath $item.path -PathType Leaf) {
            (Get-FileHash -LiteralPath $item.path -Algorithm SHA256).Hash
        }
        else { $null }
        if ($currentHash -ne $item.before_sha256) {
            throw "Configuration changed during commit: $($item.client)."
        }
        if ($item.action -eq 'create') {
            [IO.File]::Move($stage.temporary, $item.path)
        }
        else {
            [IO.File]::Replace($stage.temporary, $item.path, $stage.rollback, $true)
        }
        $stage.committed = $true
        $committed.Add($stage)
        if ((Get-FileHash -LiteralPath $item.path -Algorithm SHA256).Hash -ne $item.after_sha256) {
            throw "Installed configuration failed hash verification: $($item.client)."
        }
    }
}
catch {
    $failure = $_
    for ($index = $committed.Count - 1; $index -ge 0; $index--) {
        $stage = $committed[$index]
        $item = $stage.item
        if ($item.action -eq 'update' -and (Test-Path -LiteralPath $stage.rollback -PathType Leaf)) {
            try { [IO.File]::Replace($stage.rollback, $item.path, $stage.discard, $true) } catch { }
        }
        elseif ($item.action -eq 'create' -and (Test-Path -LiteralPath $item.path -PathType Leaf)) {
            Remove-Item -LiteralPath $item.path -Force -ErrorAction SilentlyContinue
        }
    }
    throw $failure
}
finally {
    foreach ($stage in $staged) {
        foreach ($temporaryArtifact in @($stage.temporary, $stage.rollback, $stage.discard)) {
            if (Test-Path -LiteralPath $temporaryArtifact -PathType Leaf) {
                Remove-Item -LiteralPath $temporaryArtifact -Force -ErrorAction SilentlyContinue
            }
        }
    }
    if ($lockTaken) { $mutex.ReleaseMutex() }
    $mutex.Dispose()
}

$result['backup_files'] = @($backupPaths)
[pscustomobject]$result | ConvertTo-Json -Depth 6
