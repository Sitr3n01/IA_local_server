[CmdletBinding()]
param(
    [ValidateSet('Anthropic', 'Local')]
    [string]$Mode = 'Local',
    [string]$TrayBinary = 'C:\IA\local-ai-v2\bin\cia-tray.exe',
    [string]$PanelConfig = 'C:\IA\local-ai-v2\config\panel.canary.json',
    [switch]$Apply
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path -LiteralPath $TrayBinary -PathType Leaf)) {
    throw "CIA tray binary is missing: $TrayBinary"
}
if (-not (Test-Path -LiteralPath $PanelConfig -PathType Leaf)) {
    throw "CIA panel config is missing: $PanelConfig"
}

$requestedMode = $Mode.ToLowerInvariant()
$arguments = @('-config', $PanelConfig, '-claude-mode', $requestedMode)
if ($Apply) {
    $arguments += '-apply'
}

$process = Start-Process -FilePath $TrayBinary -ArgumentList $arguments -WindowStyle Hidden -Wait -PassThru
if ($process.ExitCode -ne 0) {
    throw "Claude Desktop $Mode operation failed with exit code $($process.ExitCode)."
}

if (-not $Apply) {
    Write-Host 'Preview only. Add -Apply to write the isolated Claude-3p profile and restart Claude Desktop.'
}
