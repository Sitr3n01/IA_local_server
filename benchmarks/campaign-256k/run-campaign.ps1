# Weight-quantization campaign at KV q4_0/q4_0, run unattended.
#
# Order matters. Weight quality runs first at 32k, because a candidate that
# writes broken code at 32k does not need a context ramp and each ramp costs
# roughly an hour of prefill. Throughput runs next because it decides
# FAST/BALANCED/SLOW. The retention ramps run last and longest.
#
# The output cap is 8192 everywhere, set in coding_tasks.py, and reasoning is
# left unrestricted so the profile measured is the one the manifest ships. A
# generation that spends the whole cap inside reasoning_content is recorded as
# `no_answer` rather than graded as broken code.
$ErrorActionPreference = 'Continue'
$root = 'C:\IA\IA_local_server\benchmarks\campaign-256k'
$q = 'C:\IA\IA_local_server\scripts\v2\Invoke-V2ProfileQualification.ps1'
$t = 'C:\IA\IA_local_server\scripts\v2\Invoke-V2ThroughputSweep.ps1'
$models = [ordered]@{
    q3kxl = 'C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-Q3_K_XL.gguf'
    q2kxl = 'C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-Q2_K_XL.gguf'
    iq4xs = 'C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-IQ4_XS.gguf'
}

Write-Host "=== A. weight quality at 32k, KV q4_0/q4_0 ==="
foreach ($id in $models.Keys) {
    & $q -ModelPath $models[$id] -Label "$id-32k-kvq4" -ContextTokens 32768 `
        -CacheTypeK q4_0 -CacheTypeV q4_0 -Suites 'coding,tools,json' -OutputRoot $root
}

Write-Host "=== B. throughput at short context, KV q4_0/q4_0 ==="
foreach ($id in $models.Keys) {
    & $t -ModelPath $models[$id] -Label "$id-kvq4-short" -CacheTypeK q4_0 -CacheTypeV q4_0 `
        -Repetitions 3 -Tests @('pp:512', 'pp:8192', 'pp:32768', 'tg:128') -OutputRoot $root
}

Write-Host "=== C. decode at occupancy, KV q4_0/q4_0 ==="
foreach ($id in @('q3kxl', 'q2kxl')) {
    & $t -ModelPath $models[$id] -Label "$id-kvq4-depth" -CacheTypeK q4_0 -CacheTypeV q4_0 `
        -Repetitions 2 -Tests @('tg:128@32768', 'tg:128@131072', 'tg:128@262144') -OutputRoot $root
}

Write-Host "=== D. long-context retention ramp at ctx 262144, KV q4_0/q4_0 ==="
foreach ($id in @('q3kxl', 'q2kxl')) {
    & $q -ModelPath $models[$id] -Label "$id-256k-kvq4-ramp" -ContextTokens 262144 `
        -CacheTypeK q4_0 -CacheTypeV q4_0 -Suites 'retention' `
        -RetentionTokens 32768, 65536, 131072, 196608, 240000 -OutputRoot $root
}

Write-Host "=== CAMPAIGN COMPLETE ==="
