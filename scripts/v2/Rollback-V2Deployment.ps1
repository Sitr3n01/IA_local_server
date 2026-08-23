<#
.SYNOPSIS
Rolls one v2 deployment back to the release it replaced.

.DESCRIPTION
Complete-V2Deployment.ps1 already rolls itself back when a cutover fails. This
script is the explicit, operator-initiated version of the same restore, for the
case where a deployment succeeded technically but must be undone.

It restores exactly what the recorded release transaction replaced: the
configuration, launchers, application binaries, and scheduled task definitions
of the previous release. It deliberately does not touch candidate models,
benchmark results, published production artifacts, user data, or any
configuration the deployment did not replace.

The provider is drained first, so an in-flight generation is never killed to
make a rollback faster. If the restore cannot complete, the result is reported
as DEGRADED and the release manifest is left describing nothing rather than
describing a release that is not installed.

.EXAMPLE
.\scripts\v2\Rollback-V2Deployment.ps1 -Environment Final

.EXAMPLE
.\scripts\v2\Rollback-V2Deployment.ps1 -Environment Final -Apply
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Canary', 'Final')]
    [string]$Environment,

    [string]$InstallRoot = 'C:\IA\local-ai-v2',

    # The release to undo. Defaults to the release currently described by the
    # installed release manifest.
    [ValidatePattern('^$|^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$')]
    [string]$ReleaseId = '',

    [ValidateRange(0, 7200)]
    [int]$DrainTimeoutSeconds = 300,

    [switch]$AllowUndrainableProvider,

    [switch]$Apply
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Common.ps1')

