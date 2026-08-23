[CmdletBinding()]
param(
    [switch]$Quiet
)

<#
Self-test for the production artifact boundary.

It exercises the publication primitives directly against a temporary root, which
is what makes the security properties testable at all: the operator-facing
script pins the real installation root and requires elevation, so it cannot run
in CI. The primitives are the same ones that script calls.
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

function New-TestRoot {
    $root = Join-Path ([IO.Path]::GetTempPath()) ('cia-artifact-test-{0}' -f [Guid]::NewGuid().ToString('N'))
    foreach ($child in @('', 'state', 'artifacts', 'candidates')) {
        $path = if ($child -eq '') { $root } else { Join-Path $root $child }
        New-Item -ItemType Directory -Path $path -Force -ErrorAction Stop | Out-Null
    }
    return $root
}

function New-CandidateFile {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Content
    )

    $directory = Split-Path -Parent $Path
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) {
        New-Item -ItemType Directory -Path $directory -Force -ErrorAction Stop | Out-Null
    }
    [IO.File]::WriteAllText($Path, $Content, [Text.UTF8Encoding]::new($false))
    $item = Get-Item -LiteralPath $Path -Force
    return [pscustomobject]@{
        path = $Path
        bytes = [long]$item.Length
        sha256 = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToUpperInvariant()
    }
}

Write-Host 'Production artifact store'

