param(
    [string]$StudioDbPath = "C:\Users\Sitr3n\.unsloth\studio\studio.db",
    [string]$ProviderId = "local-llama-executor",
    [string]$ProviderType = "llama_cpp",
    [string]$DisplayName = "Local Llama Executor",
    [string]$BaseUrl = "http://127.0.0.1:8090/v1"
)

$ErrorActionPreference = "Stop"

if (-not (Test-Path -LiteralPath $StudioDbPath)) {
    throw "Unsloth studio DB not found: $StudioDbPath"
}

$python = @"
import sqlite3
from datetime import datetime, timezone

db_path = r"$StudioDbPath"
provider_id = r"$ProviderId"
provider_type = r"$ProviderType"
display_name = r"$DisplayName"
base_url = r"$BaseUrl"
now = datetime.now(timezone.utc).isoformat()

conn = sqlite3.connect(db_path)
try:
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute(
        '''
        CREATE TABLE IF NOT EXISTS llm_providers (
            id TEXT NOT NULL PRIMARY KEY,
            provider_type TEXT NOT NULL,
            display_name TEXT NOT NULL,
            base_url TEXT NOT NULL,
            is_enabled INTEGER NOT NULL DEFAULT 1,
            created_at TEXT NOT NULL,
            updated_at TEXT NOT NULL
        )
        '''
    )
    conn.execute(
        '''
        INSERT INTO llm_providers (id, provider_type, display_name, base_url, is_enabled, created_at, updated_at)
        VALUES (?, ?, ?, ?, 1, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            provider_type = excluded.provider_type,
            display_name = excluded.display_name,
            base_url = excluded.base_url,
            is_enabled = 1,
            updated_at = excluded.updated_at
        ''',
        (provider_id, provider_type, display_name, base_url, now, now),
    )
    conn.commit()
finally:
    conn.close()

print(f"Registered {display_name} at {base_url}")
"@

$python | python -
