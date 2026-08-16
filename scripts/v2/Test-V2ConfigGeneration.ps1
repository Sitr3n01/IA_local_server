[CmdletBinding()]
param(
    [string]$ManifestPath = (Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'config\models.yaml'),
    [switch]$Quiet
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Common.ps1')

$routerAPIKeyPath = 'C:\IA\local-ai-v2\state\router-api-key.txt'

function Assert-CommandEquals {
    param(
        [Parameter(Mandatory = $true)][string]$Expected,
        [Parameter(Mandatory = $true)][string]$Actual,
        [Parameter(Mandatory = $true)][string]$Label
    )
    if ($Expected -cne $Actual) {
        throw "$Label`nexpected: $Expected`nactual:   $Actual"
    }
}

function Assert-Contains {
    param(
        [Parameter(Mandatory = $true)][string]$Haystack,
        [Parameter(Mandatory = $true)][string]$Needle,
        [Parameter(Mandatory = $true)][string]$Label
    )
    if (-not $Haystack.Contains($Needle)) {
        throw "$Label`nmissing: $Needle`nin:      $Haystack"
    }
}

function Assert-NotContains {
    param(
        [Parameter(Mandatory = $true)][string]$Haystack,
        [Parameter(Mandatory = $true)][string]$Needle,
        [Parameter(Mandatory = $true)][string]$Label
    )
    if ($Haystack.Contains($Needle)) {
        throw "$Label`nunexpected: $Needle`nin:         $Haystack"
    }
}

$manifest = Read-V2Manifest -Path $ManifestPath
$runtimesById = @{}
foreach ($runtime in @($manifest.runtimes)) { $runtimesById[$runtime.id] = $runtime }

# 1. Byte-identity for every model that declares no optional tuning field. This
#    is the regression that matters most: growing the schema must not rewrite an
#    already-published deployment. The expected string is rebuilt independently
#    of New-V2LlamaServerCommand, from the pre-tuning flag list.
$untunedFields = @(
    'context_shift', 'kv_unified', 'cache_ram_mib', 'ctx_checkpoints',
    'checkpoint_min_step', 'cache_idle_slots', 'spec_decoding', 'tensor_overrides'
)
$untunedCount = 0
foreach ($model in @($manifest.models)) {
    $declared = @($untunedFields | Where-Object { $null -ne $model.PSObject.Properties[$_] })
    if ($declared.Count -gt 0) {
        continue
    }
    $runtime = $runtimesById[$model.runtime]
    $legacy = @(
        ('"{0}"' -f $runtime.artifact.path),
        '--model', ('"{0}"' -f $model.artifact.path),
        '--host', '127.0.0.1',
        '--port', '${PORT}',
        '--alias', $model.id,
        '--device', 'ROCm0',
        '--split-mode', 'none',
        '--gpu-layers', [string]$model.gpu_layers,
        '--flash-attn', 'on',
        '--ctx-size', [string]$model.context_tokens,
        '--batch-size', [string]$model.batch_size,
        '--ubatch-size', [string]$model.ubatch_size,
        '--cache-type-k', $model.cache_type_k,
        '--cache-type-v', $model.cache_type_v,
        '--parallel', '1',
        '--cont-batching',
        '--context-shift',
        '--jinja',
        '--warmup',
        '--metrics',
        '--no-webui',
        '--api-key-file', ('"{0}"' -f $routerAPIKeyPath),
        '--log-disable'
    ) -join ' '
    $actual = New-V2LlamaServerCommand -Runtime $runtime -Model $model -RouterAPIKeyPath $routerAPIKeyPath
    Assert-CommandEquals -Expected $legacy -Actual $actual -Label "Model '$($model.id)' no longer generates its historical command line."
    $untunedCount++
}
if ($untunedCount -lt 1) {
    throw 'No untuned model remained to prove generator byte-stability.'
}

# 2. A fully tuned hybrid model must emit every optional flag, in the documented
#    position, and must replace --context-shift rather than merely dropping it.
$template = @($manifest.models)[0]
$tuned = ($template | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
$tuned.id = 'tuned-hybrid'
$tuned | Add-Member -NotePropertyName 'context_shift' -NotePropertyValue $false
$tuned | Add-Member -NotePropertyName 'kv_unified' -NotePropertyValue $true
$tuned | Add-Member -NotePropertyName 'cache_ram_mib' -NotePropertyValue 6144
$tuned | Add-Member -NotePropertyName 'ctx_checkpoints' -NotePropertyValue 64
$tuned | Add-Member -NotePropertyName 'checkpoint_min_step' -NotePropertyValue 8192
$tuned | Add-Member -NotePropertyName 'cache_idle_slots' -NotePropertyValue $true
$tuned | Add-Member -NotePropertyName 'spec_decoding' -NotePropertyValue ([pscustomobject]@{ type = 'draft-mtp'; draft_n_max = 5 })
$tuned | Add-Member -NotePropertyName 'tensor_overrides' -NotePropertyValue @(
    [pscustomobject]@{ pattern = 'blk\.(4[4-9]|5[0-9]|6[0-3])\.ffn_.*'; buffer = 'CPU' })

$tunedCommand = New-V2LlamaServerCommand -Runtime $runtimesById[$tuned.runtime] -Model $tuned -RouterAPIKeyPath $routerAPIKeyPath
Assert-Contains -Haystack $tunedCommand -Needle '--cont-batching --no-context-shift --kv-unified --cache-ram 6144 --ctx-checkpoints 64 --checkpoint-min-step 8192 --cache-idle-slots --spec-type draft-mtp --spec-draft-n-max 5 -ot "blk\.(4[4-9]|5[0-9]|6[0-3])\.ffn_.*=CPU" --jinja' -Label 'Tuned hybrid command does not emit the optional flag block in order.'
Assert-NotContains -Haystack $tunedCommand -Needle ' --context-shift ' -Label 'Tuned hybrid command still enables context shift.'

# 3. Turning context_shift back on must restore the historical flag exactly.
$shifted = ($template | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
$shifted | Add-Member -NotePropertyName 'context_shift' -NotePropertyValue $true
$shiftedCommand = New-V2LlamaServerCommand -Runtime $runtimesById[$shifted.runtime] -Model $shifted -RouterAPIKeyPath $routerAPIKeyPath
Assert-Contains -Haystack $shiftedCommand -Needle '--cont-batching --context-shift --jinja' -Label 'Explicit context_shift=true did not restore the historical flag.'
Assert-NotContains -Haystack $shiftedCommand -Needle '--no-context-shift' -Label 'Explicit context_shift=true emitted the negative flag.'

# 4. Multiple tensor overrides each get their own -ot argument.
$multi = ($template | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
$multi | Add-Member -NotePropertyName 'tensor_overrides' -NotePropertyValue @(
    [pscustomobject]@{ pattern = 'blk\.6[0-3]\.ffn_.*'; buffer = 'CPU' },
    [pscustomobject]@{ pattern = 'blk\.5[0-9]\.ffn_.*'; buffer = 'CPU' })
$multiCommand = New-V2LlamaServerCommand -Runtime $runtimesById[$multi.runtime] -Model $multi -RouterAPIKeyPath $routerAPIKeyPath
Assert-Contains -Haystack $multiCommand -Needle '-ot "blk\.6[0-3]\.ffn_.*=CPU" -ot "blk\.5[0-9]\.ffn_.*=CPU"' -Label 'Multiple tensor overrides did not each produce an -ot argument.'

# 5. Runtime capability gate. The parsing and comparison halves are pure, so they
#    are asserted here without invoking a Windows binary; only Get-V2RuntimeHelpText
#    needs the real executable and it is exercised during -Apply generation.
$helpFixture = @'
usage: llama-server [options]

  -m,    --model FNAME            model path
  -c,    --ctx-size N             size of the prompt context
  -ot,   --override-tensor SPEC   tensor buffer overrides
  -cram, --cache-ram N            prompt cache size in MiB
  -cms,  --checkpoint-min-step N  minimum spacing between context checkpoints
  -ctxcp, --ctx-checkpoints N     number of context checkpoints
         --no-context-shift       disable context shift
         --jinja                  use the model's chat template
'@
$supported = Get-V2SupportedFlags -HelpText $helpFixture
foreach ($expected in @('--model', '--ctx-size', '--override-tensor', '--cache-ram', '--checkpoint-min-step', '-ot', '-cms', '--jinja')) {
    if (-not $supported.Contains($expected)) {
        throw "Help parsing lost the flag '$expected'."
    }
}
# The flag this whole gate exists to catch: deleted upstream by llama.cpp #22929.
if ($supported.Contains('--checkpoint-every-n-tokens')) {
    throw 'Help parsing invented a flag that is absent from the fixture.'
}

$flagsOf = Get-V2CommandFlags -Command '"C:\r.exe" --model "C:\m.gguf" --ctx-size 4096 -ot "blk\.6[0-3]\.ffn_.*=CPU" --jinja'
foreach ($expected in @('--model', '--ctx-size', '-ot', '--jinja')) {
    if ($flagsOf -notcontains $expected) { throw "Command flag extraction lost '$expected'." }
}
foreach ($value in @('"C:\m.gguf"', '4096', '"blk\.6[0-3]\.ffn_.*=CPU"')) {
    if ($flagsOf -contains $value) { throw "Command flag extraction treated the value '$value' as a flag." }
}

Assert-V2CommandFlagsSupported -Command '"C:\r.exe" --model "C:\m.gguf" --ctx-size 4096 --jinja' `
    -SupportedFlags $supported -RuntimeId 'fixture' -RuntimeSha256 ('0' * 64) -ModelId 'fixture-model'

$rejected = $false
try {
    Assert-V2CommandFlagsSupported -Command '"C:\r.exe" --model "C:\m.gguf" --checkpoint-every-n-tokens 8192' `
        -SupportedFlags $supported -RuntimeId 'fixture' -RuntimeSha256 ('0' * 64) -ModelId 'fixture-model'
}
catch {
    $rejected = $_.Exception.Message -match 'checkpoint-every-n-tokens' -and
                $_.Exception.Message -match 'fixture-model' -and
                $_.Exception.Message -match 'fixture'
}
if (-not $rejected) {
    throw 'A flag absent from the runtime help was accepted, or the error did not name the runtime, model, and flag.'
}

if (-not $Quiet) {
    [pscustomobject]@{
        manifest              = (Resolve-Path -LiteralPath $ManifestPath).Path
        byte_stable_models    = $untunedCount
        generation_tests      = 5
        valid                 = $true
    } | ConvertTo-Json -Depth 3
}
