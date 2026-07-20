param(
    [string]$PythonPath = "",
    [string]$DefaultProfileId = "ornith10-9b-q4km-kv-q4-128k",
    [string]$Runtime = "amd",
    [string]$ModelAlias = "local-model",
    [int]$ContextLimit = 131072
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $scriptDir
$mcpScript = Join-Path $root "mcp\local-llama-mcp.py"
$studioDb = "C:\Users\Sitr3n\.unsloth\studio\studio.db"

if (-not (Test-Path -LiteralPath $mcpScript)) {
    throw "MCP script not found: $mcpScript"
}

if (-not (Test-Path -LiteralPath $studioDb)) {
    throw "Unsloth Studio database not found: $studioDb"
}

if ([string]::IsNullOrWhiteSpace($PythonPath)) {
    $cmd = Get-Command python -ErrorAction Stop
    $PythonPath = $cmd.Source
}

$envMap = @{
    LOCAL_LLAMA_BASE_URL = "http://127.0.0.1:8080/v1"
    LOCAL_LLAMA_PANEL_URL = "http://127.0.0.1:8090"
    LOCAL_LLAMA_MODEL = $ModelAlias
    LOCAL_LLAMA_DEFAULT_PROFILE = $DefaultProfileId
    LOCAL_LLAMA_DEFAULT_RUNTIME = $Runtime
    LOCAL_LLAMA_CONTEXT_LIMIT = "$ContextLimit"
    LOCAL_LLAMA_AUTOSTART = "1"
    LOCAL_LLAMA_AUTOSTART_PANEL = "1"
    UNSLOTH_STUDIO_ALLOW_STDIO_MCP = "1"
}

$command = "`"$PythonPath`" `"$mcpScript`""
$headersJson = $envMap | ConvertTo-Json -Compress
$commandB64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($command))
$headersB64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($headersJson))

$pythonCode = @'
import datetime
import base64
import sqlite3
import sys

db_path, server_id, display_name, command_b64, headers_b64 = sys.argv[1:6]
command = base64.b64decode(command_b64).decode("utf-8")
headers_json = base64.b64decode(headers_b64).decode("utf-8")
now = datetime.datetime.now(datetime.timezone.utc).isoformat()
con = sqlite3.connect(db_path)
try:
    con.execute(
        """
        CREATE TABLE IF NOT EXISTS mcp_servers (
            id TEXT NOT NULL PRIMARY KEY,
            display_name TEXT NOT NULL,
            url TEXT NOT NULL,
            headers_json TEXT,
            is_enabled INTEGER NOT NULL DEFAULT 1,
            use_oauth INTEGER NOT NULL DEFAULT 0,
            created_at TEXT NOT NULL,
            updated_at TEXT NOT NULL
        )
        """
    )
    con.execute(
        """
        INSERT INTO mcp_servers
            (id, display_name, url, headers_json, is_enabled, use_oauth, created_at, updated_at)
        VALUES (?, ?, ?, ?, 1, 0, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            display_name = excluded.display_name,
            url = excluded.url,
            headers_json = excluded.headers_json,
            is_enabled = 1,
            use_oauth = 0,
            updated_at = excluded.updated_at
        """,
        (server_id, display_name, command, headers_json, now, now),
    )
    con.commit()
finally:
    con.close()
'@

$serverId = "local-llama-mcp"
$displayName = "Local Llama Executor"
$pythonCode | & $PythonPath - $studioDb $serverId $displayName $commandB64 $headersB64

[PSCustomObject]@{
    id = $serverId
    display_name = $displayName
    command = $command
    env = $envMap
    database = $studioDb
}
