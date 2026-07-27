[CmdletBinding()]
param(
    [string]$CredentialHelper = 'C:\IA\local-ai-v2\bin\cia-credential.exe',
    [switch]$Apply
)

$ErrorActionPreference = 'Stop'

[pscustomobject]@{
    mode = $(if ($Apply) { 'apply' } else { 'preview' })
    helper = $CredentialHelper
    operation = 'init'
    credentials = @('inference', 'admin', 'router')
    behavior = 'create missing credentials only; never print values'
} | ConvertTo-Json -Depth 3

if (-not $Apply) {
    Write-Host 'Preview only. No credential was created or changed.'
    exit 0
}

if (-not (Test-Path -LiteralPath $CredentialHelper -PathType Leaf)) {
    throw "Credential helper is missing: $CredentialHelper"
}

& $CredentialHelper init
if ($LASTEXITCODE -ne 0) {
    throw "Credential initialization failed with exit code $LASTEXITCODE."
}

Write-Host 'Missing v2 credentials were initialized in Windows Credential Manager. Values were not displayed.'
