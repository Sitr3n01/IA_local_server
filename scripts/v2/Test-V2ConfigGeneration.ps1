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
    'context_shift', 'kv_unified', 'threads', 'threads_batch', 'cache_ram_mib', 'ctx_checkpoints',
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
$tuned | Add-Member -NotePropertyName 'threads' -NotePropertyValue 8
$tuned | Add-Member -NotePropertyName 'threads_batch' -NotePropertyValue 16
$tuned | Add-Member -NotePropertyName 'cache_ram_mib' -NotePropertyValue 6144
$tuned | Add-Member -NotePropertyName 'ctx_checkpoints' -NotePropertyValue 64
$tuned | Add-Member -NotePropertyName 'checkpoint_min_step' -NotePropertyValue 8192
$tuned | Add-Member -NotePropertyName 'cache_idle_slots' -NotePropertyValue $true
$tuned | Add-Member -NotePropertyName 'spec_decoding' -NotePropertyValue ([pscustomobject]@{ type = 'draft-mtp'; draft_n_max = 5 })
$tuned | Add-Member -NotePropertyName 'tensor_overrides' -NotePropertyValue @(
    [pscustomobject]@{ pattern = 'blk\.(4[4-9]|5[0-9]|6[0-3])\.ffn_.*'; buffer = 'CPU' })

$tunedCommand = New-V2LlamaServerCommand -Runtime $runtimesById[$tuned.runtime] -Model $tuned -RouterAPIKeyPath $routerAPIKeyPath
Assert-Contains -Haystack $tunedCommand -Needle '--cont-batching --no-context-shift --kv-unified --threads 8 --threads-batch 16 --cache-ram 6144 --ctx-checkpoints 64 --checkpoint-min-step 8192 --cache-idle-slots --spec-type draft-mtp --spec-draft-n-max 5 -ot "blk\.(4[4-9]|5[0-9]|6[0-3])\.ffn_.*=CPU" --jinja' -Label 'Tuned hybrid command does not emit the optional flag block in order.'
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
  -t,    --threads N              number of CPU threads
  -tb,   --threads-batch N        number of CPU threads for batch processing
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

