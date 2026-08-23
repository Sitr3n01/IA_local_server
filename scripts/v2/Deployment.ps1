Set-StrictMode -Version Latest

<#
Deployment preflight and the maintenance handshake.

These sit between the release transaction and the deployment script: they are
what a deployment must establish *before* it mutates anything - that every
approved binary is present with the reviewed hash, that the environment's tasks
and generated configuration exist, and that the running provider has stopped
admitting work and finished what it already had.

Nothing in this file kills a request. A drain that does not complete is reported
as a refusal so the caller can abort before a single binary is replaced.
#>

function Get-V2DeploymentComponents {
    return @(
        [pscustomobject]@{ Component = 'Edge'; Binary = 'cia-edge.exe' },
        [pscustomobject]@{ Component = 'Mcp'; Binary = 'cia-mcp.exe' },
        [pscustomobject]@{ Component = 'McpAdmin'; Binary = 'cia-mcp-admin.exe' },
        [pscustomobject]@{ Component = 'McpInference'; Binary = 'cia-mcp-inference.exe' },
        [pscustomobject]@{ Component = 'Supervisor'; Binary = 'cia-supervisor.exe' },
        [pscustomobject]@{ Component = 'Tray'; Binary = 'cia-tray.exe' }
    )
}

function Get-V2DeploymentTaskNames {
    param(
        [Parameter(Mandatory = $true)]
        [ValidateSet('Canary', 'Final')]
        [string]$Environment
    )

    $prefix = "CIA Local AI v2 $Environment"
    return @("$prefix Router", "$prefix Edge")
}

function Get-V2AdminPipeName {
    param(
        [Parameter(Mandatory = $true)]
        [ValidateSet('Canary', 'Final')]
        [string]$Environment
    )

    # Must match adminpipe.DefaultName in the Go implementation. The environment
    # is part of the name so a canary and a final installation can never answer
    # each other's administrative commands.
    return ('\\.\pipe\cia-local-ai-admin-{0}' -f $Environment.ToLowerInvariant())
}

# Binds each approved SHA-256 to the staged bytes it authorizes. A missing
# staged binary, an unapproved component, or a hash that does not match is a
# refusal, never a warning.
function Resolve-V2DeploymentApprovals {
    param(
        [Parameter(Mandatory = $true)]
        [string]$StagingRoot,

        [Parameter(Mandatory = $true)]
        [hashtable]$Approvals
    )

    $components = Get-V2DeploymentComponents
    $expectedNames = @($components | ForEach-Object { $_.Component })
    foreach ($provided in @($Approvals.Keys)) {
        if ($provided -notin $expectedNames) {
            throw "Approval map contains an unknown component '$provided'."
        }
    }

    $resolved = [System.Collections.Generic.List[object]]::new()
    foreach ($component in $components) {
        if (-not $Approvals.ContainsKey($component.Component)) {
            throw "Approval map is missing component '$($component.Component)'."
        }
        $expected = [string]$Approvals[$component.Component]
        if ($expected -notmatch '^[A-Fa-f0-9]{64}$') {
            throw "Approved SHA-256 for '$($component.Component)' is not 64 hexadecimal characters."
        }
        $source = Join-Path $StagingRoot $component.Binary
        if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
            throw "Approved staging binary is missing: $source"
        }
        $actual = (Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash.ToUpperInvariant()
        if (-not [string]::Equals($actual, $expected.ToUpperInvariant(), [StringComparison]::OrdinalIgnoreCase)) {
            throw "Staging hash for '$($component.Component)' does not match its independently approved SHA-256. Expected $($expected.ToUpperInvariant()); found $actual."
        }
        $resolved.Add([pscustomobject]@{
                Component = $component.Component
                Binary = $component.Binary
                Source = $source
                Expected = $expected.ToUpperInvariant()
                Actual = $actual
            })
    }
    return @($resolved)
}

# Everything a deployment needs to already exist. Task discovery is injectable
# so the preflight can be exercised without registering real scheduled tasks.
function Assert-V2DeploymentPrerequisites {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot,

        [Parameter(Mandatory = $true)]
        [ValidateSet('Canary', 'Final')]
        [string]$Environment,

        [scriptblock]$TaskLookup = $null
    )

    if ($null -eq $TaskLookup) {
        $TaskLookup = {
            param($name)
            return ($null -ne (Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue))
        }
    }

    $root = [IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))
    $environmentName = $Environment.ToLowerInvariant()
    foreach ($required in @('bin', 'config', 'logs', 'state', 'launchers')) {
        $path = Join-Path $root $required
        if (-not (Test-Path -LiteralPath $path -PathType Container)) {
            throw "Required v2 directory is missing: $path"
        }
    }
    foreach ($required in @(
            (Join-Path $root ("config\llama-swap.{0}.yaml" -f $environmentName)),
            (Join-Path $root ("launchers\router-{0}.vbs" -f $environmentName)),
            (Join-Path $root ("launchers\edge-{0}.vbs" -f $environmentName))
        )) {
        if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
            throw "Required generated file is missing: $required. Run New-V2Config.ps1 -Environment $Environment -Apply first."
        }
    }

    $missingTasks = @(@(Get-V2DeploymentTaskNames -Environment $Environment) | Where-Object { -not (& $TaskLookup $_) })
    if ($missingTasks.Count -gt 0) {
        throw "Required $environmentName scheduled task(s) are missing: $($missingTasks -join ', '). Register them with Install-V2ScheduledTasks.ps1 before a deployment."
    }
    return $true
}

