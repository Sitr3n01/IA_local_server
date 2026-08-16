Set-StrictMode -Version Latest

function Get-V2RepoRoot {
    return (Split-Path -Parent (Split-Path -Parent $PSScriptRoot))
}

function Read-V2Manifest {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    $resolved = (Resolve-Path -LiteralPath $Path -ErrorAction Stop).Path
    try {
        # models.yaml deliberately uses JSON serialization. JSON is valid YAML 1.2 and
        # gives Windows/PowerShell a dependency-free, deterministic parser.
        $manifest = Get-Content -LiteralPath $resolved -Raw -Encoding UTF8 | ConvertFrom-Json
    }
    catch {
        throw "Manifest is not valid JSON-compatible YAML: $resolved. $($_.Exception.Message)"
    }

    return $manifest
}

# Reads an optional manifest property without tripping Set-StrictMode. Every
# tuning field added after schema_version 1 shipped is optional, so absence must
# be indistinguishable from the historical default at the call site.
function Get-V2ModelSetting {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Model,

        [Parameter(Mandatory = $true)]
        [string]$Name,

        $Default = $null
    )

    $property = $Model.PSObject.Properties[$Name]
    if ($null -eq $property -or $null -eq $property.Value) {
        return $Default
    }
    return $property.Value
}

function Assert-V2ManifestSemantics {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Manifest
    )

    if ($Manifest.schema_version -ne 1) {
        throw "Unsupported manifest schema_version '$($Manifest.schema_version)'. Expected 1."
    }
    if ($Manifest.provider.max_loaded_models -ne 1) {
        throw "provider.max_loaded_models must remain 1 for v2."
    }
    foreach ($field in @('archive_sha256', 'executable_sha256')) {
        if ($Manifest.dependencies.llama_swap.$field -notmatch '^[A-Fa-f0-9]{64}$') {
            throw "dependencies.llama_swap.$field is not a valid SHA-256."
        }
    }

    $runtimeIds = @{}
    foreach ($runtime in @($Manifest.runtimes)) {
        if ($runtimeIds.ContainsKey($runtime.id)) {
            throw "Duplicate runtime id '$($runtime.id)'."
        }
        $runtimeIds[$runtime.id] = $runtime
        if ($runtime.artifact.sha256 -notmatch '^[A-Fa-f0-9]{64}$') {
            throw "Runtime '$($runtime.id)' has an invalid SHA-256."
        }
    }

    $modelIds = @{}
    foreach ($model in @($Manifest.models)) {
        if ($modelIds.ContainsKey($model.id)) {
            throw "Duplicate model id '$($model.id)'."
        }
        $modelIds[$model.id] = $true
        if (-not $runtimeIds.ContainsKey($model.runtime)) {
            throw "Model '$($model.id)' references unknown runtime '$($model.runtime)'."
        }
        if ($model.artifact.sha256 -notmatch '^[A-Fa-f0-9]{64}$') {
            throw "Model '$($model.id)' has an invalid SHA-256."
        }
        if ($model.parallel -ne 1) {
            throw "Model '$($model.id)' must use parallel=1 in v2."
        }

        # Cross-field tuning rules. The JSON Schema owns everything expressible
        # per-field (types, ranges, the ubatch dead band); only relationships
        # between fields live here.
        $contextShift = [bool](Get-V2ModelSetting -Model $model -Name 'context_shift' -Default $true)
        $specDecoding = Get-V2ModelSetting -Model $model -Name 'spec_decoding'
        $tensorOverrides = @(Get-V2ModelSetting -Model $model -Name 'tensor_overrides' -Default @())
        $cacheRamMib = Get-V2ModelSetting -Model $model -Name 'cache_ram_mib'
        $peakVramGib = Get-V2ModelSetting -Model $model.resources -Name 'peak_vram_gib'
        $peakCommitGib = Get-V2ModelSetting -Model $model.resources -Name 'peak_commit_gib'

        if ($null -ne $specDecoding -and $contextShift) {
            # llama.cpp asserts in the sampler when a speculative draft is
            # reconciled against a shifted context, and every model that ships an
            # MTP head is a recurrent hybrid that cannot be shifted anyway.
            throw "Model '$($model.id)' enables spec_decoding and must set context_shift=false."
        }

        if ($tensorOverrides.Count -gt 0) {
            if ($null -eq $peakVramGib -and $model.gpu_layers -ge 99) {
                throw "Model '$($model.id)' declares tensor_overrides without resources.peak_vram_gib; measure the split before offloading."
            }
            foreach ($override in $tensorOverrides) {
                if ([string]$override.pattern -match '\s') {
                    throw "Model '$($model.id)' has a tensor_overrides pattern containing whitespace, which would split into separate llama-server arguments."
                }
                try {
                    [void][regex]::new([string]$override.pattern)
                }
                catch {
                    throw "Model '$($model.id)' has an invalid tensor_overrides regex '$($override.pattern)': $($_.Exception.Message)"
                }
            }
        }

        if ($null -ne $cacheRamMib -and [int]$cacheRamMib -gt 0 -and $null -eq $peakCommitGib) {
            # --cache-ram is charged against the Windows commit limit. Without a
            # measured peak the edge admission gate cannot see it at all.
            throw "Model '$($model.id)' declares cache_ram_mib without resources.peak_commit_gib; admission control cannot account for the prompt cache."
        }

        $deployments = @($model.deployments)
        if ($deployments.Count -gt 0) {
            if ($model.state -eq 'retired') {
                throw "Retired model '$($model.id)' cannot belong to a deployment."
            }

            $runtime = $runtimeIds[$model.runtime]
            if ($runtime.state -eq 'retired') {
                throw "Model '$($model.id)' cannot use retired runtime '$($runtime.id)' in a deployment."
            }
        }

        if ($deployments -contains 'final') {
            if ($model.state -notin @('qualified', 'enabled')) {
                throw "Model '$($model.id)' cannot enter final deployment while state is '$($model.state)'."
            }

            $runtime = $runtimeIds[$model.runtime]
            if ($runtime.state -notin @('qualified', 'enabled')) {
                throw "Model '$($model.id)' cannot enter final deployment with runtime '$($runtime.id)' in state '$($runtime.state)'."
            }

            # The JSON Schema permits null resource measurements while an
            # artifact is only a candidate. Promotion to final is stricter: all
            # three measured peaks must be present so admission control cannot
            # silently operate without a qualified capacity profile.
            foreach ($field in @('peak_vram_gib', 'peak_commit_gib', 'peak_ram_gib')) {
                $property = $model.resources.PSObject.Properties[$field]
                if ($null -eq $property -or $null -eq $property.Value) {
                    throw "Model '$($model.id)' cannot enter final deployment without resources.$field."
                }
            }
        }
    }

    if (-not $modelIds.ContainsKey($Manifest.provider.public_model)) {
        throw "provider.public_model references unknown model '$($Manifest.provider.public_model)'."
    }

}

