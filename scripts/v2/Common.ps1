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

# Distinguishes "the manifest declared this" from "the manifest left it to the
# runtime". For a boolean the difference is invisible to Get-V2ModelSetting - a
# declared $false and an absent field both read as $false - and it is exactly the
# difference that matters for a runtime whose shipped default is $true.
function Test-V2ModelSettingDeclared {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Model,

        [Parameter(Mandatory = $true)]
        [string]$Name
    )

    $property = $Model.PSObject.Properties[$Name]
    return ($null -ne $property -and $null -ne $property.Value)
}

# Runtimes without an explicit variant are upstream release builds, which is what
# every entry predating fork support is. Reading the default here keeps the two
# pinned baseline runtimes untouched.
function Get-V2RuntimeVariant {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Runtime
    )

    $property = $Runtime.PSObject.Properties['variant']
    if ($null -eq $property -or [string]::IsNullOrWhiteSpace([string]$property.Value)) {
        return 'upstream'
    }
    return [string]$property.Value
}

# Settings a fork runtime ships with a different default than the upstream
# baseline. Leaving any of them absent does not reproduce the baseline - it
# silently enables a fork behaviour - so a fork profile has to state each one and
# a comparison against upstream stays a comparison of one variable.
#
# The list is not guesswork: cia-fork-gate reads these defaults out of the pinned
# commit and records them in observed_defaults, and its control-variables check
# fails if it cannot.
$script:V2ForkPinnedSettings = @(
    'context_shift',
    'kv_unified',
    'cache_ram_mib',
    'cache_idle_slots',
    'ctx_checkpoints',
    'checkpoint_min_step'
)

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

        # Provenance rules. The JSON Schema already refuses a fork without
        # provenance and a revision that is not a full commit; these restate the
        # two invariants that decide whether a runtime is identifiable at all,
        # with an error an operator can act on.
        $variant = Get-V2RuntimeVariant -Runtime $runtime
        $provenance = $runtime.PSObject.Properties['provenance']
        if ($variant -eq 'fork') {
            if ($null -eq $provenance -or $null -eq $provenance.Value) {
                throw "Runtime '$($runtime.id)' is a fork and must declare provenance; a fork build has no release identity to fall back on."
            }
            $revision = [string]$provenance.Value.source_revision
            if ($revision -notmatch '^[0-9a-f]{40}$') {
                throw "Runtime '$($runtime.id)' pins source_revision '$revision'; a runtime is identified by an exact commit, never by master, main, latest, HEAD, or an abbreviation."
            }
            if ([string]$runtime.build_commit -notlike "$($revision.Substring(0, 8))*") {
                throw "Runtime '$($runtime.id)' reports build_commit '$($runtime.build_commit)', which does not begin the pinned source_revision '$revision'."
            }
        }
        elseif ($null -ne $provenance -and $null -ne $provenance.Value) {
            throw "Runtime '$($runtime.id)' declares provenance without variant 'fork'; provenance is what distinguishes a source build from an upstream release."
        }
    }

    $modelIds = @{}
    foreach ($model in @($Manifest.models)) {
        if ($modelIds.ContainsKey($model.id)) {
            throw "Duplicate model id '$($model.id)'."
        }
        $modelIds[$model.id] = $model
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

        # Fork runtimes ship different defaults than the upstream baseline, so a
        # field left absent does not mean "behave like upstream" - it means
        # "behave like the fork". Requiring each one to be stated keeps the
        # upstream-versus-fork comparison a comparison of the runtime, which is
        # the only variable it is supposed to isolate.
        if ((Get-V2RuntimeVariant -Runtime $runtimeIds[$model.runtime]) -eq 'fork') {
            $unpinned = @($script:V2ForkPinnedSettings | Where-Object { -not (Test-V2ModelSettingDeclared -Model $model -Name $_) })
            if ($unpinned.Count -gt 0) {
                throw "Model '$($model.id)' runs on fork runtime '$($model.runtime)' and leaves $($unpinned -join ', ') to the fork's own defaults. State every one of them so the run differs from the upstream baseline only by the runtime."
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

    # The public model is what every harness resolves to, so an experimental
    # runtime cannot reach it by being selected for some other model. Adopting a
    # fork is deliberately a decision about one canary entry until the fork has
    # been through the promotion gates in docs/MODEL_PROMOTION.md.
    $publicModel = $modelIds[$Manifest.provider.public_model]
    $publicRuntime = $runtimeIds[$publicModel.runtime]
    if ((Get-V2RuntimeVariant -Runtime $publicRuntime) -eq 'fork' -and
        $publicRuntime.state -notin @('qualified', 'enabled')) {
        throw "provider.public_model '$($publicModel.id)' is served by fork runtime '$($publicRuntime.id)' in state '$($publicRuntime.state)'. Qualify the fork before it can serve the public model."
    }
}

# Extracts every option token from a llama-server --help dump.
#
# Deliberately permissive: it collects any --flag appearing anywhere in the text,
# including inside prose. The failure it exists to catch is a flag that upstream
# *deleted* (--checkpoint-every-n-tokens, removed by llama.cpp PR #22929), and a
# deleted flag appears nowhere at all. Being strict about column layout would
# make this brittle against help-text reformatting for no gain, and the strict
# direction is the dangerous one: a false rejection blocks a valid deployment.
function Get-V2SupportedFlags {
    param(
        [Parameter(Mandatory = $true)]
        [AllowEmptyString()]
        [string]$HelpText
    )

    $flags = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($match in [regex]::Matches($HelpText, '(?<![\w-])(--?[A-Za-z][\w-]*)')) {
        [void]$flags.Add($match.Groups[1].Value)
    }
    return $flags
}

# Pulls the option tokens out of a generated command line. Values are skipped
# because they never start with a dash in a generated command: paths are quoted,
# cache types and buffer names are bare words, and tensor override patterns are
# quoted as one token.
function Get-V2CommandFlags {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Command
    )

    $flags = [System.Collections.Generic.List[string]]::new()
    foreach ($token in ($Command -split '\s+')) {
        if ($token -match '^--?[A-Za-z][\w-]*$') {
            $flags.Add($token)
        }
    }
    return $flags
}

# Fails generation when the selected runtime does not implement a flag the model
# requires. Without this the manifest can drift from the executable silently:
# llama-server rejects unknown arguments, so the failure surfaces as a model that
# will not start, at first inference, rather than at configuration time.
function Assert-V2CommandFlagsSupported {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Command,

        [Parameter(Mandatory = $true)]
        [object]$SupportedFlags,

        [Parameter(Mandatory = $true)]
        [string]$RuntimeId,

        [Parameter(Mandatory = $true)]
        [string]$RuntimeSha256,

        [Parameter(Mandatory = $true)]
        [string]$ModelId
    )

    $missing = @()
    foreach ($flag in (Get-V2CommandFlags -Command $Command)) {
        if (-not $SupportedFlags.Contains($flag)) {
            $missing += $flag
        }
    }
    if ($missing.Count -gt 0) {
        throw ("Runtime '{0}' (SHA-256 {1}) does not support {2} required by model '{3}': {4}. " -f
            $RuntimeId, $RuntimeSha256, $(if ($missing.Count -eq 1) { 'a flag' } else { 'flags' }), $ModelId, ($missing -join ', ')) +
            'Qualify a runtime build that implements them, or remove the manifest fields that emit them.'
    }
}