# One administrative mutation over the DACL-protected named pipe. No credential
# is sent: the pipe's DACL is the authentication. Returns $null when nothing is
# serving the pipe, so a caller can decide whether to fall back.
function Invoke-V2AdminPipeOperation {
    param(
        [Parameter(Mandatory = $true)]
        [string]$PipeName,

        [Parameter(Mandatory = $true)]
        [string]$Operation,

        [string]$ModelId = '',

        [int]$TimeoutMilliseconds = 5000
    )

    $prefix = '\\.\pipe\'
    $leaf = $PipeName
    if ($leaf.StartsWith($prefix)) {
        $leaf = $leaf.Substring($prefix.Length)
    }

    $request = [ordered]@{ operation = $Operation }
    if (-not [string]::IsNullOrWhiteSpace($ModelId)) { $request['model_id'] = $ModelId }
    $payload = ($request | ConvertTo-Json -Compress) + "`n"

    # Identification impersonation level: the server may learn who is calling and
    # nothing more. It can never act as the operator against anything else.
    $client = [System.IO.Pipes.NamedPipeClientStream]::new(
        '.',
        $leaf,
        [System.IO.Pipes.PipeDirection]::InOut,
        [System.IO.Pipes.PipeOptions]::None,
        [System.Security.Principal.TokenImpersonationLevel]::Identification)
    try {
        try {
            $client.Connect($TimeoutMilliseconds)
        }
        catch {
            return $null
        }
        $bytes = [Text.Encoding]::UTF8.GetBytes($payload)
        $client.Write($bytes, 0, $bytes.Length)
        $client.Flush()

        $reader = [IO.StreamReader]::new($client, [Text.Encoding]::UTF8)
        $line = $reader.ReadLine()
        if ([string]::IsNullOrWhiteSpace($line)) {
            throw "Administrative pipe '$PipeName' closed without a response."
        }
        $response = $line | ConvertFrom-Json
        if (-not $response.ok) {
            $code = 'unknown'
            if ($null -ne $response.PSObject.Properties['error'] -and $null -ne $response.error) {
                $code = [string]$response.error.code
            }
            throw "Administrative operation '$Operation' was refused: $code"
        }
        return $response.result
    }
    finally {
        $client.Dispose()
    }
}

# Reads the public, unauthenticated status document. Nothing here requires or
# handles a credential.
function Get-V2ProviderStatus {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ControlAddress,

        [int]$TimeoutSeconds = 5
    )

    try {
        $response = Invoke-WebRequest -UseBasicParsing -Uri "http://$ControlAddress/api/v1/status" -TimeoutSec $TimeoutSeconds
    }
    catch {
        return $null
    }
    if ($response.StatusCode -ne 200) {
        return $null
    }
    try {
        return ($response.Content | ConvertFrom-Json)
    }
    catch {
        return $null
    }
}

# Issues an administrative maintenance verb, preferring the pipe. The HTTP path
# is the documented compatibility fallback and is the only one that touches the
# administrative credential; it is read into a local variable, used once, and
# released immediately. It is never written to a file, a log, or a command line.
function Invoke-V2MaintenanceOperation {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ControlAddress,

        [Parameter(Mandatory = $true)]
        [string]$PipeName,

        [Parameter(Mandatory = $true)]
        [ValidateSet('drain', 'resume')]
        [string]$Operation,

        [string]$CredentialHelper = 'C:\IA\local-ai-v2\bin\cia-credential.exe'
    )

    $result = Invoke-V2AdminPipeOperation -PipeName $PipeName -Operation ("maintenance.{0}" -f $Operation)
    if ($null -ne $result) {
        return [pscustomobject]@{ transport = 'named-pipe'; succeeded = $true }
    }

    if (-not (Test-Path -LiteralPath $CredentialHelper -PathType Leaf)) {
        throw "Cannot $Operation the provider: neither the administrative pipe nor the credential helper is available."
    }
    $adminToken = (& $CredentialHelper get admin 2>$null | Out-String).Trim()
    try {
        if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($adminToken)) {
            throw "Unable to obtain the administrative credential for the $Operation request."
        }
        $response = Invoke-WebRequest -UseBasicParsing `
            -Method Post `
            -Uri "http://$ControlAddress/api/v1/maintenance:$Operation" `
            -Headers @{ Authorization = "Bearer $adminToken" } `
            -TimeoutSec 10
        if ($response.StatusCode -ne 200) {
            throw "The $Operation request returned HTTP $($response.StatusCode)."
        }
        return [pscustomobject]@{ transport = 'http-deprecated'; succeeded = $true }
    }
    finally {
        $adminToken = $null
    }
}

