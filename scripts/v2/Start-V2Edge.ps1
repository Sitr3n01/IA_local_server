[CmdletBinding()]
param(
    [ValidateSet('Canary', 'Final')]
    [string]$Environment = 'Canary',
    [string]$InstallRoot = 'C:\IA\local-ai-v2',
    [string]$ManifestPath = 'C:\IA\local-ai-v2\config\models.yaml',
    [string]$SchemaPath = 'C:\IA\local-ai-v2\config\models.schema.json',
    [string]$CredentialHelper = 'C:\IA\local-ai-v2\bin\cia-credential.exe',
    [switch]$Run
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Common.ps1')
$settings = Get-V2DeploymentSettings -Environment $Environment
$edge = Join-Path $InstallRoot 'bin\cia-edge.exe'

$plan = [pscustomobject]@{
    component = 'edge'
    executable = $edge
    data_address = $settings.DataAddress
    control_address = $settings.ControlAddress
    upstream = "http://$($settings.RouterAddress)"
    models_config = $ManifestPath
    credentials = @('inference', 'admin', 'router')
    mode = $(if ($Run) { 'run' } else { 'preview' })
}

if (-not $Run) {
    $plan | ConvertTo-Json -Depth 3
    Write-Host 'Preview only. -Run is required to start a process.'
    exit 0
}

foreach ($required in @($edge, $ManifestPath, $SchemaPath, $CredentialHelper)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Required file is missing: $required"
    }
}

$inferenceToken = (& $CredentialHelper get inference 2>$null | Out-String).Trim()
$adminToken = (& $CredentialHelper get admin 2>$null | Out-String).Trim()
$routerToken = (& $CredentialHelper get router 2>$null | Out-String).Trim()
if (@($inferenceToken, $adminToken, $routerToken) | Where-Object { [string]::IsNullOrWhiteSpace($_) }) {
    throw 'Unable to obtain all three v2 credentials.'
}

$env:CIA_INFERENCE_TOKEN = $inferenceToken
$env:CIA_ADMIN_TOKEN = $adminToken
$env:CIA_ROUTER_TOKEN = $routerToken
$env:CIA_EDGE_LOG_PATH = Join-Path $InstallRoot 'logs\cia-edge.jsonl'
try {
    & $edge `
        --environment $settings.Name `
        --data-addr $settings.DataAddress `
        --control-addr $settings.ControlAddress `
        --upstream "http://$($settings.RouterAddress)" `
        --models-config $ManifestPath `
        --models-schema $SchemaPath
    exit $LASTEXITCODE
}
finally {
    Remove-Item Env:\CIA_INFERENCE_TOKEN, Env:\CIA_ADMIN_TOKEN, Env:\CIA_ROUTER_TOKEN, Env:\CIA_EDGE_LOG_PATH -ErrorAction SilentlyContinue
    $inferenceToken = $null
    $adminToken = $null
    $routerToken = $null
}
