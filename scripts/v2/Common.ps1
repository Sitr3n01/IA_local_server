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