$settings = Get-V2DeploymentSettings -Environment $Environment
$environmentName = $settings.Name
$expectedRoot = [IO.Path]::GetFullPath('C:\IA\local-ai-v2').TrimEnd([char[]]@('\', '/'))
$resolvedRoot = [IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))
if (-not [string]::Equals($resolvedRoot, $expectedRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Deployment rollback is restricted to '$expectedRoot'."
}
if ($Apply -and -not (Test-V2IsAdministrator)) {
    throw 'Deployment rollback requires an elevated PowerShell. Run the preview normally, then run Apply in an elevated shell.'
}

if ([string]::IsNullOrWhiteSpace($ReleaseId)) {
    $installed = Get-V2InstalledRelease -InstallRoot $resolvedRoot -Environment $Environment
    if ($null -eq $installed) {
        throw "No release manifest is installed for '$environmentName', so there is no release to roll back. Pass -ReleaseId explicitly if you know which transaction to undo."
    }
    $ReleaseId = [string]$installed.release_id
}

$transaction = Import-V2ReleaseTransaction -InstallRoot $resolvedRoot -ReleaseId $ReleaseId
if ($transaction.environment -ne $environmentName) {
    throw "Release '$ReleaseId' belongs to environment '$($transaction.environment)', not '$environmentName'."
}

$taskNames = Get-V2DeploymentTaskNames -Environment $Environment
$routerTaskName = $taskNames[0]
$edgeTaskName = $taskNames[1]
$adminPipeName = Get-V2AdminPipeName -Environment $Environment
$binRoot = Join-Path $resolvedRoot 'bin'
$ports = @(
    [int]($settings.RouterAddress.Split(':')[-1]),
    [int]($settings.DataAddress.Split(':')[-1]),
    [int]($settings.ControlAddress.Split(':')[-1])
)

$plan = [pscustomobject]@{
    mode = $(if ($Apply) { 'apply' } else { 'preview' })
    environment = $environmentName
    release_id = $transaction.release_id
    previous_release = $transaction.previous_release_id
    version = $transaction.version
    commit = $transaction.commit
    restores_files = @(@($transaction.files) | ForEach-Object {
            [ordered]@{ path = $_.path; label = $_.label; action = $(if ($_.existed) { 'restore' } else { 'remove' }) }
        })
    restores_tasks = @(@($transaction.tasks) | ForEach-Object {
            [ordered]@{ name = $_.name; action = $(if ($_.existed) { 'restore' } else { 'unregister' }) }
        })
    not_rolled_back = @(
        'candidate model artifacts',
        'published production artifacts',
        'benchmark and qualification results',
        'user data and unrelated configuration'
    )
    ordered_operations = @(
        'drain the running provider and wait for active=0 and queued=0',
        'stop panel, Edge and Router only once drained',
        'restore the recorded bytes and scheduled task definitions',
        'reapply ACL policy',
        'restart Router then Edge',
        'health and installation verification'
    )
}

if (-not $Apply) {
    $plan | ConvertTo-Json -Depth 6
    Write-Host 'Preview only. No file, task, ACL, process, or model state was changed.'
    return
}

$rollbackMutex = [Threading.Mutex]::new($false, 'Local\CIA.LocalAI.V2.DeploymentCompletion')
$rollbackMutexAcquired = $false
$stopped = $false
$restore = $null
$operationFailure = $null
$restartFailure = $null
$drain = $null
$status = 'rolled-back'

try {
    try { $rollbackMutexAcquired = $rollbackMutex.WaitOne(0) }
    catch [Threading.AbandonedMutexException] { $rollbackMutexAcquired = $true }
    if (-not $rollbackMutexAcquired) {
        throw 'A v2 deployment or rollback is already in progress.'
    }

    $drain = Wait-V2ProviderDrain `
        -ControlAddress $settings.ControlAddress `
        -PipeName $adminPipeName `
        -TimeoutSeconds $DrainTimeoutSeconds
    Add-V2ReleaseEvent -Transaction $transaction -Stage 'rollback-drain' -Detail "$($drain.reason) over $($drain.transport) after $($drain.waited_seconds)s (active=$($drain.active) queued=$($drain.queued))"

    if (-not $drain.supported) {
        if (-not $AllowUndrainableProvider) {
            throw 'The installed provider has no drain API, so stopping it could interrupt an in-flight generation. Re-run with -AllowUndrainableProvider to accept that.'
        }
        Write-Warning 'The installed provider has no drain API. Proceeding by explicit operator decision; an in-flight request may be interrupted.'
    }
    elseif (-not $drain.drained) {
        [void](Resume-V2Provider -ControlAddress $settings.ControlAddress -PipeName $adminPipeName)
        throw "The provider did not drain within $DrainTimeoutSeconds seconds (active=$($drain.active), queued=$($drain.queued)). Nothing was restored and the provider was resumed."
    }

    $installedTray = Join-Path $binRoot 'cia-tray.exe'
    Get-Process -Name 'cia-tray' -ErrorAction SilentlyContinue | Where-Object {
        try { [string]::Equals($_.Path, $installedTray, [StringComparison]::OrdinalIgnoreCase) }
        catch { $false }
    } | Stop-Process -ErrorAction Stop
    Stop-ScheduledTask -TaskName $edgeTaskName -ErrorAction SilentlyContinue
    Stop-ScheduledTask -TaskName $routerTaskName -ErrorAction SilentlyContinue
    $stopped = $true

    if (-not (Wait-V2ServingStopped -InstallRoot $resolvedRoot -Ports $ports -TimeoutSeconds 30)) {
        throw "$Environment tasks did not release their provider processes and loopback listeners within 30 seconds; nothing was restored."
    }
    Get-Process -Name 'cia-mcp', 'cia-mcp-admin', 'cia-mcp-inference' -ErrorAction SilentlyContinue | Where-Object {
        try { $_.Path.StartsWith($binRoot, [StringComparison]::OrdinalIgnoreCase) }
        catch { $false }
    } | Stop-Process -ErrorAction SilentlyContinue

    $restore = Restore-V2Release -Transaction $transaction
    if (-not $restore.succeeded) {
        $status = 'degraded'
    }
    else {
        & (Join-Path $PSScriptRoot 'Set-V2Acl.ps1') -InstallRoot $resolvedRoot -Apply | Out-Host
    }
}
catch {
    $operationFailure = $_
    $status = 'degraded'
}

[void](Complete-V2ReleaseTransaction -Transaction $transaction -Status $status)

if ($stopped) {
    try {
        Start-ScheduledTask -TaskName $routerTaskName -ErrorAction Stop
        $routerDeadline = (Get-Date).AddSeconds(30)
        $routerPort = [int]($settings.RouterAddress.Split(':')[-1])
        do {
            $routerListener = @(Get-NetTCPConnection -State Listen -LocalPort $routerPort -ErrorAction SilentlyContinue)
            if ($routerListener.Count -eq 0) { Start-Sleep -Milliseconds 250 }
        } while ($routerListener.Count -eq 0 -and (Get-Date) -lt $routerDeadline)
        if ($routerListener.Count -eq 0) {
            throw "$Environment Router did not become ready within 30 seconds after the rollback."
        }

        Start-ScheduledTask -TaskName $edgeTaskName -ErrorAction Stop
        $edgeDeadline = (Get-Date).AddSeconds(30)
        $live = $null
        do {
            try { $live = Invoke-WebRequest -UseBasicParsing -Uri "http://$($settings.ControlAddress)/livez" -TimeoutSec 2 }
            catch { $live = $null; Start-Sleep -Milliseconds 250 }
        } while ($null -eq $live -and (Get-Date) -lt $edgeDeadline)
        if ($null -eq $live -or $live.StatusCode -ne 200) {
            throw "$Environment Edge did not become live within 30 seconds after the rollback."
        }
    }
    catch {
        $restartFailure = $_
    }
}

if ($rollbackMutexAcquired) { [void]$rollbackMutex.ReleaseMutex() }
$rollbackMutex.Dispose()

$summary = [pscustomobject]@{
    environment = $environmentName
    rolled_back_release = $transaction.release_id
    restored_release = $transaction.previous_release_id
    status = $status
    drain = $drain
    restore = $restore
    restart_failed = ($null -ne $restartFailure)
    transaction_journal = (Join-Path $transaction.directory 'transaction.json')
}
$summary | ConvertTo-Json -Depth 6

if ($operationFailure) {
    throw "DEGRADED: rollback of '$($transaction.release_id)' failed. Error: $($operationFailure.Exception.Message). Recovery record: $(Join-Path $transaction.directory 'transaction.json')"
}
if ($null -ne $restore -and -not $restore.succeeded) {
    throw "DEGRADED: rollback of '$($transaction.release_id)' restored only part of the previous release. Failures: $($restore.failures -join ' | '). Recovery record: $(Join-Path $transaction.directory 'transaction.json')"
}
if ($restartFailure) {
    throw $restartFailure
}

& (Join-Path $PSScriptRoot 'Set-V2Acl.ps1') -InstallRoot $resolvedRoot -Audit | Out-Host
& (Join-Path $PSScriptRoot 'Test-V2Installation.ps1') -Environment $Environment -InstallRoot $resolvedRoot -Online
Write-Host "Rollback complete. '$($transaction.release_id)' was undone and the installation now matches release '$($transaction.previous_release_id)'."
