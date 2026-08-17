<#
.SYNOPSIS
Builds llama-server from a pinned buun-llama-cpp commit for gfx1201/ROCm and
emits the manifest runtime entry for it.

.DESCRIPTION
The runtime's identity is repository + commit + artifact SHA-256 + backend + GPU
target. Nothing here is identified by a directory name, and nothing resolves an
executable from PATH.

The order is deliberate and every step is a gate:

  1. Refuse anything that is not a full commit. master, main, latest and HEAD are
     not identities.
  2. Fetch that exact commit and confirm the checkout is at it.
  3. Run cia-fork-gate. A commit that cannot be shown to implement the
     hybrid/recurrent checkpoint correction is not built. It is also not patched
     locally: this repository takes a reproducible dependency on a published
     commit or it takes none.
  4. Build only llama-server, only for gfx1201, only in Release.
  5. Install under a directory carrying the commit, refusing to write anywhere an
     existing manifest runtime already lives.
  6. Hash the artifact and confirm the binary's own --help lists every option the
     qualification profile emits.
  7. Print the manifest runtime entry, with the gate report hash in it.

The entry is printed rather than written into config/models.yaml. Adding a
runtime is a reviewed change, and the resource profile that admission needs has
to be measured before the model that uses it can be admitted at all.

.EXAMPLE
./Build-V2ForkRuntime.ps1 -Revision 799e3995cd4f19aa9f6a3fa9fb5b4674422bf0ee -Apply
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[0-9a-f]{40}$')]
    [string]$Revision,

    [string]$Repository = 'https://github.com/spiritbuun/buun-llama-cpp',

    [string]$SourceRoot = 'C:\IA\src\buun-llama-cpp',

    [string]$InstallRoot = 'C:\IA\local-llama\amd',

    [string]$GateExecutable = 'C:\IA\local-ai-v2\bin\cia-fork-gate.exe',

    # C++ driver used by the gate to execute the fork's checkpoint predicate. The
    # gate refuses to reach a verdict without one.
    [string]$Compiler = 'clang++',

    [ValidatePattern('^gfx[0-9a-f]+$')]
    [string]$GpuTarget = 'gfx1201',

    [string]$ManifestPath = (Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'config\models.yaml'),

    [string]$RuntimeId = 'amd-rocm-qwen38-buun',

    [switch]$Apply
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Common.ps1')

foreach ($moving in @('master', 'main', 'latest', 'head')) {
    if ($Revision.ToLowerInvariant() -eq $moving) {
        throw "Refusing to build from '$Revision'. A runtime is pinned to an exact commit."
    }
}

$shortRevision = $Revision.Substring(0, 12)
$buildRoot = Join-Path $SourceRoot 'build-rocm-gfx1201'
$installDirectory = Join-Path $InstallRoot ("llama_cpp_buun_{0}_rocm_{1}" -f $shortRevision, $GpuTarget)
$serverPath = Join-Path $installDirectory 'llama-server.exe'
$gateReportPath = Join-Path $installDirectory 'fork-gate-report.json'

# The upstream baseline must survive this untouched. Its artifact path comes from
# the manifest rather than from a convention, so a future relocation of either
# runtime cannot make them collide silently.
if (Test-Path -LiteralPath $ManifestPath -PathType Leaf) {
    $manifest = Read-V2Manifest -Path $ManifestPath
    foreach ($existing in @($manifest.runtimes)) {
        if ($existing.id -eq $RuntimeId) { continue }
        $existingDirectory = [IO.Path]::GetFullPath((Split-Path -Parent ([string]$existing.artifact.path)))
        $candidateDirectory = [IO.Path]::GetFullPath($installDirectory)
        if ([string]::Equals($existingDirectory, $candidateDirectory, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to build into '$candidateDirectory'; runtime '$($existing.id)' already lives there. The fork is installed alongside the baseline, never over it."
        }
    }
}

$plan = [pscustomobject]@{
    mode              = $(if ($Apply) { 'apply' } else { 'preview' })
    repository        = $Repository
    revision          = $Revision
    source_root       = $SourceRoot
    build_root        = $buildRoot
    install_directory = $installDirectory
    gpu_target        = $GpuTarget
    configuration     = 'Release'
    targets           = @('llama-server')
}

if (-not $Apply) {
    $plan | ConvertTo-Json -Depth 4
    Write-Host 'Preview only. Re-run with -Apply to fetch, gate, build and hash.'
    return
}

function Invoke-V2External {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [string]$WorkingDirectory
    )

    $previous = $null
    if ($WorkingDirectory) {
        $previous = (Get-Location).Path
        Set-Location -LiteralPath $WorkingDirectory
    }
    try {
        & $FilePath @Arguments
        if ($LASTEXITCODE -ne 0) {
            throw "$FilePath $($Arguments -join ' ') failed with exit code $LASTEXITCODE."
        }
    }
    finally {
        if ($previous) { Set-Location -LiteralPath $previous }
    }
}

