[CmdletBinding()]
param(
    [switch]$Quiet
)

<#
Self-test for the release transaction, the deployment preflight, and rollback.

The operator-facing deployment script pins the real installation root, requires
elevation, and stops real scheduled tasks, so it cannot run in CI. Everything it
relies on to be correct - the approval binding, the prerequisite checks, the
backup record, the restore, and the release manifest lifecycle - is exercised
here against a temporary root with injectable task discovery.
#>

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Common.ps1')

$script:Failures = [System.Collections.Generic.List[string]]::new()
$script:Passed = 0

function Test-Case {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][scriptblock]$Body
    )

    try {
        & $Body
        $script:Passed++
        if (-not $Quiet) { Write-Host "  ok   $Name" }
    }
    catch {
        $script:Failures.Add("$Name : $($_.Exception.Message)")
        Write-Host "  FAIL $Name : $($_.Exception.Message)"
    }
}

function Assert-Throws {
    param(
        [Parameter(Mandatory = $true)][scriptblock]$Body,
        [Parameter(Mandatory = $true)][string]$Match,
        [Parameter(Mandatory = $true)][string]$Because
    )

    $threw = $false
    try { & $Body }
    catch {
        $threw = $true
        if ($_.Exception.Message -notlike "*$Match*") {
            throw "$Because - refused with the wrong reason: $($_.Exception.Message)"
        }
    }
    if (-not $threw) { throw $Because }
}

function New-TestInstallation {
    param([string]$Environment = 'final')

    $root = Join-Path ([IO.Path]::GetTempPath()) ('cia-release-test-{0}' -f [Guid]::NewGuid().ToString('N'))
    foreach ($child in @('bin', 'config', 'logs', 'state', 'launchers', 'state\staging')) {
        New-Item -ItemType Directory -Path (Join-Path $root $child) -Force -ErrorAction Stop | Out-Null
    }
    foreach ($file in @(
            "config\llama-swap.$Environment.yaml",
            "launchers\router-$Environment.vbs",
            "launchers\edge-$Environment.vbs")) {
        [IO.File]::WriteAllText((Join-Path $root $file), "generated-$file", [Text.UTF8Encoding]::new($false))
    }
    return $root
}

function Write-TestFile {
    param([string]$Path, [string]$Content)
    $directory = Split-Path -Parent $Path
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) {
        New-Item -ItemType Directory -Path $directory -Force -ErrorAction Stop | Out-Null
    }
    [IO.File]::WriteAllText($Path, $Content, [Text.UTF8Encoding]::new($false))
}

function New-StagedBinaries {
    param([string]$Root)

    $staging = Join-Path $Root 'state\staging'
    $approvals = @{}
    foreach ($component in Get-V2DeploymentComponents) {
        $path = Join-Path $staging $component.Binary
        Write-TestFile -Path $path -Content ("staged-{0}" -f $component.Binary)
        $approvals[$component.Component] = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash
    }
    return $approvals
}

Write-Host 'Release transaction and deployment preflight'