# 6. The first buun-llama-cpp qualification profile. Every setting whose fork
#    default differs from the upstream baseline has to appear on the command
#    line, because on this runtime silence is not neutrality: omitting
#    --cache-ram leaves an 8 GiB host prompt cache enabled, and omitting
#    --no-cache-idle-slots leaves idle slots being written into it. Both would
#    change what is being compared and neither would be visible in the manifest.
$fork = ($template | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
$fork.id = 'qwen38-27b-buun'
$fork.context_tokens = 262144
$fork.cache_type_k = 'q4_0'
$fork.cache_type_v = 'q4_0'
$fork.batch_size = 2048
$fork.ubatch_size = 288
$fork | Add-Member -NotePropertyName 'context_shift' -NotePropertyValue $false
$fork | Add-Member -NotePropertyName 'kv_unified' -NotePropertyValue $true
$fork | Add-Member -NotePropertyName 'cache_ram_mib' -NotePropertyValue 0
$fork | Add-Member -NotePropertyName 'ctx_checkpoints' -NotePropertyValue 64
$fork | Add-Member -NotePropertyName 'checkpoint_min_step' -NotePropertyValue 512
$fork | Add-Member -NotePropertyName 'cache_idle_slots' -NotePropertyValue $false
$fork | Add-Member -NotePropertyName 'spec_decoding' -NotePropertyValue ([pscustomobject]@{ type = 'draft-mtp'; draft_n_max = 3 })
$fork | Add-Member -NotePropertyName 'tensor_overrides' -NotePropertyValue @(
    [pscustomobject]@{ pattern = 'blk\.(4[4-9]|5[0-9]|6[0-3])\.ffn_.*'; buffer = 'CPU' })

$forkCommand = New-V2LlamaServerCommand -Runtime $runtimesById[$fork.runtime] -Model $fork -RouterAPIKeyPath $routerAPIKeyPath
Assert-Contains -Haystack $forkCommand -Needle '--cache-type-k q4_0 --cache-type-v q4_0 --parallel 1 --cont-batching --no-context-shift --kv-unified --cache-ram 0 --ctx-checkpoints 64 --checkpoint-min-step 512 --no-cache-idle-slots --spec-type draft-mtp --spec-draft-n-max 3 -ot "blk\.(4[4-9]|5[0-9]|6[0-3])\.ffn_.*=CPU" --jinja' -Label 'The buun qualification profile does not pin every control variable in order.'
Assert-Contains -Haystack $forkCommand -Needle '--ctx-size 262144' -Label 'The buun profile lost its configured context.'
Assert-NotContains -Haystack $forkCommand -Needle ' --context-shift ' -Label 'The buun profile enables context shift on a recurrent model.'
Assert-NotContains -Haystack $forkCommand -Needle '--cache-idle-slots --' -Label 'The buun profile emitted the positive idle-slot flag.'

# 7. The prompt cache stays off, and off is stated rather than assumed. A run
#    that silently allocated 8 GiB of host cache would also be charged for it by
#    admission only if the manifest declared it, so the two have to agree.
if ($forkCommand -notmatch '--cache-ram\s+0(\s|$)') {
    throw "The buun profile does not disable the host prompt cache explicitly: $forkCommand"
}

# 8. cache_idle_slots is three-valued. Absent must emit nothing, so a model
#    generated before the field existed keeps its historical command line; only
#    a declared value produces a flag, and a declared false produces the negative
#    form rather than silence.
$idleAbsent = ($template | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
$idleAbsentCommand = New-V2LlamaServerCommand -Runtime $runtimesById[$idleAbsent.runtime] -Model $idleAbsent -RouterAPIKeyPath $routerAPIKeyPath
Assert-NotContains -Haystack $idleAbsentCommand -Needle 'cache-idle-slots' -Label 'An undeclared cache_idle_slots emitted a flag.'

$idleEnabled = ($template | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
$idleEnabled | Add-Member -NotePropertyName 'cache_idle_slots' -NotePropertyValue $true
$idleEnabledCommand = New-V2LlamaServerCommand -Runtime $runtimesById[$idleEnabled.runtime] -Model $idleEnabled -RouterAPIKeyPath $routerAPIKeyPath
Assert-Contains -Haystack $idleEnabledCommand -Needle '--cache-idle-slots' -Label 'A declared cache_idle_slots=true emitted no flag.'
Assert-NotContains -Haystack $idleEnabledCommand -Needle '--no-cache-idle-slots' -Label 'A declared cache_idle_slots=true emitted the negative flag.'

# 9. The capability gate has to cover the negative flag too. A runtime whose
#    help does not list --no-cache-idle-slots cannot be told to leave the cache
#    alone, and generation must fail rather than produce a command that silently
#    keeps the fork default.
$forkHelpFixture = $helpFixture + @'

       --cache-idle-slots, --no-cache-idle-slots   save idle slots to the prompt cache
       --kv-unified             use a single unified KV buffer
       --spec-type TYPE         speculative implementation
       --spec-draft-n-max N     maximum draft tokens
'@
$forkSupported = Get-V2SupportedFlags -HelpText $forkHelpFixture
foreach ($expected in @('--cache-idle-slots', '--no-cache-idle-slots', '--kv-unified', '--spec-type')) {
    if (-not $forkSupported.Contains($expected)) {
        throw "Help parsing lost the flag '$expected' from a two-form boolean option."
    }
}

$negativeRejected = $false
try {
    Assert-V2CommandFlagsSupported -Command '"C:\r.exe" --model "C:\m.gguf" --no-cache-idle-slots' `
        -SupportedFlags $supported -RuntimeId 'buun-fixture' -RuntimeSha256 ('0' * 64) -ModelId 'qwen38-27b-buun'
}
catch {
    $negativeRejected = $_.Exception.Message -match 'no-cache-idle-slots'
}
if (-not $negativeRejected) {
    throw 'A runtime whose help omits --no-cache-idle-slots accepted a profile that requires it.'
}

if (-not $Quiet) {
    [pscustomobject]@{
        manifest              = (Resolve-Path -LiteralPath $ManifestPath).Path
        byte_stable_models    = $untunedCount
        generation_tests      = 9
        valid                 = $true
    } | ConvertTo-Json -Depth 3
}
