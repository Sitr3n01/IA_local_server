[CmdletBinding()]
param(
    [ValidateSet('Anthropic', 'Local')]
    [string]$Instance = 'Local',
    [string]$TrayBinary = 'C:\IA\local-ai-v2\bin\cia-tray.exe',
    [string]$PanelConfig = 'C:\IA\local-ai-v2\config\panel.canary.json',
    [switch]$Apply
)

# Opens one of the two Claude Desktop instances (ADR 0022). Neither stops a
# Desktop process: Local starts beside the signed-in instance, and the 3P
# selector rests at 1p again before this script returns.

$ErrorActionPreference = 'Stop'

if (-not (Test-Path -LiteralPath $TrayBinary -PathType Leaf)) {
    throw "CIA tray binary is missing: $TrayBinary"
}
if (-not (Test-Path -LiteralPath $PanelConfig -PathType Leaf)) {
    throw "CIA panel config is missing: $PanelConfig"
}

if (-not $Apply) {
    # The preview is the tray's own diagnosis: whether Desktop was found and
    # whether the gateway answers its dedicated credential. It writes nothing.
    $diagnosis = & $TrayBinary -config $PanelConfig -diagnose | Out-String | ConvertFrom-Json
    [pscustomobject]@{
        instance   = $Instance.ToLowerInvariant()
        available  = [bool]$diagnosis.snapshot.ClaudeAvailable
        gateway_ok = [bool]$diagnosis.snapshot.ClaudeGatewayOK
        detail     = $diagnosis.snapshot.ClaudeDetail
    } | ConvertTo-Json
    Write-Host 'Preview only. Add -Apply to open the instance.'
    return
}

$action = if ($Instance -eq 'Local') { '-claude-local' } else { '-claude-open' }
$process = Start-Process -FilePath $TrayBinary -ArgumentList @('-config', $PanelConfig, $action) -WindowStyle Hidden -Wait -PassThru
if ($process.ExitCode -ne 0) {
    throw "Opening Claude Desktop ($Instance) failed with exit code $($process.ExitCode)."
}
