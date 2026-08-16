[CmdletBinding()]
param(
    [string]$ManifestPath = (Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'config\models.yaml'),
    [string]$SchemaPath = (Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'config\models.schema.json'),
    [string]$SchemaValidatorPath = 'C:\IA\local-ai-v2\bin\cia-manifest.exe',
    [switch]$VerifyArtifacts,
    [switch]$Quiet
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Common.ps1')

Assert-V2ManifestSchema -ManifestPath $ManifestPath -SchemaPath $SchemaPath -ValidatorPath $SchemaValidatorPath
$manifest = Read-V2Manifest -Path $ManifestPath
Assert-V2ManifestSemantics -Manifest $manifest

function Copy-V2ManifestForSemanticTest {
    param([Parameter(Mandatory = $true)][object]$Value)
    return ($Value | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
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

# Keep the promotion invariants executable instead of relying only on the
# current manifest snapshot to happen to exercise them.
$retiredModel = Copy-V2ManifestForSemanticTest -Value $manifest
$retiredModel.models[0].state = 'retired'
Assert-V2SemanticRejection -Candidate $retiredModel -ExpectedMessage 'Retired model'

$retiredRuntime = Copy-V2ManifestForSemanticTest -Value $manifest
$retiredRuntime.runtimes[0].state = 'retired'
Assert-V2SemanticRejection -Candidate $retiredRuntime -ExpectedMessage 'retired runtime'

$invalidArtifactHash = Copy-V2ManifestForSemanticTest -Value $manifest
$invalidArtifactHash.models[0].artifact.sha256 = 'invalid'
Assert-V2SemanticRejection -Candidate $invalidArtifactHash -ExpectedMessage 'invalid SHA-256'

$candidateFinalModel = Copy-V2ManifestForSemanticTest -Value $manifest
$candidateFinalModel.models[0].deployments = @('final')
$candidateFinalModel.runtimes[0].state = 'qualified'
Assert-V2SemanticRejection -Candidate $candidateFinalModel -ExpectedMessage "while state is 'candidate'"

$candidateFinalRuntime = Copy-V2ManifestForSemanticTest -Value $manifest
$candidateFinalRuntime.models[0].deployments = @('final')
$candidateFinalRuntime.models[0].state = 'qualified'
Assert-V2SemanticRejection -Candidate $candidateFinalRuntime -ExpectedMessage 'cannot enter final deployment with runtime'

$incompleteFinalResources = Copy-V2ManifestForSemanticTest -Value $manifest
$incompleteFinalResources.models[0].deployments = @('final')
$incompleteFinalResources.models[0].state = 'qualified'
$incompleteFinalResources.runtimes[0].state = 'qualified'
$incompleteFinalResources.models[0].resources.peak_vram_gib = 9.5
Assert-V2SemanticRejection -Candidate $incompleteFinalResources -ExpectedMessage 'resources\.peak_commit_gib'

# Hybrid-model tuning rules. These are relationships between fields, which the
# JSON Schema cannot express; the per-field ranges (including the 65..256 ubatch
# dead band) stay declarative in models.schema.json.
$specWithContextShift = Copy-V2ManifestForSemanticTest -Value $manifest
$specWithContextShift.models[0] | Add-Member -NotePropertyName 'spec_decoding' -NotePropertyValue ([pscustomobject]@{ type = 'draft-mtp'; draft_n_max = 5 })
Assert-V2SemanticRejection -Candidate $specWithContextShift -ExpectedMessage 'must set context_shift=false'

$offloadWithoutMeasurement = Copy-V2ManifestForSemanticTest -Value $manifest
$offloadWithoutMeasurement.models[0] | Add-Member -NotePropertyName 'tensor_overrides' -NotePropertyValue @([pscustomobject]@{ pattern = 'blk\.(4[4-9])\.ffn_.*'; buffer = 'CPU' })
Assert-V2SemanticRejection -Candidate $offloadWithoutMeasurement -ExpectedMessage 'measure the split before offloading'

$offloadWithWhitespace = Copy-V2ManifestForSemanticTest -Value $manifest
$offloadWithWhitespace.models[0].resources.peak_vram_gib = 14.5
$offloadWithWhitespace.models[0] | Add-Member -NotePropertyName 'tensor_overrides' -NotePropertyValue @([pscustomobject]@{ pattern = 'blk\.4 4\.ffn_.*'; buffer = 'CPU' })
Assert-V2SemanticRejection -Candidate $offloadWithWhitespace -ExpectedMessage 'containing whitespace'

$offloadWithBadRegex = Copy-V2ManifestForSemanticTest -Value $manifest
$offloadWithBadRegex.models[0].resources.peak_vram_gib = 14.5
$offloadWithBadRegex.models[0] | Add-Member -NotePropertyName 'tensor_overrides' -NotePropertyValue @([pscustomobject]@{ pattern = 'blk\.(4[4-9\.ffn_.*'; buffer = 'CPU' })
Assert-V2SemanticRejection -Candidate $offloadWithBadRegex -ExpectedMessage 'invalid tensor_overrides regex'

$cacheWithoutCommitMeasurement = Copy-V2ManifestForSemanticTest -Value $manifest
$cacheWithoutCommitMeasurement.models[0] | Add-Member -NotePropertyName 'cache_ram_mib' -NotePropertyValue 6144
Assert-V2SemanticRejection -Candidate $cacheWithoutCommitMeasurement -ExpectedMessage 'cannot account for the prompt cache'

# The whole tuning surface together, measured, must be accepted.
$tunedHybrid = Copy-V2ManifestForSemanticTest -Value $manifest
$tunedHybrid.models[0].resources.peak_vram_gib = 14.5
$tunedHybrid.models[0].resources.peak_commit_gib = 26
$tunedHybrid.models[0] | Add-Member -NotePropertyName 'context_shift' -NotePropertyValue $false
$tunedHybrid.models[0] | Add-Member -NotePropertyName 'kv_unified' -NotePropertyValue $true
$tunedHybrid.models[0] | Add-Member -NotePropertyName 'cache_ram_mib' -NotePropertyValue 6144
$tunedHybrid.models[0] | Add-Member -NotePropertyName 'spec_decoding' -NotePropertyValue ([pscustomobject]@{ type = 'draft-mtp'; draft_n_max = 5 })
$tunedHybrid.models[0] | Add-Member -NotePropertyName 'tensor_overrides' -NotePropertyValue @([pscustomobject]@{ pattern = 'blk\.(4[4-9]|5[0-9]|6[0-3])\.ffn_.*'; buffer = 'CPU' })
Assert-V2ManifestSemantics -Manifest $tunedHybrid

$qualifiedFinal = Copy-V2ManifestForSemanticTest -Value $manifest
$qualifiedFinal.models[0].deployments = @('final')
$qualifiedFinal.models[0].state = 'qualified'
$qualifiedFinal.runtimes[0].state = 'qualified'
$qualifiedFinal.models[0].resources.peak_vram_gib = 9.5
$qualifiedFinal.models[0].resources.peak_commit_gib = 12
$qualifiedFinal.models[0].resources.peak_ram_gib = 4
Assert-V2ManifestSemantics -Manifest $qualifiedFinal

if ($VerifyArtifacts) {
    foreach ($runtime in @($manifest.runtimes)) {
        Assert-V2Artifact -Artifact $runtime.artifact -Label "Runtime '$($runtime.id)'" -VerifyHash
    }
    foreach ($model in @($manifest.models)) {
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
        semantic_policy_tests = 13
        valid             = $true
    } | ConvertTo-Json -Depth 3
}