# 1-2. Fetch the exact commit and confirm the checkout is at it.
if (-not (Test-Path -LiteralPath (Join-Path $SourceRoot '.git') -PathType Container)) {
    New-Item -ItemType Directory -Force -Path $SourceRoot | Out-Null
    Invoke-V2External -FilePath 'git' -Arguments @('init', '--quiet') -WorkingDirectory $SourceRoot
    Invoke-V2External -FilePath 'git' -Arguments @('remote', 'add', 'origin', $Repository) -WorkingDirectory $SourceRoot
}
Invoke-V2External -FilePath 'git' -Arguments @('fetch', '--depth', '1', 'origin', $Revision) -WorkingDirectory $SourceRoot
Invoke-V2External -FilePath 'git' -Arguments @('checkout', '--detach', '--force', $Revision) -WorkingDirectory $SourceRoot
Invoke-V2External -FilePath 'git' -Arguments @('submodule', 'update', '--init', '--recursive', '--depth', '1') -WorkingDirectory $SourceRoot

$head = (& git -C $SourceRoot rev-parse HEAD).Trim()
if ($head -ne $Revision) {
    throw "Checkout is at '$head' but '$Revision' was requested."
}

# 3. Provenance gate. Nothing is compiled until the commit is shown to implement
#    the correction, and a failure is recorded rather than patched around.
if (-not (Test-Path -LiteralPath $GateExecutable -PathType Leaf)) {
    throw "The provenance gate is missing: $GateExecutable. Build it with: go build -trimpath -o `"$GateExecutable`" ./cmd/cia-fork-gate"
}
New-Item -ItemType Directory -Force -Path $installDirectory | Out-Null
& $GateExecutable --source $SourceRoot --revision $Revision --repository $Repository --compiler $Compiler --report $gateReportPath | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "Commit $Revision did not pass the fork provenance gate. Its report is at '$gateReportPath'. Do not patch the fork locally: record the reason and qualify a different commit."
}
$gateReportSha256 = (Get-FileHash -LiteralPath $gateReportPath -Algorithm SHA256).Hash
$gateReport = Get-Content -LiteralPath $gateReportPath -Raw -Encoding UTF8 | ConvertFrom-Json
if (-not $gateReport.passed) {
    throw "The gate report at '$gateReportPath' records a failure; refusing to build."
}

# 4. One target, one architecture, Release. Extra targets and extra architectures
#    only grow the artifact this deployment has to hash and store.
$configureArguments = @(
    '-S', $SourceRoot,
    '-B', $buildRoot,
    '-G', 'Ninja',
    '-DCMAKE_BUILD_TYPE=Release',
    '-DGGML_HIP=ON',
    "-DAMDGPU_TARGETS=$GpuTarget",
    "-DCMAKE_HIP_ARCHITECTURES=$GpuTarget",
    '-DGGML_NATIVE=OFF',
    '-DLLAMA_BUILD_TESTS=OFF',
    '-DLLAMA_BUILD_EXAMPLES=OFF',
    '-DLLAMA_BUILD_SERVER=ON',
    '-DLLAMA_CURL=OFF'
)
Invoke-V2External -FilePath 'cmake' -Arguments $configureArguments
Invoke-V2External -FilePath 'cmake' -Arguments @('--build', $buildRoot, '--config', 'Release', '--target', 'llama-server')

# 5. Install alongside, never over.
$built = @(Get-ChildItem -LiteralPath $buildRoot -Recurse -Filter 'llama-server.exe' -ErrorAction Stop)
if ($built.Count -lt 1) {
    throw "The build produced no llama-server.exe under '$buildRoot'."
}
$source = ($built | Sort-Object LastWriteTimeUtc -Descending)[0]
Copy-Item -LiteralPath $source.FullName -Destination $serverPath -Force
foreach ($dependency in @(Get-ChildItem -LiteralPath $source.DirectoryName -Filter '*.dll' -ErrorAction SilentlyContinue)) {
    Copy-Item -LiteralPath $dependency.FullName -Destination (Join-Path $installDirectory $dependency.Name) -Force
}

# 6. Hash the bytes that will actually be invoked, then ask them what they
#    support. The same --help gate runs again at configuration generation; doing
#    it here means a build that cannot serve the profile is caught now.
$serverItem = Get-Item -LiteralPath $serverPath
$serverSha256 = (Get-FileHash -LiteralPath $serverPath -Algorithm SHA256).Hash
$supportedFlags = Get-V2SupportedFlags -HelpText (Get-V2RuntimeHelpText -Path $serverPath)
$requiredFlags = @(
    '--ctx-checkpoints', '--checkpoint-min-step', '--cache-ram', '--no-cache-idle-slots',
    '--no-context-shift', '--kv-unified', '--override-tensor', '--spec-type',
    '--spec-draft-n-max', '--cache-type-k', '--cache-type-v', '--jinja'
)
$missingFlags = @($requiredFlags | Where-Object { -not $supportedFlags.Contains($_) })
if ($missingFlags.Count -gt 0) {
    throw "The built runtime does not implement $($missingFlags -join ', '). The qualification profile cannot be served by it."
}

# 7. The manifest entry. resources are measured against the model, not the
#    runtime, so nothing about capacity is invented here.
$runtimeEntry = [ordered]@{
    id            = $RuntimeId
    state         = 'candidate'
    engine        = 'llama.cpp'
    variant       = 'fork'
    version_label = "buun-llama-cpp $shortRevision (ROCm $GpuTarget, agentic canary)"
    build_commit  = $shortRevision
    provenance    = [ordered]@{
        source_repository = $Repository
        source_revision   = $Revision
        checkpoint_fix    = [ordered]@{
            reference          = [string]$gateReport.checkpoint_fix_reference
            evidence           = [string]$gateReport.evidence
            verified_utc       = [string]$gateReport.generated_utc
            gate_report_sha256 = $gateReportSha256
        }
        build             = [ordered]@{
            backend       = 'ROCm'
            gpu_targets   = @($GpuTarget)
            configuration = 'Release'
            targets       = @('llama-server')
        }
    }
    artifact      = [ordered]@{
        path   = $serverPath
        bytes  = [int64]$serverItem.Length
        sha256 = $serverSha256
    }
    device        = [ordered]@{
        backend  = 'ROCm'
        selector = 'ROCm0'
        gpu      = 'AMD Radeon RX 9070 XT (gfx1201)'
        vram_mib = 16304
    }
    environment   = [ordered]@{
        ROCBLAS_USE_HIPBLASLT = '0'
    }
}

[pscustomobject]@{
    status            = 'built'
    revision          = $Revision
    install_directory = $installDirectory
    gate_report       = $gateReportPath
    gate_report_sha256 = $gateReportSha256
    runtime_entry     = $runtimeEntry
} | ConvertTo-Json -Depth 8

Write-Host ''
Write-Host 'Review the runtime_entry above into config/models.yaml as a new entry. Do not edit the existing runtimes.'
Write-Host 'The model that uses it stays unadmittable until its peak VRAM, RAM and commit are measured; see docs/MODEL_PROMOTION.md.'
