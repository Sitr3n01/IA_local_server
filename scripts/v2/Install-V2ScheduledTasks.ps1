[CmdletBinding()]
param(
    [ValidateSet('Canary', 'Final')]
    [string]$Environment = 'Canary',
    [string]$InstallRoot = 'C:\IA\local-ai-v2',
    [string]$ManifestPath = 'C:\IA\local-ai-v2\config\models.yaml',
    [string]$UserId = "$env:USERDOMAIN\$env:USERNAME",
    [switch]$Apply,
    [switch]$Replace
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Common.ps1')
$settings = Get-V2DeploymentSettings -Environment $Environment
$routerLauncher = Join-Path $InstallRoot ("launchers\router-{0}.vbs" -f $settings.Name)
$edgeLauncher = Join-Path $InstallRoot ("launchers\edge-{0}.vbs" -f $settings.Name)
$supervisor = Join-Path $InstallRoot 'bin\cia-supervisor.exe'
$routerConfig = Join-Path $InstallRoot ("config\llama-swap.{0}.yaml" -f $settings.Name)
$routerLog = Join-Path $InstallRoot 'logs\cia-router-process.log'
$edgeLog = Join-Path $InstallRoot 'logs\cia-edge-process.log'
$routerArguments = '--component router --install-root "{0}" --router-config "{1}" --router-addr {2} --process-log "{3}"' -f $InstallRoot, $routerConfig, $settings.RouterAddress, $routerLog
$edgeArguments = '--component edge --environment {6} --install-root "{0}" --router-addr {1} --data-addr {2} --control-addr {3} --upstream "http://{1}" --models-config "{4}" --process-log "{5}"' -f $InstallRoot, $settings.RouterAddress, $settings.DataAddress, $settings.ControlAddress, $ManifestPath, $edgeLog, $settings.Name
$taskPrefix = "CIA Local AI v2 $Environment"
$definitions = @(
    [pscustomobject]@{ Name = "$taskPrefix Router"; Launcher = $routerLauncher; Executable = $supervisor; Arguments = $routerArguments },
    [pscustomobject]@{ Name = "$taskPrefix Edge"; Launcher = $edgeLauncher; Executable = $supervisor; Arguments = $edgeArguments }
)

$existing = @()
foreach ($definition in $definitions) {
    $task = Get-ScheduledTask -TaskName $definition.Name -ErrorAction SilentlyContinue
    if ($task) { $existing += $definition.Name }
}

[pscustomobject]@{
    mode = $(if ($Apply) { 'apply' } else { 'preview' })
    environment = $settings.Name
    user = $UserId
    tasks = @($definitions.Name)
    launchers = @($definitions.Launcher)
    executable = $supervisor
    containment = 'windows-job-object-kill-on-close'
    existing = $existing
} | ConvertTo-Json -Depth 4

if (-not $Apply) {
    Write-Host 'Preview only. No scheduled task was created or changed.'
    exit 0
}

if ($existing.Count -gt 0 -and -not $Replace) {
    throw "Tasks already exist: $($existing -join ', '). Re-run with -Replace only after reviewing them."
}
if (-not (Test-Path -LiteralPath $supervisor -PathType Leaf)) {
    throw "Supervisor executable is missing: $supervisor"
}
if (-not (Test-Path -LiteralPath $routerConfig -PathType Leaf)) {
    throw "Generated router config is missing: $routerConfig"
}
if (-not (Test-Path -LiteralPath $ManifestPath -PathType Leaf)) {
    throw "Model manifest is missing: $ManifestPath"
}

$trigger = New-ScheduledTaskTrigger -AtLogOn -User $UserId
$principal = New-ScheduledTaskPrincipal -UserId $UserId -LogonType Interactive -RunLevel Limited
$settingsSet = New-ScheduledTaskSettingsSet `
    -Hidden `
    -MultipleInstances IgnoreNew `
    -RestartCount 999 `
    -RestartInterval (New-TimeSpan -Minutes 1) `
    -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries

foreach ($definition in $definitions) {
    $action = New-ScheduledTaskAction `
        -Execute $definition.Executable `
        -Argument $definition.Arguments
    Register-ScheduledTask `
        -TaskName $definition.Name `
        -Action $action `
        -Trigger $trigger `
        -Principal $principal `
        -Settings $settingsSet `
        -Description "Local-only CIA AI v2 $($definition.Name.Split(' ')[-1]); generated from C:\IA\local-llama." `
        -Force:$Replace | Out-Null
}

Write-Host 'Tasks registered. They were not started by this script.'
