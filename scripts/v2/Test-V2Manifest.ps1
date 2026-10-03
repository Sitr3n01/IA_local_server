[CmdletBinding()]
param(
    [string]$ManifestPath,
    [string]$SchemaPath,
    [string]$SchemaValidatorPath = 'C:\IA\local-ai-v2\bin\cia-manifest.exe',
    [switch]$VerifyArtifacts,
    [switch]$Quiet
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if ([string]::IsNullOrWhiteSpace($ManifestPath)) {
    $ManifestPath = Join-Path $repoRoot 'config\models.yaml'
}
if ([string]::IsNullOrWhiteSpace($SchemaPath)) {
    $SchemaPath = Join-Path $repoRoot 'config\models.schema.json'
}
. (Join-Path $PSScriptRoot 'Common.ps1')

Assert-V2ManifestSchema -ManifestPath $ManifestPath -SchemaPath $SchemaPath -ValidatorPath $SchemaValidatorPath
$manifest = Read-V2Manifest -Path $ManifestPath
Assert-V2ManifestSemantics -Manifest $manifest

# The semantic tests below mutate models[0] of a copy of the real manifest and
# assume it starts without the optional tuning fields they add or reason about.
# The real first entry may legitimately declare its runtime's defaults, so a
# manifest copy removes those fields from models[0] rather than letting every
# test inherit them. A single-model copy is returned unchanged.
$script:SemanticTuningFields = @(
    'context_shift', 'kv_unified', 'threads', 'threads_batch', 'cache_ram_mib', 'ctx_checkpoints',
    'checkpoint_min_step', 'cache_idle_slots', 'spec_decoding', 'moe_offload', 'tensor_overrides',
    'n_predict', 'reasoning_budget', 'reasoning_budget_message', 'chat_template_file',
    'compact_threshold_tokens'
)

function Copy-V2ManifestForSemanticTest {
    param([Parameter(Mandatory = $true)][object]$Value)
    $copy = ($Value | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
    if ($null -ne $copy.PSObject.Properties['models'] -and @($copy.models).Count -gt 0) {
        $first = @($copy.models)[0]
        foreach ($field in $script:SemanticTuningFields) {
            [void]$first.PSObject.Properties.Remove($field)
        }
    }
    return $copy
}

function Assert-V2SemanticRejection {
    param(
        [Parameter(Mandatory = $true)][object]$Candidate,
        [Parameter(Mandatory = $true)][string]$ExpectedMessage
    )

    try {
        Assert-V2ManifestSemantics -Manifest $Candidate
    }
    catch {
        if ($_.Exception.Message -notmatch $ExpectedMessage) {
            throw "Semantic policy rejected a test manifest for the wrong reason. Expected '$ExpectedMessage'; found '$($_.Exception.Message)'."
        }
        return
    }
    throw "Semantic policy accepted a manifest expected to fail with '$ExpectedMessage'."
}

function Set-V2FirstModelRuntimeState {
    param(
        [Parameter(Mandatory = $true)][object]$Candidate,
        [Parameter(Mandatory = $true)][string]$State
    )

    $runtimeId = [string]$Candidate.models[0].runtime
    $matches = @($Candidate.runtimes | Where-Object { $_.id -eq $runtimeId })
    if ($matches.Count -ne 1) {
        throw "Semantic fixture expected one runtime '$runtimeId'; found $($matches.Count)."
    }
    $matches[0].state = $State
}

# Keep the promotion invariants executable instead of relying only on the
# current manifest snapshot to happen to exercise them.
$retiredModel = Copy-V2ManifestForSemanticTest -Value $manifest
$retiredModel.models[0].state = 'retired'
Assert-V2SemanticRejection -Candidate $retiredModel -ExpectedMessage 'Retired model'

$retiredRuntime = Copy-V2ManifestForSemanticTest -Value $manifest
Set-V2FirstModelRuntimeState -Candidate $retiredRuntime -State 'retired'
Assert-V2SemanticRejection -Candidate $retiredRuntime -ExpectedMessage 'retired runtime'

$invalidArtifactHash = Copy-V2ManifestForSemanticTest -Value $manifest
$invalidArtifactHash.models[0].artifact.sha256 = 'invalid'
Assert-V2SemanticRejection -Candidate $invalidArtifactHash -ExpectedMessage 'invalid SHA-256'

$candidateFinalModel = Copy-V2ManifestForSemanticTest -Value $manifest
$candidateFinalModel.models[0].deployments = @('final')
Set-V2FirstModelRuntimeState -Candidate $candidateFinalModel -State 'qualified'
Assert-V2SemanticRejection -Candidate $candidateFinalModel -ExpectedMessage "while state is 'candidate'"

$candidateFinalRuntime = Copy-V2ManifestForSemanticTest -Value $manifest
$candidateFinalRuntime.models[0].deployments = @('final')
$candidateFinalRuntime.models[0].state = 'qualified'
Set-V2FirstModelRuntimeState -Candidate $candidateFinalRuntime -State 'candidate'
Assert-V2SemanticRejection -Candidate $candidateFinalRuntime -ExpectedMessage 'cannot enter final deployment with runtime'

$incompleteFinalResources = Copy-V2ManifestForSemanticTest -Value $manifest
$incompleteFinalResources.models[0].deployments = @('final')
$incompleteFinalResources.models[0].state = 'qualified'
Set-V2FirstModelRuntimeState -Candidate $incompleteFinalResources -State 'qualified'
$incompleteFinalResources.models[0].resources.peak_vram_gib = 9.5
$incompleteFinalResources.models[0].resources.peak_commit_gib = $null
Assert-V2SemanticRejection -Candidate $incompleteFinalResources -ExpectedMessage 'resources\.peak_commit_gib'

# Hybrid-model tuning rules. These are relationships between fields, which the
# JSON Schema cannot express; the per-field ranges (including the 65..256 ubatch
# dead band) stay declarative in models.schema.json.
$specWithContextShift = Copy-V2ManifestForSemanticTest -Value $manifest
$specWithContextShift.models[0] | Add-Member -NotePropertyName 'spec_decoding' -NotePropertyValue ([pscustomobject]@{ type = 'draft-mtp'; draft_n_max = 5 })
Assert-V2SemanticRejection -Candidate $specWithContextShift -ExpectedMessage 'must set context_shift=false'

$moeWithBothModes = Copy-V2ManifestForSemanticTest -Value $manifest
$moeWithBothModes.models[0] | Add-Member -NotePropertyName 'moe_offload' -NotePropertyValue ([pscustomobject]@{ cpu_all = $true; cpu_layers = 4 })
Assert-V2SemanticRejection -Candidate $moeWithBothModes -ExpectedMessage 'moe_offload\.cpu_all and moe_offload\.cpu_layers'

$moeWithoutMeasurement = Copy-V2ManifestForSemanticTest -Value $manifest
$moeWithoutMeasurement.models[0].resources.peak_vram_gib = $null
$moeWithoutMeasurement.models[0] | Add-Member -NotePropertyName 'moe_offload' -NotePropertyValue ([pscustomobject]@{ cpu_layers = 4 })
Assert-V2SemanticRejection -Candidate $moeWithoutMeasurement -ExpectedMessage 'resources\.peak_vram_gib'

$moeZeroWithoutRam = Copy-V2ManifestForSemanticTest -Value $manifest
$moeZeroWithoutRam.models[0].resources.peak_commit_gib = 12
$moeZeroWithoutRam.models[0] | Add-Member -NotePropertyName 'moe_offload' -NotePropertyValue ([pscustomobject]@{ cpu_layers = 0 })
Assert-V2ManifestSemantics -Manifest $moeZeroWithoutRam

$offloadWithoutMeasurement = Copy-V2ManifestForSemanticTest -Value $manifest
$offloadWithoutMeasurement.models[0].resources.peak_vram_gib = $null
$offloadWithoutMeasurement.models[0] | Add-Member -NotePropertyName 'tensor_overrides' -NotePropertyValue @([pscustomobject]@{ pattern = 'blk\.(4[4-9])\.ffn_.*'; buffer = 'CPU' })
Assert-V2SemanticRejection -Candidate $offloadWithoutMeasurement -ExpectedMessage 'resources\.peak_vram_gib'

$offloadWithWhitespace = Copy-V2ManifestForSemanticTest -Value $manifest
$offloadWithWhitespace.models[0].resources.peak_vram_gib = 14.5
$offloadWithWhitespace.models[0].resources.peak_commit_gib = 26
$offloadWithWhitespace.models[0].resources.peak_ram_gib = 18
$offloadWithWhitespace.models[0] | Add-Member -NotePropertyName 'tensor_overrides' -NotePropertyValue @([pscustomobject]@{ pattern = 'blk\.4 4\.ffn_.*'; buffer = 'CPU' })
Assert-V2SemanticRejection -Candidate $offloadWithWhitespace -ExpectedMessage 'containing whitespace'

$offloadWithBadRegex = Copy-V2ManifestForSemanticTest -Value $manifest
$offloadWithBadRegex.models[0].resources.peak_vram_gib = 14.5
$offloadWithBadRegex.models[0].resources.peak_commit_gib = 26
$offloadWithBadRegex.models[0].resources.peak_ram_gib = 18
$offloadWithBadRegex.models[0] | Add-Member -NotePropertyName 'tensor_overrides' -NotePropertyValue @([pscustomobject]@{ pattern = 'blk\.(4[4-9\.ffn_.*'; buffer = 'CPU' })
Assert-V2SemanticRejection -Candidate $offloadWithBadRegex -ExpectedMessage 'invalid tensor_overrides regex'

$cacheWithoutCommitMeasurement = Copy-V2ManifestForSemanticTest -Value $manifest
$cacheWithoutCommitMeasurement.models[0].resources.peak_commit_gib = $null
$cacheWithoutCommitMeasurement.models[0] | Add-Member -NotePropertyName 'cache_ram_mib' -NotePropertyValue 6144
Assert-V2SemanticRejection -Candidate $cacheWithoutCommitMeasurement -ExpectedMessage 'cannot account for the prompt cache'

# The whole tuning surface together, measured, must be accepted.
$tunedHybrid = Copy-V2ManifestForSemanticTest -Value $manifest
$tunedHybrid.models[0].resources.peak_vram_gib = 14.5
$tunedHybrid.models[0].resources.peak_commit_gib = 26
$tunedHybrid.models[0].resources.peak_ram_gib = 18
$tunedHybrid.models[0] | Add-Member -NotePropertyName 'context_shift' -NotePropertyValue $false
$tunedHybrid.models[0] | Add-Member -NotePropertyName 'kv_unified' -NotePropertyValue $true
$tunedHybrid.models[0] | Add-Member -NotePropertyName 'cache_ram_mib' -NotePropertyValue 6144
$tunedHybrid.models[0] | Add-Member -NotePropertyName 'spec_decoding' -NotePropertyValue ([pscustomobject]@{ type = 'draft-mtp'; draft_n_max = 5 })
$tunedHybrid.models[0] | Add-Member -NotePropertyName 'moe_offload' -NotePropertyValue ([pscustomobject]@{ cpu_layers = 4 })
$tunedHybrid.models[0] | Add-Member -NotePropertyName 'tensor_overrides' -NotePropertyValue @([pscustomobject]@{ pattern = 'blk\.(4[4-9]|5[0-9]|6[0-3])\.ffn_.*'; buffer = 'CPU' })
Assert-V2ManifestSemantics -Manifest $tunedHybrid

$allMoeAccepted = Copy-V2ManifestForSemanticTest -Value $manifest
$allMoeAccepted.models[0].resources.peak_vram_gib = 14.5
$allMoeAccepted.models[0].resources.peak_commit_gib = 26
$allMoeAccepted.models[0].resources.peak_ram_gib = 18
$allMoeAccepted.models[0] | Add-Member -NotePropertyName 'moe_offload' -NotePropertyValue ([pscustomobject]@{ cpu_all = $true })
Assert-V2ManifestSemantics -Manifest $allMoeAccepted

$nPredictBelowOutput = Copy-V2ManifestForSemanticTest -Value $manifest
$nPredictBelowOutput.models[0] | Add-Member -NotePropertyName 'n_predict' -NotePropertyValue 1024
$nPredictBelowOutput.models[0].max_output_tokens = 2048
Assert-V2SemanticRejection -Candidate $nPredictBelowOutput -ExpectedMessage 'n_predict lower than max_output_tokens'

$reasoningAbovePredict = Copy-V2ManifestForSemanticTest -Value $manifest
$reasoningAbovePredict.models[0].max_output_tokens = 8192
$reasoningAbovePredict.models[0] | Add-Member -NotePropertyName 'n_predict' -NotePropertyValue 8192
$reasoningAbovePredict.models[0] | Add-Member -NotePropertyName 'reasoning_budget' -NotePropertyValue 32768
Assert-V2SemanticRejection -Candidate $reasoningAbovePredict -ExpectedMessage 'reasoning_budget greater than n_predict'

$messageWithoutBudget = Copy-V2ManifestForSemanticTest -Value $manifest
$messageWithoutBudget.models[0] | Add-Member -NotePropertyName 'reasoning_budget_message' -NotePropertyValue 'write the answer'
Assert-V2SemanticRejection -Candidate $messageWithoutBudget -ExpectedMessage 'reasoning_budget_message without a positive reasoning_budget'

$reasoningCrowdsAnswer = Copy-V2ManifestForSemanticTest -Value $manifest
$reasoningCrowdsAnswer.models[0].max_output_tokens = 32768
$reasoningCrowdsAnswer.models[0] | Add-Member -NotePropertyName 'n_predict' -NotePropertyValue 32768
$reasoningCrowdsAnswer.models[0] | Add-Member -NotePropertyName 'reasoning_budget' -NotePropertyValue 32767
Assert-V2SemanticRejection -Candidate $reasoningCrowdsAnswer -ExpectedMessage 'reserve at least 8192 answer tokens'

$compactInvadesOutput = Copy-V2ManifestForSemanticTest -Value $manifest
$compactInvadesOutput.models[0].context_tokens = 32768
$compactInvadesOutput.models[0].max_output_tokens = 8192
$compactInvadesOutput.models[0] | Add-Member -NotePropertyName 'compact_threshold_tokens' -NotePropertyValue 24576
Assert-V2SemanticRejection -Candidate $compactInvadesOutput -ExpectedMessage 'invade the max_output_tokens reserve'

$compactAtContext = Copy-V2ManifestForSemanticTest -Value $manifest
$compactAtContext.models[0].context_tokens = 32768
$compactAtContext.models[0].max_output_tokens = 8192
$compactAtContext.models[0] | Add-Member -NotePropertyName 'compact_threshold_tokens' -NotePropertyValue 32768
Assert-V2SemanticRejection -Candidate $compactAtContext -ExpectedMessage 'greater than or equal to context_tokens'

foreach ($profileId in @('qwen38-27b-deep-32k', 'qwen38-27b-agent-128k', 'qwen38-27b-huge-256k')) {
    $profileManifest = Copy-V2ManifestForSemanticTest -Value $manifest
    $profileManifest.models = @($profileManifest.models | Where-Object { $_.id -eq $profileId })
    $profileManifest.provider.public_model = $profileId
    Assert-V2ManifestSemantics -Manifest $profileManifest
}

# Fork runtime policy. A fork build has no release identity, so the manifest has
# to carry one: an exact commit, and every setting whose fork default differs
# from the upstream baseline stated rather than inherited.
function New-V2ForkManifestForTest {
    param([Parameter(Mandatory = $true)][object]$Manifest)

    $candidate = Copy-V2ManifestForSemanticTest -Value $Manifest
    $forkRuntime = [pscustomobject]@{
        id            = 'amd-rocm-qwen38-buun'
        state         = 'candidate'
        engine        = 'llama.cpp'
        variant       = 'fork'
        version_label = 'buun-llama-cpp 799e3995cd4f (agentic canary)'
        build_commit  = '799e3995cd4f'
        provenance    = [pscustomobject]@{
            source_repository = 'https://github.com/spiritbuun/buun-llama-cpp'
            source_revision   = '799e3995cd4f19aa9f6a3fa9fb5b4674422bf0ee'
            checkpoint_fix    = [pscustomobject]@{
                reference          = 'https://github.com/ggml-org/llama.cpp/pull/22384'
                evidence           = 'semantic-equivalent'
                verified_utc       = '2026-08-17T00:00:00Z'
                gate_report_sha256 = ('1' * 64)
            }
            build             = [pscustomobject]@{
                backend       = 'ROCm'
                gpu_targets   = @('gfx1201')
                configuration = 'Release'
                targets       = @('llama-server')
            }
        }
        artifact      = [pscustomobject]@{
            path   = 'C:\IA\local-llama\amd\llama_cpp_buun_799e3995cd4f_rocm_gfx1201\llama-server.exe'
            bytes  = 10305536
            sha256 = ('2' * 64)
        }
        device        = [pscustomobject]@{
            backend  = 'ROCm'
            selector = 'ROCm0'
            gpu      = 'AMD Radeon RX 9070 XT (gfx1201)'
            vram_mib = 16304
        }
        environment   = [pscustomobject]@{ ROCBLAS_USE_HIPBLASLT = '0' }
    }

    $forkModel = Copy-V2ManifestForSemanticTest -Value $candidate.models[0]
    $forkModel.id = 'qwen38-27b-buun'
    $forkModel.runtime = 'amd-rocm-qwen38-buun'
    $forkModel.deployments = @('canary')
    $forkModel.resources.peak_vram_gib = 14.5
    $forkModel.resources.peak_commit_gib = 23.3
    $forkModel.resources.peak_ram_gib = 7.4
    foreach ($setting in @(
            @{ Name = 'context_shift'; Value = $false },
            @{ Name = 'kv_unified'; Value = $true },
            @{ Name = 'cache_ram_mib'; Value = 0 },
            @{ Name = 'cache_idle_slots'; Value = $false },
            @{ Name = 'ctx_checkpoints'; Value = 64 },
            @{ Name = 'checkpoint_min_step'; Value = 512 })) {
        $forkModel | Add-Member -NotePropertyName $setting.Name -NotePropertyValue $setting.Value
    }
    $forkModel | Add-Member -NotePropertyName 'tensor_overrides' -NotePropertyValue @(
        [pscustomobject]@{ pattern = 'blk\.(4[4-9]|5[0-9]|6[0-3])\.ffn_.*'; buffer = 'CPU' })

    $candidate.runtimes = @($candidate.runtimes) + @($forkRuntime)
    $candidate.models = @($candidate.models) + @($forkModel)
    return $candidate
}

# The complete shape must be accepted, or every rejection below proves nothing.
# It is checked against the JSON Schema as well as the semantic policy, because
# the two enforce different halves of the contract and Build-V2ForkRuntime.ps1
# emits exactly this entry for an operator to review in.
$forkAccepted = New-V2ForkManifestForTest -Manifest $manifest
Assert-V2ManifestSemantics -Manifest $forkAccepted

$forkManifestPath = Join-Path ([IO.Path]::GetTempPath()) ("cia-fork-manifest-{0}.json" -f [Guid]::NewGuid().ToString('N'))
try {
    # Not Set-Content -Encoding UTF8: that writes a byte-order mark under Windows
    # PowerShell 5.1 and no mark under pwsh 7. cia-manifest decodes with Go's
    # encoding/json, which rejects a BOM outright, so the same test passed on the
    # CI runner and could not pass locally. Writing the encoding explicitly makes
    # the two hosts agree.
    [IO.File]::WriteAllText($forkManifestPath, ($forkAccepted | ConvertTo-Json -Depth 20), [Text.UTF8Encoding]::new($false))
    Assert-V2ManifestSchema -ManifestPath $forkManifestPath -SchemaPath $SchemaPath -ValidatorPath $SchemaValidatorPath
}
finally {
    if (Test-Path -LiteralPath $forkManifestPath) { Remove-Item -LiteralPath $forkManifestPath -Force }
}

$forkWithoutProvenance = New-V2ForkManifestForTest -Manifest $manifest
$forkWithoutProvenance.runtimes[-1].PSObject.Properties.Remove('provenance')
Assert-V2SemanticRejection -Candidate $forkWithoutProvenance -ExpectedMessage 'is a fork and must declare provenance'

$forkOnABranch = New-V2ForkManifestForTest -Manifest $manifest
$forkOnABranch.runtimes[-1].provenance.source_revision = 'master'
Assert-V2SemanticRejection -Candidate $forkOnABranch -ExpectedMessage 'never by master, main, latest, HEAD'

$forkWithMismatchedCommit = New-V2ForkManifestForTest -Manifest $manifest
$forkWithMismatchedCommit.runtimes[-1].build_commit = 'deadbeef'
Assert-V2SemanticRejection -Candidate $forkWithMismatchedCommit -ExpectedMessage 'does not begin the pinned source_revision'

$provenanceWithoutFork = New-V2ForkManifestForTest -Manifest $manifest
$provenanceWithoutFork.runtimes[-1].variant = 'upstream'
Assert-V2SemanticRejection -Candidate $provenanceWithoutFork -ExpectedMessage 'declares provenance without variant'

# Each of these is a setting the fork ships enabled and the upstream baseline does
# not. Leaving one absent silently changes a variable the A/B is meant to hold.
foreach ($pinned in @('context_shift', 'kv_unified', 'cache_ram_mib', 'cache_idle_slots', 'ctx_checkpoints', 'checkpoint_min_step')) {
    $unpinned = New-V2ForkManifestForTest -Manifest $manifest
    $unpinned.models[-1].PSObject.Properties.Remove($pinned)
    Assert-V2SemanticRejection -Candidate $unpinned -ExpectedMessage "leaves $pinned"
}

# The public model is what every harness resolves to. An experimental runtime
# cannot reach it by being selected for some other entry.
$forkServingPublicModel = New-V2ForkManifestForTest -Manifest $manifest
$forkServingPublicModel.provider.public_model = 'qwen38-27b-buun'
Assert-V2SemanticRejection -Candidate $forkServingPublicModel -ExpectedMessage 'Qualify the fork before it can serve the public model'

# Adopting the fork must leave every other model on the runtime it already had.
$forkSeparation = New-V2ForkManifestForTest -Manifest $manifest
foreach ($existing in @($forkSeparation.models | Where-Object { $_.id -ne 'qwen38-27b-buun' })) {
    $original = @($manifest.models | Where-Object { $_.id -eq $existing.id })[0]
    if ($existing.runtime -ne $original.runtime) {
        throw "Adding the fork runtime moved model '$($existing.id)' from '$($original.runtime)' to '$($existing.runtime)'."
    }
}
if ($forkSeparation.provider.public_model -ne $manifest.provider.public_model) {
    throw 'Adding the fork runtime changed provider.public_model.'
}

$qualifiedFinal = Copy-V2ManifestForSemanticTest -Value $manifest
$qualifiedFinal.models[0].deployments = @('final')
$qualifiedFinal.models[0].state = 'qualified'
Set-V2FirstModelRuntimeState -Candidate $qualifiedFinal -State 'qualified'
$qualifiedFinal.models[0].resources.peak_vram_gib = 9.5
$qualifiedFinal.models[0].resources.peak_commit_gib = 12
$qualifiedFinal.models[0].resources.peak_ram_gib = 4
Assert-V2ManifestSemantics -Manifest $qualifiedFinal

if ($VerifyArtifacts) {
    foreach ($runtime in @($manifest.runtimes)) {
        Assert-V2Artifact -Artifact $runtime.artifact -Label "Runtime '$($runtime.id)'" -VerifyHash
    }
    # Retired model entries are retained as provenance/history after their
    # bytes have intentionally left the machine. Active/candidate/qualified
    # artifacts remain mandatory and hash-verified.
    foreach ($model in @($manifest.models | Where-Object { $_.state -ne 'retired' })) {
        Assert-V2Artifact -Artifact $model.artifact -Label "Model '$($model.id)'" -VerifyHash
    }
}

if (-not $Quiet) {
    [pscustomobject]@{
        manifest          = (Resolve-Path -LiteralPath $ManifestPath).Path
        schema_version    = $manifest.schema_version
        runtimes          = @($manifest.runtimes).Count
        models            = @($manifest.models).Count
        canary_models     = @($manifest.models | Where-Object { $_.deployments -contains 'canary' }).Count
        final_models      = @($manifest.models | Where-Object { $_.deployments -contains 'final' }).Count
        artifacts_hashed  = [bool]$VerifyArtifacts
        semantic_policy_tests = 39
        fork_runtimes     = @($manifest.runtimes | Where-Object { (Get-V2RuntimeVariant -Runtime $_) -eq 'fork' }).Count
        valid             = $true
    } | ConvertTo-Json -Depth 3
}
