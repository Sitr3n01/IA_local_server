[CmdletBinding()]
param(
    [string]$ManifestPath,
    [switch]$Quiet
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Common.ps1')
if ([string]::IsNullOrWhiteSpace($ManifestPath)) {
    $ManifestPath = Join-Path (Get-V2RepoRoot) 'config\models.yaml'
}

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
#
#    The list is every optional field that CHANGES the emitted command line, not
#    every optional field: compact_threshold_tokens is deliberately absent
#    because it never reaches llama-server, so a model declaring only that one
#    must still match the historical line.
#
#    The generation-budget fields and chat_template_file were added on
#    2026-08-25. They had always belonged here, but no model had ever declared
#    them WITHOUT also declaring threads or tensor_overrides, so the gap could
#    not be reached. gemma4-12b-qat-ud-q4xl-256k is the first: it declares a
#    reasoning budget and nothing else from this list, and was held to a
#    historical line it has a documented reason to differ from.
$untunedFields = @(
    'context_shift', 'kv_unified', 'threads', 'threads_batch', 'cache_ram_mib', 'ctx_checkpoints',
    'checkpoint_min_step', 'cache_idle_slots', 'spec_decoding', 'moe_offload', 'tensor_overrides',
    'n_predict', 'reasoning_budget', 'reasoning_budget_message', 'chat_template_file'
)
#    A real entry that declares any of these fields is proved through a copy with
#    every such field removed: the same artifact, runtime and base fields that a
#    deployment published before the fields existed would have carried. The proof
#    therefore never depends on the manifest keeping an untuned model, since every
#    active profile may legitimately pin its runtime's defaults.
$untunedCount = 0
$strippedCount = 0
foreach ($entry in @($manifest.models)) {
    $declared = @($untunedFields | Where-Object { $null -ne $entry.PSObject.Properties[$_] })
    $model = $entry
    if ($declared.Count -gt 0) {
        $model = ($entry | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
        foreach ($field in $declared) { $model.PSObject.Properties.Remove($field) }
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
    if ($declared.Count -gt 0) {
        Assert-CommandEquals -Expected $legacy -Actual $actual -Label "Model '$($model.id)', stripped of its tuning fields, no longer generates its historical command line."
        $strippedCount++
    }
    else {
        Assert-CommandEquals -Expected $legacy -Actual $actual -Label "Model '$($model.id)' no longer generates its historical command line."
        $untunedCount++
    }
}
if (($untunedCount + $strippedCount) -lt 1) {
    throw 'No model was available to prove generator byte-stability.'
}

# 2. A fully tuned hybrid model must emit every optional flag, in the documented
#    position, and must replace --context-shift rather than merely dropping it.
#    Every case below derives from this template and adds the fields it tests, so
#    the template is the first real entry with its tuning fields removed: the
#    cases must not depend on what that entry happens to declare.
$template = (@($manifest.models)[0] | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
foreach ($field in $untunedFields) { [void]$template.PSObject.Properties.Remove($field) }
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
$tuned | Add-Member -NotePropertyName 'moe_offload' -NotePropertyValue ([pscustomobject]@{ cpu_layers = 4 })
$tuned | Add-Member -NotePropertyName 'tensor_overrides' -NotePropertyValue @(
    [pscustomobject]@{ pattern = 'blk\.(4[4-9]|5[0-9]|6[0-3])\.ffn_.*'; buffer = 'CPU' })

$tunedCommand = New-V2LlamaServerCommand -Runtime $runtimesById[$tuned.runtime] -Model $tuned -RouterAPIKeyPath $routerAPIKeyPath
Assert-Contains -Haystack $tunedCommand -Needle '--cont-batching --no-context-shift --kv-unified --threads 8 --threads-batch 16 --cache-ram 6144 --n-cpu-moe 4 --ctx-checkpoints 64 --checkpoint-min-step 8192 --cache-idle-slots --spec-type draft-mtp --spec-draft-n-max 5 -ot "blk\.(4[4-9]|5[0-9]|6[0-3])\.ffn_.*=CPU" --jinja' -Label 'Tuned hybrid command does not emit the optional flag block in order.'
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

# 5. Generic MoE offload emits llama.cpp's typed MoE placement flags without
#    relying on tensor regexes. Declared zero is emitted too, so a sweep can pin
#    its full-GPU control cell instead of inheriting a runtime default.
$moeZero = ($template | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
$moeZero | Add-Member -NotePropertyName 'moe_offload' -NotePropertyValue ([pscustomobject]@{ cpu_layers = 0 })
$moeZeroCommand = New-V2LlamaServerCommand -Runtime $runtimesById[$moeZero.runtime] -Model $moeZero -RouterAPIKeyPath $routerAPIKeyPath
Assert-Contains -Haystack $moeZeroCommand -Needle '--n-cpu-moe 0' -Label 'moe_offload.cpu_layers=0 did not emit --n-cpu-moe 0.'
Assert-NotContains -Haystack $moeZeroCommand -Needle '--cpu-moe' -Label 'moe_offload.cpu_layers=0 emitted all-CPU MoE.'

$moeAll = ($template | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
$moeAll | Add-Member -NotePropertyName 'moe_offload' -NotePropertyValue ([pscustomobject]@{ cpu_all = $true })
$moeAllCommand = New-V2LlamaServerCommand -Runtime $runtimesById[$moeAll.runtime] -Model $moeAll -RouterAPIKeyPath $routerAPIKeyPath
Assert-Contains -Haystack $moeAllCommand -Needle '--cpu-moe' -Label 'moe_offload.cpu_all=true did not emit --cpu-moe.'
Assert-NotContains -Haystack $moeAllCommand -Needle '--n-cpu-moe' -Label 'moe_offload.cpu_all=true emitted partial MoE placement.'

# 6. Production and qualification use the same llama-server argument builder.
#    This pins the Gemma shape that matters next: gpu_layers=99, cpu_layers=4,
#    KV q4/q4, 128k context, no Qwen tensor split.
$gemmaProduction = ($template | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
$gemmaProduction.id = 'local'
$gemmaProduction.artifact.path = 'C:\IA\models\Gemma-4-26B-A4B-QAT-UD-Q4_K_XL.gguf'
$gemmaProduction.context_tokens = 131072
$gemmaProduction.cache_type_k = 'q4_0'
$gemmaProduction.cache_type_v = 'q4_0'
$gemmaProduction.gpu_layers = 99
$gemmaProduction.batch_size = 2048
$gemmaProduction.ubatch_size = 288
$gemmaProduction.parallel = 1
$gemmaProduction | Add-Member -NotePropertyName 'context_shift' -NotePropertyValue $false
$gemmaProduction | Add-Member -NotePropertyName 'threads' -NotePropertyValue 8
$gemmaProduction | Add-Member -NotePropertyName 'moe_offload' -NotePropertyValue ([pscustomobject]@{ cpu_layers = 4 })

$gemmaQualification = New-V2BenchmarkModelSpec -ModelPath $gemmaProduction.artifact.path -Alias 'local' `
    -ContextTokens 131072 -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 288 -BatchSize 2048 `
    -NGpuLayers 99 -Threads 8 -Parallel 1 -NCpuMoe 4
$productionArgs = New-V2LlamaServerArguments -Model $gemmaProduction -Port '19399' -Alias 'local' -IncludeJinja -IncludeWarmup -IncludeMetrics -IncludeNoWebui
$qualificationArgs = New-V2LlamaServerArguments -Model $gemmaQualification -Port '19399' -Alias 'local' -IncludeJinja -IncludeWarmup -IncludeMetrics -IncludeNoWebui
Assert-CommandEquals -Expected ($productionArgs -join ' ') -Actual ($qualificationArgs -join ' ') `
    -Label 'Gemma production and qualification arguments drifted.'
Assert-Contains -Haystack ($qualificationArgs -join ' ') -Needle '--n-cpu-moe 4' `
    -Label 'Gemma qualification arguments lost the MoE placement.'
Assert-NotContains -Haystack ($qualificationArgs -join ' ') -Needle 'blk\.(6[0-3])\.ffn_.*=CPU' `
    -Label 'Generic Gemma qualification inherited the Qwen tensor split.'

$genericBenchmark = New-V2BenchmarkModelSpec -ModelPath $gemmaProduction.artifact.path -Alias 'local' `
    -ContextTokens 32768 -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 288 -BatchSize 2048 `
    -NGpuLayers 99 -Threads 8 -Parallel 1
$genericArgs = New-V2LlamaServerArguments -Model $genericBenchmark -Port '19399' -Alias 'local' -IncludeNoWebui
Assert-NotContains -Haystack ($genericArgs -join ' ') -Needle '-ot' `
    -Label 'Generic benchmark model emitted a tensor override by default.'

# 7. Runtime capability gate. The parsing and comparison halves are pure, so they
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
  -cmoe, --cpu-moe                keep all Mixture of Experts (MoE) weights in the CPU
  -ncmoe, --n-cpu-moe N           keep the Mixture of Experts (MoE) weights of the first N layers in the CPU
  -cms,  --checkpoint-min-step N  minimum spacing between context checkpoints
  -ctxcp, --ctx-checkpoints N     number of context checkpoints
         --no-context-shift       disable context shift
         --jinja                  use the model's chat template
'@
$supported = Get-V2SupportedFlags -HelpText $helpFixture
foreach ($expected in @('--model', '--ctx-size', '--override-tensor', '--cache-ram', '--cpu-moe', '--n-cpu-moe', '--checkpoint-min-step', '-ot', '-cms', '--jinja')) {
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

$moeRejected = $false
try {
    Assert-V2CommandFlagsSupported -Command '"C:\r.exe" --model "C:\m.gguf" --n-cpu-moe 4' `
        -SupportedFlags (Get-V2SupportedFlags -HelpText 'usage: llama-server [options] --model FNAME') -RuntimeId 'legacy-fixture' -RuntimeSha256 ('0' * 64) -ModelId 'moe-model'
}
catch {
    $moeRejected = $_.Exception.Message -match 'n-cpu-moe' -and
                   $_.Exception.Message -match 'moe-model' -and
                   $_.Exception.Message -match 'legacy-fixture'
}
if (-not $moeRejected) {
    throw 'A runtime whose help omits --n-cpu-moe accepted a MoE partial-offload profile.'
}

$benchHelpWithoutCpuAll = Get-V2SupportedFlags -HelpText @'
usage: llama-bench [options]
  -m FNAME
  -ncmoe, --n-cpu-moe N
'@
$benchPartial = New-V2LlamaBenchArguments -Model $gemmaQualification -TestKind pp -Tokens 512 -Repetitions 1 -SupportedFlags $benchHelpWithoutCpuAll
if ($benchPartial -notcontains '--n-cpu-moe' -or $benchPartial -notcontains '4') {
    throw 'llama-bench partial MoE placement did not emit --n-cpu-moe 4.'
}
$gemmaAllBench = New-V2BenchmarkModelSpec -ModelPath $gemmaProduction.artifact.path -Alias 'local' `
    -ContextTokens 32768 -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 288 -BatchSize 2048 `
    -NGpuLayers 99 -Threads 8 -Parallel 1 -CpuMoe
$benchCpuAllRejected = $false
try {
    [void](New-V2LlamaBenchArguments -Model $gemmaAllBench -TestKind pp -Tokens 512 -Repetitions 1 -SupportedFlags $benchHelpWithoutCpuAll)
}
catch {
    $benchCpuAllRejected = $_.Exception.Message -match 'does not support --cpu-moe'
}
if (-not $benchCpuAllRejected) {
    throw 'llama-bench --cpu-moe was emitted or accepted when its help did not advertise it.'
}

# 8. The first buun-llama-cpp qualification profile. Every setting whose fork
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

# 9. The prompt cache stays off, and off is stated rather than assumed. A run
#    that silently allocated 8 GiB of host cache would also be charged for it by
#    admission only if the manifest declared it, so the two have to agree.
if ($forkCommand -notmatch '--cache-ram\s+0(\s|$)') {
    throw "The buun profile does not disable the host prompt cache explicitly: $forkCommand"
}

# 10. cache_idle_slots is three-valued. Absent must emit nothing, so a model
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

# 11. The capability gate has to cover the negative flag too. A runtime whose
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

# 12. Multi-word argument values must survive command construction as ONE argv
#    token. Windows PowerShell 5.1's Start-Process joins an argument array into
#    a command line without quoting elements that contain spaces, so on
#    2026-08-23 the qwen38-27b-huge-256k reasoning-budget message reached
#    llama-server as five stray words and the process died five seconds after
#    launch on `invalid argument: budget`. Three benchmark runners now build
#    their argument line through ConvertTo-V2CommandLine for exactly that
#    reason, and this asserts the property they depend on: parsing the generated
#    line back into argv must return the phrase intact.
$reasoningMessage = 'Thinking budget reached. Stop analysing and write the final answer now.'
$spacedSpec = New-V2BenchmarkModelSpec -ModelPath 'C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-Q2_K_XL.gguf' `
    -Alias 'local' -ContextTokens 262144 -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 288 `
    -BatchSize 2048 -NGpuLayers 99 -Threads 8 -Parallel 1 -TensorOverride 'blk\.(6[0-3])\.ffn_.*=CPU'
$spacedSpec | Add-Member -NotePropertyName 'n_predict' -NotePropertyValue 32768
$spacedSpec | Add-Member -NotePropertyName 'reasoning_budget' -NotePropertyValue 24576
$spacedSpec | Add-Member -NotePropertyName 'reasoning_budget_message' -NotePropertyValue $reasoningMessage
$spacedArguments = New-V2LlamaServerArguments -Model $spacedSpec -Port '19399' -Alias 'local' `
    -IncludeJinja -IncludeWarmup -IncludeMetrics -IncludeNoWebui
$spacedLine = ConvertTo-V2CommandLine -Arguments $spacedArguments

Assert-Contains -Haystack $spacedLine -Needle ('--reasoning-budget-message "{0}"' -f $reasoningMessage) `
    -Label 'The reasoning-budget message was not emitted as a single quoted value.'

# The assertion that actually matters: split the generated line the way a
# process launcher does and count the tokens. A regression that re-introduces
# the raw-array pattern yields several tokens here instead of one.
function Split-CommandLineForTest {
    param([Parameter(Mandatory = $true)][string]$Line)
    $tokens = [System.Collections.Generic.List[string]]::new()
    $current = [System.Text.StringBuilder]::new()
    $inQuotes = $false
    $started = $false
    foreach ($char in $Line.ToCharArray()) {
        if ($char -eq [char]34) { $inQuotes = -not $inQuotes; $started = $true; continue }
        if ($char -eq ' ' -and -not $inQuotes) {
            if ($started) { [void]$tokens.Add($current.ToString()); [void]$current.Clear(); $started = $false }
            continue
        }
        [void]$current.Append($char)
        $started = $true
    }
    if ($started) { [void]$tokens.Add($current.ToString()) }
    return [string[]]$tokens
}

$argv = Split-CommandLineForTest -Line $spacedLine
$messageIndex = [Array]::IndexOf($argv, '--reasoning-budget-message')
if ($messageIndex -lt 0) {
    throw 'The generated command line lost --reasoning-budget-message entirely.'
}
if ($argv[$messageIndex + 1] -cne $reasoningMessage) {
    throw ("A multi-word argument value was split across argv tokens." +
        "`nexpected: $reasoningMessage`nactual:   $($argv[$messageIndex + 1])")
}
# The stray word that killed the first Huge-256k attempt. If quoting regresses,
# 'budget' becomes an argv entry of its own.
if ($argv -ccontains 'budget') {
    throw 'The reasoning-budget message was split into separate command-line tokens again.'
}
$otIndex = [Array]::IndexOf($argv, '-ot')
if ($otIndex -lt 0 -or $argv[$otIndex + 1] -cne 'blk\.(6[0-3])\.ffn_.*=CPU') {
    throw 'The tensor override did not survive command construction as one argv value.'
}

# The same property through the production path rather than the benchmark path,
# so the two cannot drift apart.
$spacedProduction = ($template | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
$spacedProduction | Add-Member -NotePropertyName 'reasoning_budget' -NotePropertyValue 24576
$spacedProduction | Add-Member -NotePropertyName 'reasoning_budget_message' -NotePropertyValue $reasoningMessage
$spacedProductionLine = New-V2LlamaServerCommand -Runtime $runtimesById[$spacedProduction.runtime] `
    -Model $spacedProduction -RouterAPIKeyPath $routerAPIKeyPath
$productionArgv = Split-CommandLineForTest -Line $spacedProductionLine
$productionIndex = [Array]::IndexOf($productionArgv, '--reasoning-budget-message')
if ($productionIndex -lt 0 -or $productionArgv[$productionIndex + 1] -cne $reasoningMessage) {
    throw 'The production command line split the multi-word reasoning-budget message.'
}

# 13. The qualification request ceiling. Resolve-V2QualificationRequestBudget is
#    the single source of truth for what one qualification run may generate, and
#    its precedence is the contract the 2026-08-23 campaign violated by letting
#    every profile inherit a coding-fixture default.
function Assert-Budget {
    param(
        [Parameter(Mandatory = $true)][object]$Budget,
        [Parameter(Mandatory = $true)][int]$ExpectedMaxTokens,
        [Parameter(Mandatory = $true)][string]$ExpectedSource,
        [Parameter(Mandatory = $true)][string]$Label
    )
    if ([int]$Budget.max_tokens -ne $ExpectedMaxTokens) {
        throw "$Label`nexpected max_tokens: $ExpectedMaxTokens`nactual:              $($Budget.max_tokens)"
    }
    if ([string]$Budget.source -cne $ExpectedSource) {
        throw "$Label`nexpected source: $ExpectedSource`nactual:          $($Budget.source)"
    }
}

# The shipped canary profiles, by their manifest values.
Assert-Budget -Budget (Resolve-V2QualificationRequestBudget -NPredict 8192) `
    -ExpectedMaxTokens 8192 -ExpectedSource 'profile' `
    -Label 'A Deep/Agent-shaped profile did not derive its request ceiling from n_predict.'
Assert-Budget -Budget (Resolve-V2QualificationRequestBudget -NPredict 32768 -ReasoningBudget 24576) `
    -ExpectedMaxTokens 32768 -ExpectedSource 'profile' `
    -Label 'Huge 256k did not derive its 32768-token request ceiling from n_predict.'
# Gemma declares no n_predict, so each suite keeps its own fixture default and
# the resolver says so by returning 0 rather than inventing a number.
Assert-Budget -Budget (Resolve-V2QualificationRequestBudget) `
    -ExpectedMaxTokens 0 -ExpectedSource 'fixture' `
    -Label 'A profile without n_predict did not fall through to the fixture default.'
# An operator override outranks the profile in both directions.
Assert-Budget -Budget (Resolve-V2QualificationRequestBudget -ExplicitMaxTokens 2048 -NPredict 32768) `
    -ExpectedMaxTokens 2048 -ExpectedSource 'explicit' `
    -Label 'An explicit -MaxTokens did not outrank the profile ceiling.'
Assert-Budget -Budget (Resolve-V2QualificationRequestBudget -ExplicitMaxTokens 16384) `
    -ExpectedMaxTokens 16384 -ExpectedSource 'explicit' `
    -Label 'An explicit -MaxTokens was not honoured without a profile ceiling.'

# 13b. The effective generation ceiling. The server stops at n_predict whatever
#    max_tokens asks for, so min() of the two is the only number a budget rule
#    may use. Mirrored by CEILING_TABLE in scripts/v2/eval/test_qualify_budget.py.
function Assert-EffectiveCeiling {
    param(
        [int]$RequestMaxTokens,
        [int]$NPredict,
        [Parameter(Mandatory = $true)][int]$Expected
    )
    $got = Get-V2EffectiveGenerationCeiling -RequestMaxTokens $RequestMaxTokens -NPredict $NPredict
    if ([int]$got -ne $Expected) {
        throw ("min(request=$RequestMaxTokens, n_predict=$NPredict) resolved to $got; expected $Expected.")
    }
}

Assert-EffectiveCeiling -RequestMaxTokens 0 -NPredict 0 -Expected 0
Assert-EffectiveCeiling -RequestMaxTokens 8192 -NPredict 0 -Expected 8192
Assert-EffectiveCeiling -RequestMaxTokens 0 -NPredict 32768 -Expected 32768
Assert-EffectiveCeiling -RequestMaxTokens 8192 -NPredict 8192 -Expected 8192
Assert-EffectiveCeiling -RequestMaxTokens 32768 -NPredict 32768 -Expected 32768
# The regression: an explicit request above the profile contract is truncated by
# the server, so the ceiling that applies is the profile's.
Assert-EffectiveCeiling -RequestMaxTokens 32768 -NPredict 8192 -Expected 8192
Assert-EffectiveCeiling -RequestMaxTokens 16384 -NPredict 8192 -Expected 8192
# A request below the contract is honoured as asked; the server never raises one.
Assert-EffectiveCeiling -RequestMaxTokens 4096 -NPredict 32768 -Expected 4096

# 13c. budget_profile. Only `deployment` is a baseline. Mirrored by
#    BUDGET_PROFILE_TABLE in scripts/v2/eval/test_qualify_budget.py.
function Assert-BudgetProfile {
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [int]$RequestMaxTokens,
        [int]$NPredict,
        [Parameter(Mandatory = $true)][string]$Expected
    )
    $got = Get-V2BudgetProfile -Source $Source -RequestMaxTokens $RequestMaxTokens -NPredict $NPredict
    if ([string]$got -cne $Expected) {
        throw ("budget_profile(source=$Source, request=$RequestMaxTokens, n_predict=$NPredict) " +
            "was '$got'; expected '$Expected'.")
    }
}

Assert-BudgetProfile -Source 'fixture' -RequestMaxTokens 0 -NPredict 0 -Expected 'deployment'
Assert-BudgetProfile -Source 'profile' -RequestMaxTokens 8192 -NPredict 8192 -Expected 'deployment'
Assert-BudgetProfile -Source 'profile' -RequestMaxTokens 32768 -NPredict 32768 -Expected 'deployment'
# An explicit ceiling that restates the contract exactly is still the contract.
Assert-BudgetProfile -Source 'explicit' -RequestMaxTokens 8192 -NPredict 8192 -Expected 'deployment'
Assert-BudgetProfile -Source 'explicit' -RequestMaxTokens 32768 -NPredict 32768 -Expected 'deployment'
Assert-BudgetProfile -Source 'explicit' -RequestMaxTokens 4096 -NPredict 8192 -Expected 'constrained'
Assert-BudgetProfile -Source 'explicit' -RequestMaxTokens 8192 -NPredict 32768 -Expected 'constrained'
Assert-BudgetProfile -Source 'explicit' -RequestMaxTokens 32768 -NPredict 8192 -Expected 'expanded'
Assert-BudgetProfile -Source 'explicit' -RequestMaxTokens 16384 -NPredict 8192 -Expected 'expanded'
# With no declared n_predict the per-suite fixture defaults are the contract, and
# a uniform explicit ceiling is compared against the widest of them.
Assert-BudgetProfile -Source 'explicit' -RequestMaxTokens 8192 -NPredict 0 -Expected 'deployment'
Assert-BudgetProfile -Source 'explicit' -RequestMaxTokens 4096 -NPredict 0 -Expected 'constrained'
Assert-BudgetProfile -Source 'explicit' -RequestMaxTokens 16384 -NPredict 0 -Expected 'expanded'

$hugeBudget = Resolve-V2QualificationRequestBudget -NPredict 32768 -ReasoningBudget 24576
if ([int]$hugeBudget.answer_reserve -ne 8192) {
    throw "Huge 256k reported an answer reserve of $($hugeBudget.answer_reserve); 32768 - 24576 = 8192."
}
if (-not $hugeBudget.reserve_ok) {
    throw 'The shipped Huge 256k budget was reported as leaving too little for the answer.'
}
if ($hugeBudget.diagnostic) {
    throw 'A normal profile qualification was flagged as a constrained diagnostic.'
}

# The exact combination the 2026-08-23 campaign ran, and the reason this
# resolver exists: an 8192-token request against a 24576-token thinking budget
# has a negative answer reserve and must never start a model.
$impossibleRefused = $false
try {
    Resolve-V2QualificationRequestBudget -ExplicitMaxTokens 8192 -ReasoningBudget 24576 | Out-Null
}
catch {
    $impossibleRefused = $_.Exception.Message -match 'reasoning_budget'
}
if (-not $impossibleRefused) {
    throw 'max_tokens=8192 with reasoning_budget=24576 was accepted for a normal qualification.'
}

# The shape this hardening pass added: an explicit ceiling ABOVE the profile's
# n_predict. The old resolver let the operator's 32768 outrank the profile and
# computed 32768 - 24576 = 8192, a legal-looking reserve for a run the server
# would have cut off at 8192 with 24576 already spent thinking. The effective
# ceiling is 8192, the real reserve is -16384, and it must be refused as a
# baseline exactly as -MaxTokens 8192 is.
$expandedRefused = ''
try {
    Resolve-V2QualificationRequestBudget -NPredict 8192 -ExplicitMaxTokens 32768 `
        -ReasoningBudget 24576 | Out-Null
}
catch {
    $expandedRefused = $_.Exception.Message
}
if ($expandedRefused -notmatch 'reasoning_budget') {
    throw 'n_predict=8192 with max_tokens=32768 and reasoning_budget=24576 was accepted as a baseline.'
}
if ($expandedRefused -notmatch 'effective generation ceiling of 8192') {
    throw "The refusal did not name the effective generation ceiling: $expandedRefused"
}
if ($expandedRefused -notmatch '-16384') {
    throw "The refusal did not report the real (negative) answer reserve: $expandedRefused"
}
# Named as a diagnostic it is still measurable, and still not a baseline.
$expandedDiagnostic = Resolve-V2QualificationRequestBudget -NPredict 8192 `
    -ExplicitMaxTokens 32768 -ReasoningBudget 24576 -Diagnostic
if ([int]$expandedDiagnostic.effective_generation_ceiling -ne 8192) {
    throw 'The expanded diagnostic did not report an 8192-token effective ceiling.'
}
if ([int]$expandedDiagnostic.answer_reserve -ne -16384 -or $expandedDiagnostic.reserve_ok) {
    throw 'The expanded diagnostic reported a satisfiable answer reserve.'
}
if ([string]$expandedDiagnostic.budget_profile -cne 'expanded') {
    throw "The expanded diagnostic was labelled '$($expandedDiagnostic.budget_profile)'."
}
# Every shipped profile still resolves as the deployment contract it is.
foreach ($shipped in @(
        (Resolve-V2QualificationRequestBudget -NPredict 8192),
        (Resolve-V2QualificationRequestBudget -NPredict 32768 -ReasoningBudget 24576),
        (Resolve-V2QualificationRequestBudget))) {
    if ([string]$shipped.budget_profile -cne 'deployment') {
        throw "A shipped profile was classified as '$($shipped.budget_profile)' rather than deployment."
    }
}

# The same shape reached by silence rather than by an explicit cap: a profile
# with a reasoning budget and no ceiling would inherit a fixture default that
# cannot hold the answer either.
$impossibleByDefaultRefused = $false
try {
    Resolve-V2QualificationRequestBudget -ReasoningBudget 24576 | Out-Null
}
catch {
    $impossibleByDefaultRefused = $_.Exception.Message -match 'fixture default'
}
if (-not $impossibleByDefaultRefused) {
    throw 'A reasoning budget above every fixture default was accepted with no request ceiling.'
}

# Deliberately measuring a constrained cap stays possible, and is stamped.
$diagnosticBudget = Resolve-V2QualificationRequestBudget -ExplicitMaxTokens 8192 `
    -ReasoningBudget 24576 -Diagnostic
if (-not $diagnosticBudget.diagnostic -or $diagnosticBudget.reserve_ok) {
    throw 'The constrained-budget diagnostic did not record itself as constrained.'
}
if ([int]$diagnosticBudget.max_tokens -ne 8192) {
    throw 'The constrained-budget diagnostic did not honour the explicit cap it was asked for.'
}

# A ceiling at or below the 8192 floor is judged against itself rather than
# against an unreachable target, which is what makes the manifest rule and this
# resolver the same rule: reserve min(8192, ceiling), so a ceiling that small
# cannot fund bounded reasoning at all until it is raised. The error has to name
# the profile's own ceiling, not 8192, or the operator is told to clear a bar
# that does not apply to them.
$smallCeilingRefused = ''
try {
    Resolve-V2QualificationRequestBudget -NPredict 4096 -ReasoningBudget 1024 | Out-Null
}
catch {
    $smallCeilingRefused = $_.Exception.Message
}
if ($smallCeilingRefused -notmatch 'at least 4096 are required') {
    throw "A 4096-token ceiling was judged against the 8192-token floor instead of its own: $smallCeilingRefused"
}
$smallVerdict = Test-V2AnswerReserve -RequestCeiling 4096 -ReasoningBudget 1024
if ([int]$smallVerdict.minimum_reserve -ne 4096 -or [int]$smallVerdict.answer_reserve -ne 3072) {
    throw 'Test-V2AnswerReserve did not scale its floor to a ceiling below 8192.'
}
# Raising the ceiling is the documented way out, and it works.
$raisedBudget = Resolve-V2QualificationRequestBudget -NPredict 16384 -ReasoningBudget 8192
if (-not $raisedBudget.reserve_ok -or [int]$raisedBudget.answer_reserve -ne 8192) {
    throw 'A 16384/8192 split was not accepted despite reserving the full 8192-token floor.'
}
# A profile with no reasoning budget is never asked about an answer reserve.
$noReasoning = Resolve-V2QualificationRequestBudget -NPredict 8192
if (-not $noReasoning.reserve_ok -or $null -ne $noReasoning.answer_reserve) {
    throw 'A profile without a reasoning budget was given an answer-reserve verdict.'
}

# 14. The manifest validator and the qualification runner must agree. A profile
#    the manifest accepts has to be one the runner will measure at the ceiling
#    it declares, or a legal manifest becomes an impossible benchmark -- which
#    is the whole defect.
foreach ($budgetModel in @($manifest.models)) {
    $modelNPredict = Get-V2ModelSetting -Model $budgetModel -Name 'n_predict'
    if ($null -eq $modelNPredict) { continue }
    $modelReasoning = Get-V2ModelSetting -Model $budgetModel -Name 'reasoning_budget'
    $resolved = Resolve-V2QualificationRequestBudget -NPredict ([int]$modelNPredict) `
        -ReasoningBudget $(if ($null -ne $modelReasoning) { [int]$modelReasoning } else { -1 })
    if ([int]$resolved.max_tokens -ne [int]$modelNPredict) {
        throw "Model '$($budgetModel.id)' would be qualified at $($resolved.max_tokens) tokens against a declared n_predict of $modelNPredict."
    }
    $modelMaxOutput = Get-V2ModelSetting -Model $budgetModel -Name 'max_output_tokens'
    if ($null -ne $modelMaxOutput -and [int]$resolved.max_tokens -lt [int]$modelMaxOutput) {
        throw "Model '$($budgetModel.id)' would be qualified below its advertised max_output_tokens."
    }
}

# 15. The runner's own wiring, end to end. Sections 12-14 prove the helpers are
#    right; this proves Invoke-V2ProfileQualification.ps1 actually calls them.
#    The 2026-08-23 defect was not a wrong helper -- it was a correct profile
#    ceiling that the runner never forwarded, so `--max-tokens` was absent and
#    every suite fell back to a fixture default. -DryRun resolves and prints
#    exactly what the real run would execute, without loading anything.
$qualificationRunner = Join-Path $PSScriptRoot 'Invoke-V2ProfileQualification.ps1'
$dryRunRoot = Join-Path ([IO.Path]::GetTempPath()) ('v2-dryrun-' + [Guid]::NewGuid().ToString('N'))

function Get-QualificationPlan {
    param([hashtable]$Splat)
    $defaults = @{
        DryRun     = $true
        ModelPath  = 'C:\IA\models\does-not-need-to-exist.gguf'
        Label      = 'dryrun'
        OutputRoot = $dryRunRoot
    }
    foreach ($key in $Splat.Keys) { $defaults[$key] = $Splat[$key] }
    # 6>$null drops the runner's progress banner. A test that prints a plan for
    # every cell it checks buries the one line that matters when it fails.
    return & $qualificationRunner @defaults -WarningAction SilentlyContinue 6>$null
}

function Get-ArgumentValue {
    param([string[]]$Arguments, [string]$Name)
    $index = [Array]::IndexOf($Arguments, $Name)
    if ($index -lt 0 -or $index -eq $Arguments.Count - 1) { return $null }
    return [string]$Arguments[$index + 1]
}

# Huge 256k: the profile whose declared contract the campaign never measured.
$hugePlan = Get-QualificationPlan -Splat @{
    Label                  = 'huge-256k'
    ContextTokens          = 262144
    NPredict               = 32768
    ReasoningBudget        = 24576
    ReasoningBudgetMessage = 'Thinking budget reached. Stop analysing and write the final answer now.'
}
if ((Get-ArgumentValue -Arguments $hugePlan.qualify_arguments -Name '--max-tokens') -cne '32768') {
    throw 'Huge 256k did not forward its 32768-token profile ceiling to qualify.py.'
}
if ((Get-ArgumentValue -Arguments $hugePlan.qualify_arguments -Name '--max-tokens-source') -cne 'profile') {
    throw 'Huge 256k did not record that its request ceiling came from the profile.'
}
if ((Get-ArgumentValue -Arguments $hugePlan.qualify_arguments -Name '--reasoning-budget') -cne '24576') {
    throw 'Huge 256k did not report its reasoning budget to qualify.py.'
}
if ([int]$hugePlan.request_budget.answer_reserve -ne 8192) {
    throw 'The Huge 256k plan did not report an 8192-token answer reserve.'
}
if ($hugePlan.qualify_arguments -ccontains '--allow-constrained-request-budget') {
    throw 'A normal Huge 256k qualification asked to allow a constrained request budget.'
}
if ($hugePlan.started) { throw 'A dry run reported that it started something.' }

# Deep and Agent both declare n_predict 8192, which happens to equal the coding
# fixture default. Asserted anyway: the value has to arrive by derivation, and
# --max-tokens-source is what tells a future reader which it was.
foreach ($profile in @('deep-32k', 'agent-128k')) {
    $plan = Get-QualificationPlan -Splat @{ Label = $profile; NPredict = 8192 }
    if ((Get-ArgumentValue -Arguments $plan.qualify_arguments -Name '--max-tokens') -cne '8192') {
        throw "$profile did not forward its 8192-token profile ceiling to qualify.py."
    }
    if ((Get-ArgumentValue -Arguments $plan.qualify_arguments -Name '--max-tokens-source') -cne 'profile') {
        throw "$profile did not record that its request ceiling came from the profile."
    }
}

# Gemma declares no n_predict, so the fixtures keep their own defaults and the
# plan says so rather than inventing a ceiling.
$gemmaPlan = Get-QualificationPlan -Splat @{ Label = 'gemma4-12b'; ContextTokens = 131072 }
if ($gemmaPlan.qualify_arguments -ccontains '--max-tokens') {
    throw 'A profile without n_predict invented a request ceiling instead of using fixture defaults.'
}
if ((Get-ArgumentValue -Arguments $gemmaPlan.qualify_arguments -Name '--max-tokens-source') -cne 'fixture') {
    throw 'A profile without n_predict did not record the fixture-default origin.'
}
if ($null -ne $gemmaPlan.request_budget.effective_max_tokens) {
    throw 'A fixture-default plan reported a numeric effective ceiling.'
}

# The profile's n_predict reaches qualify.py separately from the request, so the
# verifier can compute min(max_tokens, n_predict) instead of trusting a request
# the server is about to truncate.
if ((Get-ArgumentValue -Arguments $hugePlan.qualify_arguments -Name '--n-predict') -cne '32768') {
    throw 'Huge 256k did not forward its n_predict to qualify.py.'
}
if ([string]$hugePlan.request_budget.budget_profile -cne 'deployment') {
    throw "A profile qualified at its own n_predict was labelled '$($hugePlan.request_budget.budget_profile)'."
}
if ([int]$hugePlan.request_budget.effective_generation_ceiling -ne 32768) {
    throw 'The Huge 256k plan did not report a 32768-token effective generation ceiling.'
}
if ($gemmaPlan.qualify_arguments -ccontains '--n-predict') {
    throw 'A profile without n_predict forwarded one anyway.'
}
if ([string]$gemmaPlan.request_budget.budget_profile -cne 'deployment') {
    throw 'A fixture-default plan was not labelled as the deployment contract.'
}

# An explicit ceiling that is not the profile's contract is a diagnostic cell,
# whichever side of the contract it falls on, and the plan has to say so before
# the night it costs hours -- not afterwards in a report someone re-reads.
$constrainedPlan = Get-QualificationPlan -Splat @{
    Label = 'below'; NPredict = 32768; MaxTokens = 8192
}
if ([string]$constrainedPlan.request_budget.budget_profile -cne 'constrained') {
    throw "An 8192-token cap on a 32768-token profile was labelled '$($constrainedPlan.request_budget.budget_profile)'."
}
$expandedPlan = Get-QualificationPlan -Splat @{
    Label = 'above'; NPredict = 8192; MaxTokens = 32768
}
if ([string]$expandedPlan.request_budget.budget_profile -cne 'expanded') {
    throw "A 32768-token cap on an 8192-token profile was labelled '$($expandedPlan.request_budget.budget_profile)'."
}
if ([int]$expandedPlan.request_budget.effective_generation_ceiling -ne 8192) {
    throw 'An expanded plan reported more effective generation headroom than n_predict allows.'
}
# The request itself is still recorded verbatim: the operator asked for 32768 and
# the report must not quietly rewrite that to 8192.
if ((Get-ArgumentValue -Arguments $expandedPlan.qualify_arguments -Name '--max-tokens') -cne '32768') {
    throw 'An expanded plan rewrote the operator request instead of recording it.'
}
if ([int]$expandedPlan.request_budget.request_max_tokens -ne 32768) {
    throw 'An expanded plan did not record the request the operator actually made.'
}
# The runner refuses the physically impossible expansion before loading anything.
$expandedRunnerRefused = ''
try {
    Get-QualificationPlan -Splat @{
        Label = 'expanded-impossible'; NPredict = 8192; MaxTokens = 32768; ReasoningBudget = 24576
    } | Out-Null
}
catch {
    $expandedRunnerRefused = $_.Exception.Message
}
if ($expandedRunnerRefused -notmatch 'effective generation ceiling of 8192') {
    throw "The runner accepted -NPredict 8192 -MaxTokens 32768 -ReasoningBudget 24576: $expandedRunnerRefused"
}

# An operator override still wins, and is labelled as an override.
$overridePlan = Get-QualificationPlan -Splat @{
    Label = 'override'; NPredict = 32768; MaxTokens = 4096
}
if ((Get-ArgumentValue -Arguments $overridePlan.qualify_arguments -Name '--max-tokens') -cne '4096') {
    throw 'An explicit -MaxTokens did not reach qualify.py.'
}
if ((Get-ArgumentValue -Arguments $overridePlan.qualify_arguments -Name '--max-tokens-source') -cne 'explicit') {
    throw 'An explicit -MaxTokens was not labelled as an override.'
}

# The impossible combination must be refused by the runner, not only by the
# helper, and must be refused before anything expensive happens.
$runnerRefused = $false
try {
    Get-QualificationPlan -Splat @{
        Label = 'impossible'; MaxTokens = 8192; ReasoningBudget = 24576
    } | Out-Null
}
catch {
    $runnerRefused = $_.Exception.Message -match 'reasoning_budget'
}
if (-not $runnerRefused) {
    throw 'The qualification runner accepted max_tokens=8192 against reasoning_budget=24576.'
}

# ... and must stay available when the constrained cap IS the experiment.
$diagnosticPlan = Get-QualificationPlan -Splat @{
    Label = 'constrained'; MaxTokens = 8192; ReasoningBudget = 24576
    ConstrainedRequestBudgetDiagnostic = $true
}
if (-not ($diagnosticPlan.qualify_arguments -ccontains '--allow-constrained-request-budget')) {
    throw 'The constrained diagnostic did not tell qualify.py to allow the constrained cap.'
}
if (-not $diagnosticPlan.request_budget.constrained_diagnostic) {
    throw 'The constrained diagnostic plan did not label itself.'
}

# The runner also has to survive its own multi-word argument, all the way to the
# command line it would execute. This is section 12's property at the call site.
$hugeArgv = Split-CommandLineForTest -Line $hugePlan.server_command
$hugeMessageIndex = [Array]::IndexOf($hugeArgv, '--reasoning-budget-message')
if ($hugeMessageIndex -lt 0 -or
    $hugeArgv[$hugeMessageIndex + 1] -cne 'Thinking budget reached. Stop analysing and write the final answer now.') {
    throw 'The qualification runner split its multi-word reasoning-budget message.'
}
if ($hugeArgv -ccontains 'budget') {
    throw 'The qualification runner emitted the reasoning-budget message as separate tokens again.'
}

if (Test-Path -LiteralPath $dryRunRoot) {
    Remove-Item -LiteralPath $dryRunRoot -Recurse -Force -ErrorAction SilentlyContinue
}

if (-not $Quiet) {
    [pscustomobject]@{
        manifest              = (Resolve-Path -LiteralPath $ManifestPath).Path
        byte_stable_models    = $untunedCount
        byte_stable_stripped  = $strippedCount
        generation_tests      = 17
        argv_quoting_tests    = 5
        request_budget_tests  = 36
        runner_wiring_tests   = 27
        valid                 = $true
    } | ConvertTo-Json -Depth 3
}
