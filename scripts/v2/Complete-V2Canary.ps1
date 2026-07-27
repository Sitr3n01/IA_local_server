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
    [switch]$Apply,
    [switch]$Replace
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Common.ps1')

$environment = 'Canary'
$settings = Get-V2DeploymentSettings -Environment $environment
$expectedRoot = [IO.Path]::GetFullPath('C:\IA\local-ai-v2').TrimEnd([char[]]@('\', '/'))
$resolvedRoot = [IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))
if (-not [string]::Equals($resolvedRoot, $expectedRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Canary completion is restricted to '$expectedRoot'."
}
if ($Apply -and -not (Test-V2IsAdministrator)) {
    throw 'Canary completion requires an elevated PowerShell. Run the preview normally, then run Apply in an elevated shell.'
}
if ($Apply -and -not $Replace) {
    throw 'Canary completion requires explicit -Replace because this machine already has a v2 canary installation.'
}

$approvals = @(
    [pscustomobject]@{ Component = 'Edge'; Binary = 'cia-edge.exe'; Expected = $ExpectedEdgeSha256.ToUpperInvariant() },
    [pscustomobject]@{ Component = 'Mcp'; Binary = 'cia-mcp.exe'; Expected = $ExpectedMcpSha256.ToUpperInvariant() },
    [pscustomobject]@{ Component = 'McpAdmin'; Binary = 'cia-mcp-admin.exe'; Expected = $ExpectedMcpAdminSha256.ToUpperInvariant() },
    [pscustomobject]@{ Component = 'McpInference'; Binary = 'cia-mcp-inference.exe'; Expected = $ExpectedMcpInferenceSha256.ToUpperInvariant() },
    [pscustomobject]@{ Component = 'Supervisor'; Binary = 'cia-supervisor.exe'; Expected = $ExpectedSupervisorSha256.ToUpperInvariant() },
    [pscustomobject]@{ Component = 'Tray'; Binary = 'cia-tray.exe'; Expected = $ExpectedTraySha256.ToUpperInvariant() }
)
foreach ($approval in $approvals) {
    $approval | Add-Member -NotePropertyName Source -NotePropertyValue (Join-Path $resolvedRoot "state\staging\$($approval.Binary)")
    if (-not (Test-Path -LiteralPath $approval.Source -PathType Leaf)) {
        throw "Approved staging binary is missing: $($approval.Source)"
    }
    $actual = (Get-FileHash -LiteralPath $approval.Source -Algorithm SHA256).Hash
    $approval | Add-Member -NotePropertyName Actual -NotePropertyValue $actual
    if ($actual -ne $approval.Expected) {
        throw "Staging hash for '$($approval.Component)' does not match its independently approved SHA-256. Expected $($approval.Expected); found $actual."
    }
}

$routerTaskName = 'CIA Local AI v2 Canary Router'
$edgeTaskName = 'CIA Local AI v2 Canary Edge'
$missingTasks = @(@($routerTaskName, $edgeTaskName) | Where-Object {
        $null -eq (Get-ScheduledTask -TaskName $_ -ErrorAction SilentlyContinue)
    })
if ($missingTasks.Count -gt 0) {
    throw "Required canary scheduled task(s) are missing: $($missingTasks -join ', ')"
}

$completionPlan = [pscustomobject]@{
    mode = if ($Apply) { 'apply' } else { 'preview' }
    environment = 'canary'
    install_root = $resolvedRoot
    target_codex_home = [IO.Path]::GetFullPath($TargetCodexHome)
    harness_plan_sha256 = $ExpectedHarnessPlanSha256.ToUpperInvariant()
    artifacts = @($approvals | Select-Object Component, Source, Expected, Actual)
    ordered_operations = @(
        'transactional config generation with marker last',
        'transactional harness installation',
        'atomic MCP, MCP admin and MCP inference installation',
        'stop panel, canary Edge and Router',
        'atomic Edge, supervisor and tray replacement',
        'replace hidden limited-user scheduled task definitions',
        'ACL hardening and firewall egress policy',
        'restart Router then Edge in finally',
        'online installation verification'
    )
    task_restart_guaranteed_after_cutover = $true
    starts_or_loads_model = $false
}
if (-not $Apply) {
    $completionPlan | ConvertTo-Json -Depth 6
    Write-Host 'Preview only. No configuration, binary, ACL, firewall, task, process, or model state was changed.'
    return
}

$completionMutex = [Threading.Mutex]::new($false, 'Local\CIA.LocalAI.V2.CanaryCompletion')
$completionMutexAcquired = $false
$cutoverEntered = $false
$operationFailure = $null
$restartFailure = $null
try {
    try {
        $completionMutexAcquired = $completionMutex.WaitOne(0)
    }
    catch [Threading.AbandonedMutexException] {
        $completionMutexAcquired = $true
    }
    if (-not $completionMutexAcquired) {
        throw 'Another v2 canary completion is already in progress.'
    }

    & (Join-Path $PSScriptRoot 'New-V2Config.ps1') -Environment Canary -OutputRoot $resolvedRoot -Apply | Out-Host

    & (Join-Path $PSScriptRoot 'Install-V2Harness.ps1') `
        -Environment Canary `
        -InstallRoot $resolvedRoot `
        -TargetCodexHome $TargetCodexHome `
        -ExpectedPlanSha256 $ExpectedHarnessPlanSha256 `
        -Apply `
        -Replace | Out-Host

    # Nothing above interrupts the active provider. The cutover starts here,
    # only after every independent artifact and deployment preflight passed.
    $cutoverEntered = $true
    $installedTray = Join-Path $resolvedRoot 'bin\cia-tray.exe'
    Get-Process -Name 'cia-tray' -ErrorAction SilentlyContinue | Where-Object {
        try { [string]::Equals($_.Path, $installedTray, [StringComparison]::OrdinalIgnoreCase) }
        catch { $false }
    } | Stop-Process -ErrorAction Stop
    Stop-ScheduledTask -TaskName $edgeTaskName -ErrorAction Stop
    Stop-ScheduledTask -TaskName $routerTaskName -ErrorAction Stop

    $shutdownDeadline = (Get-Date).AddSeconds(30)
    do {
        $providerRunning = @(Get-Process -Name 'cia-edge', 'cia-supervisor' -ErrorAction SilentlyContinue | Where-Object {
                try { $_.Path.StartsWith((Join-Path $resolvedRoot 'bin'), [StringComparison]::OrdinalIgnoreCase) }
                catch { $false }
            }).Count -gt 0
        $portsListening = @(
            Get-NetTCPConnection -State Listen -LocalPort @(
                [int]($settings.RouterAddress.Split(':')[-1]),
                [int]($settings.DataAddress.Split(':')[-1]),
                [int]($settings.ControlAddress.Split(':')[-1])
            ) -ErrorAction SilentlyContinue
        ).Count -gt 0
        if ($providerRunning -or $portsListening) {
            Start-Sleep -Milliseconds 250
        }
    } while (($providerRunning -or $portsListening) -and (Get-Date) -lt $shutdownDeadline)
    if ($providerRunning -or $portsListening) {
        throw 'Canary tasks did not release their provider processes and loopback listeners within 30 seconds; binaries were not replaced.'
    }

    $protectedBin = Join-Path $resolvedRoot 'bin'
    Get-Process -Name 'cia-mcp', 'cia-mcp-admin', 'cia-mcp-inference' -ErrorAction SilentlyContinue | Where-Object {
        try { $_.Path.StartsWith($protectedBin, [StringComparison]::OrdinalIgnoreCase) }
        catch { $false }
    } | Stop-Process -ErrorAction Stop

    foreach ($component in @('Mcp', 'McpAdmin', 'McpInference', 'Edge', 'Supervisor')) {
        $approval = @($approvals | Where-Object { $_.Component -eq $component })[0]
        & (Join-Path $PSScriptRoot 'Install-V2Binary.ps1') `
            -Component $component `
            -Environment Canary `
            -SourceBinary $approval.Source `
            -ExpectedSha256 $approval.Expected `
            -InstallRoot $resolvedRoot `
            -Apply `
            -Replace | Out-Host
    }

    $trayApproval = @($approvals | Where-Object { $_.Component -eq 'Tray' })[0]
    & (Join-Path $PSScriptRoot 'Install-V2Panel.ps1') `
        -Environment Canary `
        -SourceBinary $trayApproval.Source `
        -ExpectedSha256 $trayApproval.Expected `
        -InstallRoot $resolvedRoot `
        -Apply `
        -Replace | Out-Host

    & (Join-Path $PSScriptRoot 'Install-V2ScheduledTasks.ps1') `
        -Environment Canary `
        -InstallRoot $resolvedRoot `
        -Apply `
        -Replace | Out-Host

    & (Join-Path $PSScriptRoot 'Set-V2Acl.ps1') -InstallRoot $resolvedRoot -Apply | Out-Host
    & (Join-Path $PSScriptRoot 'Set-V2Firewall.ps1') -InstallRoot $resolvedRoot -Apply | Out-Host
}
catch {
    $operationFailure = $_
}
finally {
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
                throw 'Canary Router did not become ready within 30 seconds.'
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
                throw 'Canary Edge did not become live within 30 seconds.'
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
}

if ($operationFailure) {
    if ($restartFailure) {
        throw "Canary completion failed and task recovery also failed. Completion error: $($operationFailure.Exception.Message). Recovery error: $($restartFailure.Exception.Message)"
    }
    throw $operationFailure
}
if ($restartFailure) {
    throw $restartFailure
}

& (Join-Path $PSScriptRoot 'Set-V2Acl.ps1') -InstallRoot $resolvedRoot -Audit | Out-Host
& (Join-Path $PSScriptRoot 'Test-V2Installation.ps1') -Environment Canary -InstallRoot $resolvedRoot -Online
Write-Host 'Canary completion succeeded: configuration, harnesses, six application binaries, ACL apply/audit, firewall, tasks, and online checks are consistent. Reopen the panel through its normal-user Startup shortcut.'
