<#
.SYNOPSIS
Completes a reviewed v2 deployment for one environment as a single transaction.

.DESCRIPTION
This is the only supported way to cut over a v2 installation. Canary and Final
are the same transaction with different ports, task names, and artifact source;
neither has a hand-written sequence of individual installers.

The transaction has four phases:

  1. Preflight - approvals, prerequisites, and staged binary hashes. Nothing is
     mutated, and a preview stops here.
  2. Preparation - configuration generation and harness installation. Both are
     individually transactional and neither interrupts the running provider.
  3. Drain and cutover - the provider is asked to stop admitting new inference
     and to finish what it already had. Only once it is drained are the tasks
     stopped and the binaries, tasks, ACLs, and firewall policy replaced. A
     drain that does not complete aborts the deployment *before* any binary is
     touched; no in-flight request is ever killed silently.
  4. Restart and verification - Router then Edge, health, ACL audit, and the
     independent installation check.

Every file and scheduled task the cutover replaces is recorded in a release
transaction beforehand. A failure during phase 3 restores the previous release
from that record; if the restore also fails the deployment reports DEGRADED
rather than hiding it, and the release manifest is left absent so every consumer
stays fail-closed.

.EXAMPLE
.\scripts\v2\Complete-V2Deployment.ps1 -Environment Canary @approval

.EXAMPLE
.\scripts\v2\Complete-V2Deployment.ps1 -Environment Final @approval -Apply
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Canary', 'Final')]
    [string]$Environment,

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
    [string]$ExpectedMcpSha256,

    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Fa-f0-9]{64}$')]
    [string]$ExpectedMcpAdminSha256,

    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Fa-f0-9]{64}$')]
    [string]$ExpectedMcpInferenceSha256,

    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Fa-f0-9]{64}$')]
    [string]$ExpectedSupervisorSha256,

    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Fa-f0-9]{64}$')]
    [string]$ExpectedTraySha256,

    # Immutable reviewed release label, recorded in the release manifest.
    [ValidatePattern('^$|^[A-Za-z0-9._-]{1,64}$')]
    [string]$Version = '',

    [ValidatePattern('^$|^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$')]
    [string]$ReleaseId = '',

    # How long the provider may take to finish the work it already had. On
    # expiry the deployment aborts before replacing anything.
    [ValidateRange(0, 7200)]
    [int]$DrainTimeoutSeconds = 300,

    # Required only when the installed provider predates the drain API, which is
    # true exactly once: the deployment that installs drain support. Stopping an
    # undrainable provider can interrupt an in-flight generation, so it must be
    # an explicit operator decision rather than a silent fallback.
    [switch]$AllowUndrainableProvider,

    [switch]$Apply,

    [switch]$Replace
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Common.ps1')

# ---------------------------------------------------------------- phase 1: preflight

