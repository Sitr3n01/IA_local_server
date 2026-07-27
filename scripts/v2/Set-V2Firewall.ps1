[CmdletBinding()]
param(
    [string]$InstallRoot = 'C:\IA\local-ai-v2',
    [string]$ManifestPath = 'C:\IA\local-ai-v2\config\models.yaml',
    [string]$SchemaPath = 'C:\IA\local-ai-v2\config\models.schema.json',
    [string]$SchemaValidatorPath = 'C:\IA\local-ai-v2\bin\cia-manifest.exe',
    [string]$PythonPath = 'C:\Users\Sitr3n\AppData\Local\Programs\Python\Python313\python.exe',
    [switch]$Apply
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Common.ps1')

function Test-IsAdministrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

$resolvedRoot = (Resolve-Path -LiteralPath $InstallRoot -ErrorAction Stop).Path
Assert-V2ManifestSchema -ManifestPath $ManifestPath -SchemaPath $SchemaPath -ValidatorPath $SchemaValidatorPath
$manifest = Read-V2Manifest -Path $ManifestPath
Assert-V2ManifestSemantics -Manifest $manifest

$programs = [System.Collections.Generic.List[string]]::new()
Get-ChildItem -LiteralPath (Join-Path $resolvedRoot 'bin') -Filter '*.exe' -File | ForEach-Object {
    $programs.Add($_.FullName)
}
foreach ($runtime in @($manifest.runtimes)) {
    $programs.Add((Resolve-Path -LiteralPath $runtime.artifact.path -ErrorAction Stop).Path)
}
$programs = @($programs | Sort-Object -Unique)

$plannedRules = for ($index = 0; $index -lt $programs.Count; $index++) {
    [pscustomobject]@{
        name = ('CIA Local AI v2 Egress Block {0:D2}' -f ($index + 1))
        program = $programs[$index]
        direction = 'Outbound'
        action = 'Block'
        remote_address = 'Internet'
        profile = 'Any'
    }
}

$pythonInbound = @()
if (Test-Path -LiteralPath $PythonPath -PathType Leaf) {
    $resolvedPython = (Resolve-Path -LiteralPath $PythonPath).Path
    $pythonInbound = @(Get-NetFirewallRule -Direction Inbound -Action Allow -Enabled True -ErrorAction SilentlyContinue | Where-Object {
        $application = $_ | Get-NetFirewallApplicationFilter
        [string]::Equals($application.Program, $resolvedPython, [StringComparison]::OrdinalIgnoreCase)
    })
}

[pscustomobject]@{
    mode = $(if ($Apply) { 'apply' } else { 'preview' })
    group = 'CIA Local AI v2'
    egress_rules = $plannedRules
    python_inbound_rules_to_disable = @($pythonInbound | Select-Object Name, DisplayName, Profile)
} | ConvertTo-Json -Depth 6

if (-not $Apply) {
    Write-Host 'Preview only. Re-run from an elevated PowerShell with -Apply.'
    return
}
if (-not (Test-IsAdministrator)) {
    throw 'Firewall changes require an elevated PowerShell session.'
}

$guardId = [Guid]::NewGuid().ToString('N')
$guardGroup = "CIA Local AI v2 deployment guard $guardId"
$guardRules = [System.Collections.Generic.List[string]]::new()
$canonicalComplete = $false
try {
    # Install a complete temporary deny set before replacing the canonical
    # group. If any later operation fails, these guards remain enabled so a
    # deployment error cannot create an outbound-network window.
    for ($index = 0; $index -lt $plannedRules.Count; $index++) {
        $rule = $plannedRules[$index]
        $guardName = "CIA Local AI v2 Guard $guardId $('{0:D2}' -f ($index + 1))"
        New-NetFirewallRule `
            -Name $guardName `
            -DisplayName $guardName `
            -Group $guardGroup `
            -Direction Outbound `
            -Action Block `
            -Program $rule.program `
            -RemoteAddress Internet `
            -Profile Any `
            -Enabled True | Out-Null
        $guardRules.Add($guardName)
    }
    $installedGuards = @(Get-NetFirewallRule -Group $guardGroup -Enabled True -ErrorAction Stop)
    if ($installedGuards.Count -ne $plannedRules.Count) {
        throw "Firewall guard verification failed: expected $($plannedRules.Count), found $($installedGuards.Count)."
    }

    foreach ($rule in @(Get-NetFirewallRule -Group 'CIA Local AI v2' -ErrorAction SilentlyContinue)) {
        Remove-NetFirewallRule -Name $rule.Name -ErrorAction Stop
    }
    foreach ($rule in $plannedRules) {
        New-NetFirewallRule `
            -Name $rule.name `
            -DisplayName $rule.name `
            -Group 'CIA Local AI v2' `
            -Direction Outbound `
            -Action Block `
            -Program $rule.program `
            -RemoteAddress Internet `
            -Profile Any `
            -Enabled True | Out-Null
    }

    $installed = @(Get-NetFirewallRule -Group 'CIA Local AI v2' -Enabled True -ErrorAction Stop)
    if ($installed.Count -ne $plannedRules.Count) {
        throw "Firewall verification failed: expected $($plannedRules.Count) egress rules, found $($installed.Count)."
    }
    $installedPrograms = @($installed | ForEach-Object {
            ($_ | Get-NetFirewallApplicationFilter -ErrorAction Stop).Program
        } | Sort-Object -Unique)
    $expectedPrograms = @($programs | Sort-Object -Unique)
    if (($installedPrograms -join "`n") -ne ($expectedPrograms -join "`n")) {
        throw 'Firewall verification failed: installed program allowlist differs from the deployment plan.'
    }

    foreach ($rule in $pythonInbound) {
        Disable-NetFirewallRule -Name $rule.Name -ErrorAction Stop | Out-Null
    }
    $canonicalComplete = $true
}
catch {
    throw "Firewall apply failed; temporary outbound deny guards remain enabled in group '$guardGroup'. $($_.Exception.Message)"
}
finally {
    if ($canonicalComplete) {
        foreach ($guard in @(Get-NetFirewallRule -Group 'CIA Local AI v2 deployment guard *' -ErrorAction SilentlyContinue)) {
            Remove-NetFirewallRule -Name $guard.Name -ErrorAction SilentlyContinue
        }
    }
}

Write-Host "Applied and verified $($installed.Count) v2 egress blocks; disabled $($pythonInbound.Count) broad Python inbound rule(s)."
