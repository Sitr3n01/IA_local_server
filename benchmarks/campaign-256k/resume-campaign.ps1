# Resume the KV-q4 campaign from phase C.
#
# Phases A (weight quality at 32k) and B (short-context throughput) are complete
# for all three quantizations; their artifacts are in this directory and this
# script does not touch them. What remains is the expensive half: decode
# measured with the context actually occupied, and the long-context retention
# ramp at a declared 262144-token window.
#
# Roughly 5 hours unattended. Every phase writes its own JSON per cell, so a run
# that is interrupted still leaves usable evidence and can be restarted by
# commenting out the phases that already finished.
#
#   powershell -File resume-campaign.ps1
#
# To skip the two most expensive cells (tg128 at 262144 depth, ~100 minutes for
# the pair), pass -SkipDeepDepth. The retention ramp at 240k still reports decode
# throughput at that occupancy from the server's own timings, so the evidence is
# not lost, only the controlled llama-bench version of it.
[CmdletBinding()]
param(
    [switch]$SkipDeepDepth,
    [switch]$SkipRetention
)

$ErrorActionPreference = 'Continue'
$root = 'C:\IA\IA_local_server\benchmarks\campaign-256k'
$q = 'C:\IA\IA_local_server\scripts\v2\Invoke-V2ProfileQualification.ps1'
$t = 'C:\IA\IA_local_server\scripts\v2\Invoke-V2ThroughputSweep.ps1'
$models = [ordered]@{
    q3kxl = 'C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-Q3_K_XL.gguf'
    q2kxl = 'C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-Q2_K_XL.gguf'
}

# A stale llama-server from an interrupted run would hold 12 GiB of VRAM and
# make every number below wrong. Refuse to start rather than measure through it.
$stale = Get-Process llama-server, llama-bench -ErrorAction SilentlyContinue
if ($stale) {
    throw ("Refusing to start: {0} still running (PIDs {1}). Stop them first." -f
        (($stale.ProcessName | Sort-Object -Unique) -join '/'), ($stale.Id -join ','))
}

$started = Get-Date
Write-Host ("=== resume started {0} ===" -f $started)

$depthTests = if ($SkipDeepDepth) { @('tg:128@32768', 'tg:128@131072') }
else { @('tg:128@32768', 'tg:128@131072', 'tg:128@262144') }

Write-Host "=== C. decode at occupancy, KV q4_0/q4_0 ==="
foreach ($id in $models.Keys) {
    # q3kxl at d=32768 is already measured (18.33 t/s); re-running it is cheap
    # and keeps this script a single self-contained unit rather than a set of
    # conditionals that have to be kept in sync with what happened earlier.
    & $t -ModelPath $models[$id] -Label "$id-kvq4-depth" -CacheTypeK q4_0 -CacheTypeV q4_0 `
        -Repetitions 2 -Tests $depthTests -OutputRoot $root
}

if (-not $SkipRetention) {
    Write-Host "=== D. long-context retention ramp at ctx 262144, KV q4_0/q4_0 ==="
    foreach ($id in $models.Keys) {
        & $q -ModelPath $models[$id] -Label "$id-256k-kvq4-ramp" -ContextTokens 262144 `
            -CacheTypeK q4_0 -CacheTypeV q4_0 -Suites 'retention' `
            -RetentionTokens 32768, 65536, 131072, 196608, 240000 -OutputRoot $root
    }
}

Write-Host ("=== resume complete, elapsed {0:hh\:mm\:ss} ===" -f ((Get-Date) - $started))
Write-Host "Summarize with:"
Write-Host "  python C:\IA\IA_local_server\scripts\v2\eval\summarize_campaign.py $root"

# Leave nothing holding the adapter.
Get-Process llama-server, llama-bench -ErrorAction SilentlyContinue |
    ForEach-Object { Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue }
