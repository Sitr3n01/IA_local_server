[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# A new native process is essential: dot-sourcing or invoking from another
# script can hide the Windows PowerShell 5.1 parameter-default binding defect.
$windowsPowerShell = Join-Path $env:WINDIR 'System32\WindowsPowerShell\v1.0\powershell.exe'
if (-not (Test-Path -LiteralPath $windowsPowerShell -PathType Leaf)) {
    throw 'This entry-point regression test requires Windows PowerShell 5.1.'
}
$temporaryBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\')
$testRoot = Join-Path $temporaryBase ('cia-v2-entry-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $testRoot | Out-Null
$checks = [Collections.Generic.List[string]]::new()

function Invoke-EntryPoint {
    param([string]$Name, [string[]]$ScriptArguments)

    $scriptPath = Join-Path $PSScriptRoot $Name
    $nativeArguments = @('-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $scriptPath) + $ScriptArguments
    $quotedArguments = foreach ($argument in $nativeArguments) {
        if ($argument.Contains('"') -or $argument.EndsWith('\')) {
            throw "Unsupported native test argument: $argument"
        }
        '"' + $argument + '"'
    }
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $windowsPowerShell
    $startInfo.Arguments = $quotedArguments -join ' '
    $startInfo.WorkingDirectory = $testRoot
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    # A direct launch gets Windows PowerShell's own module path. Inherited from
    # a PowerShell 7 host, as in CI, PSModulePath lists PowerShell 7's modules
    # first, and Windows PowerShell then cannot load Microsoft.PowerShell.Utility
    # (Get-FileHash among others). pwsh resets the variable when it starts
    # powershell.exe itself; a raw Process start does not.
    [void]$startInfo.EnvironmentVariables.Remove('PSModulePath')
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    try {
        [void]$process.Start()
        $outputTask = $process.StandardOutput.ReadToEndAsync()
        $errorTask = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(30000)) {
            $process.Kill()
            $process.WaitForExit()
            throw "Entry point timed out: $Name"
        }
        [pscustomobject]@{
            exit_code = $process.ExitCode
            output = $outputTask.GetAwaiter().GetResult() + $errorTask.GetAwaiter().GetResult()
        }
    }
    finally { $process.Dispose() }
}

function Assert-EntryPoint {
    param([string]$Label, [object]$Result, [bool]$Success, [string]$ExpectedText)
    if (($Result.exit_code -eq 0) -ne $Success -or
        $Result.output.IndexOf($ExpectedText, [StringComparison]::OrdinalIgnoreCase) -lt 0) {
        throw "$Label failed (exit $($Result.exit_code)): $($Result.output)"
    }
    $checks.Add($Label)
}

try {
    $missingValidator = Join-Path $testRoot 'missing-validator.exe'
    $missingManifest = Join-Path $testRoot 'explicit-missing-manifest.yaml'
    $missingSchema = Join-Path $testRoot 'explicit-missing-schema.json'
    $missingModel = '__entry_point_unknown_model__'
    $buildArguments = @('-Revision', '799e3995cd4f19aa9f6a3fa9fb5b4674422bf0ee',
        '-SourceRoot', (Join-Path $testRoot 'sources'), '-InstallRoot', (Join-Path $testRoot 'runtimes'))

    foreach ($name in @('New-V2Config.ps1', 'Install-V2Harness.ps1')) {
        $arguments = @('-SchemaValidatorPath', $missingValidator)
        $result = Invoke-EntryPoint $name $arguments
        Assert-EntryPoint "$name defaults reach validation" $result $false 'Manifest schema validation dependency is missing:'
        if (-not $result.output.Contains('missing-validator.exe')) { throw "$name used unexpected default paths." }
        $result = Invoke-EntryPoint $name ($arguments + @('-ManifestPath', $missingManifest))
        Assert-EntryPoint "$name preserves explicit manifest" $result $false 'explicit-missing-manifest.yaml'
        $result = Invoke-EntryPoint $name ($arguments + @('-SchemaPath', $missingSchema))
        Assert-EntryPoint "$name preserves explicit schema" $result $false 'explicit-missing-schema.json'
    }

    $publishArguments = @('-Kind', 'Model', '-Id', $missingModel)
    $result = Invoke-EntryPoint 'Publish-V2Artifact.ps1' $publishArguments
    Assert-EntryPoint 'Publisher defaults read repository manifest' $result $false 'is not declared exactly once in the manifest.'
    $result = Invoke-EntryPoint 'Publish-V2Artifact.ps1' ($publishArguments + @('-ManifestPath', $missingManifest))
    Assert-EntryPoint 'Publisher preserves explicit manifest' $result $false 'explicit-missing-manifest.yaml'

    $smokeArguments = @('-ModelId', $missingModel)
    $result = Invoke-EntryPoint 'Test-V2WorkstationSmoke.ps1' $smokeArguments
    Assert-EntryPoint 'Smoke rejects unknown model before loading' $result $false "Model '$missingModel' is not in"
    $result = Invoke-EntryPoint 'Test-V2WorkstationSmoke.ps1' ($smokeArguments + @('-ManifestPath', $missingManifest))
    Assert-EntryPoint 'Smoke preserves explicit manifest' $result $false 'explicit-missing-manifest.yaml'

    $result = Invoke-EntryPoint 'Build-V2ForkRuntime.ps1' $buildArguments
    Assert-EntryPoint 'Runtime build defaults produce preview' $result $true 'Preview only.'
    $result = Invoke-EntryPoint 'Build-V2ForkRuntime.ps1' ($buildArguments + @('-ManifestPath', $missingManifest))
    Assert-EntryPoint 'Runtime build refuses missing protection manifest' $result $false 'explicit-missing-manifest.yaml'

    $fixturePath = Join-Path $testRoot 'collision-manifest.json'
    $collisionDirectory = Join-Path $testRoot 'runtimes\llama_cpp_buun_799e3995cd4f_rocm_gfx1201'
    $collisionFixture = @{ runtimes = @(@{ id = 'baseline'; artifact = @{ path = (Join-Path $collisionDirectory 'llama-server.exe') } }) }
    [IO.File]::WriteAllText($fixturePath, ($collisionFixture | ConvertTo-Json -Depth 6), [Text.UTF8Encoding]::new($false))
    $result = Invoke-EntryPoint 'Build-V2ForkRuntime.ps1' ($buildArguments + @('-ManifestPath', $fixturePath))
    Assert-EntryPoint 'Runtime build respects explicit collision manifest' $result $false "runtime 'baseline' already lives there."

    $runtimeFixture = @{ models = @(@{ id = 'fixture'; runtime = 'unknown-runtime' }); runtimes = @() }
    [IO.File]::WriteAllText($fixturePath, ($runtimeFixture | ConvertTo-Json -Depth 6), [Text.UTF8Encoding]::new($false))
    $result = Invoke-EntryPoint 'Test-V2WorkstationSmoke.ps1' @('-ModelId', 'fixture', '-ManifestPath', $fixturePath)
    Assert-EntryPoint 'Smoke rejects unknown runtime before loading' $result $false "references unknown runtime 'unknown-runtime'."

    if ((Test-Path -LiteralPath (Join-Path $testRoot 'sources')) -or
        (Test-Path -LiteralPath (Join-Path $testRoot 'runtimes'))) {
        throw 'Entry-point previews unexpectedly created runtime or source directories.'
    }
    $checks.Add('Previews do not create build directories')
    [pscustomobject]@{ passed = $true; checks = $checks.Count; cases = @($checks.ToArray()); native_shell = 'Windows PowerShell 5.1' } |
        ConvertTo-Json -Depth 4
}
finally {
    $resolvedTestRoot = [IO.Path]::GetFullPath($testRoot)
    if (-not $resolvedTestRoot.StartsWith($temporaryBase + '\', [StringComparison]::OrdinalIgnoreCase) -or
        [IO.Path]::GetFileName($resolvedTestRoot) -notmatch '^cia-v2-entry-[0-9a-f]{32}$') {
        throw "Refusing temporary cleanup outside the test root: $resolvedTestRoot"
    }
    Remove-Item -LiteralPath $resolvedTestRoot -Recurse -Force
}
