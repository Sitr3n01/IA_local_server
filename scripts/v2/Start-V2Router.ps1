[CmdletBinding()]
param(
    [ValidateSet('Canary', 'Final')]
    [string]$Environment = 'Canary',
    [string]$InstallRoot = 'C:\IA\local-ai-v2',
    [string]$CredentialHelper = 'C:\IA\local-ai-v2\bin\cia-credential.exe',
    [switch]$Run
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Common.ps1')
$settings = Get-V2DeploymentSettings -Environment $Environment
$swap = Join-Path $InstallRoot 'bin\llama-swap.exe'
$config = Join-Path $InstallRoot ("config\llama-swap.{0}.yaml" -f $settings.Name)

$plan = [pscustomobject]@{
    component = 'router'
    executable = $swap
    config = $config
    listen = $settings.RouterAddress
    credential = 'router'
    mode = $(if ($Run) { 'run' } else { 'preview' })
}

if (-not $Run) {
    $plan | ConvertTo-Json -Depth 3
    Write-Host 'Preview only. -Run is required to start a process.'
    exit 0
}

foreach ($required in @($swap, $config, $CredentialHelper)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Required file is missing: $required"
    }
}

$routerToken = (& $CredentialHelper get router 2>$null | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($routerToken)) {
    throw 'Unable to obtain the router credential.'
}

# The production AMD baseline is ROCm0. Never inherit Unsloth Studio's
# process-local HIP_VISIBLE_DEVICES=1 selector into serving descendants.
Remove-Item Env:\HIP_VISIBLE_DEVICES -ErrorAction SilentlyContinue
$env:CIA_ROUTER_TOKEN = $routerToken
try {
    & $swap --config $config --listen $settings.RouterAddress
    exit $LASTEXITCODE
}
finally {
    Remove-Item Env:\CIA_ROUTER_TOKEN -ErrorAction SilentlyContinue
    $routerToken = $null
}