# ------------------------------------------------------------------ happy path
Test-Case 'publishes a candidate model into the protected tree and leaves the source untouched' {
    $root = New-TestRoot
    try {
        $candidate = New-CandidateFile -Path (Join-Path $root 'candidates\weights.gguf') -Content 'candidate-weights'
        $sourceBefore = (Get-FileHash -LiteralPath $candidate.path -Algorithm SHA256).Hash
        $destination = Get-V2ProductionArtifactPath -InstallRoot $root -Kind Model -Id 'demo-model' -SourcePath $candidate.path

        $result = Publish-V2ProtectedFile `
            -Source $candidate.path `
            -Destination $destination `
            -Bytes $candidate.bytes `
            -Sha256 $candidate.sha256 `
            -StagingRoot (Get-V2ArtifactStagingRoot -InstallRoot $root)

        if ($result.action -ne 'created') { throw "action = $($result.action)" }
        if (-not (Test-Path -LiteralPath $destination -PathType Leaf)) { throw 'published artifact is missing' }
        if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash -ne $candidate.sha256) { throw 'published bytes differ' }

        # The candidate must be byte-identical and still present.
        if (-not (Test-Path -LiteralPath $candidate.path -PathType Leaf)) { throw 'the source candidate was removed' }
        if ((Get-FileHash -LiteralPath $candidate.path -Algorithm SHA256).Hash -ne $sourceBefore) { throw 'the source candidate was modified' }

        # Republishing identical bytes is a no-op rather than a churn.
        $again = Publish-V2ProtectedFile -Source $candidate.path -Destination $destination -Bytes $candidate.bytes -Sha256 $candidate.sha256 -StagingRoot (Get-V2ArtifactStagingRoot -InstallRoot $root)
        if ($again.action -ne 'unchanged') { throw "republish action = $($again.action)" }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

# ------------------------------------------------------------------ path escape
Test-Case 'refuses a destination that escapes the protected artifact root' {
    $root = New-TestRoot
    try {
        Assert-Throws -Because 'a traversal identifier was accepted' -Match 'not a safe directory name' -Body {
            Get-V2ProductionArtifactPath -InstallRoot $root -Kind Model -Id '..\..\escaped' -SourcePath 'C:\candidates\weights.gguf'
        }
        Assert-Throws -Because 'a traversal file name was accepted' -Match 'not a safe file name' -Body {
            Get-V2ProductionArtifactPath -InstallRoot $root -Kind Model -Id 'demo' -SourcePath 'C:\candidates\..'
        }
        Assert-Throws -Because 'a path outside the root was accepted' -Match 'escapes the protected root' -Body {
            Assert-V2SafePath -Path (Join-Path $root '..\outside.txt') -Root (Get-V2ArtifactRoot -InstallRoot $root)
        }
        Assert-Throws -Because 'a relative path was accepted' -Match 'absolute local path' -Body {
            Assert-V2SafePath -Path 'relative\path.gguf' -Root (Get-V2ArtifactRoot -InstallRoot $root)
        }
        Assert-Throws -Because 'a filesystem root was accepted as a target' -Match 'filesystem root' -Body {
            Assert-V2SafePath -Path 'C:\' -Root 'C:\' -AllowRoot
        }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

# ------------------------------------------------------------------ reparse points
Test-Case 'refuses a reparse point anywhere on the destination path' {
    $root = New-TestRoot
    try {
        $artifactRoot = Get-V2ArtifactRoot -InstallRoot $root
        $real = Join-Path $root 'real-models'
        New-Item -ItemType Directory -Path $real -Force | Out-Null
        $junction = Join-Path $artifactRoot 'models'

        $created = $true
        try {
            New-Item -ItemType Junction -Path $junction -Target $real -ErrorAction Stop | Out-Null
        }
        catch {
            $created = $false
        }
        if (-not $created) {
            Write-Host '       (skipped: this environment cannot create a junction)'
            return
        }

        Assert-Throws -Because 'a junction on the destination path was followed' -Match 'Reparse points are not valid' -Body {
            Get-V2ProductionArtifactPath -InstallRoot $root -Kind Model -Id 'demo-model' -SourcePath 'C:\candidates\weights.gguf'
        }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

# ------------------------------------------------------------------ size mismatch
Test-Case 'refuses a source whose byte size does not match the manifest' {
    $root = New-TestRoot
    try {
        $candidate = New-CandidateFile -Path (Join-Path $root 'candidates\weights.gguf') -Content 'candidate-weights'
        $destination = Get-V2ProductionArtifactPath -InstallRoot $root -Kind Model -Id 'demo-model' -SourcePath $candidate.path
        Assert-Throws -Because 'a size mismatch was published' -Match 'size mismatch' -Body {
            Publish-V2ProtectedFile -Source $candidate.path -Destination $destination `
                -Bytes ($candidate.bytes + 1) -Sha256 $candidate.sha256 `
                -StagingRoot (Get-V2ArtifactStagingRoot -InstallRoot $root)
        }
        if (Test-Path -LiteralPath $destination -PathType Leaf) { throw 'a refused publication still wrote the destination' }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

# ------------------------------------------------------------------ hash mismatch
Test-Case 'refuses a source whose SHA-256 does not match the manifest' {
    $root = New-TestRoot
    try {
        $candidate = New-CandidateFile -Path (Join-Path $root 'candidates\weights.gguf') -Content 'candidate-weights'
        $destination = Get-V2ProductionArtifactPath -InstallRoot $root -Kind Model -Id 'demo-model' -SourcePath $candidate.path
        $wrong = ('A' * 64)
        Assert-Throws -Because 'a hash mismatch was published' -Match 'SHA-256 mismatch' -Body {
            Publish-V2ProtectedFile -Source $candidate.path -Destination $destination `
                -Bytes $candidate.bytes -Sha256 $wrong `
                -StagingRoot (Get-V2ArtifactStagingRoot -InstallRoot $root)
        }
        if (Test-Path -LiteralPath $destination -PathType Leaf) { throw 'a refused publication still wrote the destination' }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

# ------------------------------------------------------- destination changed mid-transaction
Test-Case 'aborts when the destination changed after it was inspected' {
    $root = New-TestRoot
    try {
        $first = New-CandidateFile -Path (Join-Path $root 'candidates\first.gguf') -Content 'first-weights'
        $second = New-CandidateFile -Path (Join-Path $root 'candidates\first-v2.gguf') -Content 'second-weights-longer'
        $destination = Get-V2ProductionArtifactPath -InstallRoot $root -Kind Model -Id 'demo-model' -SourcePath $first.path
        $staging = Get-V2ArtifactStagingRoot -InstallRoot $root

        [void](Publish-V2ProtectedFile -Source $first.path -Destination $destination -Bytes $first.bytes -Sha256 $first.sha256 -StagingRoot $staging)
        $observed = (Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash

        # Something replaced the published artifact between preflight and publish.
        [IO.File]::WriteAllText($destination, 'tampered-in-between', [Text.UTF8Encoding]::new($false))

        Assert-Throws -Because 'a destination that changed mid-transaction was overwritten' -Match 'changed during the transaction' -Body {
            Publish-V2ProtectedFile -Source $second.path -Destination $destination `
                -Bytes $second.bytes -Sha256 $second.sha256 -StagingRoot $staging `
                -ObservedDestinationSha256 $observed -Replace
        }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

# ------------------------------------------------------------------ replace policy
Test-Case 'refuses to overwrite different published bytes without an explicit replace' {
    $root = New-TestRoot
    try {
        $first = New-CandidateFile -Path (Join-Path $root 'candidates\a.gguf') -Content 'weights-a'
        $second = New-CandidateFile -Path (Join-Path $root 'candidates\a-v2.gguf') -Content 'weights-b-different'
        $destination = Get-V2ProductionArtifactPath -InstallRoot $root -Kind Model -Id 'demo-model' -SourcePath $first.path
        $staging = Get-V2ArtifactStagingRoot -InstallRoot $root
        [void](Publish-V2ProtectedFile -Source $first.path -Destination $destination -Bytes $first.bytes -Sha256 $first.sha256 -StagingRoot $staging)

        Assert-Throws -Because 'different bytes were published without -Replace' -Match 'already published' -Body {
            Publish-V2ProtectedFile -Source $second.path -Destination $destination -Bytes $second.bytes -Sha256 $second.sha256 -StagingRoot $staging
        }
        if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash -ne $first.sha256) {
            throw 'the refused publication changed the published artifact'
        }

        $replaced = Publish-V2ProtectedFile -Source $second.path -Destination $destination -Bytes $second.bytes -Sha256 $second.sha256 -StagingRoot $staging -Replace
        if ($replaced.action -ne 'replaced') { throw "replace action = $($replaced.action)" }
        if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash -ne $second.sha256) { throw 'replacement did not take effect' }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

# ------------------------------------------------------------------ runtime directory
Test-Case 'publishes a whole runtime directory, not just the pinned executable' {
    $root = New-TestRoot
    try {
        $runtimeDirectory = Join-Path $root 'candidates\runtime'
        $executable = New-CandidateFile -Path (Join-Path $runtimeDirectory 'llama-server.exe') -Content 'runtime-executable'
        [void](New-CandidateFile -Path (Join-Path $runtimeDirectory 'ggml-hip.dll') -Content 'backend-library')
        [void](New-CandidateFile -Path (Join-Path $runtimeDirectory 'rocblas\library\Tensile.dat') -Content 'kernel-blob')

        $destination = Get-V2ProductionArtifactPath -InstallRoot $root -Kind Runtime -Id 'demo-runtime' -SourcePath $executable.path
        $result = Publish-V2ProtectedRuntime `
            -SourceExecutable $executable.path `
            -Destination $destination `
            -Bytes $executable.bytes `
            -Sha256 $executable.sha256 `
            -StagingRoot (Get-V2ArtifactStagingRoot -InstallRoot $root)

        if ($result.action -ne 'created') { throw "action = $($result.action)" }
        $publishedDirectory = Split-Path -Parent $destination
        foreach ($relative in @('llama-server.exe', 'ggml-hip.dll', 'rocblas\library\Tensile.dat')) {
            if (-not (Test-Path -LiteralPath (Join-Path $publishedDirectory $relative) -PathType Leaf)) {
                throw "runtime publication dropped '$relative'"
            }
        }
        if ($result.file_count -ne 3) { throw "published file count = $($result.file_count)" }
        if (-not (Test-Path -LiteralPath $executable.path -PathType Leaf)) { throw 'the source runtime was removed' }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

# ------------------------------------------------------------------ the index
Test-Case 'records a deterministic index that binds identity to the derived location' {
    $root = New-TestRoot
    try {
        $candidate = New-CandidateFile -Path (Join-Path $root 'candidates\weights.gguf') -Content 'candidate-weights'
        $destination = Get-V2ProductionArtifactPath -InstallRoot $root -Kind Model -Id 'demo-model' -SourcePath $candidate.path
        [void](Publish-V2ProtectedFile -Source $candidate.path -Destination $destination -Bytes $candidate.bytes -Sha256 $candidate.sha256 -StagingRoot (Get-V2ArtifactStagingRoot -InstallRoot $root))

        $manifestArtifact = [pscustomobject]@{ path = $candidate.path; bytes = $candidate.bytes; sha256 = $candidate.sha256 }

        Assert-Throws -Because 'an unpublished artifact resolved' -Match 'no production artifact record' -Body {
            Assert-V2ProductionArtifact -InstallRoot $root -Kind Model -Id 'demo-model' -Artifact $manifestArtifact
        }

        [void](Update-V2ArtifactIndex -InstallRoot $root -Entry ([ordered]@{
                    kind = 'model'
                    id = 'demo-model'
                    source = $candidate.path
                    path = $destination
                    bytes = $candidate.bytes
                    sha256 = $candidate.sha256
                    published_utc = [DateTime]::UtcNow.ToString('o')
                }))

        $resolved = Assert-V2ProductionArtifact -InstallRoot $root -Kind Model -Id 'demo-model' -Artifact $manifestArtifact -VerifyHash
        if ($resolved -ne $destination) { throw "resolved '$resolved', expected '$destination'" }

        # A record that points somewhere other than the derived location is a
        # redirection attempt and must not be honoured.
        [void](Update-V2ArtifactIndex -InstallRoot $root -Entry ([ordered]@{
                    kind = 'model'
                    id = 'demo-model'
                    source = $candidate.path
                    path = (Join-Path $root 'candidates\weights.gguf')
                    bytes = $candidate.bytes
                    sha256 = $candidate.sha256
                    published_utc = [DateTime]::UtcNow.ToString('o')
                }))
        Assert-Throws -Because 'a redirected index record was honoured' -Match 'not the derived location' -Body {
            Assert-V2ProductionArtifact -InstallRoot $root -Kind Model -Id 'demo-model' -Artifact $manifestArtifact
        }

        # The index carries no secret material.
        $indexText = Get-Content -LiteralPath (Get-V2ArtifactIndexPath -InstallRoot $root) -Raw -Encoding UTF8
        foreach ($forbidden in @('token', 'Bearer', 'api_key', 'password')) {
            if ($indexText -match "(?i)$forbidden") { throw "the artifact index mentions '$forbidden'" }
        }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

Test-Case 'detects a production artifact whose bytes were replaced after publication' {
    $root = New-TestRoot
    try {
        $candidate = New-CandidateFile -Path (Join-Path $root 'candidates\weights.gguf') -Content 'candidate-weights'
        $destination = Get-V2ProductionArtifactPath -InstallRoot $root -Kind Model -Id 'demo-model' -SourcePath $candidate.path
        [void](Publish-V2ProtectedFile -Source $candidate.path -Destination $destination -Bytes $candidate.bytes -Sha256 $candidate.sha256 -StagingRoot (Get-V2ArtifactStagingRoot -InstallRoot $root))
        [void](Update-V2ArtifactIndex -InstallRoot $root -Entry ([ordered]@{
                    kind = 'model'; id = 'demo-model'; source = $candidate.path; path = $destination
                    bytes = $candidate.bytes; sha256 = $candidate.sha256; published_utc = [DateTime]::UtcNow.ToString('o')
                }))

        [IO.File]::WriteAllText($destination, 'tampered-production-bytes', [Text.UTF8Encoding]::new($false))
        $manifestArtifact = [pscustomobject]@{ path = $candidate.path; bytes = $candidate.bytes; sha256 = $candidate.sha256 }
        Assert-Throws -Because 'tampered production bytes were accepted' -Match 'mismatch' -Body {
            Assert-V2ProductionArtifact -InstallRoot $root -Kind Model -Id 'demo-model' -Artifact $manifestArtifact -VerifyHash
        }
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

Test-Case 'redirects only the artifact path when producing a production manifest entry' {
    $entry = [pscustomobject]@{
        id = 'demo-model'
        display_name = 'Demo'
        gpu_layers = 99
        artifact = [pscustomobject]@{ path = 'C:\IA\models\demo.gguf'; bytes = 12; sha256 = ('B' * 64) }
    }
    $clone = ConvertTo-V2ProductionEntry -Entry $entry -ProductionPath 'C:\IA\local-ai-v2\artifacts\models\demo-model\demo.gguf'
    if ($clone.artifact.path -ne 'C:\IA\local-ai-v2\artifacts\models\demo-model\demo.gguf') { throw 'the production path was not applied' }
    if ($clone.artifact.sha256 -ne $entry.artifact.sha256 -or $clone.artifact.bytes -ne $entry.artifact.bytes) { throw 'identity changed' }
    if ($clone.gpu_layers -ne 99 -or $clone.display_name -ne 'Demo') { throw 'an unrelated field changed' }
    if ($entry.artifact.path -ne 'C:\IA\models\demo.gguf') { throw 'the declared manifest entry was mutated' }
}

Write-Host ''
if ($script:Failures.Count -gt 0) {
    throw "$($script:Failures.Count) artifact store check(s) failed:`n$($script:Failures -join "`n")"
}
Write-Host "Artifact store self-test passed: $($script:Passed) check(s)."