# Reads the runtime's own option list. Runs the executable with --help only:
# no model is loaded, no port is opened, no inference happens, and nothing is
# downloaded. The caller pairs the result with the artifact SHA-256 that
# Assert-V2Artifact already verified, so the capability snapshot is bound to the
# exact bytes rather than to a directory name.
function Get-V2RuntimeHelpText {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Runtime executable is missing and its capabilities cannot be verified: $Path"
    }
    try {
        $output = & $Path --help 2>&1 | ForEach-Object { $_.ToString() }
    }
    catch {
        throw "Runtime '$Path' could not be queried with --help: $($_.Exception.Message)"
    }
    $text = ($output -join [Environment]::NewLine)
    if ([string]::IsNullOrWhiteSpace($text)) {
        throw "Runtime '$Path' returned no option list for --help; its capabilities cannot be verified."
    }
    return $text
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
    # Thread counts only matter once part of the model is CPU-resident, and the
    # winner comes from the qualification sweep rather than from the core count:
    # on an 8C/16T part SMT contention often makes 8 beat 16 for quantized GEMM.
    # Omitted here, llama-server keeps its own default, so nothing changes for
    # the fully GPU-resident models.
    $threads = Get-V2ModelSetting -Model $Model -Name 'threads'
    if ($null -ne $threads) {
        $arguments.AddRange([string[]]@('--threads', [string][int]$threads))
    }
    $threadsBatch = Get-V2ModelSetting -Model $Model -Name 'threads_batch'
    if ($null -ne $threadsBatch) {
        $arguments.AddRange([string[]]@('--threads-batch', [string][int]$threadsBatch))
    }
    $cacheRamMib = Get-V2ModelSetting -Model $Model -Name 'cache_ram_mib'
    if ($null -ne $cacheRamMib) {
        $arguments.AddRange([string[]]@('--cache-ram', [string][int]$cacheRamMib))
    }
    $ctxCheckpoints = Get-V2ModelSetting -Model $Model -Name 'ctx_checkpoints'
    if ($null -ne $ctxCheckpoints) {
        $arguments.AddRange([string[]]@('--ctx-checkpoints', [string][int]$ctxCheckpoints))
    }
    # llama.cpp PR #22929 deleted --checkpoint-every-n-tokens on 2026-05-25 and
    # replaced it with --checkpoint-min-step. llama-server rejects unknown
    # arguments outright, so emitting the old spelling does not degrade - the
    # model fails to start. The manifest field was renamed with it rather than
    # translated, so a stale manifest fails schema validation instead of
    # silently producing a command line that cannot run.
    $checkpointMinStep = Get-V2ModelSetting -Model $Model -Name 'checkpoint_min_step'
    if ($null -ne $checkpointMinStep) {
        $arguments.AddRange([string[]]@('--checkpoint-min-step', [string][int]$checkpointMinStep))
    }
    # Three states, not two. Absent keeps whatever the runtime does by itself,
    # which is what every model generated before this field existed relies on.
    # Declared is emitted either way, because a fork that saves idle slots to the
    # prompt cache by default cannot be turned off by silence - only by
    # --no-cache-idle-slots.
    if (Test-V2ModelSettingDeclared -Model $Model -Name 'cache_idle_slots') {
        if ([bool](Get-V2ModelSetting -Model $Model -Name 'cache_idle_slots' -Default $false)) {
            $arguments.Add('--cache-idle-slots')
        }
        else {
            $arguments.Add('--no-cache-idle-slots')
        }
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
