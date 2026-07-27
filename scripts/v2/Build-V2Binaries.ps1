[CmdletBinding()]
param(
    [ValidateSet('Canary', 'Final')]
    [string]$Environment = 'Canary',
    [ValidatePattern('^[A-Za-z0-9._-]{1,64}$')]
    [string]$Version = 'v2-local',
    [string]$GoPath = 'C:\IA\toolchains\go1.26.5\go\bin\go.exe',
    [string]$InstallRoot = 'C:\IA\local-ai-v2',
    [switch]$Apply
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Common.ps1')

$expectedRoot = [IO.Path]::GetFullPath('C:\IA\local-ai-v2').TrimEnd([char[]]@('\', '/'))
$resolvedRoot = [IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))
if (-not [string]::Equals($resolvedRoot, $expectedRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Build staging root must belong to the exact v2 installation '$expectedRoot'."
}
$resolvedGo = (Resolve-Path -LiteralPath $GoPath -ErrorAction Stop).Path
if (-not (Test-Path -LiteralPath $resolvedGo -PathType Leaf) -or [IO.Path]::GetExtension($resolvedGo) -ne '.exe') {
    throw "Go toolchain executable is invalid: $GoPath"
}

$repoRoot = Get-V2RepoRoot
$stagingRoot = Join-Path $resolvedRoot 'state\staging'
$components = @(
    [pscustomobject]@{ Name = 'edge'; Package = '.\cmd\cia-edge'; Binary = 'cia-edge.exe'; WindowsGUI = $false },
    [pscustomobject]@{ Name = 'mcp'; Package = '.\cmd\cia-mcp'; Binary = 'cia-mcp.exe'; WindowsGUI = $false },
    [pscustomobject]@{ Name = 'mcp-admin'; Package = '.\cmd\cia-mcp-admin'; Binary = 'cia-mcp-admin.exe'; WindowsGUI = $false },
    [pscustomobject]@{ Name = 'mcp-inference'; Package = '.\cmd\cia-mcp-inference'; Binary = 'cia-mcp-inference.exe'; WindowsGUI = $false },
	[pscustomobject]@{ Name = 'supervisor'; Package = '.\cmd\cia-supervisor'; Binary = 'cia-supervisor.exe'; WindowsGUI = $true },
    [pscustomobject]@{ Name = 'tray'; Package = '.\cmd\cia-tray'; Binary = 'cia-tray.exe'; WindowsGUI = $true }
)

$plan = @($components | ForEach-Object {
        $sourceDirectory = Join-Path $repoRoot ($_.Package -replace '^\.\\', '')
        if (-not (Test-Path -LiteralPath $sourceDirectory -PathType Container)) {
            throw "Go command package is missing: $sourceDirectory"
        }
        [pscustomobject]@{
            component = $_.Name
            package = $_.Package
            destination = Join-Path $stagingRoot $_.Binary
            windows_gui = $_.WindowsGUI
        }
    })

$preview = [pscustomobject]@{
    mode = if ($Apply) { 'apply' } else { 'preview' }
    environment = $Environment.ToLowerInvariant()
    version = $Version
    go = $resolvedGo
    repository = $repoRoot
    staging = $stagingRoot
    tests = 'go test -timeout=2m ./...'
    artifacts = $plan
}
if (-not $Apply) {
    $preview | ConvertTo-Json -Depth 5
    Write-Host 'Preview only. Re-run with -Apply to test and atomically publish binaries into writable state\staging.'
    return
}

if (-not (Test-Path -LiteralPath (Join-Path $resolvedRoot 'state') -PathType Container)) {
    throw "Required writable v2 state directory is missing: $resolvedRoot\state"
}
New-Item -ItemType Directory -Path $stagingRoot -Force -ErrorAction Stop | Out-Null

$buildMutex = [Threading.Mutex]::new($false, 'Local\CIA.LocalAI.V2.BinaryBuild')
$buildMutexAcquired = $false
try {
    try {
        $buildMutexAcquired = $buildMutex.WaitOne(0)
    }
    catch [Threading.AbandonedMutexException] {
        $buildMutexAcquired = $true
    }
    if (-not $buildMutexAcquired) {
        throw 'Another v2 binary build is already in progress.'
    }

    Push-Location $repoRoot
    try {
        & $resolvedGo test -timeout=2m ./...
        if ($LASTEXITCODE -ne 0) {
            throw "Go tests failed with exit code $LASTEXITCODE; staging was not changed."
        }
    }
    finally {
        Pop-Location
    }

    $buildRoot = Join-Path (Join-Path $resolvedRoot 'state') ('binary-build-{0}' -f [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $buildRoot -ErrorAction Stop | Out-Null
    try {
        $built = [System.Collections.Generic.List[object]]::new()
        foreach ($component in $components) {
            $builtPath = Join-Path $buildRoot $component.Binary
            $linkerFlags = if ($component.WindowsGUI) {
                "-H=windowsgui -X main.version=$Version"
            }
            else {
                "-X main.version=$Version"
            }
            Push-Location $repoRoot
            try {
                & $resolvedGo build -trimpath -ldflags $linkerFlags -o $builtPath $component.Package
                if ($LASTEXITCODE -ne 0) {
                    throw "Go build failed for '$($component.Name)' with exit code $LASTEXITCODE."
                }
            }
            finally {
                Pop-Location
            }
            if (-not (Test-Path -LiteralPath $builtPath -PathType Leaf)) {
                throw "Go build did not produce the expected binary: $builtPath"
            }
            $built.Add([pscustomobject]@{
                    Component = $component.Name
                    Source = $builtPath
                    Destination = Join-Path $stagingRoot $component.Binary
                    Sha256 = (Get-FileHash -LiteralPath $builtPath -Algorithm SHA256).Hash
                    Bytes = (Get-Item -LiteralPath $builtPath).Length
                })
        }

        $published = [System.Collections.Generic.List[object]]::new()
        try {
            foreach ($artifact in $built) {
                $destination = $artifact.Destination
                $nonce = [Guid]::NewGuid().ToString('N')
                $temporary = Join-Path $stagingRoot ('.{0}.{1}.tmp' -f ([IO.Path]::GetFileName($destination)), $nonce)
                $backup = Join-Path $buildRoot ('backup-{0}-{1}' -f ([IO.Path]::GetFileName($destination)), $nonce)
                [IO.File]::Copy($artifact.Source, $temporary, $false)
                if ((Get-FileHash -LiteralPath $temporary -Algorithm SHA256).Hash -ne $artifact.Sha256) {
                    throw "Staging copy hash mismatch for '$($artifact.Component)'."
                }
                $existed = [IO.File]::Exists($destination)
                if ($existed) {
                    [IO.File]::Replace($temporary, $destination, $backup, $true)
                }
                else {
                    [IO.File]::Move($temporary, $destination)
                }
                $published.Add([pscustomobject]@{
                        Destination = $destination
                        Backup = if ($existed) { $backup } else { $null }
                        Existed = $existed
                    })
                if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash -ne $artifact.Sha256) {
                    throw "Published staging hash mismatch for '$($artifact.Component)'."
                }
            }
        }
        catch {
            $publishFailure = $_
            $rollbackFailures = [System.Collections.Generic.List[string]]::new()
            if ($temporary -and [IO.File]::Exists($temporary)) {
                try { [IO.File]::Delete($temporary) } catch { $rollbackFailures.Add("${temporary}: $($_.Exception.Message)") }
            }
            for ($publishedIndex = $published.Count - 1; $publishedIndex -ge 0; $publishedIndex--) {
                $publishedItem = $published[$publishedIndex]
                try {
                    if ($publishedItem.Existed) {
                        $discard = Join-Path $buildRoot ('discard-{0}-{1}' -f ([IO.Path]::GetFileName($publishedItem.Destination)), [Guid]::NewGuid().ToString('N'))
                        [IO.File]::Replace($publishedItem.Backup, $publishedItem.Destination, $discard, $true)
                    }
                    elseif ([IO.File]::Exists($publishedItem.Destination)) {
                        [IO.File]::Delete($publishedItem.Destination)
                    }
                }
                catch {
                    $rollbackFailures.Add("$($publishedItem.Destination): $($_.Exception.Message)")
                }
            }
            if ($rollbackFailures.Count -gt 0) {
                throw "Binary staging publication failed and rollback was incomplete. Build error: $($publishFailure.Exception.Message). Rollback errors: $($rollbackFailures -join ' | ')"
            }
            throw $publishFailure
        }

        $goVersion = (& $resolvedGo version | Out-String).Trim()
        [pscustomobject]@{
            mode = 'apply'
            environment = $Environment.ToLowerInvariant()
            version = $Version
            go_version = $goVersion
            tests_passed = $true
            artifacts = @($built | ForEach-Object {
                    [pscustomobject]@{
                        component = $_.Component
                        path = $_.Destination
                        sha256 = $_.Sha256
                        bytes = $_.Bytes
                    }
                })
        } | ConvertTo-Json -Depth 5
    }
    finally {
        if ($buildRoot -and (Test-Path -LiteralPath $buildRoot -PathType Container)) {
            $normalizedBuildRoot = [IO.Path]::GetFullPath($buildRoot)
            $normalizedStateRoot = [IO.Path]::GetFullPath((Join-Path $resolvedRoot 'state')).TrimEnd([char[]]@('\', '/')) + [IO.Path]::DirectorySeparatorChar
            if ($normalizedBuildRoot.StartsWith($normalizedStateRoot, [StringComparison]::OrdinalIgnoreCase)) {
                Remove-Item -LiteralPath $normalizedBuildRoot -Recurse -Force
            }
        }
    }
}
finally {
    if ($buildMutexAcquired) {
        [void]$buildMutex.ReleaseMutex()
    }
    $buildMutex.Dispose()
}