$settings = Get-V2DeploymentSettings -Environment $Environment
$environmentName = $settings.Name
$expectedRoot = [IO.Path]::GetFullPath('C:\IA\local-ai-v2').TrimEnd([char[]]@('\', '/'))
$resolvedRoot = [IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))
if (-not [string]::Equals($resolvedRoot, $expectedRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Deployment completion is restricted to '$expectedRoot'."
}
if ($Apply -and -not (Test-V2IsAdministrator)) {
    throw 'Deployment completion requires an elevated PowerShell. Run the preview normally, then run Apply in an elevated shell.'
}
if ($Apply -and -not $Replace) {
    throw 'Deployment completion requires explicit -Replace because it replaces installed binaries and scheduled task definitions.'
}

$approvals = Resolve-V2DeploymentApprovals `
    -StagingRoot (Join-Path $resolvedRoot 'state\staging') `
    -Approvals @{
    Edge         = $ExpectedEdgeSha256
    Mcp          = $ExpectedMcpSha256
    McpAdmin     = $ExpectedMcpAdminSha256
    McpInference = $ExpectedMcpInferenceSha256
    Supervisor   = $ExpectedSupervisorSha256
    Tray         = $ExpectedTraySha256
}
[void](Assert-V2DeploymentPrerequisites -InstallRoot $resolvedRoot -Environment $Environment)

$taskNames = Get-V2DeploymentTaskNames -Environment $Environment
$routerTaskName = $taskNames[0]
$edgeTaskName = $taskNames[1]
$adminPipeName = Get-V2AdminPipeName -Environment $Environment
$ports = @(
    [int]($settings.RouterAddress.Split(':')[-1]),
    [int]($settings.DataAddress.Split(':')[-1]),
    [int]($settings.ControlAddress.Split(':')[-1])
)

$configRoot = Join-Path $resolvedRoot 'config'
$launcherRoot = Join-Path $resolvedRoot 'launchers'
$binRoot = Join-Path $resolvedRoot 'bin'
$replacedFiles = @(
    [pscustomobject]@{ Label = 'config:manifest'; Path = (Join-Path $configRoot 'models.yaml') },
    [pscustomobject]@{ Label = 'config:schema'; Path = (Join-Path $configRoot 'models.schema.json') },
    [pscustomobject]@{ Label = 'config:router'; Path = (Join-Path $configRoot ("llama-swap.{0}.yaml" -f $environmentName)) },
    [pscustomobject]@{ Label = 'config:deployment-marker'; Path = (Join-Path $configRoot ("deployment.{0}.json" -f $environmentName)) },
    [pscustomobject]@{ Label = 'config:panel'; Path = (Join-Path $configRoot ("panel.{0}.json" -f $environmentName)) },
    [pscustomobject]@{ Label = 'config:release'; Path = (Get-V2ReleaseManifestPath -InstallRoot $resolvedRoot -Environment $Environment) },
    [pscustomobject]@{ Label = 'launcher:router'; Path = (Join-Path $launcherRoot ("router-{0}.vbs" -f $environmentName)) },
    [pscustomobject]@{ Label = 'launcher:edge'; Path = (Join-Path $launcherRoot ("edge-{0}.vbs" -f $environmentName)) },
    [pscustomobject]@{ Label = 'launcher:tray'; Path = (Join-Path $launcherRoot ("tray-{0}.vbs" -f $environmentName)) }
) + @($approvals | ForEach-Object {
        [pscustomobject]@{ Label = "bin:$($_.Component.ToLowerInvariant())"; Path = (Join-Path $binRoot $_.Binary) }
    })

$installedRelease = Get-V2InstalledRelease -InstallRoot $resolvedRoot -Environment $Environment
$previousReleaseId = ''
if ($null -ne $installedRelease) { $previousReleaseId = [string]$installedRelease.release_id }

$plan = [pscustomobject]@{
    mode = $(if ($Apply) { 'apply' } else { 'preview' })
    environment = $environmentName
    install_root = $resolvedRoot
    target_codex_home = [IO.Path]::GetFullPath($TargetCodexHome)
    version = $Version
    previous_release = $previousReleaseId
    harness_plan_sha256 = $ExpectedHarnessPlanSha256.ToUpperInvariant()
    admin_pipe = $adminPipeName
    drain_timeout_seconds = $DrainTimeoutSeconds
    allow_undrainable_provider = [bool]$AllowUndrainableProvider
    tasks = @($taskNames)
    artifacts = @($approvals | Select-Object Component, Source, Expected, Actual)
    release_backed_up_files = @($replacedFiles | ForEach-Object { $_.Path })
    ordered_operations = @(
        'open release transaction and record replaced bytes and task definitions',
        'transactional config generation with marker last',
        'transactional harness installation',
        'drain the running provider and wait for active=0 and queued=0',
        'stop panel, Edge and Router only once drained',
        'atomic MCP, MCP admin, MCP inference, Edge, supervisor and tray replacement',
        'replace hidden limited-user scheduled task definitions',
        'ACL hardening and firewall egress policy',
        'publish the release manifest',
        'restart Router then Edge in finally',
        'ACL audit and online installation verification'
    )
    rollback = 'restores the recorded bytes and task definitions, then reports rolled-back or degraded'
    task_restart_guaranteed_after_cutover = $true
    starts_or_loads_model = $false
}

if (-not $Apply) {
    $plan | ConvertTo-Json -Depth 6
    Write-Host 'Preview only. No configuration, binary, ACL, firewall, task, process, or model state was changed.'
    return
}

# ---------------------------------------------------------------- phase 2: preparation

$completionMutex = [Threading.Mutex]::new($false, 'Local\CIA.LocalAI.V2.DeploymentCompletion')
$completionMutexAcquired = $false
$transaction = $null
$cutoverEntered = $false
$operationFailure = $null
$restartFailure = $null
$rollback = $null
$drain = $null
$deploymentStatus = 'installed'

try {
    try { $completionMutexAcquired = $completionMutex.WaitOne(0) }
    catch [Threading.AbandonedMutexException] { $completionMutexAcquired = $true }
    if (-not $completionMutexAcquired) {
        throw 'Another v2 deployment completion is already in progress.'
    }

    $transaction = New-V2ReleaseTransaction `
        -InstallRoot $resolvedRoot `
        -Environment $Environment `
        -Version $Version `
        -ReleaseId $ReleaseId `
        -RepositoryRoot (Get-V2RepoRoot)
    Add-V2ReleaseEvent -Transaction $transaction -Stage 'preflight' -Detail "approved $($approvals.Count) staged binaries"

    foreach ($entry in $replacedFiles) {
        Backup-V2ReleaseFile -Transaction $transaction -Path $entry.Path -Label $entry.Label
    }
    foreach ($taskName in $taskNames) {
        Backup-V2ReleaseTask -Transaction $transaction -TaskName $taskName
    }
    Save-V2ReleaseTaskDefinitions -Transaction $transaction
    Add-V2ReleaseEvent -Transaction $transaction -Stage 'backed-up' -Detail "recorded $($transaction.files.Count) file(s) and $($transaction.tasks.Count) task(s)"

    & (Join-Path $PSScriptRoot 'New-V2Config.ps1') -Environment $Environment -OutputRoot $resolvedRoot -Apply | Out-Host
    Add-V2ReleaseEvent -Transaction $transaction -Stage 'configured' -Detail 'generated configuration and launchers'

    & (Join-Path $PSScriptRoot 'Install-V2Harness.ps1') `
        -Environment $Environment `
        -InstallRoot $resolvedRoot `
        -TargetCodexHome $TargetCodexHome `
        -ExpectedPlanSha256 $ExpectedHarnessPlanSha256 `
        -Apply `
        -Replace | Out-Host
    Add-V2ReleaseEvent -Transaction $transaction -Stage 'harness-installed' -Detail 'installed the reviewed harness transaction'

    # ------------------------------------------------------------ phase 3: drain

    $drain = Wait-V2ProviderDrain `
        -ControlAddress $settings.ControlAddress `
        -PipeName $adminPipeName `
        -TimeoutSeconds $DrainTimeoutSeconds
    Add-V2ReleaseEvent -Transaction $transaction -Stage 'drain' -Detail "$($drain.reason) over $($drain.transport) after $($drain.waited_seconds)s (active=$($drain.active) queued=$($drain.queued))"

    if (-not $drain.supported) {
        if (-not $AllowUndrainableProvider) {
            throw "The installed provider has no drain API, so stopping it could interrupt an in-flight generation. Re-run with -AllowUndrainableProvider to accept that for this one deployment; afterwards every deployment drains first."
        }
        Write-Warning 'The installed provider has no drain API. Proceeding by explicit operator decision; an in-flight request may be interrupted.'
    }
    elseif (-not $drain.drained) {
        [void](Resume-V2Provider -ControlAddress $settings.ControlAddress -PipeName $adminPipeName)
        throw "The provider did not drain within $DrainTimeoutSeconds seconds (active=$($drain.active), queued=$($drain.queued)). No binary was replaced and the provider was resumed."
    }

    # ------------------------------------------------------------ phase 3: cutover

    $cutoverEntered = $true
    Add-V2ReleaseEvent -Transaction $transaction -Stage 'cutover' -Detail 'stopping the panel, Edge and Router'

    $installedTray = Join-Path $binRoot 'cia-tray.exe'
    Get-Process -Name 'cia-tray' -ErrorAction SilentlyContinue | Where-Object {
        try { [string]::Equals($_.Path, $installedTray, [StringComparison]::OrdinalIgnoreCase) }
        catch { $false }
    } | Stop-Process -ErrorAction Stop
    Stop-ScheduledTask -TaskName $edgeTaskName -ErrorAction Stop
    Stop-ScheduledTask -TaskName $routerTaskName -ErrorAction Stop

    if (-not (Wait-V2ServingStopped -InstallRoot $resolvedRoot -Ports $ports -TimeoutSeconds 30)) {
        throw "$Environment tasks did not release their provider processes and loopback listeners within 30 seconds; binaries were not replaced."
    }

    Get-Process -Name 'cia-mcp', 'cia-mcp-admin', 'cia-mcp-inference' -ErrorAction SilentlyContinue | Where-Object {
        try { $_.Path.StartsWith($binRoot, [StringComparison]::OrdinalIgnoreCase) }
        catch { $false }
    } | Stop-Process -ErrorAction Stop

    foreach ($component in @('Mcp', 'McpAdmin', 'McpInference', 'Edge', 'Supervisor')) {
        $approval = @($approvals | Where-Object { $_.Component -eq $component })[0]
        & (Join-Path $PSScriptRoot 'Install-V2Binary.ps1') `
            -Component $component `
            -Environment $Environment `
            -SourceBinary $approval.Source `
            -ExpectedSha256 $approval.Expected `
            -InstallRoot $resolvedRoot `
            -Apply `
            -Replace | Out-Host
        Add-V2ReleaseComponent -Transaction $transaction -Name $approval.Binary -Path (Join-Path $binRoot $approval.Binary) -Sha256 $approval.Expected
    }

    $trayApproval = @($approvals | Where-Object { $_.Component -eq 'Tray' })[0]
    & (Join-Path $PSScriptRoot 'Install-V2Panel.ps1') `
        -Environment $Environment `
        -SourceBinary $trayApproval.Source `
        -ExpectedSha256 $trayApproval.Expected `
        -InstallRoot $resolvedRoot `
        -Apply `
        -Replace | Out-Host
    Add-V2ReleaseComponent -Transaction $transaction -Name $trayApproval.Binary -Path (Join-Path $binRoot $trayApproval.Binary) -Sha256 $trayApproval.Expected
    Add-V2ReleaseEvent -Transaction $transaction -Stage 'binaries-installed' -Detail "replaced $($approvals.Count) application binaries"

    & (Join-Path $PSScriptRoot 'Install-V2ScheduledTasks.ps1') `
        -Environment $Environment `
        -InstallRoot $resolvedRoot `
        -Apply `
        -Replace | Out-Host
    Add-V2ReleaseEvent -Transaction $transaction -Stage 'tasks-installed' -Detail "replaced $($taskNames.Count) scheduled task definitions"

    & (Join-Path $PSScriptRoot 'Set-V2Acl.ps1') -InstallRoot $resolvedRoot -Apply | Out-Host
    & (Join-Path $PSScriptRoot 'Set-V2Firewall.ps1') -InstallRoot $resolvedRoot -Apply | Out-Host
    Add-V2ReleaseEvent -Transaction $transaction -Stage 'hardened' -Detail 'applied the ACL and firewall egress policy'

    # Record the artifacts this release actually serves, taken from the installed
    # manifest rather than the source tree.
    $installedManifest = Read-V2Manifest -Path (Join-Path $configRoot 'models.yaml')
    foreach ($model in @($installedManifest.models | Where-Object { $_.deployments -contains $environmentName })) {
        Add-V2ReleaseArtifact -Transaction $transaction -Kind 'model' -Id ([string]$model.id) -Path ([string]$model.artifact.path) -Sha256 ([string]$model.artifact.sha256) -Bytes ([long]$model.artifact.bytes)
        $runtime = @($installedManifest.runtimes | Where-Object { $_.id -eq $model.runtime })[0]
        if (@($transaction.artifacts | Where-Object { $_.kind -eq 'runtime' -and $_.id -eq [string]$runtime.id }).Count -eq 0) {
            Add-V2ReleaseArtifact -Transaction $transaction -Kind 'runtime' -Id ([string]$runtime.id) -Path ([string]$runtime.artifact.path) -Sha256 ([string]$runtime.artifact.sha256) -Bytes ([long]$runtime.artifact.bytes)
        }
    }
}
catch {
    $operationFailure = $_
}

# ---------------------------------------------------------------- phase 4: unwind

if ($operationFailure -and $cutoverEntered -and $null -ne $transaction) {
    # The cutover was interrupted after it started replacing installed state.
    # Restoring the recorded bytes and task definitions is the only way back to a
    # describable installation.
    Add-V2ReleaseEvent -Transaction $transaction -Stage 'rolling-back' -Detail $operationFailure.Exception.Message
    $rollback = Restore-V2Release -Transaction $transaction
    $deploymentStatus = 'rolled-back'
    if (-not $rollback.succeeded) {
        $deploymentStatus = 'degraded'
    }
}
elseif ($operationFailure -and $null -ne $transaction) {
    # The failure happened before the cutover. Configuration and harness files
    # were already replaced, so they are restored too, but nothing was stopped.
    Add-V2ReleaseEvent -Transaction $transaction -Stage 'rolling-back' -Detail $operationFailure.Exception.Message
    $rollback = Restore-V2Release -Transaction $transaction
    $deploymentStatus = 'rolled-back'
    if (-not $rollback.succeeded) {
        $deploymentStatus = 'degraded'
    }
}

if ($null -ne $transaction) {
    # The release manifest must be on disk before the Edge restarts: the
    # supervisor passes --release-manifest only when the file exists at spawn.
    [void](Complete-V2ReleaseTransaction -Transaction $transaction -Status $deploymentStatus)
}

if ($cutoverEntered) {
    try {
        Start-ScheduledTask -TaskName $routerTaskName -ErrorAction Stop
        $routerDeadline = (Get-Date).AddSeconds(30)
        $routerPort = [int]($settings.RouterAddress.Split(':')[-1])
        do {
            $routerListener = @(Get-NetTCPConnection -State Listen -LocalPort $routerPort -ErrorAction SilentlyContinue)
            if ($routerListener.Count -eq 0) { Start-Sleep -Milliseconds 250 }
        } while ($routerListener.Count -eq 0 -and (Get-Date) -lt $routerDeadline)
        if ($routerListener.Count -eq 0) {
            throw "$Environment Router did not become ready within 30 seconds."
        }

        Start-ScheduledTask -TaskName $edgeTaskName -ErrorAction Stop
        $edgeDeadline = (Get-Date).AddSeconds(30)
        $live = $null
        do {
            try {
                $live = Invoke-WebRequest -UseBasicParsing -Uri "http://$($settings.ControlAddress)/livez" -TimeoutSec 2
            }
            catch {
                $live = $null
                Start-Sleep -Milliseconds 250
            }
        } while ($null -eq $live -and (Get-Date) -lt $edgeDeadline)
        if ($null -eq $live -or $live.StatusCode -ne 200) {
            throw "$Environment Edge did not become live within 30 seconds."
        }
    }
    catch {
        $restartFailure = $_
    }
}

if ($completionMutexAcquired) {
    [void]$completionMutex.ReleaseMutex()
}
$completionMutex.Dispose()

# ---------------------------------------------------------------- phase 5: outcome

if ($null -ne $transaction) {
    $summary = [pscustomobject]@{
        release_id = $transaction.release_id
        environment = $environmentName
        version = $Version
        previous_release = $transaction.previous_release_id
        status = $deploymentStatus
        drain = $drain
        rollback = $rollback
        restart_failed = ($null -ne $restartFailure)
        release_manifest = (Get-V2ReleaseManifestPath -InstallRoot $resolvedRoot -Environment $Environment)
        transaction_journal = (Join-Path $transaction.directory 'transaction.json')
    }
    $summary | ConvertTo-Json -Depth 6
}

if ($operationFailure) {
    if ($null -eq $transaction) {
        # The transaction never opened, so nothing was mutated and there is
        # nothing to roll back. Saying otherwise would send an operator looking
        # for a recovery record that does not exist.
        throw "$Environment deployment failed before it changed anything. Error: $($operationFailure.Exception.Message)"
    }
    if ($deploymentStatus -eq 'degraded') {
        throw "DEGRADED: $Environment deployment failed and the rollback did not fully restore the previous release. Deployment error: $($operationFailure.Exception.Message). Rollback failures: $($rollback.failures -join ' | '). Recovery record: $(Join-Path $transaction.directory 'transaction.json')"
    }
    if ($restartFailure) {
        throw "$Environment deployment failed and was rolled back, and the service restart also failed. Deployment error: $($operationFailure.Exception.Message). Restart error: $($restartFailure.Exception.Message)"
    }
    throw "$Environment deployment failed and was rolled back to the previous release. Error: $($operationFailure.Exception.Message)"
}
if ($restartFailure) {
    throw $restartFailure
}

& (Join-Path $PSScriptRoot 'Set-V2Acl.ps1') -InstallRoot $resolvedRoot -Audit | Out-Host
& (Join-Path $PSScriptRoot 'Test-V2Installation.ps1') -Environment $Environment -InstallRoot $resolvedRoot -Online
Write-Host "$Environment deployment $($transaction.release_id) succeeded: configuration, harnesses, six application binaries, ACL apply/audit, firewall, tasks, and online checks are consistent. Reopen the panel through its normal-user Startup shortcut."