# Builds the llama-server command line for one model. Kept here, apart from the
# publication transaction in New-V2Config.ps1, so the emitted flags can be
# asserted directly by Test-V2ConfigGeneration.ps1.
#
# Ordering is load-bearing: every optional flag is emitted between
# --context-shift and --jinja, so a model that declares none of the optional
# tuning fields produces the exact byte sequence this generator emitted before
# those fields existed. Regenerating a qualified deployment must never rewrite
# its configuration just because the schema grew.
function New-V2LlamaServerCommand {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Runtime,

        [Parameter(Mandatory = $true)]
        [object]$Model,

        [Parameter(Mandatory = $true)]
        [string]$RouterAPIKeyPath
    )

    $arguments = [System.Collections.Generic.List[string]]::new()
    foreach ($argument in @(
            ('"{0}"' -f $Runtime.artifact.path),
            '--model', ('"{0}"' -f $Model.artifact.path),
            '--host', '127.0.0.1',
            '--port', '${PORT}',
            '--alias', $Model.id,
            '--device', 'ROCm0',
            '--split-mode', 'none',
            '--gpu-layers', [string]$Model.gpu_layers,
            '--flash-attn', 'on',
            '--ctx-size', [string]$Model.context_tokens,
            '--batch-size', [string]$Model.batch_size,
            '--ubatch-size', [string]$Model.ubatch_size,
            '--cache-type-k', $Model.cache_type_k,
            '--cache-type-v', $Model.cache_type_v,
            '--parallel', [string]$Model.parallel,
            '--cont-batching'
        )) { $arguments.Add($argument) }

    # Context shift rewrites absolute positions in the KV cache. Models with a
    # recurrent state (Gated DeltaNet hybrids) have no way to rewind that state,
    # so the flag has to be selectable per model rather than always emitted.
    if ([bool](Get-V2ModelSetting -Model $Model -Name 'context_shift' -Default $true)) {
        $arguments.Add('--context-shift')
    }
    else {
        $arguments.Add('--no-context-shift')
    }

    if ([bool](Get-V2ModelSetting -Model $Model -Name 'kv_unified' -Default $false)) {
        $arguments.Add('--kv-unified')
    }
    $cacheRamMib = Get-V2ModelSetting -Model $Model -Name 'cache_ram_mib'
    if ($null -ne $cacheRamMib) {
        $arguments.AddRange([string[]]@('--cache-ram', [string][int]$cacheRamMib))
    }
    $ctxCheckpoints = Get-V2ModelSetting -Model $Model -Name 'ctx_checkpoints'
    if ($null -ne $ctxCheckpoints) {
        $arguments.AddRange([string[]]@('--ctx-checkpoints', [string][int]$ctxCheckpoints))
    }
    $checkpointEvery = Get-V2ModelSetting -Model $Model -Name 'checkpoint_every_n_tokens'
    if ($null -ne $checkpointEvery) {
        $arguments.AddRange([string[]]@('--checkpoint-every-n-tokens', [string][int]$checkpointEvery))
    }
    if ([bool](Get-V2ModelSetting -Model $Model -Name 'cache_idle_slots' -Default $false)) {
        $arguments.Add('--cache-idle-slots')
    }
    $specDecoding = Get-V2ModelSetting -Model $Model -Name 'spec_decoding'
    if ($null -ne $specDecoding) {
        $arguments.AddRange([string[]]@(
                '--spec-type', [string]$specDecoding.type,
                '--spec-draft-n-max', [string][int]$specDecoding.draft_n_max))
    }
    foreach ($override in @(Get-V2ModelSetting -Model $Model -Name 'tensor_overrides' -Default @())) {
        $arguments.AddRange([string[]]@('-ot', ('"{0}={1}"' -f $override.pattern, $override.buffer)))
    }

    foreach ($argument in @(
            '--jinja',
            '--warmup',
            '--metrics',
            '--no-webui',
            '--api-key-file', ('"{0}"' -f $RouterAPIKeyPath),
            '--log-disable'
        )) { $arguments.Add($argument) }

    return ($arguments -join ' ')
}

function Assert-V2ManifestSchema {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ManifestPath,

        [Parameter(Mandatory = $true)]
        [string]$SchemaPath,

        [string]$ValidatorPath = 'C:\IA\local-ai-v2\bin\cia-manifest.exe'
    )

    foreach ($required in @($ManifestPath, $SchemaPath, $ValidatorPath)) {
        if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
            throw "Manifest schema validation dependency is missing: $required"
        }
    }
    & $ValidatorPath --schema $SchemaPath --manifest $ManifestPath
    if ($LASTEXITCODE -ne 0) {
        throw "Manifest does not match its versioned JSON Schema: $ManifestPath"
    }
}

function Assert-V2Artifact {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Artifact,

        [Parameter(Mandatory = $true)]
        [string]$Label,

        [switch]$VerifyHash
    )

    if (-not (Test-Path -LiteralPath $Artifact.path -PathType Leaf)) {
        throw "$Label is missing: $($Artifact.path)"
    }

    $file = Get-Item -LiteralPath $Artifact.path -ErrorAction Stop
    if ([int64]$file.Length -ne [int64]$Artifact.bytes) {
        throw "$Label size mismatch at '$($Artifact.path)': expected $($Artifact.bytes), found $($file.Length)."
    }

    if ($VerifyHash) {
        $actual = (Get-FileHash -LiteralPath $Artifact.path -Algorithm SHA256).Hash
        if ($actual -ne $Artifact.sha256) {
            throw "$Label SHA-256 mismatch at '$($Artifact.path)'."
        }
    }
}

