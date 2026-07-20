param(
    [string]$HostName = "127.0.0.1",
    [int]$Port = 8090
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $scriptDir
$panel = Join-Path $root "control\local_llama_panel.py"

if (-not (Test-Path -LiteralPath $panel)) {
    throw "Panel daemon not found: $panel"
}

Write-Host "Local llama panel: http://$HostName`:$Port"
python $panel --host $HostName --port $Port