# ---------------------------------------------------------------- approvals
Test-Case 'binds every approved hash to the staged bytes it authorizes' {
    $root = New-TestInstallation
    try {
        $approvals = New-StagedBinaries -Root $root
        $staging = Join-Path $root 'state\staging'

        $resolved = Resolve-V2DeploymentApprovals -StagingRoot $staging -Approvals $approvals
		if ($resolved.Count -ne 8) { throw "resolved $($resolved.Count) components, expected 8" }

        # A wrong hash is a refusal, not a warning.
        $wrong = @{} + $approvals
        $wrong['Edge'] = ('C' * 64)
        Assert-Throws -Because 'a wrong approved hash was accepted' -Match 'does not match its independently approved SHA-256' -Body {
            Resolve-V2DeploymentApprovals -StagingRoot $staging -Approvals $wrong
        }

        $malformed = @{} + $approvals
        $malformed['Edge'] = 'not-a-hash'
        Assert-Throws -Because 'a malformed approved hash was accepted' -Match '64 hexadecimal characters' -Body {
            Resolve-V2DeploymentApprovals -StagingRoot $staging -Approvals $malformed
        }

        $incomplete = @{} + $approvals
        $incomplete.Remove('Supervisor')
        Assert-Throws -Because 'a missing component approval was accepted' -Match "missing component 'Supervisor'" -Body {
            Resolve-V2DeploymentApprovals -StagingRoot $staging -Approvals $incomplete
        }

        $extra = @{} + $approvals
        $extra['Router'] = ('D' * 64)
        Assert-Throws -Because 'an unknown component approval was accepted' -Match "unknown component 'Router'" -Body {
            Resolve-V2DeploymentApprovals -StagingRoot $staging -Approvals $extra
        }

        Remove-Item -LiteralPath (Join-Path $staging 'cia-tray.exe') -Force
        Assert-Throws -Because 'a missing staged binary was accepted' -Match 'Approved staging binary is missing' -Body {
            Resolve-V2DeploymentApprovals -StagingRoot $staging -Approvals $approvals
        }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

# ---------------------------------------------------------------- prerequisites
Test-Case 'refuses a deployment whose configuration or tasks are missing' {
    $root = New-TestInstallation
    try {
        $allPresent = { param($name) return $true }
        $noneRegistered = { param($name) return $false }

        if (-not (Assert-V2DeploymentPrerequisites -InstallRoot $root -Environment Final -TaskLookup $allPresent)) {
            throw 'a complete installation was rejected'
        }

        Assert-Throws -Because 'missing scheduled tasks were accepted' -Match 'scheduled task(s) are missing' -Body {
            Assert-V2DeploymentPrerequisites -InstallRoot $root -Environment Final -TaskLookup $noneRegistered
        }

        Remove-Item -LiteralPath (Join-Path $root 'config\llama-swap.final.yaml') -Force
        Assert-Throws -Because 'a missing generated router config was accepted' -Match 'Required generated file is missing' -Body {
            Assert-V2DeploymentPrerequisites -InstallRoot $root -Environment Final -TaskLookup $allPresent
        }

        Remove-Item -LiteralPath (Join-Path $root 'launchers') -Recurse -Force
        Assert-Throws -Because 'a missing directory was accepted' -Match 'Required v2 directory is missing' -Body {
            Assert-V2DeploymentPrerequisites -InstallRoot $root -Environment Final -TaskLookup $allPresent
        }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

# ---------------------------------------------------------------- backup and restore
Test-Case 'restores replaced bytes and removes files the previous release never had' {
    $root = New-TestInstallation
    try {
        $existing = Join-Path $root 'bin\cia-edge.exe'
        Write-TestFile -Path $existing -Content 'release-one-edge'
        $originalHash = (Get-FileHash -LiteralPath $existing -Algorithm SHA256).Hash
        $introduced = Join-Path $root 'bin\cia-tray.exe'

        $transaction = New-V2ReleaseTransaction -InstallRoot $root -Environment Final -Version 'v2-final-test.1'
        Backup-V2ReleaseFile -Transaction $transaction -Path $existing -Label 'bin:edge'
        Backup-V2ReleaseFile -Transaction $transaction -Path $introduced -Label 'bin:tray'

        # The deployment replaces one file and introduces another.
        Write-TestFile -Path $existing -Content 'release-two-edge'
        Write-TestFile -Path $introduced -Content 'release-two-tray'

        $restore = Restore-V2Release -Transaction $transaction -TaskRestorer { param($n, $d) } -TaskRemover { param($n) }
        if (-not $restore.succeeded) { throw "restore reported failures: $($restore.failures -join ' | ')" }
        if ((Get-FileHash -LiteralPath $existing -Algorithm SHA256).Hash -ne $originalHash) {
            throw 'the replaced file was not restored to its previous bytes'
        }
        if (Test-Path -LiteralPath $introduced -PathType Leaf) {
            throw 'a file the previous release never had survived the rollback'
        }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

Test-Case 'reports a rollback that could not complete instead of hiding it' {
    $root = New-TestInstallation
    try {
        $target = Join-Path $root 'bin\cia-edge.exe'
        Write-TestFile -Path $target -Content 'release-one-edge'
        $transaction = New-V2ReleaseTransaction -InstallRoot $root -Environment Final
        Backup-V2ReleaseFile -Transaction $transaction -Path $target -Label 'bin:edge'
        Write-TestFile -Path $target -Content 'release-two-edge'

        # The recovery copy is destroyed, which is the only way a file restore
        # can fail once the record exists.
        Remove-Item -LiteralPath ($transaction.files[0].backup) -Force

        $restore = Restore-V2Release -Transaction $transaction -TaskRestorer { param($n, $d) } -TaskRemover { param($n) }
        if ($restore.succeeded) { throw 'a failed restore reported success' }
        if ($restore.failures.Count -ne 1) { throw "expected exactly one recorded failure, got $($restore.failures.Count)" }
        if ($transaction.stage -ne 'rollback-failed') { throw "transaction stage = $($transaction.stage)" }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

Test-Case 'skips unchanged files during a pre-cutover rollback' {
    $root = New-TestInstallation
    try {
        $target = Join-Path $root 'bin\cia-edge.exe'
        Write-TestFile -Path $target -Content 'release-one-edge'
        $transaction = New-V2ReleaseTransaction -InstallRoot $root -Environment Final
        Backup-V2ReleaseFile -Transaction $transaction -Path $target -Label 'bin:edge'

        # An unchanged target must not depend on a writable destination or even
        # on the backup copy. This is the normal pre-cutover failure case for a
        # running executable on Windows.
        Remove-Item -LiteralPath ($transaction.files[0].backup) -Force
        $restore = Restore-V2Release -Transaction $transaction -TaskRestorer { param($n, $d) } -TaskRemover { param($n) }
        if (-not $restore.succeeded) { throw "unchanged restore failed: $($restore.failures -join ' | ')" }
        if (@($restore.restored | Where-Object { $_ -like 'unchanged *' }).Count -ne 1) {
            throw "unchanged target was not reported as skipped: $($restore.restored -join ', ')"
        }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

Test-Case 'restores scheduled task definitions and unregisters tasks a release introduced' {
    $root = New-TestInstallation
    try {
        $transaction = New-V2ReleaseTransaction -InstallRoot $root -Environment Final
        Backup-V2ReleaseTask -Transaction $transaction -TaskName 'CIA Local AI v2 Final Router' -TaskExporter {
            param($name) return "<Task><Name>$name</Name></Task>"
        }
        Backup-V2ReleaseTask -Transaction $transaction -TaskName 'CIA Local AI v2 Final Edge' -TaskExporter {
            param($name) return $null
        }
        Save-V2ReleaseTaskDefinitions -Transaction $transaction

        $observedRestores = [System.Collections.Generic.List[string]]::new()
        $observedRemovals = [System.Collections.Generic.List[string]]::new()
        $restore = Restore-V2Release -Transaction $transaction `
            -TaskRestorer { param($n, $d) $observedRestores.Add("$n=$d") } `
            -TaskRemover { param($n) $observedRemovals.Add($n) }

        if (-not $restore.succeeded) { throw "restore reported failures: $($restore.failures -join ' | ')" }
        if ($observedRestores.Count -ne 1 -or $observedRestores[0] -notlike '*Router*') { throw "restored tasks: $($observedRestores -join ', ')" }
        if ($observedRemovals.Count -ne 1 -or $observedRemovals[0] -notlike '*Edge*') { throw "removed tasks: $($observedRemovals -join ', ')" }

        # A task restore that throws is recorded, not swallowed.
        $failing = New-V2ReleaseTransaction -InstallRoot $root -Environment Final
        Backup-V2ReleaseTask -Transaction $failing -TaskName 'CIA Local AI v2 Final Router' -TaskExporter {
            param($name) return "<Task><Name>$name</Name></Task>"
        }
        $failed = Restore-V2Release -Transaction $failing `
            -TaskRestorer { param($n, $d) throw 'the task scheduler refused the definition' } `
            -TaskRemover { param($n) }
        if ($failed.succeeded) { throw 'a failing task restore reported success' }
        if ($failed.failures[0] -notlike '*task scheduler refused*') { throw "unexpected failure text: $($failed.failures[0])" }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

# ---------------------------------------------------------------- release manifest
Test-Case 'publishes a release manifest on success and withdraws it on rollback' {
    $root = New-TestInstallation
    try {
        $manifestPath = Get-V2ReleaseManifestPath -InstallRoot $root -Environment Final

        $first = New-V2ReleaseTransaction -InstallRoot $root -Environment Final -Version 'v2-final-test.1'
        $firstRecord = Complete-V2ReleaseTransaction -Transaction $first -Status 'installed'
        if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { throw 'no release manifest was published' }
        $manifestBytes = [IO.File]::ReadAllBytes($manifestPath)
        if ($manifestBytes.Length -ge 3 -and $manifestBytes[0] -eq 0xEF -and $manifestBytes[1] -eq 0xBB -and $manifestBytes[2] -eq 0xBF) {
            throw 'release manifest contains a UTF-8 BOM that strict consumers reject'
        }
        if ($firstRecord.previous_release_id) { throw 'the first release claims a predecessor' }

        $installed = Get-V2InstalledRelease -InstallRoot $root -Environment Final
        if ($installed.release_id -ne $first.release_id) { throw 'the manifest names a different release' }
        if ($installed.status -ne 'installed') { throw "status = $($installed.status)" }

        $second = New-V2ReleaseTransaction -InstallRoot $root -Environment Final -Version 'v2-final-test.2'
        if ($second.previous_release_id -ne $first.release_id) { throw 'the second release did not record its predecessor' }

        # A rolled-back release must not leave a manifest naming itself. The
        # previous manifest is still on disk because the file restore put it back.
        Backup-V2ReleaseFile -Transaction $second -Path $manifestPath -Label 'config:release'
        Complete-V2ReleaseTransaction -Transaction $second -Status 'installed' | Out-Null
        $restore = Restore-V2Release -Transaction $second -TaskRestorer { param($n, $d) } -TaskRemover { param($n) }
        if (-not $restore.succeeded) { throw "restore failed: $($restore.failures -join ' | ')" }
        Complete-V2ReleaseTransaction -Transaction $second -Status 'rolled-back' | Out-Null

        $afterRollback = Get-V2InstalledRelease -InstallRoot $root -Environment Final
        if ($null -eq $afterRollback) { throw 'the rollback removed the restored previous manifest' }
        if ($afterRollback.release_id -ne $first.release_id) {
            throw "after rollback the manifest names '$($afterRollback.release_id)', expected '$($first.release_id)'"
        }

        $history = Get-V2ReleaseHistory -InstallRoot $root
        if ($history.Count -ne 2) { throw "history has $($history.Count) entries, expected 2" }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

Test-Case 'withdraws the manifest entirely when no previous release can be restored' {
    $root = New-TestInstallation
    try {
        $manifestPath = Get-V2ReleaseManifestPath -InstallRoot $root -Environment Final
        $only = New-V2ReleaseTransaction -InstallRoot $root -Environment Final -Version 'v2-final-test.1'
        Complete-V2ReleaseTransaction -Transaction $only -Status 'installed' | Out-Null
        if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { throw 'no release manifest was published' }

        Complete-V2ReleaseTransaction -Transaction $only -Status 'degraded' | Out-Null
        if (Test-Path -LiteralPath $manifestPath -PathType Leaf) {
            throw 'a degraded deployment left a manifest describing itself'
        }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

Test-Case 'the release record is deterministic, parseable, and carries no secret' {
    $root = New-TestInstallation
    try {
        $transaction = New-V2ReleaseTransaction -InstallRoot $root -Environment Final -Version 'v2-final-test.1' -RepositoryRoot (Get-V2RepoRoot)
        Write-TestFile -Path (Join-Path $root 'bin\cia-edge.exe') -Content 'edge-bytes'
        Add-V2ReleaseComponent -Transaction $transaction -Name 'cia-edge.exe' -Path (Join-Path $root 'bin\cia-edge.exe')
        Add-V2ReleaseArtifact -Transaction $transaction -Kind 'model' -Id 'demo-model' `
            -Path 'C:\IA\local-ai-v2\artifacts\models\demo-model\demo.gguf' -Sha256 ('E' * 64) -Bytes 1024
        Add-V2ReleaseEvent -Transaction $transaction -Stage 'cutover' -Detail 'replaced binaries'
        $record = Complete-V2ReleaseTransaction -Transaction $transaction -Status 'installed'

        $manifestPath = Get-V2ReleaseManifestPath -InstallRoot $root -Environment Final
        $text = Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8
        $parsed = $text | ConvertFrom-Json

        foreach ($field in @('schema_version', 'environment', 'release_id', 'version', 'commit', 'previous_release_id', 'status', 'created_utc')) {
            if ($null -eq $parsed.PSObject.Properties[$field]) { throw "release manifest is missing '$field'" }
        }
        if ($parsed.schema_version -ne 1) { throw "schema_version = $($parsed.schema_version)" }
        if ($parsed.environment -ne 'final') { throw "environment = $($parsed.environment)" }
        if ($parsed.release_id -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$') { throw "release_id is not a safe identifier: $($parsed.release_id)" }
        if ($parsed.commit -and $parsed.commit -notmatch '^[0-9a-f]{40}$') { throw "commit is not a full revision: $($parsed.commit)" }
        if ($parsed.components.Count -ne 1 -or $parsed.components[0].sha256 -notmatch '^[A-F0-9]{64}$') { throw 'component hashes are not recorded' }
        if ($record.status -ne 'installed') { throw "record status = $($record.status)" }

        foreach ($forbidden in @('token', 'Bearer', 'api_key', 'password', 'CIA_ADMIN', 'CIA_ROUTER', 'CIA_INFERENCE')) {
            if ($text -match "(?i)$forbidden") { throw "the release manifest mentions '$forbidden'" }
        }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

Test-Case 'a stored transaction can be reloaded and drives the same restore' {
    $root = New-TestInstallation
    try {
        $target = Join-Path $root 'bin\cia-edge.exe'
        Write-TestFile -Path $target -Content 'release-one-edge'
        $originalHash = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash

        $transaction = New-V2ReleaseTransaction -InstallRoot $root -Environment Final -Version 'v2-final-test.1'
        Backup-V2ReleaseFile -Transaction $transaction -Path $target -Label 'bin:edge'
        Backup-V2ReleaseTask -Transaction $transaction -TaskName 'CIA Local AI v2 Final Router' -TaskExporter {
            param($name) return "<Task><Name>$name</Name></Task>"
        }
        Save-V2ReleaseTaskDefinitions -Transaction $transaction
        Complete-V2ReleaseTransaction -Transaction $transaction -Status 'installed' | Out-Null
        Write-TestFile -Path $target -Content 'release-two-edge'

        $reloaded = Import-V2ReleaseTransaction -InstallRoot $root -ReleaseId $transaction.release_id
        if ($reloaded.files.Count -ne 1) { throw "reloaded $($reloaded.files.Count) file records" }
        if ($reloaded.tasks.Count -ne 1 -or $reloaded.tasks[0].definition -notlike '*<Task>*') { throw 'the task definition was not reloaded from the backup directory' }

        $restore = Restore-V2Release -Transaction $reloaded -TaskRestorer { param($n, $d) } -TaskRemover { param($n) }
        if (-not $restore.succeeded) { throw "restore failed: $($restore.failures -join ' | ')" }
        if ((Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash -ne $originalHash) { throw 'the reloaded transaction did not restore the previous bytes' }

        Assert-Throws -Because 'an unsafe release identifier was accepted' -Match 'not a safe identifier' -Body {
            Import-V2ReleaseTransaction -InstallRoot $root -ReleaseId '..\..\elsewhere'
        }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

Test-Case 'canary and final never share an administrative pipe or task name' {
    $canaryPipe = Get-V2AdminPipeName -Environment Canary
    $finalPipe = Get-V2AdminPipeName -Environment Final
    if ($canaryPipe -eq $finalPipe) { throw 'both environments resolved to the same pipe' }
    foreach ($pipe in @($canaryPipe, $finalPipe)) {
        if ($pipe -notlike '\\.\pipe\cia-local-ai-admin-*') { throw "unexpected pipe name: $pipe" }
    }
    $canaryTasks = Get-V2DeploymentTaskNames -Environment Canary
    $finalTasks = Get-V2DeploymentTaskNames -Environment Final
    if (@($canaryTasks | Where-Object { $_ -in $finalTasks }).Count -gt 0) { throw 'canary and final share a task name' }
}

Write-Host ''
if ($script:Failures.Count -gt 0) {
    throw "$($script:Failures.Count) release transaction check(s) failed:`n$($script:Failures -join "`n")"
}
Write-Host "Release transaction self-test passed: $($script:Passed) check(s)."