function ConvertTo-V2YamlSingleQuoted {
    param([AllowEmptyString()][string]$Value)
    return "'" + $Value.Replace("'", "''") + "'"
}

function Get-V2DeploymentSettings {
    param(
        [ValidateSet('Canary', 'Final')]
        [string]$Environment
    )

    if ($Environment -eq 'Canary') {
        return [pscustomobject]@{
            Name             = 'canary'
            RouterAddress    = '127.0.0.1:19292'
            DataAddress      = '127.0.0.1:18090'
            ControlAddress   = '127.0.0.1:18091'
            ModelStartPort   = 19300
        }
    }

    return [pscustomobject]@{
        Name             = 'final'
        RouterAddress    = '127.0.0.1:9292'
        DataAddress      = '127.0.0.1:8090'
        ControlAddress   = '127.0.0.1:8091'
        ModelStartPort   = 9300
    }
}

function Test-V2IsAdministrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Assert-V2DeploymentMarker {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,

        [Parameter(Mandatory = $true)]
        [ValidateSet('Canary', 'Final')]
        [string]$Environment,

        [string]$InstallRoot = 'C:\IA\local-ai-v2'
    )

    $settings = Get-V2DeploymentSettings -Environment $Environment
    $environmentName = $settings.Name
    $resolvedRoot = [IO.Path]::GetFullPath($InstallRoot).TrimEnd([char[]]@('\', '/'))
    $expectedRoot = [IO.Path]::GetFullPath('C:\IA\local-ai-v2').TrimEnd([char[]]@('\', '/'))
    if (-not [string]::Equals($resolvedRoot, $expectedRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Deployment marker validation is restricted to '$expectedRoot'."
    }
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Deployment marker is missing: $Path"
    }

    try {
        $marker = Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json
    }
    catch {
        throw "Deployment marker is not valid JSON: $Path. $($_.Exception.Message)"
    }

    foreach ($propertyName in @(
            'schema_version',
            'environment',
            'manifest_path',
            'manifest_schema_path',
            'manifest_sha256',
            'schema_sha256',
            'generated_files'
        )) {
        $property = $marker.PSObject.Properties[$propertyName]
        if ($null -eq $property -or $null -eq $property.Value) {
            throw "Deployment marker is missing required property '$propertyName'. Regenerate it with New-V2Config.ps1."
        }
    }
    if ($marker.PSObject.Properties.Match('models').Count -eq 0 -and
        $marker.PSObject.Properties.Match('model').Count -eq 0) {
        throw "Deployment marker is missing required property 'models' (or transitional 'model'). Regenerate it with New-V2Config.ps1."
    }
    if ($marker.schema_version -ne 1) {
        throw "Deployment marker has unsupported schema_version '$($marker.schema_version)'."
    }
    if ($marker.environment -ne $environmentName) {
        throw "Deployment marker environment '$($marker.environment)' does not match '$environmentName'."
    }
    if ($marker.manifest_sha256 -notmatch '^[A-Fa-f0-9]{64}$' -or $marker.schema_sha256 -notmatch '^[A-Fa-f0-9]{64}$') {
        throw 'Deployment marker contains an invalid manifest or schema SHA-256.'
    }

    $expectedManifestPath = Join-Path $resolvedRoot 'config\models.yaml'
    $expectedSchemaPath = Join-Path $resolvedRoot 'config\models.schema.json'
    try {
        $markerManifestPath = [IO.Path]::GetFullPath([string]$marker.manifest_path)
        $markerSchemaPath = [IO.Path]::GetFullPath([string]$marker.manifest_schema_path)
    }
    catch {
        throw 'Deployment marker contains an invalid installed manifest or schema path.'
    }
    if (-not [string]::Equals($markerManifestPath, $expectedManifestPath, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Deployment marker references an unexpected installed manifest: $markerManifestPath"
    }
    if (-not [string]::Equals($markerSchemaPath, $expectedSchemaPath, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Deployment marker references an unexpected installed schema: $markerSchemaPath"
    }
    foreach ($requiredPath in @($expectedManifestPath, $expectedSchemaPath)) {
        if (-not (Test-Path -LiteralPath $requiredPath -PathType Leaf)) {
            throw "Deployment marker references a missing file: $requiredPath"
        }
    }
    $manifestHash = (Get-FileHash -LiteralPath $expectedManifestPath -Algorithm SHA256).Hash
    $schemaHash = (Get-FileHash -LiteralPath $expectedSchemaPath -Algorithm SHA256).Hash
    if (-not [string]::Equals($manifestHash, [string]$marker.manifest_sha256, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Deployment marker does not certify the installed manifest bytes.'
    }
    if (-not [string]::Equals($schemaHash, [string]$marker.schema_sha256, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Deployment marker does not certify the installed schema bytes.'
    }

    $expectedGeneratedPaths = @(
        (Join-Path $resolvedRoot "config\llama-swap.$environmentName.yaml"),
        (Join-Path $resolvedRoot "config\panel.$environmentName.json"),
        (Join-Path $resolvedRoot "launchers\router-$environmentName.vbs"),
        (Join-Path $resolvedRoot "launchers\edge-$environmentName.vbs"),
        (Join-Path $resolvedRoot "launchers\tray-$environmentName.vbs")
    )
    $certifiedPaths = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($generatedFile in @($marker.generated_files)) {
        if ($null -eq $generatedFile -or $generatedFile.sha256 -notmatch '^[A-Fa-f0-9]{64}$') {
            throw 'Deployment marker contains an invalid generated_files entry.'
        }
        try {
            $generatedPath = [IO.Path]::GetFullPath([string]$generatedFile.path)
        }
        catch {
            throw 'Deployment marker contains an invalid generated file path.'
        }
        if ($generatedPath -notin $expectedGeneratedPaths) {
            throw "Deployment marker certifies an unexpected generated file: $generatedPath"
        }
        if (-not $certifiedPaths.Add($generatedPath)) {
            throw "Deployment marker contains a duplicate generated file: $generatedPath"
        }
        if (-not (Test-Path -LiteralPath $generatedPath -PathType Leaf)) {
            throw "Deployment marker certifies a missing generated file: $generatedPath"
        }
        $actualHash = (Get-FileHash -LiteralPath $generatedPath -Algorithm SHA256).Hash
        if (-not [string]::Equals($actualHash, [string]$generatedFile.sha256, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Deployment marker does not certify the installed bytes of '$generatedPath'."
        }
    }
    if ($certifiedPaths.Count -ne $expectedGeneratedPaths.Count) {
        $missing = @($expectedGeneratedPaths | Where-Object { -not $certifiedPaths.Contains($_) })
        throw "Deployment marker does not certify every generated file: $($missing -join ', ')"
    }

    $manifest = Read-V2Manifest -Path $expectedManifestPath
    Assert-V2ManifestSemantics -Manifest $manifest
    $markerModels = if ($marker.PSObject.Properties.Match('models').Count -gt 0) {
        @($marker.models)
    }
    else {
        @()
    }
    if ($markerModels.Count -eq 0 -and $marker.PSObject.Properties.Match('model').Count -gt 0 -and
        -not [string]::IsNullOrWhiteSpace([string]$marker.model)) {
        $markerModels = @([string]$marker.model)
    }
    $deployedModels = @($manifest.models | Where-Object { $_.deployments -contains $environmentName })
    $deployedIds = @($deployedModels | ForEach-Object { [string]$_.id })
    if ($markerModels.Count -eq 0 -or $markerModels.Count -ne $deployedIds.Count -or
        @($markerModels | Where-Object { $_ -notin $deployedIds }).Count -gt 0) {
        throw "Deployment marker models do not match the installed manifest for '$environmentName'."
    }

    return $marker
}
