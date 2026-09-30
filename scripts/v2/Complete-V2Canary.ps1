<#
.SYNOPSIS
Completes a reviewed canary deployment.

.DESCRIPTION
A thin wrapper over Complete-V2Deployment.ps1 with -Environment Canary. It
exists so the documented canary command and the operator muscle memory built
around it keep working; the transaction, drain, rollback, and verification are
the shared implementation.
#>
[CmdletBinding()]
param(
    [string]$InstallRoot = 'C:\IA\local-ai-v2',
    [string]$TargetCodexHome = 'C:\Users\Sitr3n\.codex',
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Fa-f0-9]{64}$')]
    [string]$ExpectedHarnessPlanSha256,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Fa-f0-9]{64}$')]
    [string]$ExpectedEdgeSha256,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Fa-f0-9]{64}$')]
    [string]$ExpectedCredentialSha256,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Fa-f0-9]{64}$')]
    [string]$ExpectedMcpSha256,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Fa-f0-9]{64}$')]
    [string]$ExpectedMcpAdminSha256,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Fa-f0-9]{64}$')]
    [string]$ExpectedMcpInferenceSha256,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Fa-f0-9]{64}$')]
    [string]$ExpectedMonitorSha256,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Fa-f0-9]{64}$')]
    [string]$ExpectedSupervisorSha256,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Fa-f0-9]{64}$')]
    [string]$ExpectedTraySha256,
    [ValidatePattern('^$|^[A-Za-z0-9._-]{1,64}$')]
    [string]$Version = '',
    [ValidatePattern('^$|^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$')]
    [string]$ReleaseId = '',
    [ValidateRange(0, 7200)]
    [int]$DrainTimeoutSeconds = 300,
    [switch]$AllowUndrainableProvider,
    [switch]$Apply,
    [switch]$Replace
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

& (Join-Path $PSScriptRoot 'Complete-V2Deployment.ps1') `
    -Environment Canary `
    -InstallRoot $InstallRoot `
    -TargetCodexHome $TargetCodexHome `
    -ExpectedHarnessPlanSha256 $ExpectedHarnessPlanSha256 `
    -ExpectedEdgeSha256 $ExpectedEdgeSha256 `
	-ExpectedCredentialSha256 $ExpectedCredentialSha256 `
    -ExpectedMcpSha256 $ExpectedMcpSha256 `
    -ExpectedMcpAdminSha256 $ExpectedMcpAdminSha256 `
    -ExpectedMcpInferenceSha256 $ExpectedMcpInferenceSha256 `
    -ExpectedMonitorSha256 $ExpectedMonitorSha256 `
    -ExpectedSupervisorSha256 $ExpectedSupervisorSha256 `
    -ExpectedTraySha256 $ExpectedTraySha256 `
    -Version $Version `
    -ReleaseId $ReleaseId `
    -DrainTimeoutSeconds $DrainTimeoutSeconds `
    -AllowUndrainableProvider:$AllowUndrainableProvider `
    -Apply:$Apply `
    -Replace:$Replace