<#
Asks the running provider to stop admitting new inference and waits until the
work already in the system has finished.

A drain never cancels anything. If the wait expires the caller aborts the
deployment before any binary is replaced and resumes the provider; the
alternative would be killing an in-flight generation without saying so.

Three outcomes are distinguished on purpose:
 - drained: nothing is active or queued and new work is refused.
 - provider-not-running: there is nothing to drain (a first installation).
 - provider-has-no-drain-support: the installed edge predates the drain API.
   The caller must decide explicitly; this function never assumes it is safe.
#>
function Wait-V2ProviderDrain {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ControlAddress,

        [Parameter(Mandatory = $true)]
        [string]$PipeName,

        [int]$TimeoutSeconds = 300,

        [int]$PollMilliseconds = 500,

        [string]$CredentialHelper = 'C:\IA\local-ai-v2\bin\cia-credential.exe'
    )

    $status = Get-V2ProviderStatus -ControlAddress $ControlAddress
    if ($null -eq $status) {
        return [pscustomobject]@{
            drained = $true
            supported = $true
            reason = 'provider-not-running'
            transport = 'none'
            active = 0
            queued = 0
            waited_seconds = 0
        }
    }
    if ($null -eq $status.PSObject.Properties['maintenance']) {
        return [pscustomobject]@{
            drained = $false
            supported = $false
            reason = 'provider-has-no-drain-support'
            transport = 'none'
            active = 0
            queued = 0
            waited_seconds = 0
        }
    }

    $issued = Invoke-V2MaintenanceOperation `
        -ControlAddress $ControlAddress `
        -PipeName $PipeName `
        -Operation 'drain' `
        -CredentialHelper $CredentialHelper

    $started = Get-Date
    $deadline = $started.AddSeconds($TimeoutSeconds)
    do {
        $status = Get-V2ProviderStatus -ControlAddress $ControlAddress
        if ($null -eq $status) {
            # The provider went away while draining. Nothing is in flight.
            return [pscustomobject]@{
                drained = $true
                supported = $true
                reason = 'provider-stopped-while-draining'
                transport = $issued.transport
                active = 0
                queued = 0
                waited_seconds = [int]((Get-Date) - $started).TotalSeconds
            }
        }
        $maintenance = $status.maintenance
        if ([bool]$maintenance.drained) {
            return [pscustomobject]@{
                drained = $true
                supported = $true
                reason = 'drained'
                transport = $issued.transport
                active = [int]$maintenance.active
                queued = [int]$maintenance.queued
                waited_seconds = [int]((Get-Date) - $started).TotalSeconds
            }
        }
        Start-Sleep -Milliseconds $PollMilliseconds
    } while ((Get-Date) -lt $deadline)

    $maintenance = $status.maintenance
    return [pscustomobject]@{
        drained = $false
        supported = $true
        reason = 'drain-timeout'
        transport = $issued.transport
        active = [int]$maintenance.active
        queued = [int]$maintenance.queued
        waited_seconds = [int]((Get-Date) - $started).TotalSeconds
    }
}

# Best-effort return to normal admission after an aborted deployment. A failure
# is reported rather than thrown: the caller is already unwinding, and a
# provider left draining is visible in /api/v1/status and cleared by a restart.
function Resume-V2Provider {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ControlAddress,

        [Parameter(Mandatory = $true)]
        [string]$PipeName,

        [string]$CredentialHelper = 'C:\IA\local-ai-v2\bin\cia-credential.exe'
    )

    try {
        [void](Invoke-V2MaintenanceOperation `
                -ControlAddress $ControlAddress `
                -PipeName $PipeName `
                -Operation 'resume' `
                -CredentialHelper $CredentialHelper)
        return $true
    }
    catch {
        return $false
    }
}

# Waits until the environment's serving processes have exited and released their
# loopback listeners. Replacing a binary that is still mapped would fail; doing
# it while a listener is up would race a restart.
function Wait-V2ServingStopped {
    param(
        [Parameter(Mandatory = $true)]
        [string]$InstallRoot,

        [Parameter(Mandatory = $true)]
        [int[]]$Ports,

        [int]$TimeoutSeconds = 30
    )

    $binRoot = Join-Path ([IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))) 'bin'
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $running = @(Get-Process -Name 'cia-edge', 'cia-supervisor' -ErrorAction SilentlyContinue | Where-Object {
                try { $_.Path.StartsWith($binRoot, [StringComparison]::OrdinalIgnoreCase) }
                catch { $false }
            }).Count -gt 0
        $listening = @(Get-NetTCPConnection -State Listen -LocalPort $Ports -ErrorAction SilentlyContinue).Count -gt 0
        if ($running -or $listening) {
            Start-Sleep -Milliseconds 250
        }
    } while (($running -or $listening) -and (Get-Date) -lt $deadline)

    return (-not ($running -or $listening))
}
